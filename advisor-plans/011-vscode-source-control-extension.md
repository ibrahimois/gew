# Plan 011: Add repository-scoped GEW actions to VS Code Source Control

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report—do not improvise. When done, update this plan's row in
> `advisor-plans/README.md`, unless a reviewer says they maintain the index.
>
> **Drift check (run first)**:
> `git diff --stat 1e0cff6..HEAD -- extensions/vscode README.md .gitignore advisor-plans/README.md`
> If `extensions/vscode/` now exists, or the README's hybrid-backend contract
> differs from the excerpts below, compare the live design with this plan and
> stop on any semantic conflict.

## Status

- **Priority**: P2
- **Effort**: M (three to five focused days)
- **Risk**: MED — this executes synchronization commands from editor UI
- **Depends on**: none; Plans 001–010 are already DONE
- **Category**: dx / editor integration
- **Planned at**: commit `1e0cff6`, 2026-08-09

## Why this matters

GEW's hybrid backend intentionally gives editors a standards-compliant local
`.git` repository while retaining `.gew` as the REST synchronization journal.
VS Code can therefore own local status, diff, stage, and commit UX, but its
built-in Pull, Push, and Sync actions always use Git transport and cannot be
redirected to GEW through settings. An official, separately packaged extension
should add clearly named GEW Pull, Push, and Sync actions to the Source Control
view without replacing Git commands or weakening GEW's REST-only safety model.

The extension must be explicitly enabled per hybrid repository. It must execute
GEW without a shell, honor Workspace Trust, avoid exposing credentials, and
never modify GEW's internal journal directly.

## Current state

- This repository is the GEW source repository. It is a Go 1.22 module with no
  Node package or editor extension today (`go.mod:1-3`).
