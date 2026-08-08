# Plan 003: Make ambiguous pushes self-healing and large Gitea commits recoverable

> **Executor instructions**: Execute only after Plan 002 is `DONE`. Run every
> verification gate. Stop on a STOP condition; never weaken the one-local-
> commit/one-remote-commit invariant. Mark Plan 003 `DONE` in the index when
> complete.
>
> **Drift check (run first)**:
>
> ```sh
> git diff --stat e8a7b47..HEAD -- \
>   internal/forge/http.go internal/forge/forge.go internal/forge/gitea \
>   internal/cli/cli.go internal/cli/main_test.go internal/cli/staging.go \
>   internal/cli/command.go internal/cli/command_test.go \
>   internal/cli/workspace_git.go internal/cli/git_export_test.go README.md
> shasum -a 256 internal/forge/gitea/gitea.go \
>   internal/forge/gitea/gitea_test.go internal/cli/cli.go \
>   internal/cli/main_test.go internal/cli/workspace_git.go
> ```
>
> Pre-Plan-002 v0.5.0 SHA-256 prefixes are `2664ae8`, `eb44660`, `5f9425a`,
> and `9090064` for the first four listed files. Reconcile expected Plan 002
> changes first; STOP if push semantics changed independently.

## Status

- **Status**: DONE
- **Priority**: P1
- **Effort**: L (multi-day)
- **Risk**: HIGH — queue checkpointing and remote atomicity are core safety properties
- **Depends on**: Plan 002
- **Category**: bug / reliability / DX
- **Planned at**: commit `e8a7b47`, 2026-08-08, against published v0.5.0 source

## Why this matters

During v0.5.0 publication, both forges accepted commits whose immediate
read-back failed; Gew returned an error and left them queued until a manual
retry reconciled them. Gitea also reset a single large Base64 JSON mutation,
forcing the release archives into separate commits. Gew should complete
accepted writes in the same invocation when remote state proves success, and
should make oversized Gitea commits stream reliably or give users a safe,
Gew-native way to uncommit and split them without duplicating remote history.

## Current state

- `internal/forge/gitea/gitea.go:230-266` Base64-encodes every file, marshals a
  single `/contents` request, discards the successful `FilesResponse`, then
  calls `Head` and heavy `CommitDetails` to rediscover the commit.
- Gitea 1.27's `/contents` response contains `commit.sha` and
  `commit.parents`; its REST API exposes no Git blob/tree/commit write methods.
  Therefore splitting one local commit while retaining one atomic remote
  commit is not available through another Gitea REST endpoint.
- `internal/forge/gitea/gitea.go:211-227` requests
  `files=true` for every commit-detail lookup, although file lists are needed
  only during ambiguous reconciliation.
- `internal/cli/cli.go:403-526` treats any post-write Head/Tree failure as a
  failed command. `reconcileAppliedCommit` at lines 559-638 runs only on the
  next `gew push` when head has moved.
- `internal/cli/main_test.go:693-734` explicitly expects the first ambiguous
  push to fail and the second to reconcile. That expectation must change.
- The hybrid backend has a prepared-export journal in
  `internal/cli/workspace_git.go:1088-1329`; the pure Gew backend relies on its
  queue and immutable objects. Preserve both recovery models.
- README promises one atomic remote commit per queued local commit and partial
  queue checkpointing after each confirmed commit.

## Target contract

1. A normal Gitea write parses the commit ID and parents from `FilesResponse`
   and performs no adapter-local Head or CommitDetails request.
2. After any successful writer result, the command layer polls the target head
   and tree through Plan 002's read policy for a bounded 15-second visibility
   window. Once exact commit/tree state is proven, it checkpoints immediately.
3. After a mutation transport error, Gew never repeats the mutation blindly.
   It polls head; if head advanced, it runs the existing message/parent/path/
   byte reconciliation. Exact success is checkpointed in the same invocation.
   Unchanged head after the grace window leaves the queue/prepared export intact
   and returns an explicitly ambiguous error.
