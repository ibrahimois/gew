# Plan 001: Release v0.5.0 with urfave/cli v3

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report; do not improvise. When done, update the status row for this plan in
> `advisor-plans/README.md`, unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**:
>
> ```sh
> git diff --stat e8a7b47..HEAD -- \
>   go.mod go.sum cmd/gew internal/cli internal/version README.md release
> shasum -a 256 \
>   go.mod cmd/gew/main.go internal/version/version.go \
>   internal/cli/cli.go internal/cli/staging.go internal/cli/diff.go \
>   internal/cli/merge.go internal/cli/migration.go \
>   internal/cli/workspace_git.go internal/cli/main_test.go README.md \
>   release/v0.4.1/RELEASE_NOTES.md
> ```
>
> This plan was written at commit `e8a7b47` **against the current uncommitted
> package-layout refactor**, not against the old root-level source tree stored
> in that commit. The expected fingerprints are listed under "Working-tree
> baseline" below. If `cmd/gew/main.go` or `internal/cli/cli.go` does not exist,
> STOP. If the package-layout move has not yet been committed or explicitly
> accepted by the operator as the implementation baseline, STOP. If an
> in-scope fingerprint differs, compare the live code with every excerpt and
> target contract in this plan; STOP on any semantic mismatch.

## Status

- **Status**: DONE
- **Priority**: P1
- **Effort**: L (multi-day, including release packaging)
- **Risk**: HIGH
- **Depends on**: none
- **Category**: migration / tests / DX / release
- **Planned at**: commit `e8a7b47`, 2026-08-08, plus the fingerprinted
  uncommitted package-layout refactor described below

## Why this matters

`gew` has 13 public commands, 12 independent standard-library `flag.FlagSet`
parsers, and a handwritten root dispatcher and usage page. Command metadata,
validation, help, and implementation are spread across five production files,
so changing a flag requires coordinated edits and users do not get successful
per-command help or shell completion. Release v0.5.0 will adopt
`github.com/urfave/cli/v3@v3.10.1`, make the command graph the single source of
truth, preserve the engine's safety semantics, propagate cancellation through
remote operations, and ship completion for bash, zsh, fish, and PowerShell.

This is a CLI-boundary migration. It must not change workspace state version
4, provider API behavior, queue/recovery semantics, supported forges, or the
existing GitLab and Bitbucket push gates.

## Current state

### Relevant files

- `cmd/gew/main.go` — owns final `gew:` error formatting and process exit.
- `internal/cli/cli.go` — contains `Run`, the manual command switch, handwritten
  root usage, six command parsers, and common workspace/config helpers.
- `internal/cli/staging.go` — contains parsers and implementations for `add`,
  `reset`, `commit`, and `log`.
- `internal/cli/diff.go` — contains the `diff` parser and implementation.
- `internal/cli/merge.go` — contains `merge` parsing and remote-merge flow.
- `internal/cli/migration.go` — contains `migrate` parsing and an already
  context-aware migration core.
- `internal/cli/workspace_git.go` — contains hybrid pull/push remote calls that
  currently discard caller cancellation.
- `internal/cli/main_test.go` — exercises most command workflows through about
  76 direct handler calls, bypassing exported `Run`.
- `internal/cli/workspace_git_test.go` and `internal/cli/git_export_test.go` —
  directly exercise hybrid pull/push helpers and must follow context signature
  changes.
- `internal/version/version.go` — contains the release version.
- `README.md` — contains the public command reference and environment guidance.
- `release/v0.4.1/` — exemplar for release notes, archive layout, and checksums.

### Existing public seam and error ownership

`internal/cli/cli.go:60-67`:

```go
type app struct {
    out    io.Writer
    errOut io.Writer
}

func Run(args []string, output, errorOutput io.Writer) error {
    return (app{out: output, errOut: errorOutput}).run(args)
}
```

`cmd/gew/main.go:10-14`:

```go
if err := cli.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
    fmt.Fprintf(os.Stderr, "gew: %v\n", err)
    os.Exit(1)
}
```

Preserve that separation: library code returns errors; the executable prints
the final prefix once and chooses the process status.

### Duplicated command schema

`internal/cli/cli.go:69-110` dispatches every command with a switch, while
`internal/cli/cli.go:113-146` separately maintains root help. Each handler then
parses again. For example, `internal/cli/staging.go:55-71` defines `add`'s two
aliases as separate booleans and mixes parsing with workspace operations:

```go
flags := flag.NewFlagSet("add", flag.ContinueOnError)
allShort := flags.Bool("A", false, "stage all changes")
allLong := flags.Bool("all", false, "stage all changes")
if err := flags.Parse(args); err != nil {
    return err
}
if !*allShort && !*allLong && flags.NArg() == 0 {
    return errors.New("usage: gew add [-A|--all] PATH...")
}
```

`internal/cli/merge.go:39-56` similarly parses `--abort`, `--continue`, and
`-m` and enforces mode selection manually. It currently accepts `--abort -m
ignored`, then silently ignores the message.

### Current help and version behavior

