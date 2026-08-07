# Plan 007: Harden and modularize the forge adapter contract

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update this plan's status row in
> `docs/plans/README.md` unless a reviewer dispatched you and told you they maintain
> the index.
>
> **Drift check (run first)**:
> `git diff --stat 4e4c30f..HEAD -- forge.go forge_http.go forge_registry.go forge_gitea.go forge_github.go forge_gitlab.go forge_bitbucket.go forge_azure.go forge_contract_test.go forge_gitea_test.go forge_github_test.go forge_gitlab_test.go forge_bitbucket_test.go forge_azure_test.go main.go main_test.go merge.go workspace_git.go git_export_test.go migration_test.go README.md`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding. STOP if a
> changed file alters the forge method signatures, push recovery, snapshot
> semantics, capability gates, or provider constructors described below.

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH
- **Depends on**: Plans 001–006 (all DONE at planning time)
- **Category**: tech-debt / correctness / tests
- **Planned at**: commit `4e4c30f`, 2026-08-07

## Why this matters

The repository already uses the Adapter pattern: five forge implementations
translate provider APIs into one provider-neutral `Forge` contract. The
boundary is now broad enough that read-only providers must implement write and
snapshot methods, several capability flags are descriptive but unused, and
common safety validation is duplicated across adapters and callers. In
addition, the generic HTTP error type currently classifies every 409, 412, and
422 response as a stale branch head, so unrelated validation or branch-policy
errors can be reported as "run pull."

This plan keeps the static, compile-time adapter model and strengthens it with
smaller role interfaces, a validated writer decorator, adapter-local
concurrency classification, a shared snapshot fallback, one registry
descriptor catalog, and executable conformance tests. The resulting change
path for a new provider should be: implement the base reader, optionally
implement native snapshots and safe writes, add one catalog entry, run the
contract suite, and add provider-specific HTTP fixtures.

## Current state

### Repository and verification baseline

- The module is `gew`, targets Go 1.22, and keeps all production code in
  `package main`.
- `README.md:290-298` documents the provider-neutral adapter contract and the
  expected-head/parent safety invariant.
- Normal tests are hermetic and use `httptest.Server`; they must not contact
  live providers.
- At planning time `go test -race ./...`, `go vet ./...`,
  `go test -shuffle=on -count=10 ./...`, and `go test -cover ./...` pass. The
  measured statement coverage is 67.8%.
- The worktree contains untracked `dist/`, `gew`, and `release/` artifacts.
  They are user-owned and outside this plan. Do not delete, overwrite, stage,
  or package them. Build verification must write to `/tmp`, not `./gew`.
- Commit messages use Conventional Commits, for example
  `feat: add GitHub REST adapter` and
  `fix: harden hybrid recovery for v0.4.0`.

### The current interface combines every role

`forge.go:94-120` currently defines five capabilities and requires every
provider to implement identity, connection probing, repository resolution,
reads, snapshots, commit inspection, and writes:

```go
type ForgeCapabilities struct {
    ArchiveSnapshot bool
    AtomicMultiFile bool
    ConditionalRef  bool
    BranchCreate    bool
    Push            bool
}

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

Only `Push` and `BranchCreate` are consulted by command code. GitLab and
Bitbucket advertise `Push: false` but still implement `ApplyCommit`, and test
doubles such as `contractForge` in `forge_contract_test.go:52-64` embed the
whole interface to avoid implementing unrelated methods.

### Transport errors currently make provider-semantic decisions

`forge.go:144-151` maps statuses globally:

```go
func (e *RemoteError) Unwrap() error {
    if e.Status == 404 {
        return ErrNotFound
    }
    if e.Status == 409 || e.Status == 412 || e.Status == 422 {
        return ErrStaleHead
    }
    return nil
}
```

`forge_http.go:116-118` creates `RemoteError` for every non-2xx JSON response.
`main.go:638-646` and `workspace_git.go:1215-1220` treat
`errors.Is(err, ErrStaleHead)` as a confirmed concurrency failure. Therefore a
422 from a GitHub blob/tree request, or a branch-policy rejection whose status
matches the global list, can be misdiagnosed as a changed remote head.

### Validation and result checking are distributed

- `forge.go:169-189` contains `validateApplyResult`, which enforces the parent
  invariant after a write.
- `main.go:638-646` and `workspace_git.go:1215-1220` both call
  `ApplyCommit` and then call `validateApplyResult` separately.
- GitHub (`forge_github.go:331-363`), GitLab
  (`forge_gitlab.go:253-285`), Bitbucket
  (`forge_bitbucket.go:369-379`), and Azure
  (`forge_azure.go:284-305`) independently validate paths, duplicates, and
  operations.
- Gitea (`forge_gitea.go:250-260`) validates paths but forwards operation names
  without the same duplicate/operation checks.

### Synthetic snapshots and registration metadata are duplicated

- Azure (`forge_azure.go:198-233`) and Bitbucket
  (`forge_bitbucket.go:277-310`) both synthesize a ZIP by calling `Tree`, then
  `Blob` for each file.
- `forge_registry.go:9-67` stores factories in one map but stores default
  authentication behavior in a separate switch. Constructors repeat those
  authentication defaults.
- Adding a provider currently requires coordinated edits to the `ForgeKind`
  constants, the factory map, default-auth logic, constructor defaults,
  capability reporting, and tests.

### The intended conformance suite is incomplete

`forge_contract_test.go:9-72` tests the parent invariant, generic status
classification, and a fake stale error. It is not a reusable suite invoked by
all registered adapters. Provider tests are useful but uneven: Gitea has only
resolve/authentication, redaction, and capability tests, while other adapters
have different subsets of parsing, pagination, snapshots, writes, redirects,
and concurrency cases.

### Existing conventions to preserve

- Provider HTTP request/response DTOs remain in their provider's
  `forge_<provider>.go` file and use provider-prefixed names where practical.
- `httpRequester` owns bounded reads, timeouts, redirect credential stripping,
  endpoint sanitization, and secret redaction. Authentication remains an
  adapter-supplied policy.
- The shared engine supplies `ExpectedHead`; a safe write must either perform
  an atomic conditional ref update or return parents that prove the new commit
  extends that exact head.
- GitLab and Bitbucket push remain disabled until their existing live-safety
  gates pass. This refactor must not enable them.
- Gitea, GitHub, and Azure write behavior, one-local-commit/one-remote-commit
  mapping, ambiguous-response recovery, and queue checkpointing must remain
  unchanged.

## Target architecture

Use Go interface segregation and composition; do not introduce inheritance,
dynamic plugins, `init()` registration, or provider switches in command code.
Names may be adjusted to match existing Go naming, but preserve this shape:

```go
// Required for every registered provider.
type RepositoryReader interface {
    Head(context.Context, RepositoryRef, string) (string, error)
    Tree(context.Context, RepositoryRef, string) (map[string]RemoteFile, error)
    Blob(context.Context, RepositoryRef, RemoteFile) ([]byte, error)
}

type Forge interface {
    RepositoryReader
    Kind() ForgeKind
    Capabilities() ForgeCapabilities
    Probe(context.Context) error
    ResolveRepository(context.Context, string) (RepositoryRef, RepositoryInfo, error)
}

// Optional native optimization. The shared engine supplies a Tree+Blob fallback.
type ForgeSnapshotter interface {
    Snapshot(context.Context, RepositoryRef, string) ([]byte, error)
}

type ForgeCommitInspector interface {
    CommitDetails(context.Context, RepositoryRef, string) (RemoteCommit, error)
}

type ForgeCommitWriter interface {
    ForgeCommitInspector
    ApplyCommit(context.Context, ApplyCommitRequest) (ApplyCommitResult, error)
}

type ForgeCapabilities struct {
    BranchCreate bool
    Push         bool
}
```

Use one helper to obtain a writer. It must check `Push`, assert that the
adapter implements `ForgeCommitWriter`, reject unsupported new-branch writes,
and return a decorator that validates every request and result. Command code
must never call a concrete adapter's raw `ApplyCommit` directly.

Use an ordered, explicit registry catalog:

```go
type forgeDefinition struct {
    Kind        ForgeKind
    DefaultAuth AuthKind
    Factory     forgeFactory
}