- The product's documented build gate is `go test ./...`; `go build -o gew
  ./cmd/gew` builds the CLI (`README.md:60-68`).
- `README.md:216-233` defines the integration boundary:

  ```text
  The opt-in hybrid backend creates a standards-compliant local `.git`
  repository for editors and Git-aware tools.
  ...
  In hybrid mode, `.git` owns local history while `.gew` remains the remote
  synchronization journal.
  ...
  All network access still uses REST APIs; `gew` does not invoke Git transport,
  hooks, filters, credential helpers, SSH, or smart HTTP.
  ```

- Pull and Push already expose editor-friendly progress output:
  `README.md:192-194` documents `--progress=auto|always|never`; progress and
  summaries go to stderr.
- `README.md:16-18` states that GitLab and Bitbucket push remain disabled until
  live concurrency safety verification passes. The extension must surface CLI
  failures rather than bypassing provider gates.
- `.gitignore` currently ignores `/gew` and `/dist/`. Package the VSIX under the
  existing ignored root `dist/` directory.
- Existing implementation plans live under `advisor-plans/`; Plans 001–010 are
  DONE. Continue numbering with 011 rather than creating a new plan directory.
- Current commits use short imperative subjects, for example `Update README.md`.
  Match that style.

## Target user experience and stable IDs

The npm package must be `gew-vscode`, version `0.1.0`, with extension identifier
`ibrahimois.gew-vscode`. Marketplace publication is not part of this plan; the
publisher identifier is used for deterministic local VSIX installation.

The public commands must be stable:

| Command ID | User-facing title | Behavior |
|---|---|---|
| `gew.enableRepository` | `GEW: Enable for Current Repository` | Opt the selected hybrid repository into GEW SCM actions. |
| `gew.disableRepository` | `GEW: Disable for Current Repository` | Remove the selected repository from the local opt-in set. |
| `gew.pull` | `GEW: Pull via REST` | Run `gew pull --progress always`. |
| `gew.push` | `GEW: Push via REST` | Run `gew push --progress always`. |
| `gew.sync` | `GEW: Sync via REST` | Run Pull, then Push only after successful Pull. |

Repository opt-in must be stored in extension `globalState` under the versioned
key `gew.enabledRepositoryPaths.v1`, using canonical absolute paths. Do not add
markers below `.gew`, `.git`, or `.vscode`. Contribute a machine-scoped string
setting `gew.executablePath`, default `gew`, so users may select an absolute
binary without committing or Settings-Syncing a machine-specific path.

The built-in Git Sync command cannot be replaced or hidden through VS Code's
public API. The extension adds separate actions and must never register or
shadow a `git.*` command.

## Commands you will need

Run extension commands from `extensions/vscode/` unless stated otherwise. The
extension package does not exist yet, so Step 1 establishes these scripts as its
required verification interface.

| Purpose | Command | Expected on success |
|---|---|---|
| Locked install | `npm ci` | exit 0; lockfile does not drift |
| Compile | `npm run compile` | exit 0; JavaScript emitted under `out/` |
| Typecheck | `npm run typecheck` | exit 0; no diagnostics |
| Lint | `npm run lint` | exit 0; no warnings or errors |
| Unit tests | `npm run test:unit` | exit 0; all tests pass |
| Extension-host tests | `npm run test:integration` | exit 0; all tests pass |
| Full extension gate | `npm test` | all extension gates pass in sequence |
| Package | `npm run package` | creates `../../dist/gew-vscode-0.1.0.vsix` |
| Go regression | `cd ../.. && go test ./...` | all Go tests pass |
| Go quality | `cd ../.. && go vet ./... && test -z "$(gofmt -l cmd internal)"` | exit 0 |
| Install locally | `code --install-extension ../../dist/gew-vscode-0.1.0.vsix --force` | successful installation |

Use the official VS Code Extension API documentation for command and menu
contributions, `scm/title`, context keys, Workspace Trust, extension tests, and
VSIX packaging. Invoke `@vscode/vsce` from the package script; do not require a
global `vsce` installation.

## Scope

**In scope** (the only implementation paths to create or modify):

- `extensions/vscode/package.json`
- `extensions/vscode/package-lock.json`
- `extensions/vscode/tsconfig.json`
- `extensions/vscode/eslint.config.mjs`
- `extensions/vscode/.gitignore`
- `extensions/vscode/.vscodeignore`
- `extensions/vscode/README.md`
- `extensions/vscode/src/extension.ts`
- `extensions/vscode/src/repositoryRegistry.ts`
- `extensions/vscode/src/gewRunner.ts`
- `extensions/vscode/src/operations.ts`
- `extensions/vscode/test/unit/**/*.test.ts`
- `extensions/vscode/test/integration/**/*.test.ts`
- `extensions/vscode/test/fixtures/**`
- `README.md` (editor integration and local installation documentation only)
- `advisor-plans/README.md` (status update only)

Generated `extensions/vscode/node_modules/`, `extensions/vscode/out/`,
`extensions/vscode/.vscode-test/`, and root `dist/` artifacts must remain
ignored and uncommitted.

**Out of scope**:

- Modifying any `.gew/` workspace content, model, receipt, object, or journal.
- Modifying `.git/`, Git remotes, Git hooks, or Git configuration.
- Calling Git transport, provider CLIs, SSH, credential helpers, or smart HTTP.
- Replacing, intercepting, hiding, or invoking VS Code's private `git.*` APIs.
- Implementing a full Source Control provider, status, diff, stage, commit,
  branch, merge, or conflict UI. VS Code's built-in Git extension continues to
  own local SCM for hybrid repositories.
- Supporting the default backend in the Source Control tab. It has no `.git`;
  a future full GEW SCM provider would be a separate plan.
- Passing tokens, provider URLs, usernames, or author details through command
  arguments. The CLI must use its existing profile/environment behavior.
- Enabling GitLab or Bitbucket push, changing provider safety gates, or changing
  Pull/Push semantics in Go.
- Marketplace publication, telemetry, auto-update, release automation, remote
  extension hosts, or organization-wide deployment.
- Adding CI in the first local VSIX iteration.

## Git workflow

- Branch: `advisor/011-vscode-source-control-extension`
- Keep the TypeScript package isolated under `extensions/vscode/`.
- Suggested commits: `Add GEW VS Code extension tests`, then `Add GEW Source
  Control actions`, then `Document VS Code integration`.
- Do not commit generated VSIX, compiled output, test downloads, or dependencies.
- Do not push or open a pull request unless the operator explicitly requests it.

## Steps

### Step 1: Scaffold an isolated TypeScript extension package

Create `extensions/vscode/` as an npm package named `gew-vscode`, version
`0.1.0`, publisher `ibrahimois`, and repository URL
`https://github.com/ibrahimois/gew.git`. Set `engines.vscode` to `^1.125.0`,
`main` to `./out/src/extension.js`, and compile CommonJS targeting ES2022.
Commit `package-lock.json`.

Add compatible, non-wildcard development dependencies for VS Code and Node type
definitions, TypeScript, ESLint, `@vscode/test-electron`, and `@vscode/vsce`.
The lockfile is the reproducibility source of truth.

Define scripts:

- `clean`: remove only generated extension output/test/package files.
- `compile`: compile TypeScript.
- `typecheck`: run `tsc --noEmit`.
- `lint`: lint `src/**/*.ts` and `test/**/*.ts`, treating warnings as errors.
- `test:unit`: compile and run Node's test runner on compiled unit tests.
- `test:integration`: compile and run the isolated VS Code extension-host tests.
- `test`: compile, typecheck, lint, unit test, then integration test sequentially.
- `package`: run `npm test`, then package to
  `../../dist/gew-vscode-0.1.0.vsix` with `vsce`.

Contribute all five command IDs and `gew.executablePath` with machine scope.
Add `onStartupFinished` activation so persisted enablement is restored, plus
explicit command activation events for compatibility and readability.

Contribute `gew.sync` to `scm/title` in the `navigation` group when:

```text
scmProvider == git && gew.hasEnabledHybridRepository && isWorkspaceTrusted
```

Contribute Pull, Push, and Disable to the Source Control title overflow under a
GEW-specific group with the same guard. Enable remains available from the
Command Palette. Use VS Code theme icons; do not ship platform-specific image
buttons in v0.1.0.

Add package-local ignores for `node_modules/`, `out/`, `.vscode-test/`, and
`*.vsix`. Configure `.vscodeignore` so source, tests, TypeScript/ESLint config,
and downloaded test data are excluded while compiled runtime files,
`package.json`, and the extension README remain in the VSIX.

**Verify**:

```sh
cd extensions/vscode
npm ci
npm run compile
npm run typecheck
npm run lint
```

Expected: all commands exit 0 and `out/src/extension.js` exists.

### Step 2: Implement explicit per-repository opt-in and target resolution

Implement `src/repositoryRegistry.ts` as the sole owner of repository
eligibility, selection, canonicalization, and persistence.

Requirements:

1. Canonicalize paths with `path.resolve` and `fs.promises.realpath` when the
   root exists. Use canonical paths for persistence, comparison, locks, and cwd.
2. Persist an array of canonical paths under
   `gew.enabledRepositoryPaths.v1`. Treat malformed state as empty and rewrite
   valid state on the next Enable/Disable operation.
3. Only enable workspace folders containing directories named both `.gew` and
   `.git`. Reject `.gew`-only workspaces with guidance to clone using
   `gew clone --backend git` or migrate a clean workspace with
   `gew migrate --to git`.
4. Resolve operation targets in this order: active editor's enabled workspace
   folder; the sole enabled/open folder; otherwise a Quick Pick among enabled
   open folders.
5. Enable and Disable must select only one folder in multi-root windows, using
   the active editor when unambiguous or a Quick Pick otherwise.
6. Recompute context key `gew.hasEnabledHybridRepository` on activation,
   workspace-folder changes, Enable, and Disable. It is true only when at least
   one currently open enabled hybrid repository still exists.
7. Register and dispose workspace listeners through `ExtensionContext`.

Checking that `.gew` and `.git` are directories is sufficient. Never enumerate,
read, or modify their internal content.

**Verify**: `npm run typecheck && npm run lint` → both exit 0.

### Step 3: Implement a safe, cancellable GEW process runner

Implement `src/gewRunner.ts` so process behavior can be tested independently of
menus and notifications.

Requirements:

1. Use `child_process.spawn(executable, args, { cwd, shell: false, env:
   process.env })`. Never construct a shell command string.
2. Read the executable from `gew.executablePath`. Reject an empty or
   whitespace-only value before spawning.
3. Stream stdout and stderr to one `GEW Source Control` OutputChannel. Prefix
   output with the repository basename and operation phase, but never print
   environment variables, profile files, or credentials.
4. Return a structured result with exit code, signal, spawn error, and cancelled
   state. Treat spawn errors and nonzero exits as failures.
5. Connect VS Code cancellation to child termination. Escalate after a short,
   documented timeout only if the child remains alive; dispose all timers and
   listeners on every completion path.
6. Keep a per-canonical-root in-flight map. Reject overlapping operations for
   one repository with an informational message, while permitting operations
   in different repositories.

**Verify**: `npm run typecheck && npm run lint` → both exit 0.

### Step 4: Register trusted Source Control operations

Implement `src/operations.ts` and `src/extension.ts`.

On activation, create one OutputChannel, repository registry, and process
runner; restore context before command execution; register all five commands;
and place every disposable in the extension context.

Operation behavior:

- Refuse execution when `vscode.workspace.isTrusted` is false. Show a concise
  message and do not spawn GEW.
- Pull arguments are exactly `pull --progress always`.
- Push arguments are exactly `push --progress always`.
- Sync runs Pull first and starts Push only when Pull exits 0 without
  cancellation.
- Use `window.withProgress` at notification location, cancellable, showing the
  repository and current phase.
- On failure, reveal the OutputChannel and show one concise error naming the
  operation and exit status. Do not expose environment or token data.
- On success, show a short completion notification.
- Let the built-in Git extension detect local ref/worktree changes. Do not call
  undocumented refresh commands or private Git APIs.
- Always use the registry's canonical root as cwd.

The primary toolbar command and tooltip must remain `GEW: Sync via REST`, so it
cannot be mistaken for proof that built-in Git Sync was redirected.

**Verify**: `npm run compile && npm run typecheck && npm run lint` → all exit 0.

### Step 5: Add deterministic unit and extension-host tests

Automated tests must use temporary directories and a fixture Node process. They
must not execute the real GEW binary, contact a forge, use a real token, or
change the developer's VS Code profile.

Required unit coverage:

1. Canonical path deduplication and malformed global-state recovery.
2. Hybrid eligibility accepts `.git` plus `.gew` and rejects either missing.
3. Target resolution prefers an active enabled folder, returns a sole enabled
   folder, and requires a pick for multiple candidates.
4. Executable/cwd values containing spaces and shell metacharacters remain
   separate process arguments, proving no shell interpolation.
5. stdout/stderr streaming, successful exit, nonzero exit, and spawn errors.
6. Cancellation terminates the fixture child and returns cancelled state.
7. Per-root locking rejects same-repository overlap but allows different roots.
8. Sync orders Pull before Push and skips Push after failure or cancellation.

Required extension-host coverage:

- activation registers all five commands;
- Enable persists a hybrid fixture and updates selection;
- Enable rejects a `.gew`-only fixture;
- Disable removes the hybrid fixture from selection;
- untrusted execution does not spawn the fixture process;
- isolated launch arguments use temporary `--user-data-dir` and
  `--extensions-dir` paths.

Do not assert internal menu-rendering details; command contributions and context
behavior are the stable test boundary.

**Verify**:

```sh
npm run test:unit
npm run test:integration
npm test
```

Expected: all named cases pass and all commands exit 0.

### Step 6: Document the supported editor integration boundary

Write `extensions/vscode/README.md` covering:

- requirement for a hybrid workspace and system Git for VS Code's local Git UI;
- local VSIX installation and uninstallation;
- Enable/Disable per repository;
- Source Control Pull, Push, and Sync actions;
- machine-local `gew.executablePath`, with `gew` as the portable default and
  `/usr/local/bin/gew` as a macOS example;
- warning that built-in Git Sync remains unchanged;
- Workspace Trust, missing executable, dirty pull, non-hybrid workspace, and
  OutputChannel troubleshooting;
- GEW 0.7.0 GitLab/Bitbucket push safety gates;
- development/test/package commands;
- explicit statement that Marketplace publication and telemetry are absent.

Add a concise `## VS Code` section to the root `README.md` after Workspace
backends. Link to `extensions/vscode/README.md`, explain the hybrid prerequisite,
and keep the REST-only/no-Git-transport invariant explicit.