- Empty args, `help`, `-h`, and `--help` print handwritten root help and return
  success.
- `help status` prints root help rather than status help.
- `status --help` prints the standard-library flag block and returns an error,
  so the executable exits non-zero.
- `version` and `--version` print `gew 0.4.1`; `-v` is not supported.
- The standard-library parser stops at the first positional operand, so flags
  after that operand are treated as operands.
- Every returned error receives the `gew:` prefix exactly once in
  `cmd/gew/main.go`.

### Context behavior

Production code under `internal/cli` contains 33 `context.Background()` calls.
Remote clone, pull, push, merge, migration, hybrid pull/push, and missing
snapshot recovery therefore ignore caller cancellation even though forge
interfaces already accept `context.Context`.

### Test conventions and verification baseline

- Tests are standard Go tests with `testing`, `httptest.Server`, `bytes.Buffer`,
  `t.TempDir`, and `t.Setenv`; follow `internal/cli/main_test.go:207-323` and
  `internal/cli/main_test.go:1228-1254`.
- Provider HTTP tests are hermetic. Normal test commands must not contact live
  forges.
- At planning time these commands pass:
  - `go test ./...`
  - `go test -race ./...`
  - `go vet ./...`
  - `go test -cover ./...` (`internal/cli`: 67.9%; `cmd/gew`: 0.0%)
- Commit messages use Conventional Commits, such as
  `refactor: harden forge adapter contract` and `release: publish v0.4.1`.
- Release archives contain exactly `gew` and `README.md`, and v0.4.1 ships
  `darwin`/`linux` for `amd64`/`arm64` with a four-line `SHA256SUMS`.

### Working-tree baseline

These SHA-256 values capture the source layout reviewed by this plan:

```text
887c1902c9906e0e14f658d06e7608147373d1cb9c8560d78c58a1d0cfb42966  go.mod
dac77fc5290d0daebca90967bc77dbe2430dec4175f2c854d627df362633d1ea  cmd/gew/main.go
3f8a3582568b514ff40b903a12c23c77ce4d75eb43d13c1bee5bdde93fb749f8  internal/version/version.go
03c7be7cadadac067a87fc83168c161604db5b5a96eb7cb7921f4f39974227bc  internal/cli/cli.go
8dd40aafe150e52c9620f936359a55f11414dc1007793de1f8e647db2c11f418  internal/cli/staging.go
913388911c7bfa3ecee085b9cdbcbce3f12fc5b650939a789f9fb1507891f7ba  internal/cli/diff.go
c718cbb59deaa8ee0562179fa2ce6dd17d02ec0d56d5f4b2428f71a4c3f4e6a7  internal/cli/merge.go
a1401a558610b1205614b1dacf119dfae5c4dc845e7d8036ac8c83cc6f52cb21  internal/cli/migration.go
4b7d9626e65c1acd923055735ea64292f24d3244e45867132f6d1985d74e246c  internal/cli/workspace_git.go
d19494727ca331883f75237a54fa3200f3545d4fecd58514ee9e07ae10991500  internal/cli/main_test.go
d79853fb0b83b54d94225adc36994527e2400dbc18ca175347c7ed0cf5f32d06  README.md
3b719d1e210f84204023496ce7937a882c2c0502397a7be77f32638187b42d03  release/v0.4.1/RELEASE_NOTES.md
```

## Target v0.5.0 contract

### Command schema

The new command graph must define exactly this surface:

| Command | Flags | Positional arguments |
|---------|-------|----------------------|
| `login` | `--provider` (default `gitea`), `--name` (default `default`), `--token` (source `GEW_TOKEN`), `--auth-kind`, `--username`, `--insecure` | exactly `URL` |
| `doctor` | none | none |
| `clone` | `--branch`, `--backend` (default `gew`) | `OWNER/REPO` and optional `DIRECTORY` |
| `status` | `--json` | none |
| `add` | `--all`, alias `-A` | one or more `PATH`, unless `--all` is set |
| `reset` | none | zero or more `PATH` |
| `diff` | `--staged` | none |
| `commit` | required nonblank `--message`, alias `-m`; `--author-name`; `--author-email` | none |
| `log` | `--oneline` | none |
| `pull` | `--ff-only` | none |
| `merge` | required exclusive `--abort` or `--continue`; optional `--message`, alias `-m`, valid only with `--continue` | none |
| `migrate` | required `--to`, whose only accepted value is `git`; `--dry-run`; `--author-name`; `--author-email` | none |
| `push` | `--new-branch` | none |
| `version` | none | none |

Root behavior:

- No arguments, `help`, `-h`, and `--help` return success and print generated
  root help.
- `help <command>` and `<command> --help`/`-h` return success and print
  command-specific help.
- `version`, `--version`, and the new `-v` alias print exactly `gew 0.5.0` plus
  one newline.
- Unknown commands return `unknown command %q; run 'gew help'`; only
  `cmd/gew/main.go` adds `gew:` and exits 1.
- Flags may appear before or after positional arguments. This is an intentional
  v0.5.0 improvement. `--` terminates flag parsing for paths beginning with
  `-`.
