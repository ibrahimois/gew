# Plan 010: Prove REST pushes without downloading the repository per commit

> **Executor instructions**: Execute after Plans 006–009. This is a high-risk
> safety/performance change. GEW must create and verify commits exclusively via
> forge HTTPS REST APIs; never use Git transport, SSH, provider CLIs, the system
> `git` executable, or an external server helper. Run every gate and stop on any
> uncertainty listed below. Mark Plan 010 `DONE` only after all provider proof
> fixtures and ambiguous-response tests pass.
>
> **Drift check (run first)**:
> `git diff --stat e8a7b47..HEAD -- internal/forge/forge.go internal/forge/gitea internal/forge/github internal/forge/azure internal/cli/cli.go internal/cli/workspace_git.go internal/cli/git_export_test.go`
> Baseline SHA-256 prefixes include `ab80a44`, `3a6cba5` (GitHub), `4e4b36b`
> (Azure), `5a5caf2`, `ad2b007`, and `6bfb645`. Reconcile Plans 006–009 first;
> STOP if push/reconciliation semantics changed independently.

## Status

- **Priority**: P1
- **Effort**: L (five to eight days)
- **Risk**: HIGH — proof and checkpointing prevent duplicate or false pushes
- **Depends on**: Plans 006, 008, 009; consume Plan 007 artifacts only for strict fallback
- **Category**: perf / architecture / reliability
- **Planned at**: commit `e8a7b47`, 2026-08-08, plus the fingerprinted working tree

## Why this matters

The hybrid backend currently decodes complete parent/current Git trees and
downloads the complete remote repository after every queued commit. The Gew
backend fetches a complete remote Tree after every commit. Push therefore
scales as queued commits × repository size even when each commit changes one
file. REST mutation responses, expected-head compare-and-swap, commit details,
tree/blob identities, and changed-path byte reads can provide equivalent proof
without whole-snapshot verification on every healthy write.

## Current state

- `internal/forge/forge.go:106-119` returns only commit ID, parents, and a
  conditional-ref flag from `ApplyCommit`.
- `internal/cli/cli.go:471-543` applies each queued Gew commit, then requests
  Head and the complete Tree before checkpointing.
- `internal/cli/workspace_git.go:984-1016` loads complete child and parent byte
  snapshots to derive a commit delta.
- `internal/cli/workspace_git.go:1079-1114,1280-1317` loads the complete local
  expected tree, downloads/extracts the complete remote snapshot, compares all
  bytes, and fetches Tree again for every local commit.
- `internal/forge/github/github.go:339-436` already knows the base tree, each
  uploaded blob SHA, created tree SHA, commit SHA, and conditional ref result,
  but discards most of that evidence.
- `internal/forge/azure/azure.go:237-293` receives a conditional `oldObjectId`
  update, commit parents, and confirmed new object ID in one REST response.
- Plan 003’s invariant remains: never automatically replay an ambiguous
  mutation; inspect remote state and reconcile first. One local commit remains
  one atomic remote commit.

## Target proof model

Define explicit proof levels rather than a boolean:

1. **Mutation proof**: adapter response confirms commit ID, expected parent or
   atomic expected-head update, target ref, and submitted path set.
2. **Tree proof**: provider returns a tree identity that can be compared with
   the expected local Git tree/canonical manifest, or returns changed blob
   identities sufficient to update the prior proven manifest.
3. **Changed-byte proof**: CommitDetails confirms parent/message/path set and
   GEW reads only changed non-deleted blobs at the new exact commit and compares
   them with submitted bytes.
4. **Strict snapshot proof**: complete exact-revision snapshot comparison,
   retained only for providers lacking the above evidence, explicit diagnostic
   mode, or a proof inconsistency—not the normal per-commit path.

A healthy commit may checkpoint after mutation + tree proof, or mutation +
changed-byte proof. An ambiguous response requires CommitDetails plus tree or
changed-byte proof before checkpointing. No proof permits replay.

## Target contract

1. Extend `ApplyCommitResult` with provider-neutral evidence: target head,
   optional tree ID, and changed-file metadata. Validation copies all data and
   rejects paths/results not present in the request.