**Verify**:

```sh
cd ../..
git diff --check
grep -n 'VS Code' README.md extensions/vscode/README.md
```

Expected: no whitespace errors and both documents contain the integration
section.

### Step 7: Package, inspect, and install the local VSIX

Run:

```sh
cd extensions/vscode
npm test
npm run package
npx vsce ls
unzip -l ../../dist/gew-vscode-0.1.0.vsix
```

Expected: one VSIX exists. Its listing includes compiled runtime files,
`package.json`, and README; it excludes source, tests, dependencies, test
downloads, repository `.git`/`.gew` data, and credentials.

Install and confirm identity:

```sh
code --install-extension ../../dist/gew-vscode-0.1.0.vsix --force
code --list-extensions --show-versions | grep '^ibrahimois\.gew-vscode@0\.1\.0$'
```

Expected: successful installation and exact identifier/version output.

Open a disposable hybrid fixture or an operator-approved hybrid repository,
trust it, and run `GEW: Enable for Current Repository`. Confirm the GEW Sync
action appears in Source Control. Do not run live Pull, Push, or Sync without
explicit approval.

### Step 8: Run cross-project regression and acceptance gates

Run all extension and Go gates:

```sh
cd extensions/vscode
npm ci
npm test
npm run package
cd ../..
go test ./...
go vet ./...
test -z "$(gofmt -l cmd internal)"
git diff --check
git status --short
```