- `UseShortOptionHandling` and `PrefixMatchCommands` remain disabled. Do not
  introduce compound short flags or command-prefix aliases.
- Parser/usage errors return ordinary errors. They must not call `os.Exit`,
  print through package-global writers, or cause duplicate error output.
- Help and completion write to the injected `output`; parser diagnostics write
  only to injected `errorOutput` when intentionally emitted.
- `completion bash`, `completion zsh`, `completion fish`, and `completion pwsh`
  emit non-empty scripts and return success.

Compatibility invariants:

- `stateVersion` remains 4. No workspace migration is added.
- Existing command names and long flags remain supported.
- `-A` and `-m` remain supported.
- Operational success/error messages remain unchanged unless this plan
  explicitly identifies the help/version/usage change.
- Gitea, GitHub, and Azure push behavior remains enabled and unchanged.
- GitLab and Bitbucket push remain disabled.
- Tokens are never printed in help, errors, tests, or release artifacts. Help
  may name `GEW_TOKEN`, but must never display its value.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Baseline tests | `go test ./...` | exit 0; all packages pass |
| Race tests | `go test -race ./...` | exit 0; no race reports |
| Vet | `go vet ./...` | exit 0; no diagnostics |
| Stress tests | `go test -shuffle=on -count=20 ./...` | exit 0; all 20 shuffled runs pass |
| Coverage | `go test -cover ./...` | exit 0; CLI coverage does not regress below the pre-plan 67.9% without an explained package-boundary shift |
| Format check | `test -z "$(gofmt -l cmd internal)"` | exit 0; no filenames printed |
| Host build | `go build -trimpath -o /tmp/gew-v0.5.0 ./cmd/gew` | exit 0; no repository-local binary created |
| Dependency version | `go list -m -f '{{.Version}}' github.com/urfave/cli/v3` | exactly `v3.10.1` |
| Module hygiene | `go mod tidy && git diff --check` | exit 0; no whitespace errors; only intended module changes |

## Suggested executor toolkit

- Use the tagged v3.10.1 source as the API authority:
  - `https://github.com/urfave/cli/releases/tag/v3.10.1`
  - `https://github.com/urfave/cli/blob/v3.10.1/command.go`
  - `https://github.com/urfave/cli/blob/v3.10.1/command_run.go`
  - `https://github.com/urfave/cli/blob/v3.10.1/args.go`
  - `https://github.com/urfave/cli/blob/v3.10.1/flag_mutex.go`
  - `https://cli.urfave.org/v3/examples/completions/shell-completions/`
- Import the dependency inside package `cli` with an explicit alias such as
  `ucli`; do not create an ambiguous `cli` identifier.
- If Context7 is available, use it to confirm v3.10.1 API fields before coding,
  but the tagged source wins if hosted documentation disagrees.

## Scope

**In scope** (the only source, test, documentation, and release paths that may
be modified or created):

- `go.mod`
- `go.sum`
- `cmd/gew/main.go`
- `internal/version/version.go`
- `internal/cli/command.go` (create; command graph and `Run`/`RunContext`)
- `internal/cli/command_test.go` (create; public CLI contract)
- `internal/cli/cli.go`
- `internal/cli/staging.go`
- `internal/cli/diff.go`
- `internal/cli/merge.go`
- `internal/cli/migration.go`
- `internal/cli/workspace_git.go`
- `internal/cli/main_test.go`
- `internal/cli/workspace_git_test.go`
- `internal/cli/git_export_test.go`
- `README.md`
- `release/v0.5.0/RELEASE_NOTES.md` (create)
- `release/v0.5.0/SHA256SUMS` (create)
- `release/v0.5.0/gew_0.5.0_darwin_amd64.tar.gz` (create)
- `release/v0.5.0/gew_0.5.0_darwin_arm64.tar.gz` (create)
- `release/v0.5.0/gew_0.5.0_linux_amd64.tar.gz` (create)
- `release/v0.5.0/gew_0.5.0_linux_arm64.tar.gz` (create)
- `advisor-plans/README.md` (status update only)
- `advisor-plans/001-adopt-urfave-cli-v0.5.0.md` (status update only)

**Out of scope** (do not touch even if related):

- `internal/forge/**`, `internal/merge/**`, and `internal/workspace/**` — the
  CLI migration consumes their APIs; it does not redesign them.
- Any change to `stateVersion`, `.gew` JSON, Git export receipts, merge recovery
  data, provider request shapes, authentication storage, or push safety.
- Enabling GitLab or Bitbucket writes.
- Adding `github.com/urfave/cli-docs/v3`, generated Markdown/man pages, a tools
  module, or raising the Go 1.22 minimum.
- Windows release binaries. The existing release matrix is macOS/Linux only.
- Git tags, GitHub/Gitea releases, uploads, pushes, or PR creation. Those require
  separate operator authorization.
- Reformatting or rewriting the unrelated current README edits beyond the
  command/help/completion/version sections needed for v0.5.0.
- Reversing the user-owned move from root-level Go files and `plans/` into
  `cmd/`, `internal/`, and `docs/plans/`.

