# Plan 007: Stream exact-revision snapshots through bounded artifacts

> **Executor instructions**: Execute after Plan 006. Run every verification
> gate. GEW remains REST-only; all remote bytes must come from forge HTTPS REST
> endpoints. Do not substitute Git clone/fetch, SSH, a provider CLI, or the
> system `git` executable. Stop on any STOP condition and report it. Mark this
> plan `DONE` in `advisor-plans/README.md` when complete.
>
> **Drift check (run first)**:
> `git diff --stat e8a7b47..HEAD -- internal/forge/http.go internal/forge/snapshot.go internal/forge/forge.go internal/cli/cli.go internal/cli/staging.go internal/cli/workspace_git.go`
> Confirm SHA-256 prefixes from the planned baseline: `4499140`, `0ca6db8`,
> `ab80a44`, `5a5caf2`, `e5f67f7`, and `ad2b007`. Reconcile expected Plan 006
> edits first; STOP on other semantic drift.

## Status

- **Priority**: P1
- **Effort**: L (multi-day)
- **Risk**: MED–HIGH — snapshot ownership and cleanup touch every sync path
- **Depends on**: Plan 006
- **Category**: perf / tech-debt
- **Planned at**: commit `e8a7b47`, 2026-08-08, plus the fingerprinted working tree

## Why this matters

Native downloads and fallback ZIPs are complete `[]byte` values, with a 1 GiB
limit, and Git workflows then materialize complete uncompressed byte maps.
Clone/pull also hash and reread files repeatedly to populate `.gew/objects`.
This plan makes snapshot storage file-backed and bounded, then hashes and
stores extracted content in one pass while preserving exact revisions and
archive validation.

## Current state

- `internal/forge/forge.go:147-149` defines `ForgeSnapshotter.Snapshot` as
  returning `[]byte`.
- `internal/forge/http.go:229-239,340-349` reads the full download into memory.
- `internal/forge/snapshot.go:17-20,94-142` stores either the native archive or
  a fallback `bytes.Buffer` as one slice.
- `internal/cli/cli.go:808-867` requires the full slice for `zip.NewReader`.
- `internal/cli/cli.go:206-230` extracts, scans, saves state, then populates
  baseline objects.
- `internal/cli/staging.go:527-545` hashes a file and then
  `storeObjectFromFile` rereads and hashes it again.
- `internal/cli/workspace_git.go:603-630` extracts a full snapshot and rereads
  every file into `map[string][]byte`.
- Preserve path traversal, symlink/submodule, executable mode, 1 GiB, exact
  revision, cancellation, and partial-workspace protections from Plan 004.

## Target contract

1. Introduce an owned `SnapshotArtifact` backed by a 0600 temporary file. It
   exposes `io.ReaderAt`, size, optional exact Tree metadata, source
   (`native`/`reader`), and idempotent `Close` cleanup. It never exposes its
   temporary path in user errors.
2. `HTTPRequester.DownloadArtifact` copies a successful REST response into the
   artifact through `io.LimitReader(limit+1)`. It removes partial artifacts on
   errors, cancellation, oversized responses, and retries.
3. Native providers return artifacts. Fallback construction writes ZIP output
   directly to an artifact; deterministic entry ordering remains unchanged.
4. ZIP extraction accepts `io.ReaderAt` + size and returns validated metadata
   (path, SHA-256, mode, size) while writing each file.
5. The Gew backend can tee each extracted file into its content-addressed
   object store using the same hash pass. Never publish an object until the
   expected bytes and final hash are known.
6. Clone and clean pull do not rescan freshly extracted content. Existing
   workspace scans remain for detecting user edits.
7. Git workflows must stop retaining remote repositories as
   `map[string][]byte`; use staged files/metadata and open content on demand.
8. Peak heap is bounded by ZIP metadata plus fixed copy buffers, not snapshot
   byte size. Temporary disk remains bounded by the existing 1 GiB contract.
9. All artifacts close on every success/error path. Cleanup failures join the
   primary error without hiding it.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Snapshot | `go test -race -run 'HTTP.*Artifact|Snapshot|Archive|Clone|Pull' ./internal/forge ./internal/cli` | pass |
| Allocation | `go test -run '^$' -bench 'SnapshotArtifact|ExtractArchive' -benchmem ./internal/forge ./internal/cli` | bounded allocation rows |
| Stress | `go test -shuffle=on -count=20 -run 'Snapshot|Archive|Clone|Pull' ./...` | pass |
| Full | `go test -race ./...` | pass |
| Vet/format | `go vet ./... && test -z "$(gofmt -l cmd internal)"` | exit 0 |