2. Add optional REST reader interfaces for exact commit tree identity and
   changed-path metadata. Capabilities declare which proof a provider supports;
   unsupported providers use strict fallback and remain correct.
3. Compute hybrid changes with go-git tree/object diffs, reading bytes only for
   created/modified paths. Do not construct parent/current repository byte maps.
4. Maintain an expected manifest by applying each local commit delta to the
   previous proven manifest. Update it from adapter evidence; do not fetch a
   complete Tree after every commit.
5. GitHub returns created tree SHA and blob SHAs. Independent blob POSTs use a
   bounded pool of at most 8; tree, commit, and ref mutations remain serial and
   are never automatically retried.
6. Azure and Gitea use their atomic REST response evidence plus exact-commit
   CommitDetails/changed blobs when no comparable tree ID is available.
7. Head visibility polling from Plan 003 remains where provider consistency
   requires it, but a confirmed mutation response must not trigger redundant
   adapter-local and caller-local Head reads.
8. Queue/tracking state checkpoints after each proven commit. Prepared journals
   survive cancellation/failure. Partial queues resume without duplicate writes.
9. Fetch at most one complete Tree per push invocation to initialize or audit
   the manifest, not one per commit. Complete snapshot bytes are zero on the
   healthy supported-provider path.
10. GitLab and Bitbucket push safety gates remain disabled; implement proof
    contracts/tests without enabling them.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Forge proof | `go test -race -run 'Apply.*Proof|TreeIdentity|ChangedByte|GitHub.*Blob|Azure.*Push|Gitea.*Push' ./internal/forge/...` | pass |
| CLI push | `go test -race -run 'Push|GitExport|Ambiguous|Partial|Prepared|Stale' ./internal/cli` | pass |
| Stress | `go test -race -shuffle=on -count=30 -run 'Push|GitExport|Ambiguous|Partial' ./internal/cli` | pass |
| Full | `go test -race ./...` | pass |
| Vet/format | `go vet ./... && test -z "$(gofmt -l cmd internal)"` | exit 0 |

## Scope

**In scope**:

- `internal/forge/forge.go`, `forge_test.go`
- `internal/forge/gitea/gitea.go`, tests
- `internal/forge/github/github.go`, tests
- `internal/forge/azure/azure.go`, tests
- Capability declarations/tests for GitLab/Bitbucket without enabling push
- `internal/cli/push_proof.go`, `push_proof_test.go` (create)
- `internal/cli/cli.go`, `workspace_git.go`
- `internal/cli/main_test.go`, `git_export_test.go`
- `internal/workspace/model.go` only for journal/receipt evidence fields
- `README.md`
- `advisor-plans/README.md` (status only)

**Out of scope**:

- Squashing/batching local commits or changing one-commit/one-remote-commit
- Enabling GitLab or Bitbucket push
- Automatically retrying POST/PATCH/PUT/DELETE
- Git transport, SSH, system Git, provider CLI, server extensions
- Trusting branch HEAD alone as proof of an ambiguous write

## Git workflow

- Branch: `advisor/010-rest-native-push-proof`
- Suggested commits: `test: characterize REST push proof`,
  `refactor: derive Git deltas from objects`,
  `perf: checkpoint provider commit evidence`.
- Keep proof-model changes separate from GitHub upload concurrency.

## Steps

### Step 1: Characterize provider evidence and request counts

Extend fake forges/HTTP fixtures to record exact request sequences for normal,
stale, accepted-but-lost, malformed-response, wrong-parent, wrong-path,
wrong-byte, and partial-queue cases. For a repository with 10,000 unchanged
paths and three queued one-file commits, assert the current behavior before the
refactor and the target contract after it: no per-commit Snapshot and at most
one Tree for the invocation.

**Verify**: characterization tests fail only on missing optimized proof paths.

### Step 2: Add validated proof types and capability gates

Extend result/evidence types with deep-copy validation. A capability must name
the proof strategy supported by that adapter. Reject empty/mismatched commit,
parent, tree, path, mode, or changed-file evidence. Keep provider DTOs private.

