# Plan 006: Add an opt-in hybrid `.git` workspace and migrate `.gew` workspaces safely

> **Executor instructions**: Complete Plans 001–005 first. Follow this plan
> step by step, run every verification gate, and confirm the expected result
> before continuing. If a STOP condition occurs, stop and report; do not invent
> a partial `.git` format or weaken REST push safety. When finished, update this
> plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 23f7e926591984afbfb2dc852a88974e16abff62..HEAD -- go.mod go.sum main.go staging.go diff.go merge.go forge.go forge_registry.go main_test.go README.md`
> Changes from Plans 003–005 are expected. Compare the excerpts and invariants
> below with the live provider contract before proceeding. STOP if the remote
> engine no longer exposes exact head, tree/blob, snapshot, commit-detail, and
> one-commit apply operations with stale-head protection.

## Status

- **Priority**: P2
- **Effort**: L
- **Risk**: HIGH
- **Depends on**: `plans/001-provider-core-and-gitea.md`,
  `plans/002-github-adapter.md`, `plans/003-gitlab-adapter.md`,
  `plans/004-bitbucket-cloud-adapter.md`, and
  `plans/005-azure-devops-adapter.md`
- **Category**: migration / architecture
- **Planned at**: commit `23f7e926591984afbfb2dc852a88974e16abff62`, 2026-08-07
- **Status**: DONE (hybrid remains opt-in; live Gitea/GitHub passed, Azure credentials unavailable, GitLab/Bitbucket push-gated)

## Why this matters

The current `.gew` store intentionally reimplements only the local behavior
needed by a REST-only workflow. That keeps the binary small and independent of
Git, but editors and Git-aware tools cannot recognize the workspace, and Gew
must maintain its own index, commit records, diff, and merge lifecycle.

This plan adds a second, opt-in **hybrid** backend. A standards-compliant local
`.git` repository owns the local index, objects, commits, branch, and worktree
history. `.gew` remains mandatory and owns provider identity, the actual remote
head ID, local-to-remote commit mappings, and crash-safe REST push recovery.
No Git transport is introduced: clone, pull, and push continue to use only the
existing `Forge` REST adapters.

The original `.gew` backend remains supported and remains the default until the
hybrid backend has passed migration and live-provider conformance. This is not
a conversion of Gew into a general Git remote helper and not permission to call
the `git` executable at runtime.

## Non-negotiable target model

```text
working files
    |
    +-- .git/  local index, objects, commits, HEAD, local branch
    |
    +-- .gew/  provider identity, actual remote head, export journal,
               local Git OID -> provider commit ID mappings, migration record
                      |
                      +-- existing Forge REST adapter -> hosted repository
```

Ownership rules:

1. `.git` is authoritative for staged content, local commits, local branch, and
   the worktree/index comparison in hybrid mode.
2. `.gew` is authoritative for the provider's real branch-head ID and for
   whether a local Git commit was exported successfully through REST.
3. A local Git OID must never be assumed to equal a provider commit ID. The
   provider may generate different author metadata, timestamps, parents, trees,
   or signatures even when the final files and message match.
4. A successful remote export never rewrites an already-created local commit.
   It records a mapping and advances a private Gew tracking ref only after the
   remote parent and final tree have been verified.
5. Runtime code must not execute `git`, hooks, filters, credential helpers,
   submodule commands, or Git transport. A system Git executable may be used
   only by optional conformance tests to inspect artifacts produced by Gew.
6. Never put Gew state into `.git/config`, commit messages, notes, or user refs.
   `.gew` remains the recoverable synchronization journal.

Use private refs under this namespace:

```text
refs/gew/remotes/<provider>/<escaped-branch>  last local representation whose
                                              remote mapping is confirmed
refs/gew/superseded/<timestamp>               optional preservation point when
                                              pull linearizes queued work