4. Unrelated head advancement remains a stale-head error requiring pull.
5. Pure and hybrid backends use one shared remote-write verification policy.
6. Gitea mutation JSON is spooled/streamed with known Content-Length rather
   than materialized as one additional unbounded byte slice. Temporary files
   use 0600 mode outside the repository and are removed on all paths.
7. Add an internal requester option for HTTP/1.1-only mutation transport and
   enable it for Gitea only if the protocol fixture/live characterization
   reproduces the proxy reset. Do not globally disable HTTP/2 without evidence.
8. HTTP 413 becomes typed `ErrRequestTooLarge`. Ambiguous reset/broken-pipe
   errors include change count, raw bytes, and encoded request bytes after
   reconciliation proves head unchanged; never claim size is the cause when it
   is not proven.
9. Add `gew uncommit` to undo only the newest unpushed local commit: restore its
   exact staged snapshot, remove it atomically from queue/history, and retain
   working-tree edits. Refuse pushed commits, merges, prepared exports, or a
   non-tail target. Support both workspace backends (go-git soft reset for the
   hybrid backend). This is the recovery path before re-staging smaller groups.
10. Never silently split a commit or weaken parent/head validation.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Focused | `go test -race -run 'Push|Ambiguous|Partial|Gitea|Uncommit|GitExport' ./...` | pass |
| Stress | `go test -shuffle=on -count=30 -run 'Push|Ambiguous|Uncommit' ./...` | pass |
| Full | `go test -race ./...` | pass |
| Vet/format | `go vet ./... && test -z "$(gofmt -l cmd internal)"` | exit 0 |

## Scope

**In scope**:

- `internal/forge/http.go` and tests, only for replayable/spooled bodies and optional HTTP/1.1 transport
- `internal/forge/forge.go` and tests for typed large-request errors
- `internal/forge/gitea/gitea.go`, `gitea_test.go`
- `internal/cli/cli.go`, `main_test.go`
- `internal/cli/staging.go` and tests
- `internal/cli/command.go`, `command_test.go`
- `internal/cli/workspace_git.go`, `git_export_test.go`
- A small new `internal/cli/push_recovery.go` and test file if it prevents duplication
- `README.md`
- `advisor-plans/README.md` (status only)

**Out of scope**:

- Multiple remote commits for one local commit
- Git transport, LFS, server configuration changes, or raising server limits
- GitLab/Bitbucket push enablement
- General rebase/reset/history editing; `uncommit` is tail-only and unpushed-only
- Release objects/assets (Plan 005)
- Workspace state-version bump unless atomic pure-backend uncommit is impossible without one; STOP before changing it

## Git workflow

- Branch: `advisor/003-self-healing-push`
- Suggested commits: `test: reproduce release push failures`,
  `fix: consume gitea commit responses`, `fix: reconcile writes in one push`,
  `feat: recover oversized local commits`.
- Do not push or run live destructive tests without operator instruction.

## Steps

### Step 1: Replace success-on-second-try characterization

Extend Gitea and both backend fixtures with scripted sequences for lost POST
response, delayed old head, transient Head/Tree/CommitDetails failures,
unrelated advancement, 413, reset during a large body, and cancellation.
Change `TestAmbiguousSuccessfulPushReconcilesOnRetry`: the first invocation must
now succeed after internal reconciliation and exactly one mutation request.
Retain a case where all read-back attempts fail and the queue remains intact.

**Verify**: focused tests fail for the old implementation without contacting a
real forge.

### Step 2: Consume Gitea's authoritative mutation response

Replace `json.RawMessage` with DTOs for `FilesResponse.commit.sha`, message,
tree, and parents. Return `ApplyCommitResult` directly. Validate non-empty SHA
and matching first parent through the existing writer decorator. Keep
`CommitDetails(files=true)` only for actual ambiguous recovery.

Spool the exact JSON body to a 0600 temporary file, record raw and encoded
sizes, rewind it for the single POST, set Content-Length, and always remove it.
Use no repository-local temporary file.

**Verify**: `TestGiteaApplyCommitUsesAtomicContentsEndpoint` expects exactly one
POST and no success-path GET; binary bytes and parent validation still pass.