## Git workflow

- Suggested branch after the package-layout baseline is committed:
  `advisor/001-urfave-cli-v0.5.0`.
- Keep the tree buildable between logical commits. Suggested commits:
  1. `test: lock the CLI release contract`
  2. `refactor: adopt urfave cli v3`
  3. `feat: add CLI completion and cancellation`
  4. `docs: document the v0.5 command surface`
  5. `release: publish v0.5.0`
- Do not push, tag, open a PR, or publish archives unless the operator
  explicitly instructs it.
- Never stage or rewrite unrelated pre-existing changes.

## Steps

### Step 1: Stabilize the package-layout baseline

Confirm that the operator's move from root-level files into `cmd/`, `internal/`,
and `docs/plans/` is captured in a commit or explicitly approved as the dirty
baseline. Do not stash, reset, commit, or clean user changes without direct
authorization. Re-run the drift check and compare any changed file with the
target contract above.

Confirm that `git status --short` contains no unexplained modifications in the
in-scope files. Existing release directories and the historical plan move are
user-owned.

**Verify**:

```sh
test -f cmd/gew/main.go
test -f internal/cli/cli.go
test -f docs/plans/007-harden-forge-adapter-contract.md
go test ./...
go vet ./...
```

Expected: every command exits 0. If the structural refactor itself does not
pass, STOP; do not mix its repair into this release.

### Step 2: Add public CLI characterization tests before changing the parser

Create `internal/cli/command_test.go`. Test the exported `Run` seam with
separate `bytes.Buffer` values for output and error output. Build the command
fresh for every test; do not share mutable parser state.

Before the migration, lock only behavior that must remain compatible:

- empty args and `help` return nil and print a root command reference;
- `version` and `--version` return nil and print `gew 0.4.1\n` at this step;
- an unknown command returns `unknown command "unknown"; run 'gew help'` and
  does not print the `gew:` executable prefix from library code;
- missing/extra operands return errors before workspace/network operations;
- output and error output remain distinct;
- repeated independent calls to `Run` do not retain a prior flag value.

Do not lock the accidental non-zero behavior of `<command> --help`, the lack of
`help <command>`, or the old stop-at-first-operand parsing rule. Those are
deliberate v0.5 changes.

Add a small test helper that invokes `RunContext(context.Background(), args,
out, errOut)` once that function exists; until then use `Run`. Do not add a
subprocess or call `os.Exit` in unit tests.

**Verify**: `go test ./internal/cli -run 'TestCommand|TestRun' -count=1` → exit
0 and all new characterization cases pass against the old parser.

### Step 3: Introduce parse-free command operations without switching routing

Refactor each command so argument parsing becomes a thin temporary wrapper and
business work moves into a typed operation accepting `context.Context`.
Introduce unexported option structs near the command graph or the owning
domain file:

```go
type loginOptions struct {
    Name, Provider, Token, AuthKind, Username, URL string
    Insecure                                      bool
}

type cloneOptions struct {
    Repository, Directory, Branch string
    Backend                       WorkspaceBackendKind
}

type addOptions struct {
    Paths []string
    All   bool
}

type commitOptions struct {
    Message, AuthorName, AuthorEmail string
}

type mergeOptions struct {
    Abort, Continue bool
    Message         string
}

type migrateOptions struct {
    Target, AuthorName, AuthorEmail string
    DryRun                          bool
}
```

Use equivalent small structs or typed parameters for `status`, `reset`,
`diff`, `log`, `pull`, and `push`. The final operation shape should be
consistent, for example:

```go
func (a app) clone(ctx context.Context, options cloneOptions) error
func (a app) add(ctx context.Context, options addOptions) error
func (a app) doctor(ctx context.Context) error
```

During this step only, the old `[]string` parsers may remain as wrappers so the
existing test suite stays green. Do not duplicate domain validation: keep
provider/backend normalization, nonblank-message validation, path safety,
workspace checks, and forge capability checks in the parse-free operations.

Thread `ctx` into existing context-aware forge/migration helpers. Update
`mergeRemote`, `ensureSnapshotObjects`, `gitPull`, `gitPush`, and
`gitPushWithForge` to accept it. Replace their remote-operation
`context.Background()` values with the passed context. Update direct calls in
`workspace_git_test.go` and `git_export_test.go` with
`context.Background()`; those tests are operation tests, not cancellation
tests.

Do not add cancellation checks between local journal/state writes unless the
called operation already returns an error. Existing prepared-export and
accepted-but-lost recovery semantics must remain the authority.

**Verify**:

```sh
go test ./internal/cli
go test -race ./internal/cli
go test ./...
```

Expected: exit 0; all pre-existing workflow, merge, migration, and export
recovery tests remain unchanged in meaning.

### Step 4: Add the pinned dependency and declarative command graph

Run:

```sh
go get github.com/urfave/cli/v3@v3.10.1
go mod tidy
```

Create `internal/cli/command.go`. Import the framework as `ucli`. Implement:

```go
func Run(args []string, output, errorOutput io.Writer) error {
    return RunContext(context.Background(), args, output, errorOutput)
}

func RunContext(ctx context.Context, args []string, output, errorOutput io.Writer) error {
    application := newCommand(app{out: output, errOut: errorOutput})
    return application.Run(ctx, append([]string{"gew"}, args...))
}
```

Requirements for `newCommand`:

1. Return a **new** `*ucli.Command` graph on every call. Never store the graph
   or flag values in package globals.
2. Set root `Writer` and `ErrWriter` from the injected app writers.
3. Set a root `ExitErrHandler` that does not call `os.Exit`; allow `RunContext`
   to return the error to `cmd/gew/main.go`.
4. Set `HideVersion: true` and define a local root `--version`/`-v` boolean.
   The root action prints `gew <version>` through the injected writer when it
   is set. Define a `version` command that uses the same helper. Do not mutate
   package-global `ucli.VersionPrinter`, `ucli.OsExiter`, `ucli.ErrWriter`, or
   help templates.
5. With no root args, call generated root help and return nil. With an unknown
   root arg, return the existing unknown-command error.
6. Set `EnableShellCompletion: true`.
7. Keep `UseShortOptionHandling` and `PrefixMatchCommands` false.
8. Use one helper to apply a compact `OnUsageError` policy to every command.
   It must return ordinary errors without the framework's default duplicate
   `Incorrect Usage` block. Explicit help must still return nil and print help.
9. Define the exact command/flag/argument table from "Target v0.5.0 contract".
   Use aliases on one flag object (`all` with `A`, `message` with `m`) rather
   than duplicate booleans.
10. Use `ucli.EnvVars("GEW_TOKEN")` only for login's token fallback. Leave
    coupled `GEW_SERVER`/`GEW_TOKEN`, profile selection, config paths, and author
    identity precedence in existing domain functions.
11. Represent merge modes with
    `ucli.MutuallyExclusiveFlags{Required: true}` and also reject `-m` when
    `--abort` is selected.
12. Use `Required` and `Validator` where they preserve existing semantics, but
    retain explicit arity and leftover-argument checks in actions. In v3.10.1,
    a singular typed argument is optional when absent and bounded argument
    parsing can leave extras.
13. Let v3's normal parsing accept flags before or after positionals. Test `--`
    as the path escape hatch.
14. Keep urfave types inside `command.go`. Parse-free operations and all forge,
    workspace, merge, and provider packages must not import the framework.

Switch exported `Run` to this graph only after all commands are represented.
Delete the manual `app.run` switch and `app.usage` function from `cli.go`.

**Verify**:

```sh
go list -m -f '{{.Version}}' github.com/urfave/cli/v3
go test ./internal/cli
go test ./...
```

Expected: version output is exactly `v3.10.1`; all commands compile through the
new graph; every test passes.

### Step 5: Replace parser wrappers and route workflow tests through Run

Remove all 12 standard-library `flag.FlagSet` parsers and their handwritten
`usage: gew ...` duplicates from `cli.go`, `staging.go`, `diff.go`, `merge.go`,
and `migration.go`. Remove now-unused `flag` imports.

Mechanically update the approximately 76 direct command-handler calls in
`internal/cli/main_test.go` to invoke the public `RunContext` surface. Add a
test-only helper that reuses the `app`'s writers but creates a fresh command
graph each time, for example:

```go
func runTestCommand(a app, args ...string) error {
    return RunContext(context.Background(), args, a.out, a.errOut)
}
```

Then convert calls such as `a.add([]string{"-A"})` to
`runTestCommand(a, "add", "-A")`. Preserve direct testing of non-command
helpers and typed operations only where the test is intentionally below the
CLI boundary.

Expand `command_test.go` to assert the complete target contract:

- every command appears in root help;
- `help status`, `status --help`, and `status -h` succeed, describe `--json`,
  and write no error output;
- all commands' help succeeds without requiring credentials or a workspace;
- `version`, `--version`, and `-v` produce identical exact output;
- `version extra`, no-arg `login`, three-arg `clone`, and extra operands on
  no-arg commands fail before any network/workspace access;
- `add -A` and `add --all` map to one boolean;
- `commit -m` trims and rejects an empty/whitespace-only message;
- merge requires exactly one mode and rejects `--abort -m ignored`;
- migrate rejects any `--to` value other than `git`;
- flags after positionals are parsed intentionally;
- `--` permits a path beginning with `-`;
- unknown flags/commands return once, use injected writers, and never terminate
  the test process;
- two sequential `RunContext` calls with different flags prove the command
  graph has no leaked parse state;
- no help, error, or completion output contains a configured token value.

**Verify**:

```sh
if rg -n 'flag\.NewFlagSet|"flag"|errors\.New\("usage: gew' internal/cli --glob '*.go'; then
  exit 1
fi
test "$(rg -l 'github.com/urfave/cli/v3' internal --glob '*.go')" = "internal/cli/command.go"
go test ./internal/cli -run 'TestCommand|TestRun|TestEndToEndWorkflow|TestCommandErrors' -count=1
go test ./...
```

