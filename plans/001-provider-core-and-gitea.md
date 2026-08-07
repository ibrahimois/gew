# Plan 001: Extract a provider-neutral remote core while preserving Gitea behavior

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If a STOP condition occurs, stop and report; do not improvise.
> When finished, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 2f1a3fc6723ded372aa5f94cf863c6a66b08c866..HEAD -- main.go merge.go staging.go main_test.go README.md`
> Plan-only changes are expected. If any listed source file changed, compare the
> excerpts below with live code before continuing.

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH
- **Depends on**: none
- **Category**: direction / architecture
- **Planned at**: GitHub `2f1a3fc6723ded372aa5f94cf863c6a66b08c866`, Gitea `1a860b47b8b26597ca3c1ab91971c63b621e5baf`, 2026-08-07

## Why this matters

The staging area, local commit queue, diff engine, and merge engine are already
provider-independent, but every remote operation is coupled to one concrete
Gitea client. Adding providers directly to the command handlers would multiply
conditionals across clone, pull, merge, push, login, and recovery. This plan
introduces one strict remote contract, moves current Gitea behavior behind it,
and establishes conformance tests before another provider is added.

The contract must represent differences honestly. In particular, an adapter
must report whether its branch update is an atomic compare-and-swap. The engine
must never silently treat a commit whose parent differs from `ExpectedHead` as
a clean push.

## Current state

- `main.go:31-40` stores only URL, token, and TLS mode in a profile; no provider
  or authentication scheme exists.
- `main.go:48-59` stores Gitea's `owner/repository` identity directly in the
  workspace state.
- `main.go:61-65` defines one concrete `client`.
- `main.go:245-309` hardcodes Gitea login and doctor endpoints.
- `main.go:313-408` parses only `OWNER/REPO` and directly calls Gitea archive,
  branch, and tree endpoints.
- `main.go:520-546`, `main.go:605-674`, and `merge.go:137-170` accept or construct
  the concrete Gitea client.
- `main.go:650-668` builds Gitea-specific `changeFilesRequest` and posts it to
  `/contents`.
- `staging.go:31-47` models local commit changes without remote-specific fields;
  preserve this separation.
- Existing tests use `httptest.Server` and are the pattern for HTTP fixtures.
- The module is Go 1.22 with no third-party dependencies. Keep that property.

Current profile and workspace shapes:

```go
// main.go:31-39
type profile struct {
    URL      string `json:"url"`
    Token    string `json:"token"`
    Insecure bool   `json:"insecure,omitempty"`
}

type workspaceState struct {
    Version    int
    Server     string
    Owner      string
    Repository string
    Branch     string
    BaseCommit string
    // files, queue, and history omitted
}
```

Current push coupling:

```go
// main.go:650-668
payload := changeFilesRequest{
    Branch: state.Branch,
    Message: commit.Message,
    Files: operations,
}
err = c.doJSON(
    http.MethodPost,
    repoAPIPath(state.Owner, state.Repository)+"/contents",
    payload,
    &response,
)
```

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Format | `gofmt -w *.go` | exit 0 |
| Tests | `go test -race ./...` | all tests pass |
| Static analysis | `go vet ./...` | exit 0, no findings |
| Order independence | `go test -shuffle=on -count=20 ./...` | all 20 runs pass |
| Merge fuzz smoke test | `go test -run='^$' -fuzz=FuzzThreeWayMergeIdentities -fuzztime=5s` | exit 0, no crash |

## Suggested executor toolkit

- Go standard library documentation for `context`, `net/http`, `net/url`, and
  `httptest`.
- Live Gitea Swagger from the configured test instance for the existing adapter.
- Do not introduce an SDK in this plan; request/response ownership is part of
  the adapter contract being established.

## Scope

**In scope**:

- `main.go`
- `merge.go`
- `staging.go` only if state migration or recovery metadata requires it
- `main_test.go`
- `README.md`
- `forge.go` (new: provider-neutral types and interface)
- `forge_http.go` (new: bounded HTTP transport and sanitized errors)
- `forge_registry.go` (new: compile-time adapter registration/detection)
- `forge_gitea.go` (new: extracted Gitea implementation)
- `forge_gitea_test.go` (new)
- `forge_contract_test.go` (new reusable behavior suite)

**Out of scope**:

- GitHub, GitLab, Bitbucket, or Azure implementation code.
- Changes to diff or three-way merge algorithms.
- Dynamic shared-library or subprocess plugins.
- Native Git protocol, `.git`, `go-git`, tags, rebase, or historical checkout.
- A new third-party dependency.

## Git workflow

- Branch: `next/multi-forge` or a child branch such as
  `next/provider-core`.
- Use conventional commits, matching `feat: add three-way merge engine` and
  `fix: detect only exact conflict-marker lines`.
- Keep characterization tests, core contract, and Gitea extraction in separate
  logical commits when possible.
- Do not merge to `main` until all current Gitea live tests pass.

## Target contract

Define provider-neutral identifiers instead of passing owner/repository strings:

```go
type ForgeKind string