```

Do not use `refs/remotes/origin/*`: the OID there conventionally represents the
actual remote Git object, which the REST adapter may not have imported exactly.

## Current state

- `main.go:50-63` stores provider identity, remote base, file snapshots, and the
  custom local queue in one version-3 state:

  ```go
  type workspaceState struct {
      Version    int
      Provider   ForgeKind
      Remote     RepositoryRef
      Branch     string
      BaseCommit string
      Files      map[string]fileState
      Queue      []string
      History    []string
      LocalHead  string
  }
  ```

- `staging.go:31-47` defines Gew-owned `indexEntry`, `stageIndex`,
  `commitChange`, and `localCommit` records. `commit` updates `state.Files`,
  appends the custom commit ID to `Queue` and `History`, and clears
  `.gew/index.json`.
- `staging.go:320-350`, `staging.go:448-475`, and `staging.go:542-565` persist
  the custom index, content-addressed file snapshots, and commit JSON under
  `.gew/`.
- `main.go:500-631` pushes `state.Queue` sequentially. It checkpoints each
  accepted remote commit by recording `RemoteSHA`, removing one queue entry,
  and advancing `BaseCommit`.
- `main.go:1115-1155` discovers a workspace only through `.gew/state.json` and
  migrates older state versions in memory.
- `forge.go:109-120` is the provider boundary. Preserve it as the only remote
  access path:

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

- `merge.go` performs a line-oriented three-way merge and preserves recoverable
  conflict state in `.gew/merge.json` and `.gew/conflicts/`. Do not replace its
  behavior until hybrid pull has equivalent abort/continue tests.
- Tests are Go `testing` tests, with end-to-end command fixtures in
  `main_test.go` and adapter/contract fixtures in `forge_*_test.go`. Match those
  conventions. The module currently targets Go 1.22.
- At the planned commit, `go test ./...` and `go vet ./...` both pass.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Format | `gofmt -w *.go` | exit 0 |
| Focused tests | `go test -race -run 'GitWorkspace|GitMigration|GitExport|GitPull' ./...` | all pass |
| Full tests | `go test -race ./...` | all pass |
| Static analysis | `go vet ./...` | exit 0, no findings |
| Order independence | `go test -shuffle=on -count=20 ./...` | all 20 runs pass |
| Merge fuzz smoke test | `go test -run='^$' -fuzz=FuzzThreeWayMergeIdentities -fuzztime=5s` | exit 0, no crash |
| Git interoperability | `go test -run GitCLIConformance ./...` | passes when `git` exists; otherwise explicitly skips |
| Dependency audit | `go list -m all` | only the approved local Git engine and its reviewed transitive modules are new |

Normal unit tests must remain hermetic and offline. Live-provider tests remain
opt-in behind the provider-specific environment variables established by
Plans 001–005.

## Suggested executor toolkit

- Read the official `gitrepository-layout`, `gitformat-index`,
  `gitformat-pack`, `git-commit-tree`, and `git-check-ref-format`
  documentation before accepting a local engine.
- Evaluate `github.com/go-git/go-git/v5` first because it is pure Go and does
  not require the `git` executable. Treat it as a candidate, not as permission
  to bypass Step 1's conformance gate.
- Use the system `git` command only in tests to run read-only checks such as
  `git fsck --full`, `git status --porcelain`, `git log`, and `git cat-file`.

## Scope

**In scope**:

- `go.mod` and `go.sum` for one reviewed pure-Go local repository engine
- `main.go`, `staging.go`, `diff.go`, and `merge.go`
- `forge.go` only if a provider-neutral immutable snapshot helper is required;
  do not add local Git concepts to the `Forge` interface
- `workspace.go` (new: backend-neutral local workspace contract)
- `workspace_gew.go` (new: adapter around current `.gew` behavior)
- `workspace_git.go` (new: standards-compliant hybrid backend)
- `git_export.go` (new: Git commit/tree diff to `RemoteChange` translation,
  mapping, tracking refs, and recovery journal)
- `migration.go` (new: dry-run and transactional v3-to-hybrid migration)
- `workspace_git_test.go`, `git_export_test.go`, and `migration_test.go` (new)
- existing tests that must become backend-parameterized
- `README.md` and CLI help
- `plans/README.md` status only when execution is complete

Names may be adjusted to match the live code after Plans 003–005, but the
separation between local backend, REST export, and migration must remain.

**Out of scope**:

- Replacing or weakening any forge adapter's expected-head/concurrency policy.
- Git wire protocols, SSH, smart HTTP, custom remote-helper subprocesses, or
  shelling out to `git` at runtime.
- Importing complete remote history, tags, signed commits, notes, submodules,
  LFS, alternates, worktrees, sparse checkout, or partial-clone promisor logic.
- Pretending provider-created commits have the same SHA as local commits.
- Adopting an already-existing `.git` repository. Initial migration must refuse
  that case; a future `gew adopt` feature needs a separate plan.
- Reverse migration from hybrid to the original `.gew` backend.
- Deleting legacy `.gew` objects/commits after migration. Cleanup requires a
  later retention plan after real-world recovery has been proven.
- Making hybrid mode the default before all providers enabled in the registry
  pass the live tests in Step 9.
- Changing the existing documented handling of symlinks, executable-only
  changes, empty directories, or renames.

## Git workflow

- Branch: `next/hybrid-git-workspace` from completed Plans 001–005.
- Follow the repository's conventional commit style, for example
  `feat: add GitHub REST adapter`.
- Keep the engine spike, backend extraction, hybrid implementation, migration,
  and documentation in separate logical commits.
- Do not push or open a PR unless the operator instructs it.

## Steps

### Step 1: Prove a standards-compliant pure-Go `.git` engine

Create a test-only spike before routing any command through a new dependency.
Evaluate the candidate engine for these operations without network access and
without invoking `git`:

1. initialize a repository at a temporary path;
2. create and switch a branch with an arbitrary valid Unicode/path-safe name;
3. write binary and text blobs, trees, and commits with controlled parent,
   author, committer, message, and timestamp;
4. populate/read the index, including deletion and an empty file;
5. detect staged and unstaged changes without modifying the worktree;
6. read commit parents and diff two commit trees into create/update/delete;
7. update a private ref with expected-old-OID protection or implement that
   compare-and-swap safely using lock files;
8. reopen after process exit and reproduce the same results;
9. never execute hooks, filters, credential helpers, or transport.

Add `GitCLIConformance` tests that, when `git` is present, run `git fsck
--full`, inspect refs and commits, and ensure `git status --porcelain` agrees
with Gew's view. The production binary must not import an `os/exec` Git helper;
keep any `exec.Command("git", ...)` in `_test.go` files only.

Review the dependency's license, maintained release line, Go compatibility,
open security advisories, and transitive module list. Record the chosen version
in a comment beside the backend construction only when a non-obvious upstream
workaround is required; do not add a general dependency essay to source.

**Verify**: `go test -race -run 'GitEngine|GitCLIConformance' ./...` -> all
engine tests pass; CLI conformance passes or skips only because `git` is absent.

**STOP** if no pure-Go engine can produce a repository that passes `git fsck`
and stable index/ref tests without runtime subprocesses. Do not implement Git's
object database or index format from scratch inside this plan.

### Step 2: Introduce explicit local workspace backends without changing behavior

Add a `WorkspaceBackend`/`LocalWorkspace` contract in `workspace.go`. It must
represent operations the command layer already performs: status, stage, reset,
diff, commit, log, worktree snapshot, pending commits, remote-snapshot merge,
merge continue/abort, and post-export checkpointing. Prefer domain types over
one method per CLI flag. Do not put `Forge`, HTTP payloads, or provider names in
this interface.

Move or wrap the existing `.gew` index/objects/commits behavior behind
`workspace_gew.go` with byte-for-byte compatible persisted state and unchanged
CLI output. Route backend selection through a new versioned state field:

```go
type WorkspaceBackendKind string

const (
    WorkspaceGew WorkspaceBackendKind = "gew"
    WorkspaceGit WorkspaceBackendKind = "git"
)
```

Version-1/2/3 states with no backend field must normalize to `gew`. Do not
rewrite them merely because they were read. Unknown values fail closed.

Parameterize current end-to-end local-workflow tests so the unchanged Gew
backend remains characterized before the hybrid implementation is added.

**Verify**: `go test -race ./...` -> every existing test passes; fixture bytes
for version-3 state, index, objects, queue, history, and merge recovery are
unchanged except for state written by an explicit version-4 mutation.

### Step 3: Define version-4 hybrid state, mappings, refs, and recovery invariants

Bump workspace state only after Step 2 is green. In hybrid mode, retain
provider identity, branch, and the actual provider `BaseCommit`, but stop using
`Files`, `Queue`, `History`, and `LocalHead` as authorities. Preserve those
fields for reading/migration of older backends.

Add hybrid metadata sufficient to identify:

- checked-out local branch ref;
- private Gew remote-tracking ref;
- actual provider branch head;
- last confirmed local OID/provider commit-ID pair;
- prepared/in-flight export with expected provider parent, local OID, message,
  normalized change digest, and target branch;
- completed export receipts, one per local OID;
- migration source version and retained legacy-data location.

Store receipts as individually atomic files under `.gew/exports/` rather than
growing one unbounded JSON map. Keep the minimal current pointers in
`state.json`. Define startup validation that cross-checks state, the tracking
ref, and its receipt. A mismatch must enter deterministic reconciliation; it
must not silently advance either side.

Use `.git/info/exclude` to exclude `/.gew/` without editing the user's tracked
`.gitignore`. Validate and escape provider/branch components before constructing
private ref names. Reject ref traversal, lock-file suffixes, control characters,
and names invalid under Git's ref rules.

**Verify**: `go test -race -run 'StateV4|GitRef|ExportReceipt|Recovery' ./...` ->
fixtures cover old-state defaulting, valid refs, hostile refs, torn state/ref
updates in every ordering, duplicate receipts, and unknown future versions.

### Step 4: Implement hybrid local add, reset, diff, commit, log, and status

Implement `workspace_git.go` using only the approved local engine. In hybrid
mode:

- `gew add` writes exact file bytes to the real Git index;
- `gew reset` restores index entries from `HEAD` without changing worktree;
- `gew diff` compares worktree to index;
- `gew diff --staged` compares index to `HEAD`;
- `gew commit` writes one real local Git commit and advances the current branch;
- `gew log` walks local Git commits and annotates exported commits from `.gew`
  receipts;
- `gew status` reports staged, unstaged, conflict, and pending-export state.

Continue rejecting `.git/**` and `.gew/**` pathspecs. Do not execute clean/smudge
filters, hooks, submodules, or external diff drivers found in configuration.
Preserve binary bytes and empty files. Keep the existing limitations for
symlinks, modes, empty directories, and rename presentation unless a separate
plan expands them.

Add user identity configuration for new local Git commits. Resolve it in this
order: explicit command flags where provided, `GEW_AUTHOR_NAME` /
`GEW_AUTHOR_EMAIL`, then safe values in local `.git/config`. Never copy forge
tokens or account emails implicitly. If identity is absent, fail before staging
or refs are mutated and explain how to supply it.

**Verify**: `go test -race -run 'GitWorkspace|GitCLIConformance' ./...` -> both
backends pass the shared add/reset/diff/commit/log/status cases; Git CLI
inspection agrees with hybrid status and history.

### Step 5: Translate local Git commits into REST commits without equating IDs

Implement `git_export.go`. The private tracking ref identifies the last local
commit whose provider mapping is confirmed. Before push:

1. require that tracking commit to be an ancestor of the checked-out branch;
2. enumerate pending commits in deterministic first-parent order;
3. reject octopus merges and unsupported graph shapes before any remote write;
4. diff each pending commit's tree against its first parent into normalized
   `RemoteChange` values;
5. set `ExpectedHead` to the actual provider ID from state/previous receipt;
6. call exactly one `ApplyCommit` per local commit;
7. verify the returned parent/head/tree through the existing generic engine;
8. atomically record the local-OID/provider-ID receipt;
9. advance the private tracking ref with expected-old-OID protection;
10. update state and continue to the next commit.

A local merge commit may be exported as a linear provider commit containing its
final first-parent tree diff. Record that loss of second-parent topology in the
receipt. Do not claim the provider contains the same merge DAG.

Retain Plan 001's ambiguous-response behavior. Persist the prepared export
before transmission. On restart or a lost response, reconcile the provider
commit's expected parent, message, path set, and final bytes. Create no second
remote commit until the first attempt is proven absent. Test crashes after each
of the ten transitions above.

Provider limitations continue to apply: for example, a provider that cannot
initialize an empty repository must refuse before creating remote objects and
must leave every local commit pending.

**Verify**: `go test -race -run 'GitExport|Ambiguous|PartialPush|StaleHead' ./...`
-> tests prove one local commit creates at most one remote commit, local and
remote IDs may differ, partial queues resume exactly once, and stale heads
never overwrite remote work.

### Step 6: Import remote snapshots as valid local anchor commits

Add a provider-neutral snapshot importer used by hybrid clone and pull. It must
pin the provider branch to an immutable head, obtain the exact tree/bytes
through `Forge`, and create a real local Git commit representing that snapshot.
This is a **local representation**, not a claim to reproduce the provider's raw
commit object.

Use deterministic synthetic identity and message metadata that clearly include
the provider kind and abbreviated remote ID without embedding server URLs or
credentials. Store the actual provider ID only in `.gew` state/receipt. Point
the private tracking ref and initial local branch to this anchor. For an empty
repository, create an unborn local branch and an all-zero/empty-base mapping
rather than a fake remote commit.

Add `gew clone --backend git ...`; keep the default `gew` until Step 9. Hybrid
clone must construct `.git` in a temporary sibling directory, validate it, then
rename it into place. It must never leave a partial `.git` that other tools can
mistake for a valid repository.

**Verify**: `go test -race -run 'GitClone|GitSnapshot|GitCLIConformance' ./...`
-> text/binary/empty files, empty repositories, unusual safe paths, moving
branches, failed downloads, and interrupted initialization all pass; `git fsck`
accepts every completed fixture and no failed fixture leaves `.git` behind.

### Step 7: Preserve pull, conflict, continue, and abort semantics

Implement hybrid pull without silently rebasing or discarding local commits.
For a clean branch, import the new provider snapshot as a local anchor whose
parent is the previous tracking commit, then advance the worktree, local branch,
tracking ref, receipt, and `BaseCommit` through a recoverable journal.

When pending local commits exist and the provider advanced, preserve current
Gew behavior for the first hybrid release:

1. keep the old local tip at `refs/gew/superseded/<timestamp>`;
2. import the remote snapshot as a new tracking anchor;
3. three-way merge old tracking tree, old local-tip tree, and new remote tree;
4. on success, create one new linear local commit parented by the new tracking
   anchor and containing the merged result;
5. on conflict, preserve enough `.gew` state for `merge --continue` and
   `merge --abort` to restore both `.git` refs/index and worktree exactly.

This deliberately mirrors the existing queue-supersession model. Native
two-parent pull merges and arbitrary user-created Git DAG export are deferred
until the linear bridge is proven. `pull --ff-only` must retain its current
refusal behavior.

**Verify**: `go test -race -run 'GitPull|GitMerge|MergeAbort|MergeContinue' ./...`
-> fast-forward, unrelated edits, overlapping text conflicts, binary conflicts,
queued work, abort at every journal phase, and process restart all preserve the
expected refs, index, worktree, receipts, and actual remote ID.

### Step 8: Add transactional `gew migrate --to git`

Add:

```text
gew migrate --to git --dry-run
gew migrate --to git [--author-name NAME --author-email EMAIL]
```

The first migration release accepts version-1, version-2, and version-3 `gew`
backend workspaces that meet all of these preconditions (the release gate
confirmed that v0.3.0 persists version 2):

- no `.git` path already exists;
- no merge is active;
- staging index is empty;
- worktree is clean relative to `state.Files`;
- every queued commit record and referenced object is readable and hash-valid;
- configured credentials remain outside the workspace;
- the current provider head still equals `BaseCommit`, or the provider is
  verifiably empty.

`--dry-run` performs every read, graph reconstruction, identity, capability,
space, path, and remote-head check but writes nothing.

For actual migration:

1. create a migration manifest containing source version, state digest, queue
   order, object digests, remote head, destination backend, and phase;
2. fetch the exact `BaseCommit` snapshot and create its local anchor;
3. replay queued `.gew` commits in order as real local Git commits using their
   messages, timestamps, file changes, and immutable object bytes;
4. record old Gew commit ID -> new local Git OID mappings in the manifest;
5. validate the resulting tip tree equals `state.Files` and worktree bytes;
6. run internal integrity checks and optional Git CLI conformance;
7. atomically install `.git` and version-4 hybrid state;
8. retain the old index, objects, commits, history, and source state under
   `.gew/legacy/v<source-version>-<migration-id>/` with a checksum manifest;
9. reopen the workspace from disk and rerun status/tree/ref/mapping validation
   before reporting success.

Existing pushed and superseded custom history need not be converted into the
new Git graph because the current state may not retain enough historical tree
metadata to reproduce it exactly. It must remain readable in the checksummed
legacy archive. Queued, unpushed commits must be replayed without squashing.

Use temporary sibling paths and explicit journal phases so interruption before
installation leaves the original workspace active, while interruption after
installation resumes validation or rolls back using the retained source state.
Never overwrite an existing `.git`; never delete legacy data in this plan.

**Verify**: `go test -race -run 'GitMigration' ./...` -> fixtures cover zero,
one, and many queued commits; create/update/delete and binary changes; empty
remote; differing local/remote IDs; corrupt objects; stale remote; staged,
dirty, and merging workspaces; existing `.git`; disk/write failures at every
journal phase; restart; dry-run; and checksum validation.

### Step 9: Run cross-backend and live-provider conformance

Run the same command workflow against the original and hybrid backends for
every provider enabled after Plans 001–005:

1. clone an exact branch;
2. stage exact contents and commit multiple local changes;
3. stop and restart between commits;
4. push and verify one remote commit per local commit;
5. lose a response after server acceptance and reconcile without duplication;
6. race an unrelated and same-file remote commit;
7. pull cleanly and with queued changes;
8. continue and abort text/binary conflicts;
9. create a new branch where the provider supports it;
10. exercise the provider's empty-repository behavior;
11. migrate a native Gew workspace with pending commits, then complete push;
12. inspect `.git` with `git fsck`, status, log, and tree commands.

Keep ordinary tests offline. Live tests must use disposable repositories and
the provider-specific environment variables/capability gates. Hybrid mode
must remain opt-in if any enabled write-capable provider cannot pass crash,
stale-head, or mapping tests.

**Verify**: all focused live suites pass repeatedly, followed by `go test -race
./...`, `go vet ./...`, shuffled tests, fuzz smoke test, and Git CLI
conformance.

### Step 10: Document the two backends and migration boundary

Update README and help with:

- `gew` backend: current no-`.git`, no-third-party-local-Git behavior;
- `git` hybrid backend: real local `.git`, mandatory `.gew` REST bridge;
- why local OIDs can differ from hosted commit IDs;
- which data each directory owns;
- clone and migration commands, preconditions, dry-run, and recovery location;
- explicit statement that no Git transport or runtime `git` executable is used;
- provider-specific empty-repository and branch limitations;
- retained legacy history and lack of reverse migration;
- warning that deleting `.gew` from a hybrid workspace destroys synchronization
  mappings even though local Git history remains readable.

Do not advertise arbitrary Git command compatibility. Say that Git-aware tools
may inspect/edit the local repository, while Gew push initially supports only a
tracking-ancestor, first-parent-exportable history and will fail closed on
unsupported graphs.

**Verify**: every documented command appears in `gew help`; parser tests cover
all examples; the provider capability table distinguishes local backend from
remote adapter behavior.

## Test plan

- Model shared local-workflow tests after existing `main_test.go` command
  fixtures and run them against both backends.
- Add engine interoperability tests in `workspace_git_test.go`; system Git is
  inspection-only and the tests skip when unavailable.
- Add request/translation and crash-transition tests in `git_export_test.go`,
  using the fake Forge patterns from `forge_contract_test.go`.
- Add table-driven version-3 migration and fault-injection tests in
  `migration_test.go`. Assert source checksums and exact queue replay order.
- Cover hostile paths/ref names, binary and empty files, permissions, missing
  identity, corrupt objects, stale heads, ambiguous success, delayed remote
  visibility, new branches, empty repositories, and unsupported DAGs.
- Assert a token-shaped sentinel never appears in `.git`, `.gew`, commit
  messages, refs, config, output, or errors.
- Assert runtime production files contain no subprocess invocation of `git`.
- Run provider live tests only against disposable repositories and never
  delete branches/repos that the test did not create.

## Done criteria

- [x] A pure-Go engine creates `.git` repositories accepted by `git fsck`.
- [x] Production runtime does not execute `git` or use Git transport.
- [x] Existing version-1/2/3 workspaces still operate through the original
      backend with unchanged persisted semantics.
- [x] Hybrid `.git` owns index/objects/commits; `.gew` owns actual remote IDs,
      receipts, and recovery state.
- [x] No code assumes local Git OID equals provider commit ID.
- [x] One pending local commit creates at most one provider commit.
- [x] Stale-head and ambiguous-response behavior remains fail-closed and
      recoverable for every enabled provider.
- [x] Hybrid pull/continue/abort preserve refs, index, worktree, and mappings.
- [x] `migrate --dry-run` writes nothing.
- [x] Successful migration replays every queued commit separately and retains
      checksummed legacy `.gew` data.
- [x] Failed/interrupted migration leaves either the original workspace intact
      or a deterministically resumable installed hybrid workspace.
- [x] Existing `.git` is never overwritten or adopted implicitly.
- [x] Credentials never enter `.git`, workspace `.gew`, commits, refs, output,
      or diagnostics.
- [x] `go test -race ./...`, `go vet ./...`, shuffled tests, fuzz smoke test,
      and Git CLI conformance all pass.
- [x] Opt-in live tests pass for every enabled provider before hybrid mode is
      considered for default status.
- [x] README and help document backend ownership and migration limitations.
- [x] `plans/README.md` status row is updated.

Execution note: `go-git` v5.13.2 is the newest release compatible with the
module's Go 1.22 target and contains the v5.13 security fix. Offline tests cover
Git CLI interoperability, local workflows, ref/receipt validation, stale and
ambiguous exports, pull/merge recovery, and transactional migration. Disposable
Gitea and GitHub branches passed two-commit export, new-branch, clean-pull, and
`git fsck` tests; Gitea additionally exercised two real accepted-but-lost
responses without duplicate commits. Those branches were deleted. GitLab and
Bitbucket hybrid writes inherit their adapters' push gates. Azure remains
fixture-verified because no Azure credential/session was available, so hybrid
mode remains opt-in and is not promoted to the default backend.

## STOP conditions

- No reviewed pure-Go engine can create and maintain a standards-compliant
  `.git` without a runtime subprocess.
- The implementation starts writing a partial/custom format under `.git`.
- Plans 003–005 changed `Forge` so an exact remote head/tree or verified
  one-commit apply is unavailable.
- Safe export requires force-updating a provider branch or silently changing
  `ExpectedHead` after a conflict.
- Local/provider ID mapping cannot recover a crash between remote acceptance,
  receipt persistence, state update, and tracking-ref update.
- Hybrid push would need to claim a provider contains local merge topology that
  its REST API did not create.
- Migration cannot reconstruct every queued commit's final tree from the exact
  remote base and stored objects.
- Migration would overwrite an existing `.git`, discard legacy `.gew` history,
  or mutate files during `--dry-run`.
- A hook, filter, credential helper, submodule, external diff, or transport can
  execute implicitly from untrusted repository configuration.
- Any credential can enter Git config, objects, refs, logs, or diagnostics.
- A step's verification continues to fail after two reasonable correction
  attempts.

## Maintenance notes

The hybrid backend is a foreign-VCS bridge: local Git commits are exported to a
forge REST API that may create different commits. Reviewers should scrutinize
every place that crosses the local-OID/provider-ID boundary and every multi-file
state/ref transition.

Do not remove the original backend or legacy migration archives based only on
unit tests. A later plan may consider defaulting new clones to hybrid, adopting
existing `.git` repositories, importing exact remote history, native two-parent
local merges, garbage collection, or reverse migration. Each requires its own
compatibility and recovery design.

When adding another forge, run both the forge contract and hybrid export/live
suite before enabling hybrid push. When upgrading the local Git engine, rerun
Git CLI conformance, crash-transition tests, and security review for hooks,
filters, config, filesystem paths, and decompression/object limits.