**Verify**: table tests reject forged/malformed evidence and accept each valid provider strategy.

### Step 3: Return evidence from enabled REST adapters

- GitHub: return created tree SHA and blob SHAs already present in responses.
- Azure: return confirmed ref/commit/parent plus changed metadata available or
  deterministically addressable at the returned commit; use CommitDetails and
  changed Blob reads when tree identity is absent.
- Gitea: consume its atomic files response where complete; otherwise use its
  exact commit details and changed Blob reads.

Do not add successful-write snapshot downloads. Retain strict fallback when a
fixture proves the documented response lacks sufficient evidence.

**Verify**: provider request sequences and malformed-evidence tests pass.

### Step 4: Derive Git changes from object trees

Use go-git tree diff/object IDs to enumerate create/update/delete and modes.
Read created/updated blob bytes only when building the REST request. Cache
adjacent parent tree metadata across the queue. Remove healthy-path uses of
`gitCommitSnapshot` and `verifyRemoteTree`; retain a clearly named strict
fallback for unsupported proof.

**Verify**: tests with large unchanged fake trees show reads proportional to
changed files and preserve merge-linearization/receipt behavior.

### Step 5: Checkpoint manifests incrementally

For both backends, apply proven changed metadata to the in-memory manifest,
then durably save commit receipt/tracking/queue/state in existing safety order.
On ambiguity, reconcile parent/message/paths and changed bytes before the same
checkpoint. Never discard a prepared journal until proof and state are durable.

**Verify**: fault injection after mutation, proof, receipt, tracking, state,
and journal deletion resumes with exactly one remote commit.

### Step 6: Bound independent GitHub blob uploads

Upload only blob-creation POSTs concurrently with a limit of 8. Preserve input
order when building the tree. On first error cancel outstanding work, do not
create tree/commit/ref, and do not retry mutations. Include a barrier-based
fixture proving the bound without sleeps.

**Verify**: race tests prove deterministic tree input, maximum concurrency,
and no ref mutation after any blob failure.

### Step 7: Document proof levels and performance contract

Update the safety model to explain exact REST evidence and strict fallback.
Through Plan 006, report proof strategy, changed files/bytes, request counts,
and strict fallback reason without exposing repository content or credentials.

**Verify**: run all gates plus `git diff --check`.

## Test plan

- Per-provider valid/malformed proof and exact request sequences.
- Hybrid object diff: create/update/delete/mode, merge commit, empty parent.
- Normal/stale/ambiguous/accepted-lost/wrong-parent/wrong-path/wrong-byte.
- Three-commit partial queue with failures at every durable transition.
- GitHub bounded uploads, deterministic order, cancellation, no mutation retry.
- Disabled-provider capability tests prove GitLab/Bitbucket remain gated.

## Done criteria

- [ ] Healthy supported-provider push downloads no complete repository snapshot.
- [ ] A push invocation fetches at most one complete Tree, not one per commit.
- [ ] Git change derivation reads only changed blob content.
- [ ] Every checkpoint has validated REST mutation/tree/changed-byte proof.
- [ ] Ambiguous writes never replay without reconciliation.
- [ ] One local commit remains one atomic remote commit.
- [ ] Race, stress, full, vet, format, and diff gates pass.
- [ ] GEW remains free of Git transport and provider subprocesses.

## STOP conditions

- An enabled provider cannot produce mutation, tree, or changed-byte proof
  without a full snapshot; retain strict fallback for that provider and report.
- Proof would trust only branch HEAD, message, or path names without parents/bytes.
- Optimization changes commit atomicity or prepared-journal durability.
- Concurrency would retry or reorder tree/commit/ref mutations.
- GitLab/Bitbucket enablement becomes necessary to complete shared refactoring.

## Maintenance notes

Proof capability is security- and correctness-sensitive; adding a provider
requires fixtures for accepted-but-lost responses and malformed evidence.
Reviewers should trace every state checkpoint back to explicit proof and reject
“fewer requests” changes that weaken parent, path, byte, or ambiguity checks.
