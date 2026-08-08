# Plan 008: Select fast REST snapshot strategies per provider

> **Executor instructions**: Execute after Plan 007. GEW’s defining constraint
> is REST-only synchronization. Use documented HTTPS REST endpoints and the
> shared requester only—never Git transport, SSH, HTML scraping, provider CLIs,
> or undocumented archive URLs. Run every gate and stop on a STOP condition.
> Mark Plan 008 `DONE` in `advisor-plans/README.md` when complete.
>
> **Drift check (run first)**:
> `git diff --stat e8a7b47..HEAD -- internal/forge/forge.go internal/forge/snapshot.go internal/forge/azure internal/forge/bitbucket internal/forge/github internal/forge/gitlab internal/forge/gitea`
> Baseline SHA-256 prefixes include `ab80a44` (`forge.go`), `0ca6db8`
> (`snapshot.go`), `4e4b36b` (Azure), and `8b0bb9c` (Bitbucket). Reconcile Plans
> 006–007 first; STOP on unrelated semantic drift.

## Status

- **Priority**: P1
- **Effort**: L (two to four days)
- **Risk**: MED — provider throttling and exact-revision behavior differ
- **Depends on**: Plan 007
- **Category**: perf / architecture
- **Planned at**: commit `e8a7b47`, 2026-08-08, plus the fingerprinted working tree

## Why this matters

Azure and Bitbucket currently build snapshots by listing a tree and issuing one
serial Blob REST request per file. At 50 ms RTT, 10,000 files impose an 8.3
minute request-latency floor. Azure officially supports exact-revision ZIP
responses, and the provider-neutral fallback can safely overlap independent
GETs with bounded concurrency and file-backed spooling.

## Current state

- `internal/forge/forge.go:121-124` capabilities only describe branch create
  and push; strategy selection relies on runtime interface assertions.
- `internal/forge/snapshot.go:51-57,111-135` performs Tree then serial Blob
  calls and writes a deterministic ZIP.
- `internal/forge/azure/azure.go:157-200` has exact-commit Tree/Blob methods but
  no `ForgeSnapshotter` implementation.
- `internal/forge/bitbucket/bitbucket.go:177-280` walks directories/pages and
  then uses the same serial Blob fallback.
- Azure DevOps REST 7.1 Items List documents `$format=zip`,
  `versionDescriptor.version`, `versionDescriptor.versionType=commit`, and
  `zipForUnix`; use the official contract at
  `https://learn.microsoft.com/en-us/rest/api/azure/devops/git/items/list?view=azure-devops-rest-7.1`.
- Bitbucket’s documented Source REST API supports exact commit paths and
  `max_depth`; use
  `https://developer.atlassian.com/cloud/bitbucket/rest/api-group-source/`.
- Plan 004 requires native-first, exact SHA fallback, deterministic bytes,
  cancellation, size/mode/path safety, and joined errors. Preserve it.

## Target contract

1. Extend `ForgeCapabilities` with explicit read features such as native exact
   snapshot, recursive tree, compare/delta, tree identity, and a conservative
   provider read-concurrency ceiling. Capability declarations must be checked
   against implemented optional interfaces in conformance tests.
2. Add Azure `ForgeSnapshotter` using one REST Items request pinned with
   `versionDescriptor.version=<commit>`, `versionType=commit`, `$format=zip`,
   `download=true`, and `zipForUnix=true`. The shared artifact size/path/mode
   validation remains authoritative.
3. Do not add a Bitbucket native archive until an official documented REST
   endpoint can be pinned to an exact commit and tested. Improve its documented
   tree traversal with `max_depth` where fixtures prove complete results.
4. Add a reusable forge-level batch reader that accepts validated
   `map[path]RemoteFile` subsets and returns owned per-path artifacts with a
   bounded worker pool. Snapshot fallback consumes it now; Plan 009 will reuse
   it for only the changed paths. Initial ceilings: Azure 8, Bitbucket 4,
   generic 4; encode them as reviewed provider policy, not unbounded goroutines.
5. Each worker spools to Plan 007 artifacts. Aggregate declared and actual
   bytes remain bounded at 1 GiB. One deterministic serializer writes sorted
   ZIP entries; the ZIP writer is never used concurrently.
6. First error cancels outstanding work. HTTP 429/Retry-After still follows
   Plan 002. Concurrency must not add mutation retries.
7. Request-count tests prove Azure native success uses one snapshot request,
   fallback uses one Tree plus N Blobs, and maximum in-flight Blob calls never
   exceeds the provider ceiling.
8. Native validation failure is a hard failure; fallback occurs only after
   retrieval failure, as established by Plan 004.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Forge | `go test -race -run 'Capabilities|Snapshot|Azure|Bitbucket|Concurrency' ./internal/forge/...` | pass |