Expected: all commands exit 0. Git status contains only intended source,
documentation, lockfile, and advisor-plan changes; no generated artifacts.

Manual non-network acceptance:

1. Standard Git Source Control still shows local changes in a hybrid fixture.
2. GEW toolbar text/tooltip is explicitly `GEW: Sync via REST`.
3. Pull, Push, and Disable are available in Source Control overflow.
4. A non-enabled repository cannot run GEW operations.
5. Multi-root targeting prompts rather than choosing arbitrarily.
6. An untrusted workspace refuses execution.
7. An invalid executable setting produces a concise notification and safe
   OutputChannel diagnostics.

Live forge synchronization is a separate operator-approved acceptance test.
Never create or push a dummy commit merely to complete this plan.

## Test plan

Step 5 defines the automated suite. The highest-value regression boundaries are
explicit repository opt-in, no-shell process execution, Sync sequencing,
cancellation cleanup, per-root concurrency, Workspace Trust, and persistence.
No automated test may depend on forge credentials, a real repository, network
synchronization, or the operator's actual VS Code profile.

The root Go suite must remain green to prove the editor package did not require
changes to CLI/provider safety behavior.

## Done criteria

- [ ] `npm ci` succeeds without lockfile drift in `extensions/vscode/`.
- [ ] `npm test` passes every unit and extension-host case named in Step 5.
- [ ] `npm run package` creates `dist/gew-vscode-0.1.0.vsix`.
- [ ] VSIX inspection excludes source, tests, dependencies, workspace metadata,
      generated test data, and credentials.
