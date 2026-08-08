# Plan 004: Fall back to Tree+Blob when native archives fail

> **Executor instructions**: Execute after Plan 002. Follow all gates and STOP
> rather than weakening exact-revision or archive-extraction safety. Mark Plan
> 004 `DONE` in `advisor-plans/README.md` when complete.
>
> **Drift check (run first)**:
>
> ```sh
> git diff --stat e8a7b47..HEAD -- \
>   internal/forge/snapshot.go internal/forge/snapshot_test.go \
>   internal/forge/gitea internal/forge/github internal/forge/gitlab \
>   internal/cli/cli.go internal/cli/workspace_git.go internal/cli/main_test.go README.md
> shasum -a 256 internal/forge/snapshot.go internal/forge/gitea/gitea.go \
>   internal/cli/cli.go internal/cli/workspace_git.go
> ```
>
> Pre-Plan-002 v0.5.0 SHA-256 prefixes are `8926e62`, `2664ae8`, and
> `5f9425a` for the first three relevant files. Reconcile Plan 002 changes;
> STOP if snapshot semantics changed elsewhere.

## Status

- **Status**: DONE
- **Priority**: P1
- **Effort**: M (one to two days)
- **Risk**: MED — fallback must preserve exact bytes, modes, and revision
- **Depends on**: Plan 002
- **Category**: bug / reliability
- **Planned at**: commit `e8a7b47`, 2026-08-08, against published v0.5.0 source

## Why this matters

Fresh Gitea clones failed during the release because the native archive
endpoint reset or exceeded the fixed deadline even though Head, Tree, and Blob
APIs remained usable. Gew already has a safe Tree+Blob snapshot builder, but
`forge.Snapshot` never reaches it when an adapter advertises a native archive.
Native archives should remain the fast path while reader reconstruction acts
as a bounded exact-revision fallback.

## Current state

- `internal/forge/snapshot.go:17-22` returns the native snapshot result directly
  whenever `ForgeSnapshotter` is implemented; any error aborts.
- `readerSnapshot` at lines 24-110 already sorts paths, rejects unsafe objects,
  preserves executable mode, bounds aggregate data at 1 GiB, and emits the ZIP
  shape consumed by `extractArchive`.
- Gitea's native archive is `gitea.go:207-209`; GitHub and GitLab also have
  native snapshots. Azure and Bitbucket already use reader fallback.
- `internal/cli/cli.go:179-205` clone requests Snapshot and Tree separately;
  pull and hybrid flows also consume the shared snapshot wrapper.
- README calls a native snapshot an optimization rather than a semantic
  alternative, so fallback is consistent with documented architecture.

## Target contract

1. Try an exact-revision native archive first.
2. If it fails after Plan 002's safe read retries, build the same revision from
   Tree+Blob unless the context is canceled/deadline-exceeded.
3. Never fall back from an unsafe or semantically invalid successful archive
   (path traversal, symlink, wrong root). Those remain hard failures; accepting
   a different representation after validation failure could mask compromise.
4. If both paths fail, return a sanitized joined error that labels native and
   reader failures while preserving typed causes and exposing no credentials.
5. Avoid fetching Tree twice on a fallback clone/pull. Introduce a
   provider-neutral snapshot result carrying archive bytes and the exact
   `map[string]RemoteFile`, or an equally small cache scoped to one operation.
6. Preserve exact commit pinning, deterministic ordering, binary bytes,
   executable modes, 1 GiB bounds, cancellation, and safe extraction.
7. Do not leave a partially-created clone destination or partially-updated
   worktree when either path fails.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Focused | `go test -race -run 'Snapshot|Clone|Pull|Merge|Migration|Gitea' ./...` | pass |
| Full | `go test -race ./...` | pass |
| Stress | `go test -shuffle=on -count=20 -run 'Snapshot|Clone|Pull' ./...` | pass |
| Vet/format | `go vet ./... && test -z "$(gofmt -l cmd internal)"` | exit 0 |

## Scope

**In scope**:

- `internal/forge/snapshot.go`, `snapshot_test.go`
- `internal/forge/forge.go` only for a snapshot-result type
- Native snapshot provider tests under Gitea, GitHub, and GitLab
- `internal/cli/forge_bridge.go`, `cli.go`, `workspace_git.go`, `migration.go`
- Corresponding CLI/hybrid/migration tests
- `README.md`
- `advisor-plans/README.md` (status only)

**Out of scope**:

- Removing native archive implementations
- Parallel/unbounded blob downloads, persistent caches, partial clone, LFS
- Raising the 1 GiB bound
- Retrying mutations or changing push behavior
- Accepting symlinks/submodules or weakening extraction checks

## Git workflow

- Branch: `advisor/004-snapshot-fallback`
- Suggested commits: `test: reproduce native archive failure`, then
  `fix: fall back to forge reader snapshots`.
- Do not push or run live tests without operator instruction.

## Steps

### Step 1: Add native-failure characterization

Extend `snapshot_test.go` with a fake implementing both native and reader roles.
Cover native success (reader unused), transient native failure then reader
success, both failures, cancellation, unsafe tree, binary data, executable
mode, aggregate-size rejection, and deterministic output. Add a Gitea HTTP
fixture whose archive fails while tree/blob succeed.

At the CLI layer, clone into a non-existent destination and prove fallback
success; on double failure prove no destination content or `.gew` state remains.

**Verify**: focused tests fail only because native errors do not yet fall back.

### Step 2: Return archive and tree as one snapshot result

Introduce a small result type such as:

```go
type SnapshotResult struct {
    Archive []byte
    Files   map[string]RemoteFile
    Source  string // "native" or "reader", only if useful for diagnostics
}
```

Do not expose provider DTOs. Native success may fetch Tree once after the
archive; fallback must reuse the Tree it already fetched. Copy maps/slices at
ownership boundaries so later state mutation cannot corrupt cached metadata.

**Verify**: request-count fixtures prove fallback uses one Tree listing and one
Blob read per file, not a second complete Tree call.

### Step 3: Implement bounded fallback classification

After native retrieval exhausts Plan 002 retries, check `ctx.Err()`. Return
cancellation immediately. Otherwise invoke reader reconstruction at the same
commit SHA. Join labeled errors if it fails. Keep archive validation in the CLI
hard-fail path; do not catch `extractArchive` errors and try another source.

Update clone, pull, merge, migration, and hybrid snapshot consumers to use the
result's Files map and preserve their existing atomic worktree/state ordering.

**Verify**: focused race tests pass and all request-count assertions match.

### Step 4: Live verification and documentation

Add an opt-in disposable Gitea test that injects or targets a deliberately
unavailable archive route while allowing Tree/Blob reads. If server-side fault
injection is unavailable, run it against an authenticated local proxy owned by
the test process; never alter the real repository/server.

Update README's provider matrix to say native archive with Tree+Blob fallback.
Run all gates plus `git diff --check`.

## Test plan

- Native fast path and exact fallback request counts.
- Reset/timeout/5xx archive failures followed by successful fallback.
- Cancellation skips fallback.
- Both-failure error preserves both causes and redacts tokens.
- Unsafe tree, symlink/submodule mode, size overflow, Blob failure, binary and
  executable round trips.
- Clone/pull/hybrid/migration state remains atomic.

## Done criteria

- [ ] Gitea clone succeeds through Tree+Blob when archive retrieval fails.
- [ ] Native success remains the fast path.
- [ ] Fallback Tree data is not fetched twice.
- [ ] Exact revision, bytes, mode, bounds, and extraction safety remain intact.
- [ ] Failed operations leave no partial workspace/state.
- [ ] Full race/stress/vet/format/diff gates pass.
- [ ] Plan 004 is `DONE`.

## STOP conditions

- Fallback would use a branch name instead of the resolved commit SHA.
- The only way to fall back is to weaken archive/path/object validation.
- Snapshot metadata cannot be reused without shared mutable state across
  commands; choose a per-operation result instead.
- Memory use would become unbounded beyond the existing 1 GiB contract.

## Maintenance notes

Every future native snapshot adapter must pass both fast-path and fallback
contracts. Reviewers should verify that validation failures are not treated as
transport failures and that fallback does not silently hide authentication or
authorization errors in final diagnostics.