var forgeDefinitions = []forgeDefinition{
    // one entry per provider, in stable lexical or documented order
}
```

`forgeFromProfile`, provider normalization, available-provider help, and
default authentication must all derive from this catalog. Keep provider-
specific authentication validation in each constructor.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Baseline | `go test ./...` | package `gew` passes |
| Race tests | `go test -race ./...` | package `gew` passes with no race report |
| Adapter-focused tests | `go test -race -run 'Forge|Gitea|GitHub|GitLab|Bitbucket|Azure|Push|Clone|Pull|Merge' ./...` | all selected tests pass |
| Order independence | `go test -shuffle=on -count=20 ./...` | all 20 runs pass |
| Static analysis | `go vet ./...` | exit 0, no findings |
| Coverage signal | `go test -cover ./...` | exits 0; coverage does not fall below 67.8% |
| Format check | `test -z "$(gofmt -l *.go)"` | exit 0, no filenames |
| Build | `go build -o /tmp/gew-plan007 .` | exit 0; no workspace binary written |
| Diff hygiene | `git diff --check` | exit 0, no output |

## Scope

**In scope** (the only production/test/documentation paths to modify):

- `forge.go`
- `forge_http.go`
- `forge_registry.go`
- `forge_gitea.go`
- `forge_github.go`
- `forge_gitlab.go`
- `forge_bitbucket.go`
- `forge_azure.go`
- `forge_contract_test.go`
- `forge_gitea_test.go`
- `forge_github_test.go`
- `forge_gitlab_test.go`
- `forge_bitbucket_test.go`
- `forge_azure_test.go`
- `main.go`
- `main_test.go`
- `merge.go`
- `workspace_git.go`
- `git_export_test.go`
- `migration_test.go`
- `README.md`
- `docs/plans/README.md` only for the final status update
- New files, if the separation is useful:
  - `forge_http_test.go`
  - `forge_registry_test.go`
  - `forge_snapshot.go`
  - `forge_snapshot_test.go`

**Out of scope** (do NOT touch):

- `dist/`, `gew`, `release/`, and release archives/checksums.
- `go.mod` and `go.sum`; this plan needs no new dependency.
- Enabling GitLab or Bitbucket push.
- Changing provider API versions, supported hosts, authentication schemes, or
  workspace/config/state JSON.
- Moving adapters into separate Go packages. That can follow after this
  contract is stable; doing it simultaneously obscures semantic regressions.
- Dynamic Go plugins, subprocess plugins, runtime self-registration, or SDK
  migrations.
- Changing staging, diff, merge algorithms, local Git topology, migration
  behavior, or CLI command syntax.
- Adding a sixth provider.

## Git workflow

- Suggested branch: `refactor/forge-adapter-contract`.
- Use one commit per verified step when practical.
- Match Conventional Commits. Suggested messages:
  - `test: establish forge adapter conformance`
  - `fix: classify stale heads in provider adapters`
  - `refactor: segregate forge adapter roles`
  - `refactor: centralize forge snapshot fallback`
  - `refactor: consolidate forge registration metadata`
- Do not stage the existing untracked build/release artifacts.
- Do not push or open a PR unless the operator explicitly requests it.

## Steps

### Step 1: Freeze current adapter behavior with characterization tests

Before changing production code, fill the highest-risk fixture gaps using the
existing `httptest.Server` style.

1. In `forge_gitea_test.go`, add request-level coverage for `ApplyCommit`:
   assert the exact atomic `/contents` request, Base64 binary content,
   create/update/delete operations, branch/new-branch fields, refreshed head,
   returned parent IDs, and credential redaction. Model the request assertions
   after `TestGitHubApplyCommitUsesGitDatabaseAndNonForceRefUpdate`.
2. Add a Gitea conflict fixture that returns a candidate conflict status from
   the `/contents` endpoint and confirm the current public behavior is
   `errors.Is(err, ErrStaleHead)`. This characterization will be refined in
   Step 2, not deleted.
3. In `forge_github_test.go`, add separate failure fixtures for:
   - a ref-update conflict after blobs/tree/commit were accepted;
   - a 422 from blob or tree creation before any ref update.
   Initially record the current behavior without weakening queue preservation.
   The second case becomes a regression assertion in Step 2.
4. Keep and extend `TestAzureConflictAndValidationPreserveConditionalSafety`
   so it distinguishes a changed head from a policy/validation failure where
   the head remains unchanged.
5. Add or retain explicit tests that GitLab and Bitbucket `Push` remain false
   and that their public `ApplyCommit` methods return `ErrUnsupported` before
   transmission.
6. In `main_test.go` and `git_export_test.go`, ensure both workspace backends
   retain queued/prepared commits after a non-stale provider error and after an
   ambiguous accepted response.

Do not create a generalized fixture abstraction yet. These tests are the
behavioral baseline for the semantic changes that follow.

**Verify**:
`go test -race -run 'Gitea|GitHub|GitLab|Bitbucket|Azure|Push|Ambiguous' ./...`
→ all selected tests pass before production refactoring.

### Step 2: Keep `RemoteError` transport-level and classify concurrency in adapters

1. Change `RemoteError.Unwrap` in `forge.go` so only HTTP 404 unwraps to
   `ErrNotFound`. A bare `RemoteError` with status 409, 412, or 422 must no
   longer satisfy `errors.Is(err, ErrStaleHead)`.
2. Add a small helper that safely extracts an HTTP status from `RemoteError`
   without inspecting unsanitized data. Do not move provider status tables into
   `forge_http.go`.
3. Add a helper for adapters to confirm a suspected stale-head failure:
   re-read the relevant branch with `Head`; only join/wrap `ErrStaleHead` when
   the observed head differs from `ExpectedHead`. Preserve the original,
   already-redacted `RemoteError` in the returned error chain, using
   `errors.Join` or an equivalent multi-unwrapping error.
4. Call this helper only at provider mutation boundaries and only for that
   provider's documented candidate conflict statuses:
   - Gitea atomic contents submission;
   - GitHub existing-branch ref update, not blob/tree/commit creation;
   - GitLab batched commit submission inside the gated unchecked path;
   - Bitbucket source submission inside the gated unchecked path;
   - Azure push/ref update.
5. For new-branch operations, do not label "target branch already exists" or a
   branch-policy rejection as a stale source head. Return the provider error
   unless a read proves the source head changed.
6. If the confirming `Head` read fails or still equals `ExpectedHead`, return
   the original provider error. Do not guess from status alone.
7. Update `TestForgeContractRemoteErrorClassification`: 404 remains
   `ErrNotFound`; generic 409/412/422 explicitly do not imply
   `ErrStaleHead`.
8. Update the Step 1 provider fixtures so a changed head is stale, while the
   same candidate status with an unchanged head remains a provider error.
   Assert that the latter error retains its sanitized endpoint/status details.

**Verify**:
`go test -race -run 'RemoteError|Stale|Conflict|Policy|GitHub|Gitea|Azure' ./...`
→ all selected tests pass; generic 409/412/422 are not stale, and confirmed
provider head changes are stale.

### Step 3: Introduce the role interfaces without breaking callers

Introduce the interfaces from "Target architecture" in `forge.go`, but migrate
the broad `Forge` interface incrementally so every commit remains buildable.

1. Add `RepositoryReader`, `ForgeSnapshotter`, `ForgeCommitInspector`, and
   `ForgeCommitWriter` around the existing method signatures.
2. Make the existing `Forge` interface embed `RepositoryReader`, but keep its
   `Snapshot`, `CommitDetails`, and `ApplyCommit` requirements temporarily.
   Step 4 removes `Snapshot` after every call site uses the selection helper;
   Step 5 removes the writer methods after every push/recovery call site uses
   the validated writer.
3. Reduce `ForgeCapabilities` to actionable `Push` and `BranchCreate` fields.
   Remove `ArchiveSnapshot`, `AtomicMultiFile`, and static `ConditionalRef`:
   - native snapshots are discoverable through `ForgeSnapshotter`;
   - atomic multi-file behavior is mandatory for any enabled writer and belongs
     in its contract tests;
   - conditionality is reported per `ApplyCommitResult`, where it already
     affects validation.
4. Add compile-time assertions beside or near each adapter constructor:
   every adapter implements `Forge`; Gitea/GitHub/GitLab implement
   `ForgeSnapshotter`; all current concrete adapters implement
   `ForgeCommitWriter`, although GitLab and Bitbucket remain capability-gated.
5. Do not narrow command/helper parameters yet. That occurs with the call-site
   migrations in Steps 4 and 5 and avoids an uncompilable intermediate state.

At the end of this step the code must compile and behave exactly as before;
only named role interfaces and simplified capability fields are new.

**Verify**:
`go test -race -run 'Forge|Migration|GitExport|Clone|Pull|Merge|Push' ./...`
→ all selected tests compile and pass with the segregated interfaces.

### Step 4: Centralize snapshot selection and Tree+Blob fallback

Create `forge_snapshot.go` and `forge_snapshot_test.go` unless an equally clear
location already exists.

1. Implement one provider-neutral helper, such as:

   ```go
   func forgeSnapshot(
       ctx context.Context,
       remote Forge,
       ref RepositoryRef,
       revision string,
   ) ([]byte, error)
   ```

2. If `remote` implements `ForgeSnapshotter`, call its native implementation.
   Otherwise:
   - call `Tree` at the exact immutable revision;
   - sort paths lexically for deterministic ZIP output;
   - validate every path with `validateRemotePath`;
   - fetch each file through `Blob` using the returned metadata;
   - create one stable `<repository>-<revision>/` root;
   - preserve executable mode as `0755`, otherwise use `0644`;
   - reject symlinks/submodules through existing tree semantics;
   - enforce the existing `maxRemoteSnapshot` aggregate bound;
   - close the ZIP writer on every exit path without hiding the primary error.
3. Replace every direct `remote.Snapshot` call in `main.go`, `merge.go`, and
   `workspace_git.go` with `forgeSnapshot`.
4. After all call sites use the helper, remove `Snapshot` from the broad
   `Forge` interface. Narrow helper inputs to `RepositoryReader` where that is
   sufficient.
5. Remove the duplicated synthetic `Snapshot` methods and now-unused ZIP/sort
   imports from Azure and Bitbucket. Keep native Gitea, GitHub, and GitLab
   snapshot methods.
6. Convert Azure and Bitbucket snapshot tests to exercise the shared fallback
   through the `Forge` interface.
7. Add fake-reader tests for deterministic ordering, binary bytes, executable
   modes, unsafe paths, blob failure, writer-close failure where injectable,
   and aggregate-size rejection.

**Verify**:
`go test -race -run 'Snapshot|Clone|Pull|Merge|Azure|Bitbucket' ./...`
→ all selected tests pass, and
`rg -n 'func \(.*(azureForge|bitbucketForge)\) Snapshot' forge_*.go`
→ no matches.

### Step 5: Put request and result invariants behind a validated writer decorator

1. Add a provider-neutral request normalizer/validator in `forge.go` or a small
   adjacent file. It must copy rather than mutate caller-owned slices and:
   - require a non-empty branch and trimmed commit message;
   - require at least one change;
   - accept only `create`, `update`, and `delete` operations;
   - normalize and validate paths with `validateRemotePath`;
   - reject duplicate normalized paths;
   - reject a `RepositoryRef.Forge` that is non-empty and differs from the
     selected adapter kind;
   - allow empty file content and an empty `ExpectedHead` because initial
     commits are valid for some providers.
2. Add a helper such as `forgeWriter(remote Forge, newBranch bool)` that:
   - returns `ErrUnsupported` before mutation when `Push` is false;
   - returns `ErrUnsupported` when branch creation is requested but
     `BranchCreate` is false;
   - verifies that a push-enabled adapter implements `ForgeCommitWriter`;
   - wraps the raw writer in a decorator.
3. The decorator's `ApplyCommit` must validate/copy the request before calling
   the provider and call `validateApplyResult` before returning success.
   Provider-specific validation may remain only when it validates API-specific
   fields or response semantics; replace common duplicate/path/operation loops
   with the shared validated request.
4. In `main.go` and `workspace_git.go`, obtain the writer once before the push
   loop. Route `ApplyCommit` and ambiguous-response `CommitDetails` through
   that writer. Remove separate caller invocations of `validateApplyResult`.
5. After every production call site uses `ForgeCommitWriter` or
   `ForgeCommitInspector`, remove `CommitDetails` and `ApplyCommit` from the
   broad `Forge` interface. `Forge` is now the final required base interface
   shown in "Target architecture."
6. Update fake forges in `forge_contract_test.go`, `git_export_test.go`, and
   `migration_test.go` to implement only the roles required by the function
   under test. Remove embedded `Forge` escape hatches used solely to satisfy the
   old broad interface.
7. Keep post-write head refresh, tree verification, prepared journal handling,
   queue checkpointing, and receipt persistence exactly where they are. The
   decorator validates the adapter boundary; it does not own workspace state.
8. Add table-driven contract tests proving:
   - invalid operation, path, duplicate, empty branch/message/change set, and
     mismatched provider fail before the raw writer is called;
   - conditional success is accepted;
   - nonconditional matching parent is accepted;
   - missing commit ID and unexpected/missing parent are rejected;
   - disabled push and unsupported branch creation do not call the raw writer;
   - an adapter error leaves the original error classification intact.
9. Add an end-to-end assertion for each workspace backend that one valid local
   commit results in exactly one raw provider write and one result validation.

**Verify**:
`go test -race -run 'ForgeContract|Push|GitExport|Ambiguous|Partial' ./...`
→ all selected tests pass, and
`rg -n 'remote\.ApplyCommit|validateApplyResult\(' main.go workspace_git.go`
→ no direct command-layer `remote.ApplyCommit` calls and no caller-side result
validation; validation appears only inside the decorator/tests.

### Step 6: Consolidate registration metadata and remove Gitea compatibility shims

1. Replace `forgeFactories` plus `defaultAuthKind`'s provider switch with the
   ordered `forgeDefinitions` catalog from "Target architecture."
2. Add lookup helpers derived only from the catalog. `registeredForgeKinds`,
   `normalizeForgeKind`, `defaultAuthKind`, and `forgeFromProfile` must not
   maintain separate provider lists.
3. Apply default authentication in `forgeFromProfile` before calling the
   constructor. Constructors must still reject unsupported explicit auth kinds
   and provider-specific missing fields such as the Bitbucket Basic username.
4. Remove repeated constructor defaults. Update direct constructor tests to
   pass explicit authentication kinds when they intentionally bypass the
   registry.
5. Add `forge_registry_test.go` with a table for all five definitions. Assert:
   - kinds are non-empty and unique;
   - factories and default auth kinds are non-zero;
   - `registeredForgeKinds` is stable and sorted;
   - empty provider input remains backward-compatible with Gitea;
   - unknown providers list every registered kind;
   - each factory can be constructed with a syntactically valid dummy profile
     without network I/O and returns the declared kind;
   - every push-enabled result implements `ForgeCommitWriter`;
   - `BranchCreate` cannot be true for an adapter with neither a writer nor a
     documented gated writer.
6. In `forge_gitea.go`, remove unused legacy aliases/helpers after updating the
   one old tree-pagination test in `main_test.go` to use `newGiteaForge` and
   `Tree` directly. Remove `client`, `apiError`, `newClient`, `isAPINotFound`,
   the context-free request helpers, old `branchCommit/tree/blob/commit`
   wrappers, `repoAPIPath`, `archiveAPIPath`, and `parseRepository` when `rg`
   confirms no real caller remains.
7. Rename generic Gitea-only DTOs such as `repository`, `branchResponse`,
   `treeEntry`, and `blobResponse` with a `gitea` prefix, and update Gitea/main
   fixtures. Remove the unused `giteaForge.token` field if it remains unread;
   redaction continues through `httpRequester.secrets`.

Do not add `RegisterForge`, `init()` functions, mutable test-time global
registration, or a public plugin API.

**Verify**:
`go test -race -run 'Registry|Profile|Login|Gitea|TreePagination' ./...`
→ all selected tests pass, and
`rg -n '\b(newClient|apiError|isAPINotFound|repoAPIPath|archiveAPIPath|parseRepository)\b' --glob '*.go'`
→ no matches.

### Step 7: Establish the reusable conformance entry points

Turn the accumulated contract cases into small reusable helpers rather than a
single framework that attempts to emulate every provider API.

1. In `forge_contract_test.go`, provide reusable table-driven helpers for:
   - base adapter shape (`Kind`, capabilities, required reader role);
   - validated writer request/result invariants;
   - capability/interface consistency;
   - stale-head vs raw-provider-error classification;
   - secret-free error strings using a sentinel token generated inside tests.
2. In `forge_snapshot_test.go`, provide the reader/fallback snapshot contract.
3. Each provider test file must invoke the applicable shared helpers and retain
   provider-specific HTTP encoding tests. Do not force native archive providers
   and fallback providers through identical HTTP fixtures.
4. Maintain this explicit behavior matrix in tests or adjacent comments:

   | Provider | Reader | Native snapshot | Writer contract | Push enabled |
   |----------|--------|-----------------|-----------------|--------------|
   | Gitea | required | yes | yes | yes |
   | GitHub | required | yes | yes | yes |
   | GitLab | required | yes | gated raw writer | no |
   | Bitbucket | required | fallback | gated raw writer | no |
   | Azure | required | fallback | yes | yes |

5. Normal `go test ./...` must remain offline. Live-provider checks remain
   opt-in and are not required to enable currently gated writers in this plan.

**Verify**:
`go test -race -run 'ForgeContract|Snapshot|Gitea|GitHub|GitLab|Bitbucket|Azure' ./...`
→ every registered provider runs its applicable shared contracts and all
provider-specific fixtures pass.

### Step 8: Document the adapter extension workflow and run final gates

Update `README.md`'s provider adapter section so it matches the implemented
roles and gives a concise checklist for a future provider:

1. Add one `ForgeKind` constant.
2. Implement required identity, probe, repository resolution, and reader
   methods using `httpRequester`.
3. Implement `ForgeSnapshotter` only when a safe exact-revision native archive
   exists; otherwise rely on the shared Tree+Blob fallback.
4. Implement `ForgeCommitWriter` only with atomic multi-file behavior and the
   expected-head/parent invariant; keep `Push` false until live concurrency and
   ambiguous-response tests pass.
5. Add one `forgeDefinition` catalog entry with the default auth kind.
6. Invoke the shared contract helpers and add provider-specific HTTP fixtures.
7. Run the full offline suite and opt-in live tests against disposable repos.

Also update the capability descriptions: remove claims about deleted static
fields, explain that snapshot and writer roles are discovered structurally,
and preserve the current GitLab/Bitbucket safety-gate language.

Run all final gates:

```sh
go test -race ./...
go vet ./...
go test -shuffle=on -count=20 ./...
go test -cover ./...
test -z "$(gofmt -l *.go)"
go build -o /tmp/gew-plan007 .
git diff --check
```

Expected: every command exits 0, coverage is at least 67.8%, no live network is
used, and no workspace artifact is created or overwritten.

Finally inspect `git status --short`. Only intended in-scope source/test/docs
files plus the user's pre-existing untracked `dist/`, `gew`, and `release/`
artifacts may appear. Update the Plan 007 row in `docs/plans/README.md` to `DONE`.

## Test plan

### Core contracts

- Generic HTTP 409/412/422 do not imply `ErrStaleHead`; 404 remains
  `ErrNotFound`.
- A candidate mutation conflict becomes stale only after the adapter confirms
  the branch head differs from `ExpectedHead`.
- Request validation rejects invalid operations, unsafe/duplicate paths,
  empty required fields, and provider mismatches before transmission.
- Result validation accepts conditional success and exact parent extension,
  and rejects missing IDs or unexpected parents.
- Capability gates reject disabled push/new-branch operations before the raw
  writer is called.

### Snapshot contract

- Native snapshot adapters retain their existing exact-revision behavior.
- Azure and Bitbucket use the deterministic shared Tree+Blob ZIP fallback.
- Binary content, executable modes, path safety, deterministic ordering,
  bounded size, and blob failures are covered.

### Provider fixtures

- Gitea: resolve/auth, atomic contents payload, binary encoding, head/parent
  refresh, error redaction, and confirmed stale head.
- GitHub: Git database sequence, non-force ref update, empty repository refusal,
  truncated tree fallback, redirect redaction, true ref conflict, and non-stale
  validation failure.
- GitLab: nested project resolution, pagination, private token, batched payload,
  and public push gate.
- Bitbucket: Basic/Bearer auth, pinned pagination, safe fallback ZIP, multipart
  encoding, redirect/pagination trust, and public push gate.
- Azure: parsing, stable IDs, pinned reads, exact old-object update, initial/new
  branch semantics, true conflict vs policy error, redirect redaction, and API
  versioning.

### Engine regression coverage

- Both local workspace backends retain queue/journal state after provider
  errors.
- Partial multi-commit pushes resume without replaying confirmed commits.
- Ambiguous accepted writes reconcile without duplicate remote commits.
- Clone, pull, merge, migration, and hybrid tree verification use the new
  snapshot/reader roles without changing results.

## Done criteria

All criteria must hold:

- [ ] `go test -race ./...` exits 0.
- [ ] `go vet ./...` exits 0 with no findings.
- [ ] `go test -shuffle=on -count=20 ./...` exits 0.
- [ ] `go test -cover ./...` reports at least 67.8% statement coverage.
- [ ] `test -z "$(gofmt -l *.go)"` exits 0.
- [ ] `go build -o /tmp/gew-plan007 .` exits 0.
- [ ] `git diff --check` exits 0.
- [ ] `RemoteError` no longer maps every 409/412/422 to `ErrStaleHead`.
- [ ] Confirmed provider head changes still satisfy
      `errors.Is(err, ErrStaleHead)`.
- [ ] `Forge` no longer requires snapshots, commit inspection, or writes.
- [ ] `ForgeCapabilities` contains only actionable push/branch-create gates.
- [ ] Every command-layer write goes through the validated writer decorator.
- [ ] Azure and Bitbucket use the shared Tree+Blob snapshot fallback.
- [ ] Registry kind/factory/default-auth metadata has one source of truth.
- [ ] Every provider invokes the applicable shared conformance helpers.
- [ ] GitLab and Bitbucket push remain disabled.
- [ ] No config/workspace/state JSON or CLI syntax changes.
- [ ] No new dependency and no file outside Scope is modified.
- [ ] Pre-existing `dist/`, `gew`, and `release/` artifacts are untouched.
- [ ] `docs/plans/README.md` marks Plan 007 `DONE`.

## STOP conditions

Stop and report back; do not improvise if:

- In-scope code drift changes the parent invariant, push journal ordering,
  snapshot format consumed by extraction, capability gates, or adapter API
  endpoints described in this plan.
- Any normal test attempts to contact a live provider.
- A provider's candidate conflict response cannot be distinguished from a
  policy/validation error by confirming the current head. Keep the error raw
  and report the ambiguity rather than restoring global status classification.
- The interface split appears to require a workspace/config state-version bump
  or JSON migration.
- Central validation would reject an existing valid operation such as an empty
  file or a provider-supported initial commit.
- A shared snapshot fallback cannot preserve exact revision, binary bytes,
  executable mode, path safety, and the current response-size bound.
- GitLab or Bitbucket would need `Push: true` for tests to pass.
- The change requires moving packages, adding a dependency, changing API
  versions, or modifying an out-of-scope file.
- Any credential-shaped test sentinel appears in an error, log, snapshot,
  workspace state, or committed fixture.
- A verification command fails twice after a reasonable scoped correction.

## Maintenance notes

- Reviewers should scrutinize the exact point at which `ErrStaleHead` is added,
  the moment a queued commit is removed, and whether the original sanitized
  provider error remains available for diagnosis.
- Capability booleans and Go interface satisfaction must not become competing
  sources of truth. `Push` is the safety/product gate; the writer interface is
  the structural implementation. Tests must enforce their allowed
  combinations.
- A native snapshot method is an optimization, not a semantic alternative. It
  must return the same exact revision and safe archive shape as the fallback.
- Keep provider-specific payload structs and response interpretation in the
  adapters. Only invariants shared by every provider belong in the decorator.
- Moving adapters to `internal/forge/<provider>` packages is intentionally
  deferred. Reconsider it only after this contract has shipped without
  regressions; package extraction should then be mechanical.
- Do not stabilize an external plugin protocol until additional providers and
  live conformance reveal a need that the compile-time catalog cannot satisfy.