- [ ] `code --list-extensions --show-versions` contains
      `ibrahimois.gew-vscode@0.1.0`.
- [ ] Every GEW process uses `spawn` with `shell: false` and canonical cwd.
- [ ] Sync never starts Push after failed or cancelled Pull.
- [ ] Commands refuse to execute in untrusted workspaces.
- [ ] One hybrid repository can be enabled while another remains disabled.
- [ ] No implementation reads or writes GEW internals below `.gew`.
- [ ] No built-in `git.*` command is replaced, shadowed, or invoked privately.
- [ ] Root and extension documentation distinguish GEW Sync from Git Sync.
- [ ] `go test ./...`, `go vet ./...`, Go formatting, and `git diff --check` pass.
- [ ] `git status --short` contains no generated `node_modules`, `out`,
      `.vscode-test`, `dist`, or VSIX artifact.
- [ ] `advisor-plans/README.md` marks Plan 011 DONE only after all checks pass.

## STOP conditions

Stop and report instead of improvising if:

- `extensions/vscode/` or an extension with identifier
  `ibrahimois.gew-vscode` already exists from another implementation.
- The hybrid contract no longer assigns local history to `.git` and remote
  synchronization mappings to `.gew`.
- Source Control actions require overriding built-in `git.*` commands or using
  a private Git extension API.
- The declared VS Code engine does not expose documented command/menu,
  `scm/title`, context-key, Workspace Trust, or global-state APIs.
- Correct targeting requires reading or modifying `.gew` internals.
- Tests require real credentials, a real forge, or live Pull/Push.
- Packaging includes credentials, environment dumps, `.git`, `.gew`, source
  outside the extension package, or the dependency tree.
- The implementation would bypass provider capability/safety gates.
- A verification command fails twice after a reasonable correction.
- Completion requires modifying an out-of-scope path.

## Maintenance notes

- Review process spawning, canonicalization, cancellation, and logs as security-
  sensitive code. Reject shell execution or environment/profile dumps.
- Enabled repositories are machine-local canonical paths. Moving a repository
  intentionally requires enabling its new path.
- Keep `gew.executablePath` machine-scoped unless a security review explicitly
  approves repository-controlled executable selection.
- The context key is window-level; multi-root commands must still resolve or
  prompt for the exact enabled root.
- Adding default-backend Source Control support requires a real GEW SCM provider
  with status/diff/stage/commit semantics and is deliberately deferred.
- Before Marketplace publication, add a dedicated CI matrix, establish the
  publisher account, settle licensing, add release signing/provenance, and
  review telemetry/update policies. Do not treat the local VSIX as a published
  support commitment.