### Step 3: Unify same-invocation verification and reconciliation

Extract a command-layer helper used by pure and hybrid push. It accepts the
base head, optional writer result, local message/path/content identity, and
target branch. It may retry reads under Plan 002 but may invoke ApplyCommit only
once. Persist a known candidate remote SHA before lengthy verification where
the existing local-commit/prepared-export format can do so safely.

On transport ambiguity, poll head then reconcile exact identity. On successful
writer result, wait for head visibility and verify tree before checkpointing.
All state writes remain atomic and occur only after proof.

**Verify**: focused push tests prove one mutation, same-command success, no
duplicate commit, unchanged queue on unresolved ambiguity, and correct partial
queue checkpointing.

### Step 4: Characterize and harden large Gitea requests

Add TLS fixtures that record HTTP protocol and consume a body slowly or reset
mid-upload. Compare default and HTTP/1.1-only requester behavior. Add an opt-in
live test using a disposable branch and generated incompressible files at
1 MiB, 8 MiB, and an operator-configured ceiling; clean only the branch it
creates. If HTTP/2 is not reproducibly implicated, do not force HTTP/1.1—retain
streaming and typed errors.

Map explicit 413 to `ErrRequestTooLarge`. After an ambiguous large write whose
head remains unchanged, report measured sizes and the `gew uncommit` recovery
command without asserting an undocumented server limit.

**Verify**: hermetic large-body tests stay within a documented memory bound and
leave no temp files; the live test remains opt-in.

### Step 5: Add safe tail-only `gew uncommit`

Add the command to the urfave graph and completion/help tests. For the Gew
backend, restore the tail commit's immutable objects/deletions into the stage
index using an atomic recovery sequence, then remove only that queue/history
tail. For hybrid, soft-reset only the newest unexported commit to its parent so
the index retains its tree. Refuse every state listed in the target contract.

Document the recovery sequence: `gew uncommit`, `gew reset PATH...`, commit the
first group, then stage/commit the remainder.

**Verify**: tests cover create/update/delete/binary changes, dirty working tree,
multiple queued commits unwound tail-first, crashes injected between state
writes, hybrid receipts, and every refusal case.

### Step 6: Final gates

Run all commands above plus `git diff --check`. With explicit operator approval,
run the opt-in live Gitea test and a lost-response test against a disposable
branch. Never use `main` for fault injection.

## Test plan

- Same-command reconciliation for reset, 500, delayed reads, and lost 2xx.
- Exact path/parent/message/bytes mismatch remains unreconciled.
- Successful Gitea POST uses response SHA/parents and no redundant GET.
- Large-body temp spool has correct Content-Length, mode, cleanup, and redaction.
- `uncommit` round-trips every change kind without touching working files.
- Pure and hybrid backends pass the same behavior matrix.

## Done criteria

- [ ] Accepted commits normally finish in one `gew push` invocation.
- [ ] No ambiguity path sends a second mutation without remote proof.
- [ ] Gitea success path is one `/contents` POST at adapter level.
- [ ] Large request bodies are bounded/spooled and 413 is typed/actionable.
- [ ] `gew uncommit` safely recovers newest unpushed commits on both backends.
- [ ] Atomic remote commit, stale-head, queue, and credential invariants hold.
- [ ] Full race/stress/vet/format/diff gates pass.
- [ ] Plan 003 is `DONE`.

## STOP conditions

- Gitea's success response lacks SHA or parent data on a supported live version.
- A proposed retry could repeat a mutation without exact reconciliation.
- Large-commit support requires multiple remote commits, Git transport, or a
  server configuration change.
- `uncommit` cannot be crash-safe without a workspace migration; report a
  separate migration design before proceeding.
- Pure and hybrid backends would use materially different safety criteria.

## Maintenance notes

Reviewers must scrutinize the exact checkpoint moment and prove every mutation
count. Future providers should return authoritative IDs from mutation responses
where possible. Request size is server-dependent; never encode one observed
Gitea limit as a universal constant.