| Stress | `go test -race -shuffle=on -count=30 -run 'Snapshot.*Concurrent|Snapshot.*Cancel' ./internal/forge` | pass |
| Full | `go test -race ./...` | pass |
| Vet/format | `go vet ./... && test -z "$(gofmt -l cmd internal)"` | exit 0 |

## Scope

**In scope**:

- `internal/forge/forge.go`, `forge_test.go`
- `internal/forge/blob_batch.go`, `blob_batch_test.go` (create)
- `internal/forge/snapshot.go`, `snapshot_test.go`
- `internal/forge/azure/azure.go`, `azure_test.go`
- `internal/forge/bitbucket/bitbucket.go`, `bitbucket_test.go`
- Capability declarations/tests for Gitea, GitHub, and GitLab
- `internal/forge/registry` conformance tests if needed
- `internal/cli/forge_bridge.go` only for aliases
- `README.md`
- `advisor-plans/README.md` (status only)

**Out of scope**:

- Provider compare APIs or incremental worktree application (Plan 009)
- Push behavior or verification (Plan 010)
- User-configurable/unbounded concurrency, adaptive mutation retries, LFS
- Undocumented Bitbucket archive URLs, HTML/source-page scraping
- Git transport, system Git, SSH, or provider CLI subprocesses

## Git workflow

- Branch: `advisor/008-rest-snapshot-capabilities`
- Suggested commits: `test: declare forge read capabilities`,
  `feat: stream Azure exact-revision archives`,
  `perf: bound concurrent fallback reads`.
- Do not run live provider tests against non-disposable repositories.

## Steps

### Step 1: Define and validate read capabilities

Add explicit fields without removing `BranchCreate` or `Push`. Extend shared
adapter conformance so a native declaration requires `ForgeSnapshotter`, a
positive concurrency value is capped at 16, and zero selects the conservative
generic default. Keep provider DTOs outside the shared contract.

**Verify**: capability tests cover all five providers and reject contradictions.

### Step 2: Implement Azure’s documented exact-commit ZIP

Build the query through existing `azureQuery`; do not construct an unauthenticated
URL. Add an HTTP fixture asserting every pinning parameter and `api-version=7.1`.
Return Plan 007’s owned artifact. Test binary files, executable mode, malformed
ZIP, wrong root, cancellation, 404/403, retryable 5xx, and fallback after a
retrieval error.

**Verify**: Azure native success records one archive request and zero Blob calls.

### Step 3: Reduce Bitbucket tree request depth safely

Use documented `max_depth` on exact-commit source listings. Follow pagination
and directory links when the response remains incomplete; protect against
cycles and the existing one-million-entry bound. Do not claim native archive
capability.

**Verify**: shallow, nested, paginated, incomplete-depth, and cycle fixtures pass.

### Step 4: Add a reusable bounded batch reader

Sort/validate metadata first. Start a cancellation group with the declared
limit. Spool each Blob into an owned per-entry artifact and atomically account
actual bytes. Return artifacts keyed by validated path with explicit ownership;
callers must close all results. Snapshot fallback serializes those artifacts
into the final ZIP in sorted order and closes each promptly. A test server
should block workers at a barrier to prove the exact maximum concurrency and
cancellation behavior without sleeps.

**Verify**: race/stress tests show deterministic output and no artifacts or
goroutines left after first error/cancellation.

### Step 5: Report strategy and update documentation

Emit native/fallback strategy, file counts, bytes, and concurrency via Plan
006’s observer. Update the provider matrix accurately: Azure native ZIP plus
fallback; Bitbucket exact-commit REST tree/blob fallback.

**Verify**: run all gates and `git diff --check`.

## Test plan

- Capability/interface consistency across all adapters.
- Azure query pinning, native success, retrieval fallback, validation hard fail.
- Bitbucket recursive-depth completeness and pagination.
- Worker maximum, deterministic ZIP order, aggregate size overflow, 429,
  cancellation, first error, cleanup, and race freedom.

## Done criteria

- [ ] Azure clone/pull snapshot retrieval is one exact-commit REST ZIP request.
- [ ] Bitbucket remains on documented REST APIs only.
- [ ] Fallback Blob reads are bounded and concurrent, never unbounded.
- [ ] Exact revision, deterministic bytes, modes, limits, and validation remain.
- [ ] Request-count/concurrency tests pass under race and stress.
- [ ] No Git transport or subprocess dependency is introduced.

## STOP conditions

- Azure ZIP cannot be proven pinned to the requested commit in fixtures/live disposable testing.
- Bitbucket acceleration requires an undocumented endpoint or HTML scraping.
- Parallelism requires concurrent writes to one ZIP writer or unbounded memory.
- A provider needs mutation retry behavior to support read concurrency.
- Rate-limit behavior cannot preserve cancellation and Plan 002 semantics.

## Maintenance notes

New adapters must declare conservative capabilities and pass the same contract.
Provider API version changes should be reviewed against official documentation.
Do not raise concurrency because a LAN fixture is fast; provider throttling and
high-latency SaaS behavior are the governing constraints.
