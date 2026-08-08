# Plan 009: Pull REST manifests and apply only changed paths

> **Executor instructions**: Execute after Plans 006–008. Follow the gates and
> preserve GEW’s REST-only purpose: remote state comes only from forge HTTPS
> REST APIs. Do not invoke Git transport, SSH, provider CLIs, or a server-side
> helper. This plan changes workspace persistence and must stop rather than
> improvise on any recovery ambiguity. Mark Plan 009 `DONE` in the index when
> complete.
>
> **Drift check (run first)**:
> `git diff --stat e8a7b47..HEAD -- internal/workspace internal/cli/cli.go internal/cli/staging.go internal/cli/merge.go internal/cli/workspace_git.go internal/cli/migration.go`
> Baseline SHA-256 prefixes include `5a5caf2` (`cli.go`), `e5f67f7`
> (`staging.go`), and `ad2b007` (`workspace_git.go`). Reconcile Plans 006–008;
> STOP if workspace state/recovery changed independently.

## Status

- **Priority**: P1
- **Effort**: L (four to seven days)
- **Risk**: HIGH — incremental filesystem mutation must be crash-recoverable
- **Depends on**: Plans 006, 007, 008
- **Category**: perf / architecture / migration
- **Planned at**: commit `e8a7b47`, 2026-08-08, plus the fingerprinted working tree

## Why this matters

A one-file remote change currently downloads and reinstalls the complete
repository. The persisted state already contains base commit, path, provider
blob identity, local SHA-256, and mode—the information required to calculate a
delta from one exact-commit REST Tree response. This plan makes clean pulls
proportional to changed paths while retaining a full-snapshot merge fallback.

## Current state

- `internal/cli/cli.go:29-49` stores `BlobSHA`, content hash, mode, base commit,
  and file map in state version 4.
- `internal/cli/cli.go:372-407` downloads/extracts a complete snapshot, scans
  it, replaces all tracked files, scans again, and creates baseline objects.
- `internal/cli/cli.go:1106-1147` deletes every tracked path and recopies the
  complete stage.
- `internal/forge/forge.go:90-95,133-137` exposes exact-revision Tree metadata
  and exact Blob reads through REST adapters.
- `internal/cli/merge.go` owns three-way merge and recovery. It is not replaced
  in the first delta release.
- `internal/workspace/model.go` is the backend-independent metadata package;
  add reusable manifest/plan types there rather than growing `cli.go` further.
- `internal/cli/cli.go:995-1035` provides atomic JSON/file write conventions.

## Target contract

1. State version 5 stores a typed manifest entry per path: local SHA-256,
   provider blob identity, mode, and size. Read versions 1–4 unchanged; every
   explicit write upgrades to v5. No network access is needed for migration.
2. A pure deterministic planner compares the persisted base manifest with the
   new exact-commit REST Tree and returns sorted create/modify/delete/mode
   operations. Provider blob identity equality means content equality only
   within the same provider/repository; never compare identities across remotes.
3. Clean fast-forward pull fetches only created/modified blobs. The generic
   Tree path is mandatory; an optional documented provider compare API may
   supply an equivalent candidate delta only after validating its target HEAD
   and result against Tree metadata.
4. Downloaded changes are staged under `.gew/tmp/pull-<id>` on the workspace
   filesystem, hashed during download/write, and checked against declared size
   where available. No live file changes before the complete plan validates.
5. Apply is journaled in `.gew/pull-journal.json`. Existing tracked files move
   to `.gew/recovery/pull-<id>` before replacements/deletions. Staged files then
   rename into place. State advances last; cleanup occurs after state fsync.
6. On startup/pull, an unfinished journal deterministically rolls back to the
   old base before new network work. Never guess whether to continue.
7. Remote creates colliding with untracked local paths fail without overwrite.
   Local changes detected after remote HEAD moved continue through existing
   merge behavior and full exact snapshot fallback.
8. The Gew backend is the first required incremental path. Hybrid clean pulls
   may use the same downloaded delta to patch the worktree, but if go-git anchor
   creation or merge correctness cannot be preserved, retain its full snapshot
   fallback and report that deferral—do not weaken Git state.
9. A provider Tree inconsistency, Blob mismatch, or apply failure leaves
   `BaseCommit`, manifest, queue, and user files recoverable at the old base.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Planner | `go test -race ./internal/workspace` | all pass |
| Pull | `go test -race -run 'DeltaPull|PullJournal|Pull.*Collision|StateV5|Pull.*Recovery' ./internal/cli` | pass |
| Stress | `go test -race -shuffle=on -count=30 -run 'DeltaPull|PullJournal|Pull.*Recovery' ./internal/cli` | pass |
| Full | `go test -race ./...` | pass |
| Vet/format | `go vet ./... && test -z "$(gofmt -l cmd internal)"` | exit 0 |

## Scope

**In scope**:

- `internal/workspace/manifest.go`, `manifest_test.go` (create)
- `internal/workspace/pull_plan.go`, `pull_plan_test.go` (create)
- `internal/cli/pull_apply.go`, `pull_apply_test.go` (create)
- `internal/cli/cli.go`, `staging.go`, `workspace_bridge.go`
- `internal/cli/main_test.go`, migration/state tests
- `internal/cli/workspace_git.go` and tests only for a safe clean-pull reuse
- `README.md`
- `advisor-plans/README.md` (status only)

**Out of scope**:

- Rewriting three-way merge or conflict presentation
- Rename detection, symlinks, submodules, LFS, empty directories
- Enabling GitLab/Bitbucket push or changing push proof (Plan 010)
- Persistent cross-workspace caches or background daemons
- Git transport, system Git, SSH, or provider CLI subprocesses

## Git workflow

- Branch: `advisor/009-transactional-delta-pull`
- Suggested commits: `test: characterize pull recovery`,
  `feat: persist workspace manifests`, `perf: apply clean pull deltas`.
- Commit the planner/state migration separately from filesystem mutation.

## Steps

### Step 1: Add manifest and planner as pure workspace logic

Move/alias file metadata into `internal/workspace` without changing JSON field
names. Implement a sorted plan builder and validator. Cover empty trees,
create/modify/delete, mode-only changes, provider-ID equality, unsafe paths,
duplicate operations, size changes, and deterministic order.

**Verify**: workspace tests pass with no filesystem or network dependency.

### Step 2: Introduce state v5 with backward reads

Bump the write version, decode v1–v4 fixtures, populate absent size as unknown,
and preserve hybrid fields/queues/history. Add golden JSON tests and prove a
read-only command does not rewrite state while the next explicit mutation does.

**Verify**: `go test -race -run 'StateV5|Migration|Legacy' ./internal/cli ./internal/workspace` passes.

### Step 3: Build and validate a staged delta

After HEAD changes and existing local-safety checks pass, request the new Tree,
build the plan, and fetch only create/modify content. Use Plan 008’s bounded
reader and Plan 007’s streaming/hash sink. Stage on the workspace filesystem.
Before apply, validate exact target HEAD, every planned path, aggregate limits,
hash/size/mode, and untracked collisions.

**Verify**: counting forge tests prove a 10,000-entry Tree with three modified
files performs one Head, one Tree, and three Blob reads—no Snapshot call.

### Step 4: Implement rollback-first transactional apply

Write the complete journal atomically before touching tracked files. Move old
versions to recovery, install staged paths, record phase/progress durably, then
atomically save target state. On injected failure after every filesystem/state
transition, reopen and roll back to byte-identical old files/state. Keep
recovery content until the state write is durable.

**Verify**: table-driven fault injection and 30x race/shuffle recovery tests pass.

### Step 5: Integrate fallback and hybrid behavior

Use delta apply only for clean fast-forward pulls. Dirty/queued pulls keep the
existing merge snapshot path. Reuse the delta for hybrid clean pull only if
go-git worktree/index/anchor tests remain exact; otherwise retain the full
snapshot and document a follow-up rather than expanding this plan unsafely.

**Verify**: all existing pull/merge/abort/migration tests pass unchanged plus
new request-count assertions.

### Step 6: Report and document

Emit plan counts, changed bytes, apply/rollback phases, and fallback reason
through Plan 006. Update limitations from “all pulls require full snapshots”
to the exact clean/merge/hybrid behavior implemented.

**Verify**: run all gates and `git diff --check`.

## Test plan

- Pure manifest plan matrix and deterministic serialization.
- Legacy v1–v4 load and v5 upgrade without network.
- Three-file delta in a large fake tree with exact REST request counts.
- Untracked collision, changed HEAD during planning, blob mismatch/overflow.
- Failure injection before/after each journal, rename, state, and cleanup step.
- Existing dirty merge, conflict continue/abort, queue, and hybrid invariants.

## Done criteria

- [ ] Clean Gew pull downloads and writes only changed paths.
- [ ] Unchanged pull remains one HEAD request from Plan 006.
- [ ] Every interrupted apply rolls back to the old base on reopen.
- [ ] Legacy workspaces load; explicit writes upgrade to v5.
- [ ] Dirty/merge paths retain exact full-snapshot safety.
- [ ] Race, stress, full, vet, format, and diff gates pass.
- [ ] All remote reads remain exact-commit HTTPS REST calls.

## STOP conditions

- A provider cannot return stable exact-commit Tree/Blob identities.
- Incremental apply would overwrite an untracked path or cross filesystems.
- Crash recovery cannot prove byte-identical rollback at every injected point.
- State v5 would discard queue, history, hybrid mappings, or legacy identity.
- Hybrid reuse requires weakening go-git anchor/merge correctness.

## Maintenance notes

Reviewers should focus on journal durability ordering and untracked collisions,
not only the happy-path speedup. Future provider compare APIs are optimizations:
the exact-commit Tree remains the provider-neutral truth and safe fallback.
