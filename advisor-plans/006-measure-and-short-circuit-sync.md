# Plan 006: Measure sync phases and make unchanged pulls constant-time

> **Executor instructions**: Follow this plan in order. Run every verification
> command and confirm the expected result before continuing. GEW must remain a
> REST-only client: do not invoke the `git` executable, SSH, smart HTTP, a
> provider CLI, or a server-side helper. If a STOP condition occurs, report it
> instead of improvising. When done, mark Plan 006 `DONE` in
> `advisor-plans/README.md` unless a reviewer owns the index.
>
> **Drift check (run first)**:
> `git diff --stat e8a7b47..HEAD -- internal/cli/cli.go internal/cli/workspace_git.go internal/cli/command.go internal/cli/main_test.go internal/cli/workspace_git_test.go README.md`
> The repository layout is intentionally dirty relative to `e8a7b47`. Confirm
> current SHA-256 prefixes `5a5caf2` (`cli.go`), `ad2b007` (`workspace_git.go`),
> `6605996` (`command.go`), and `bf4131d` (`README.md`). STOP on a semantic
> mismatch rather than treating the layout move as drift.

## Status

- **Priority**: P1
- **Effort**: M (one to two days)
- **Risk**: LOW–MED — output and unchanged-pull semantics are user-visible
- **Depends on**: none; Plans 002–004 must remain intact
- **Category**: perf / DX / tests
- **Planned at**: commit `e8a7b47`, 2026-08-08, plus the fingerprinted working tree

## Why this matters

GEW has no phase timings, request counters, or byte/file progress, so healthy
work is indistinguishable from a hang. It also scans the complete local tree
before asking the REST API whether the remote moved. This plan establishes
request-count and phase baselines, exposes sanitized progress, and makes the
common unchanged pull one HEAD request with no repository scan.

## Current state

- `internal/cli/cli.go:343-364` calls `workspaceChanges`, which hashes every
  workspace file, before `remote.Head` can return “Already up to date.”
- `internal/cli/workspace_git.go:668-692` materializes HEAD, index, and worktree
  byte snapshots before the same HEAD comparison.
- `internal/cli/cli.go:893-950` walks and SHA-256 hashes all regular files.
- `internal/forge/http.go:259-295` silently retries GET/HEAD up to four times.
- `internal/cli/command.go:174-190` shows the declarative urfave command/flag
  convention. Match it; progress goes to `app.errOut`, never stdout.
- Tests use in-memory forges and `httptest.Server`; follow
  `internal/cli/main_test.go:391-470` and
  `internal/cli/workspace_git_test.go:146-166`.

## Target contract

1. Clone, pull, and push report named phases through one internal observer:
   resolve, head, tree, download, extract, scan, apply, upload, verify, state.
2. `--progress=auto|always|never` controls human progress on stderr. `auto`
   emits updates only to a terminal; tests/non-TTY pipes stay quiet.
3. `--timings` emits a final deterministic text summary to stderr. It contains
   durations, sanitized provider kind, requests, files, and bytes—never tokens,
   URLs with queries, local file contents, or repository credentials.
4. Existing stdout text and JSON output remain byte-for-byte compatible when
   the new flags are absent.
5. Both backends request remote HEAD before any content scan. When it equals
   `state.BaseCommit`, pull returns successfully without validating local
   staged/unstaged state. This intentional behavior matches “no remote work to
   integrate”; document it.
6. When HEAD moved, all current staged/dirty/merge checks still run before any
   workspace mutation.
7. Performance tests assert work/request counts, not wall-clock thresholds.
   Benchmarks may report time but must never make CI flaky.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Focused | `go test -race -run 'Progress|Timing|Pull.*UpToDate|GitPull.*UpToDate' ./internal/cli` | pass, no races |
| Benchmarks | `go test -run '^$' -bench 'Sync|ScanWorkspace' -benchmem ./internal/cli` | benchmark rows with allocations |
| Full | `go test -race ./...` | all pass |
| Vet/format | `go vet ./... && test -z "$(gofmt -l cmd internal)"` | exit 0 |

## Scope

**In scope**:

- `internal/cli/sync_progress.go` and `_test.go` (create)
- `internal/cli/cli.go`, `workspace_git.go`, `command.go`
- `internal/cli/main_test.go`, `workspace_git_test.go`, `command_test.go`
- `internal/cli/sync_benchmark_test.go` (create)
- `README.md`
- `advisor-plans/README.md` (status only)

**Out of scope**:

- Snapshot representation, parallel downloads, manifest schema, or push proof
- Changing Plan 002 retry classifications or mutation replay rules
- Persisting telemetry, sending analytics, or logging credentials/endpoints
- Git transport, the system `git` executable, or provider CLIs

## Git workflow

- Branch: `advisor/006-measure-short-circuit-sync`
- Suggested commits: `test: benchmark sync work`, `perf: short-circuit unchanged pull`,
  `feat: report sync progress`.
- Do not push or publish without operator instruction.

## Steps

### Step 1: Add deterministic work-count fixtures and benchmarks

Create counting fake forges that record Head, Tree, Blob, Snapshot,
ApplyCommit, and CommitDetails calls. Add generated-tree benchmarks for (a)
1,000 medium files and (b) 20,000 tiny files. Use `t.TempDir`, deterministic
bytes, `b.ReportAllocs`, and setup outside timed sections.

**Verify**: benchmark command prints both repository shapes; existing tests pass.

### Step 2: Fetch HEAD before expensive local inspection

In both pull paths, retain cheap workspace discovery and merge-journal checks,
then call `remote.Head`. Return immediately on an unchanged/empty remote. Only
after a changed HEAD should the Gew backend call `workspaceChanges` and the Git
backend call `gitSnapshots`/`pendingGitCommits`. Add tests with unchanged HEAD
and deliberately unsupported local content so any scan would fail. Assert one
Head and zero Tree/Blob/Snapshot calls.

**Verify**: focused up-to-date tests pass; changed-HEAD staged/dirty tests still
fail before remote content is downloaded.

### Step 3: Introduce a command-scoped sync observer

Add a small observer owned by `app`, with no package-global state. Each phase
must support start, progress counters, retry note, and finish. Make the no-op
implementation allocation-light. Wire command flags through clone/pull/push;
do not change forge public interfaces in this plan. Use stderr and serialize
writes so later concurrent readers cannot interleave lines.

**Verify**: tests assert stdout compatibility, quiet non-TTY behavior, forced
progress, phase order, cancellation, and token/URL-query absence.

### Step 4: Document baseline numbers and stable timing fields

Document flag semantics, stderr behavior, and the intentional unchanged-pull
short circuit. Record only reproducible benchmark commands—not one developer
machine’s numbers—as the maintenance baseline.

**Verify**: `go test -shuffle=on -count=20 -run 'Progress|Timing|UpToDate' ./internal/cli`
passes, then run all repository gates.

## Test plan

- Gew and Git unchanged pulls: one HEAD; zero content work; local dirt ignored.
- Changed pulls: existing staged/dirty/merge safety behavior is retained.
- Progress: auto/always/never, non-TTY, cancellation, concurrent updates.
- Timing output: deterministic keys, non-negative durations, no secrets.
- Benchmarks: many-small and medium-file repositories, setup excluded.

## Done criteria

- [ ] Unchanged pull performs one HEAD and no scan/tree/blob/snapshot work.
- [ ] Changed pull retains all mutation safety checks.
- [ ] Default stdout is unchanged; progress/timings use stderr.
- [ ] Benchmarks and exact request-count fixtures exist.
- [ ] Race, shuffled, vet, format, and diff checks pass.
- [ ] No REST-only invariant is weakened.

## STOP conditions

- Progress requires a package-global writer or emits credentials/query strings.
- HEAD-first ordering would mutate state before local safety checks.
- Tests require real provider credentials or wall-clock performance assertions.
- Supporting flags requires changing unrelated command output formats.

## Maintenance notes

Plans 007–010 must emit through this observer and extend the counting fixtures.
Reviewers should reject per-provider ad hoc logging and timing assertions with
fixed millisecond limits. The request-count assertions are the stable contract.