Expected: the searches find no old parsers/usage strings; only `command.go`
imports urfave; all selected and full tests pass.

### Step 6: Expose cancellation safely

Update `cmd/gew/main.go` to create a cancellable root context with
`signal.NotifyContext(context.Background(), os.Interrupt)`, defer the stop
function, and call `cli.RunContext`. Keep the existing final error prefix and
exit status.

After Step 3, production code should contain only the compatibility wrapper's
`context.Background()` call. Replace any remaining background context in
command and remote paths with the caller context. Do not change test-only
background contexts.

Add a cancellation test in `command_test.go` using an `httptest.Server` whose
handler blocks until `request.Context().Done()`. Configure the existing
environment-based profile path with non-secret test values, call `RunContext`
with a canceled or deadline-bound context, and assert the command returns a
wrapped `context.Canceled`/`context.DeadlineExceeded` promptly. The test must
not depend on wall-clock sleeps longer than a small timeout guard.

Re-run ambiguous-response and export-journal tests to prove cancellation
plumbing did not weaken recovery.

**Verify**:

```sh
rg -n 'context\.Background\(\)' internal/cli --glob '*.go' --glob '!**/*_test.go'
go test ./internal/cli -run 'Cancellation|Ambiguous|Export|PartialPush' -count=1
go test -race ./internal/cli
```

Expected: the search reports exactly one production call, in the backward-
compatible `Run` wrapper; all targeted and race tests pass.

### Step 7: Document generated help, completion, and compatibility changes

Update `README.md` without replacing unrelated user edits:

- retain the existing quick start and command reference;
- add `gew help <command>` and `<command> --help` examples;
- add a concise completion section with:
  - `source <(gew completion bash)`
  - `source <(gew completion zsh)`
  - `gew completion fish > ~/.config/fish/completions/gew.fish`
  - PowerShell generation via `gew completion pwsh`;
- document `-v` and `--version`;
- state that flags may appear after positionals and `--` must be used for paths
  beginning with `-`;
- correct login token wording: omission reads `GEW_TOKEN`; there is no
  interactive prompt;
- do not claim Markdown/man documentation generation.

Verify completion scripts are generated through injected output and contain
no environment values.

**Verify**:

```sh
go build -trimpath -o /tmp/gew-v0.5.0 ./cmd/gew
/tmp/gew-v0.5.0 help status >/tmp/gew-help-status.txt
/tmp/gew-v0.5.0 status --help >/tmp/gew-status-help.txt
/tmp/gew-v0.5.0 completion bash >/tmp/gew-completion-bash.txt
/tmp/gew-v0.5.0 completion zsh >/tmp/gew-completion-zsh.txt
/tmp/gew-v0.5.0 completion fish >/tmp/gew-completion-fish.txt
/tmp/gew-v0.5.0 completion pwsh >/tmp/gew-completion-pwsh.txt
test -s /tmp/gew-help-status.txt
test -s /tmp/gew-status-help.txt
test -s /tmp/gew-completion-bash.txt
test -s /tmp/gew-completion-zsh.txt
test -s /tmp/gew-completion-fish.txt
test -s /tmp/gew-completion-pwsh.txt
```

Expected: every command exits 0 and every output file is non-empty.

### Step 8: Set v0.5.0 and write release notes

Change `internal/version/version.go` from `0.4.1` to `0.5.0`. Do not change
`stateVersion`.

Create `release/v0.5.0/RELEASE_NOTES.md` following
`release/v0.4.1/RELEASE_NOTES.md`. Include:

- adoption of `urfave/cli` v3.10.1;
- generated root and per-command help;
- declarative aliases, required flags, arity, and merge-mode validation;
- shell completion for bash, zsh, fish, and PowerShell;
- `RunContext` and interrupt propagation;
- the new `-v` alias and flexible flag placement;
- explicit compatibility notes: state schema remains 4, no workspace migration,
  providers and authentication unchanged, GitLab/Bitbucket push still gated;
- the exact verification commands actually run;
- the same macOS/Linux amd64/arm64 package matrix as v0.4.1.

Do not create checksums or archives until all source verification passes.

**Verify**:

```sh
go build -trimpath -o /tmp/gew-v0.5.0 ./cmd/gew
test "$(/tmp/gew-v0.5.0 version)" = "gew 0.5.0"
test "$(/tmp/gew-v0.5.0 --version)" = "gew 0.5.0"
test "$(/tmp/gew-v0.5.0 -v)" = "gew 0.5.0"
rg -n 'v0\.5\.0|urfave/cli|completion|state.*4' release/v0.5.0/RELEASE_NOTES.md
```

Expected: all version checks pass; release-note search finds each topic.

### Step 9: Run the complete release gate

Run from the repository root:

```sh
test -z "$(gofmt -l cmd internal)"
go vet ./...
go test ./...
go test -race ./...
go test -shuffle=on -count=20 ./...
go test -cover ./...
go build -trimpath -o /tmp/gew-v0.5.0 ./cmd/gew
go list -m -f '{{.Version}}' github.com/urfave/cli/v3
git diff --check
```