type RepositoryRef struct {
    Forge      ForgeKind
    Server     string
    Namespace  string // owner, GitLab namespace, or Bitbucket workspace
    Project    string // Azure project; empty where not applicable
    Name       string
    RemoteID   string // provider ID/UUID when required
}

type RemoteFile struct {
    BlobID string
    Mode   uint32
    Size   int64
}

type RemoteChange struct {
    Operation string // create, update, delete
    Path      string
    Content   []byte
    BlobID    string // expected remote blob for update/delete when supported
    Mode      uint32
}

type ApplyCommitRequest struct {
    Repository  RepositoryRef
    Branch      string
    NewBranch   string
    ExpectedHead string
    Message     string
    Changes     []RemoteChange
}

type ApplyCommitResult struct {
    CommitID       string
    ParentIDs      []string
    ConditionalRef bool
}

type ForgeCapabilities struct {
    ArchiveSnapshot bool
    AtomicMultiFile bool
    ConditionalRef  bool
    BranchCreate    bool
}
```

The `Forge` interface must cover only operations already required by commands:

```go
type Forge interface {
    Kind() ForgeKind
    Capabilities() ForgeCapabilities
    Probe(context.Context) error
    ResolveRepository(context.Context, string) (RepositoryRef, RepositoryInfo, error)
    Head(context.Context, RepositoryRef, string) (string, error)
    Tree(context.Context, RepositoryRef, string) (map[string]RemoteFile, error)
    Blob(context.Context, RepositoryRef, RemoteFile) ([]byte, error)
    Snapshot(context.Context, RepositoryRef, string) ([]byte, error)
    CommitDetails(context.Context, RepositoryRef, string) (RemoteCommit, error)
    ApplyCommit(context.Context, ApplyCommitRequest) (ApplyCommitResult, error)
}
```

Names may change if needed, but the semantics may not. `ExpectedHead`, returned
parents, capabilities, and provider-neutral repository identity are required.

## Steps

### Step 1: Freeze Gitea behavior with characterization tests

Move no production code yet. Add request-level tests for login/doctor, clone,
pull, merge-base download, tree pagination, normal push, new-branch push,
empty-repository initial push, partial push checkpointing, and ambiguous push
reconciliation. Assert HTTP method, escaped path, authentication header shape,
JSON request body, and request order. Ensure errors never include token values.

**Verify**: `go test -race ./...` -> all current and new tests pass.

### Step 2: Introduce provider-neutral remote types and capabilities

Create `forge.go`. Convert command-layer and merge-layer inputs to
`RepositoryRef`, `RemoteFile`, and `RemoteChange`. Keep Gitea response structs
out of this file. Represent missing repositories/branches with typed sentinel
errors so commands no longer inspect a Gitea-specific `apiError`.

Define the parent invariant in code comments and tests:

- If `ConditionalRef` is true, the provider guarantees it did not replace an
  unexpected branch head.
- If false, the engine must verify the returned first parent equals
  `ExpectedHead` before declaring the workspace synchronized.
- A parent mismatch is a recoverable concurrent append, not a clean success.
  Persist enough push-recovery state to make retry deterministic and force a
  pull/merge before another push.

**Verify**: `go test -race ./...` -> fake-provider tests cover conditional
success, stale-head rejection, matching-parent nonconditional success,
unexpected-parent recovery, and ambiguous response retry.

### Step 3: Version and migrate profiles and workspace state

Bump `stateVersion` from 2 to 3. Add `Provider`, provider-neutral repository
identity, authentication kind, and optional username. Do not store credentials
inside `.gew/`.

Backward compatibility requirements:

- A version-2 workspace migrates to provider `gitea` with its existing server,
  owner, repository, branch, queue, history, and object snapshots unchanged.
- Existing config profiles without `provider` migrate in memory to `gitea` and
  are rewritten only during a normal config mutation.
- `GEW_SERVER` + `GEW_TOKEN` keep working and accept optional
  `GEW_PROVIDER`, `GEW_AUTH_KIND`, and `GEW_USERNAME` overrides.
- Unknown future state versions fail closed with the existing style of error.

Add `gew login --provider gitea` while preserving the current no-flag behavior
for existing users. Auto-detection must be registry-driven and may not guess a
self-hosted provider solely from its hostname.

**Verify**: `go test -race -run 'Migration|Login|Doctor|Profile' ./...` -> tests
pass and assert no secret enters workspace JSON or error output.

### Step 4: Extract the HTTP and Gitea adapters

Create a bounded shared HTTP requester in `forge_http.go` with context support,
timeout, response-size limits, TLS mode, redirect policy suitable for archives,
and sanitized provider-aware errors. Authentication headers must be added by an
adapter/auth policy, not hardcoded in the generic requester.

Move all Gitea endpoints, payloads, response structs, and `repoAPIPath` /
`archiveAPIPath` helpers to `forge_gitea.go`. The adapter must produce exactly
the characterization requests from Step 1. Register it at compile time in
`forge_registry.go`.

**Verify**: `go test -race -run 'Gitea|EndToEnd|Push|Pull|Merge|Clone' ./...` ->
all pass with unchanged observable CLI behavior.

### Step 5: Route clone, pull, merge, push, login, and doctor through `Forge`

Remove concrete `*client` parameters from command and merge functions. Resolve
the active adapter from workspace/profile state once per command. Staging,
diffing, local commits, and merge functions must not switch on provider names.

Provider-specific language must appear only when the adapter supplies it.
Generic command errors should say `remote API`, `provider`, or the adapter's
display name rather than always `Gitea API`.

After each successful remote commit:

1. Validate the returned commit ID and parent invariant.
2. Refresh head and tree.
3. Persist the local commit's remote ID and dequeue it atomically.
4. Preserve the existing per-commit checkpoint behavior.
5. On a lost response, use `CommitDetails`, `Tree`, and `Blob` to reconcile the
   exact message, parent, path set, and bytes before dequeuing.

**Verify**: `go test -race ./...` and `go test -shuffle=on -count=20 ./...` ->
all pass.

### Step 6: Establish the adapter conformance suite

Create table-driven contract tests that every provider can invoke. Cover:

- repository parsing and canonical identity;
- authentication without credential leakage;
- branch-head lookup and empty repository;
- complete tree pagination and archive/path safety;
- binary blob round trip;
- atomic create/update/delete in one remote commit;
- new branch from an exact base;
- stale expected head;
- changed remote file;
- ambiguous response after the server accepted a commit;
- partial multi-commit queue checkpointing;
- request-size and malformed-response failures.

Split tests into deterministic `httptest` fixtures and opt-in live tests behind
provider-specific environment variables. Normal `go test ./...` must never
contact the network.

**Verify**: `go test -race ./...`, `go vet ./...`, shuffled tests, and the merge
fuzz smoke test all pass.

### Step 7: Document the provider model without promising future adapters

Update README configuration and local-state sections for provider metadata and
the explicit `--provider gitea` form. Add a short adapter contract section and
state that only Gitea is enabled until later plans land. Preserve current Gitea
examples.

**Verify**: every documented command appears in `gew help`; local README image
links still resolve.

## Test plan

- Use existing `main_test.go` end-to-end fixtures as the structural pattern.
- Add a fake forge that can script head changes, accepted-but-lost responses,
  parent mismatches, and malformed data without provider HTTP details.
- Add migration fixtures for every state/config version currently supported.
- Add assertions that a token-shaped sentinel never appears in output/errors.
- Add one opt-in live Gitea contract test against a disposable repository; it
  must create a temporary branch and delete only that branch when complete.

## Done criteria

- [ ] `go test -race ./...` exits 0.
- [ ] `go vet ./...` exits 0.
- [ ] `go test -shuffle=on -count=20 ./...` exits 0.
- [ ] Merge fuzz smoke test exits 0.
- [ ] Existing Gitea clone/add/commit/pull/merge/push live workflow passes.
- [ ] Source outside `forge_gitea.go` contains no Gitea endpoint literals.
- [ ] Staging, diff, and merge code contain no provider-name switches.
- [ ] Version-2 workspaces and existing config profiles migrate without data
  loss.
- [ ] No credentials are stored under `.gew/` or emitted in diagnostics.
- [ ] No third-party dependency was added.
- [ ] `plans/README.md` status row is updated.

## STOP conditions

- Existing Gitea request behavior cannot be characterized deterministically.
- The proposed interface requires provider response structs in staging, diff,
  or merge code.
- Supporting parent-mismatch recovery would require silently discarding working
  files, queued commits, or unstaged changes.
- Any migration loses queue/history/object references or rewrites credentials
  into a workspace.
- A provider-neutral error can expose an authorization header or token.
- The refactor appears to require changing merge semantics; split that into a
  separately reviewed plan instead.

## Maintenance notes

The first three real adapters will reveal whether the interface is too broad or
too narrow. Keep provider capability checks explicit and avoid optional methods
that return `not implemented`; unsupported behavior should be discoverable
before a mutating request. Reviewers should scrutinize state migration,
ambiguous-response reconciliation, and the exact moment a queued commit is
removed. Do not stabilize an external plugin protocol until GitHub, GitLab, and
at least one of Bitbucket/Azure have passed the same live conformance suite.