## Scope

**In scope**:

- `internal/forge/artifact.go`, `artifact_test.go` (create)
- `internal/forge/http.go`, `http_test.go`
- `internal/forge/forge.go`, `snapshot.go`, `snapshot_test.go`
- Native snapshot adapters/tests under `internal/forge/{gitea,github,gitlab}`
- `internal/cli/cli.go`, `staging.go`, `workspace_git.go`
- Corresponding CLI/Git tests and allocation benchmarks
- `README.md`
- `advisor-plans/README.md` (status only)

**Out of scope**:

- Parallel blob fetching or Azure native ZIP (Plan 008)
- Incremental worktree application (Plan 009)
- Push proof changes (Plan 010)
- Raising the 1 GiB bound, accepting symlinks/submodules, or changing retries
- Git transport, Git CLI calls, LFS, or persistent global caches

## Git workflow

- Branch: `advisor/007-stream-snapshots`
- Suggested commits: `test: characterize snapshot allocations`,
  `refactor: own file-backed snapshot artifacts`,
  `perf: ingest snapshots in one pass`.
- Do not push or publish without operator instruction.

## Steps

### Step 1: Characterize ownership, cleanup, and allocation

Add tests for partial reads, oversized bodies, cancellation, native failure
then fallback, deterministic ZIP bytes, double Close, joined cleanup errors,
unsafe archives, and zero leaked `gew-*` temp files. Add an allocation
benchmark using generated large content; assert behavior through benchmark
output, not a flaky hard limit.

**Verify**: focused tests describe current gaps and existing safety tests pass.

### Step 2: Add the bounded artifact abstraction

Create one forge-owned artifact type with private path/file ownership. Add
`DownloadArtifact` without removing `Download` until all snapshot callers are
migrated. Ensure retry attempts truncate or replace partial files and that
successful status/body processing stays inside the method’s ownership window.

**Verify**: artifact tests pass under `-race`; every failure leaves no file.

### Step 3: Migrate native and fallback snapshots

Change `ForgeSnapshotter` and `SnapshotResult` to return the artifact. Update
GitHub, GitLab, and Gitea REST adapters. Rewrite fallback ZIP generation to a
file-backed writer while retaining sorted paths, modes, binary bytes, exact
commit pinning, and joined native/fallback errors. Remove obsolete byte-slice
wrappers only after all call sites compile.

**Verify**: snapshot request-count and deterministic-order tests pass.

### Step 4: Extract, hash, and store in one pass

Replace `extractArchive([]byte, ...)` with an artifact reader. Return extracted
metadata and allow a Gew object sink to receive verified content during the
same copy. Use atomic object publication and remove `ensureBaselineObjects`
from fresh clone/clean-pull paths only when tests prove equivalent recovery.

For Git consumers, represent remote content as paths in an owned staging area
plus metadata; read individual files only when merge/diff logic needs bytes.

**Verify**: clone/pull tests prove one content read per extracted file, exact
object hashes, mode retention, and artifact/stage cleanup.

### Step 5: Wire progress and document resource bounds

Emit download/extract byte/file counters through Plan 006’s observer. Document
temporary disk requirements, heap behavior, and unchanged security checks.

**Verify**: run all gates and `git diff --check`.

## Test plan

- HTTP artifact success, retry, truncated body, cancellation, size overflow.
- Native and fallback artifacts: determinism, modes, binary bytes, cleanup.
- Archive rejection: traversal, absolute paths, symlinks, submodules, overflow.
- Clone/pull: metadata returned without rescan; object hashes and recovery work.
- Git staging representation: content opened on demand and cleaned up.

## Done criteria

- [ ] Snapshot APIs no longer return repository-sized `[]byte` values.
- [ ] Clone/clean pull hash freshly downloaded files once.
- [ ] Snapshot heap use does not scale with archive bytes.
- [ ] Every artifact/stage is removed on success, failure, and cancellation.
- [ ] Exact-revision and archive safety contracts remain unchanged.
- [ ] Race, stress, vet, format, and diff gates pass.

## STOP conditions

- Streaming would move validation after live-worktree mutation.
- Retries would append to a partial prior artifact.
- Cleanup requires exposing temporary paths or weakening error redaction.
- Git merge correctness requires deleting the only recoverable base snapshot.
- Any implementation invokes Git transport or a provider CLI.

## Maintenance notes

All future snapshot providers must return owned artifacts. Reviewers should
trace ownership from HTTP response to final Close and reject unbounded
`io.ReadAll` on repository content. Plan 008 builds concurrency on this bounded
storage; do not add concurrency inside ZIP writers here.