Expected: every command exits 0; no race or vet diagnostics; dependency output
is `v3.10.1`; internal CLI coverage is at least 67.9% unless the report clearly
moved statements into a separately measured package (do not waive missing CLI
contract coverage).

Run a smoke matrix against `/tmp/gew-v0.5.0`:

```sh
/tmp/gew-v0.5.0 --help >/tmp/gew-root-help.txt
/tmp/gew-v0.5.0 help commit >/tmp/gew-commit-help.txt
/tmp/gew-v0.5.0 commit --help >/tmp/gew-commit-help-flag.txt
test "$(/tmp/gew-v0.5.0 version)" = "gew 0.5.0"
test "$(/tmp/gew-v0.5.0 --version)" = "gew 0.5.0"
test "$(/tmp/gew-v0.5.0 -v)" = "gew 0.5.0"
test -s /tmp/gew-root-help.txt
test -s /tmp/gew-commit-help.txt
test -s /tmp/gew-commit-help-flag.txt
```

Expected: all commands exit 0 and help files are non-empty.

### Step 10: Build and verify the four release archives

Create the release directory only after Step 9 passes. Build in a scoped
temporary directory so no `gew` or `dist/` artifact is created in the repo:

```sh
release_tmp="$(mktemp -d)"
test -n "$release_tmp" && test -d "$release_tmp"
trap 'rm -rf "$release_tmp"' EXIT
mkdir -p release/v0.5.0

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  goos="${target%/*}"
  goarch="${target#*/}"
  package_dir="$release_tmp/${goos}_${goarch}"
  mkdir -p "$package_dir"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags='-s -w' -o "$package_dir/gew" ./cmd/gew
  cp README.md "$package_dir/README.md"
  tar -C "$package_dir" -czf \
    "release/v0.5.0/gew_0.5.0_${goos}_${goarch}.tar.gz" \
    gew README.md
done

(
  cd release/v0.5.0
  shasum -a 256 gew_0.5.0_*.tar.gz > SHA256SUMS
)
```

The `rm -rf` appears only in a trap against the freshly validated `mktemp -d`
path. Do not substitute a repository path, `$HOME`, `~`, or an unresolved
variable.

Verify checksums, archive membership, target metadata, and source version:

```sh
(
  cd release/v0.5.0
  shasum -a 256 -c SHA256SUMS
  test "$(wc -l < SHA256SUMS | tr -d ' ')" = "4"
)

for archive in release/v0.5.0/gew_0.5.0_*.tar.gz; do
  members="$(tar -tzf "$archive" | LC_ALL=C sort)"
  test "$members" = "README.md
gew"
done

for archive in release/v0.5.0/gew_0.5.0_*.tar.gz; do
  inspect_dir="$(mktemp -d)"
  tar -xzf "$archive" -C "$inspect_dir" gew
  go version -m "$inspect_dir/gew" | rg 'path\s+gew/cmd/gew|github.com/urfave/cli/v3\s+v3\.10\.1'
  rm -rf "$inspect_dir"
done
```

Expected: all four checksums report `OK`; every archive contains exactly
`README.md` and `gew`; every binary reports module path `gew/cmd/gew` and
dependency `github.com/urfave/cli/v3 v3.10.1`.

Do not tag, upload, publish, or push. Leave those actions to the operator after
review.

### Step 11: Perform final scope and release review

Inspect the final diff. Confirm every source change traces to the command
boundary, context propagation, documentation, version, or release packaging.
Confirm no secrets, config files, workspaces, provider fixtures, or unrelated
README edits entered the diff.

```sh
git status --short
git diff --stat
git diff --check
git diff -- go.mod go.sum cmd/gew internal/cli internal/version README.md \
  release/v0.5.0 advisor-plans
```

Expected: only in-scope paths are modified; `release/v0.5.0` contains one notes
file, one four-line checksum file, and four archives; no credential values are
present.

Update this plan and `advisor-plans/README.md` from `TODO` to `DONE` only after
all gates pass.

## Test plan

### New command-contract tests

Create `internal/cli/command_test.go` and cover:

- root help from no args and all help aliases;
- help for every command through `help <command>` and `<command> --help`;
- exact version output through `version`, `--version`, and `-v`;
- unknown commands and flags, writer separation, and no duplicate prefix;
- exact positional minima/maxima and leftover rejection;
- one alias object for `-A`/`--all` and `-m`/`--message`;
- nonblank trimmed commit messages;
- required exclusive merge modes and invalid abort-message pairing;
- required exact `migrate --to git`;
- flags after positionals and `--` path termination;
- repeated `RunContext` calls with no parser-state leakage;
- completion output for `bash`, `zsh`, `fish`, and `pwsh`;
- output redaction with a non-secret test token;
- cancellation reaching an HTTP request context.

### Existing regression suites

- Keep `internal/cli/main_test.go` as the full workflow pattern, but route
  command actions through `RunContext` so it now covers the public CLI boundary.
- Preserve `internal/cli/workspace_git_test.go` hybrid pull/merge coverage.
- Preserve `internal/cli/git_export_test.go` stale-head and accepted-but-lost
  recovery coverage.
- Preserve `internal/cli/migration_test.go`; its core already accepts context.
- Run all forge adapter tests unchanged to prove the framework did not enter or
  alter provider packages.

### Required verification

`go test ./...`, `go test -race ./...`, `go test -shuffle=on -count=20 ./...`,
`go vet ./...`, format checks, the host-binary smoke matrix, and the archive
verification in Step 10 must all pass.

## Done criteria

- [ ] The package-layout baseline is committed or explicitly approved; no
  user-owned changes were overwritten.
- [ ] `github.com/urfave/cli/v3` is pinned exactly to `v3.10.1`.
- [ ] A fresh declarative command graph is created per `RunContext` call.
- [ ] Only `internal/cli/command.go` imports urfave/cli.
- [ ] No production `flag.NewFlagSet`, `"flag"` import, or handwritten
  `errors.New("usage: gew ...")` remains under `internal/cli`.
- [ ] All 13 commands and every existing flag are represented in generated
  help.
- [ ] Root and per-command help exit successfully.
- [ ] `version`, `--version`, and `-v` print exactly `gew 0.5.0`.
- [ ] Flags after positionals and `--` path handling are tested and documented.
- [ ] Merge mode/message and migrate-target validation are tested.
- [ ] `Run` remains backward compatible and delegates to `RunContext`.
- [ ] Cancellation reaches remote HTTP requests; only the compatibility wrapper
  creates a production background context.
- [ ] `cmd/gew/main.go` remains the sole owner of the `gew:` prefix and exit 1.
- [ ] State version remains 4 and provider safety gates are unchanged.
- [ ] Shell completion emits non-empty scripts for bash, zsh, fish, and pwsh.
- [ ] README documents help, completion, `-v`, flexible flags, and `--`.
- [ ] `internal/version.Current` is `0.5.0` and release notes accurately describe
  compatibility and verification.
- [ ] Format, vet, normal, race, shuffle, and coverage gates pass.
- [ ] Four release archives contain exactly `gew` and `README.md`; all four
  checksums verify and binaries report urfave/cli v3.10.1.
- [ ] No file outside the in-scope list changed because of this plan.
- [ ] `advisor-plans/README.md` and this plan show `DONE` only after all checks.

## STOP conditions

Stop and report; do not improvise if any of these occurs:

- The `cmd/`/`internal/` restructuring is not captured or explicitly approved,
  or the live tree has reverted to root-level `package main` files.
- An in-scope file differs from the fingerprinted baseline in a way that changes
  command names, flags, workspace state, provider safety, or release layout.
- `github.com/urfave/cli/v3@v3.10.1` no longer resolves, requires a Go version
  above 1.22, or its tagged `Command.Run`, `ExitErrHandler`, argument, flag,
  mutual-exclusion, or completion API differs from this plan.
- Implementing the graph appears to require importing urfave into forge,
  merge-core, workspace-model, or provider packages.
- A typed argument accepts missing or surplus values and cannot be made strict
  with explicit action validation.
- Help, usage, or completion invokes `os.Exit` during tests despite the
  per-command exit policy.
- Cancellation causes loss of a prepared export journal, duplicated remote
  commits, skipped state checkpoints, or failure of accepted-but-lost recovery.
- Any provider test, merge recovery test, migration test, or hybrid export test
  fails twice after a reasonable correction.
- Internal CLI coverage drops below 67.9% because public command paths are not
  exercised.
- Release archives contain anything other than `gew` and `README.md`, a binary
  is not the requested target, or checksum verification fails.
- Completing the work would require a Git tag, upload, remote release, push,
  credential, or changes outside the in-scope list without operator approval.

## Maintenance notes

- Build a new command graph per invocation. urfave flag objects retain parse
  state; reusing a global graph will make tests and embedded callers leak flags.
- Keep ordinary domain errors ordinary. `ucli.Exit` can invoke exit handling;
  library code must continue returning errors to `cmd/gew/main.go`.
- When adding a command or flag after v0.5.0, update only the declarative graph,
  its contract test, and user-facing docs. Do not reintroduce local FlagSets or
  handwritten root command tables.
- Treat changes to help text, aliases, flag placement, and exit codes as public
  API changes even before v1.0.
- Keep environment-source conversion selective. `GEW_SERVER` and `GEW_TOKEN`
  have coupled ephemeral-profile semantics; `GEW_PROFILE` and `GEW_CONFIG`
  affect configuration selection; do not flatten them into global flags without
  a separate precedence design.
- Do not add `urfave/cli-docs` to the main module while Go 1.22 is supported.
  If generated man/Markdown docs are later desired, use an isolated tools
  module or CI environment with its own Go floor.
- Review cancellation changes around remote-accepted/local-unconfirmed states
  especially carefully. Existing recovery journals and reconciliation logic
  must remain authoritative.
- A future release automation plan should generate the same four archives and
  checksums in CI. This plan preserves the current manual release convention
  rather than adding publishing infrastructure.
