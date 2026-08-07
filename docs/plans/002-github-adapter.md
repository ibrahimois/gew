# Plan 002: Add a GitHub REST adapter with atomic Git-object commits

> **Executor instructions**: Complete Plan 001 first. Follow every step and
> verification gate here. Stop on a listed STOP condition rather than changing
> push semantics or falling back to the Git protocol.
>
> **Drift check (run first)**:
> `git diff --stat 2f1a3fc6723ded372aa5f94cf863c6a66b08c866..HEAD -- forge.go forge_http.go forge_registry.go main.go main_test.go README.md`
> Confirm the Plan 001 contract still has expected-head, parent, capability,
> tree, blob, snapshot, commit-detail, and apply-commit semantics.

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED
- **Depends on**: `docs/plans/001-provider-core-and-gitea.md`
- **Category**: direction / integration
- **Planned at**: GitHub `2f1a3fc6723ded372aa5f94cf863c6a66b08c866`, Gitea `1a860b47b8b26597ca3c1ab91971c63b621e5baf`, 2026-08-07
- **Execution revision**: GitHub empty repositories remain cloneable, but push
  is supported only after the repository has an existing branch head. An
  attempted initial push must return a clear capability error and preserve the
  complete local queue. The adapter must not bootstrap through the per-file
  Contents API because that would violate atomic one-local-commit /
  one-remote-commit semantics for multi-file commits.

## Why this matters

GitHub's per-file Contents API would turn one queued multi-file commit into
multiple remote commits. Its Git database API is a better match: create blobs,
create a tree based on the expected tree, create one commit with the expected
parent, then update the branch reference without force. This retains `gew`'s
one-local-commit/one-remote-commit model and rejects concurrent divergence at
the ref update.

## Current state

- Plan 001 is expected to expose a provider-neutral `Forge` contract and a
  compile-time adapter registry.
- `staging.go:31-47` already stores immutable content object IDs and messages;
  the GitHub adapter should consume `RemoteChange`, not local object paths.
- `main.go:619-629` currently refuses a changed remote head and reconciles an
  ambiguous prior push. Preserve both behaviors through the generic engine.
- GitHub REST requires a different public API base from its web base:
  `https://github.com` maps to `https://api.github.com`; GitHub Enterprise uses
  `<server>/api/v3`.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Format | `gofmt -w *.go` | exit 0 |
| Adapter tests | `go test -race -run 'GitHub|ForgeContract' ./...` | all pass |
| Full tests | `go test -race ./...` | all pass |
| Static analysis | `go vet ./...` | exit 0 |
| Order independence | `go test -shuffle=on -count=20 ./...` | all pass |

## Suggested executor toolkit

- [GitHub Git database guide](https://docs.github.com/en/rest/guides/using-the-rest-api-to-interact-with-your-git-database)
- [Git trees API](https://docs.github.com/en/rest/git/trees)
- [Git commits API](https://docs.github.com/en/rest/git/commits)
- [Git references API](https://docs.github.com/en/rest/git/refs)
- [Repository archive API](https://docs.github.com/en/rest/repos/contents)

Use the documented API version header centrally; do not scatter version strings
through endpoint methods.

## Scope

**In scope**:

- `forge_github.go` (new)
- `forge_github_test.go` (new)
- `forge_contract_test.go` only to add reusable cases exposed by this adapter
- `forge_registry.go`
- profile/repository URL parsing files created by Plan 001
- `main_test.go` for CLI-level GitHub fixtures
- `README.md`

**Out of scope**:

- GitHub issues, pull requests, releases, Actions, LFS uploads, or GitHub Apps.
- GitHub's per-file Contents API as the push implementation.
- Native Git protocol or `.git` creation.
- GitLab, Bitbucket, or Azure code.
- Force-updating a reference.
- Creating the first branch or commit in an empty GitHub repository.

## Git workflow

- Branch: `next/github-adapter` from completed Plan 001.
- Conventional commits, for example `feat: add GitHub REST adapter`.
- Do not enable the adapter in help/README until fixture and live contract tests
  pass.

## Steps

### Step 1: Add GitHub URL, profile, and repository resolution

Recognize these inputs without ambiguous guessing:

- `https://github.com/OWNER/REPO`
- `github.com/OWNER/REPO`
- `OWNER/REPO` when the active profile is explicitly GitHub
- GitHub Enterprise web URLs under an explicitly configured profile

Strip one optional `.git` suffix. Reject extra path components for GitHub.com.
Preserve case for display, but do not use case-sensitive comparisons for owner
or repository names. Resolve repository metadata through
`GET /repos/{owner}/{repo}` and store canonical owner/name/default branch.

Profile authentication is `Authorization: Bearer <token>`. Send
`Accept: application/vnd.github+json`, a stable `X-GitHub-Api-Version`, and the
existing `gew/<version>` user agent. Probe with an authenticated endpoint and
distinguish bad credentials from a missing repository.

**Verify**: table tests cover GitHub.com, Enterprise, `.git`, URL escaping,
invalid paths, missing scopes, and token redaction.

### Step 2: Implement read operations and snapshots

Implement:

- branch head/ref lookup;
- commit details including parent IDs and changed paths;
- recursive trees with GitHub's `truncated` handling;
- blob retrieval with Base64 decoding and response-size limits;
- zipball snapshot download with redirect following and authorization stripped
  when redirecting to a different host unless GitHub requires it safely.

If a recursive tree is truncated, fall back to walking subtrees; never treat a
partial tree as complete. Normalize archive top-level prefixes through the
existing safe extractor.

**Verify**: `httptest` fixtures cover redirects, truncated trees, pagination /
walk fallback, binary blobs, empty repository, malformed Base64, oversized
responses, and archive traversal rejection.

### Step 3: Implement one queued commit through the Git database API

For `ApplyCommit(ExpectedHead, Changes)`:

1. Resolve `ExpectedHead` to its commit and base tree.
2. For each create/update, create a blob using Base64 content.
3. Build tree entries with normalized path, blob SHA, and supported mode.
4. Represent deletions with a null SHA in the tree request.
5. Create a tree using `base_tree` so untouched files remain.
6. Create one commit with exactly `ExpectedHead` as its sole parent.
7. For an existing branch, PATCH its ref with `force: false`.
8. For `--new-branch`, create `refs/heads/<name>` pointing to the new commit;
   reject an existing target branch.
9. Return the created commit ID, parent, and `ConditionalRef: true`.

If `ExpectedHead` is empty, stop before creating any Git object and return an
unsupported-capability error explaining that GitHub REST cannot create a ref
in an empty repository. Leave every local commit queued. Empty repositories
remain cloneable so they can be initialized through another Git client or the
GitHub UI and then pulled by `gew`.

Blob/tree/commit objects created before a rejected ref update are unreachable
and must not be reported as a pushed commit. Never retry the ref update with
`force: true`.

**Verify**: request-order tests assert exact parents, base tree, deletion
encoding, file modes, and `force:false`. A stale-ref fixture must leave the
queue intact and return the generic run-pull-first error.

### Step 4: Implement ambiguous-response reconciliation

Test failure after GitHub accepted the ref update but before the client received
the response. On retry, the generic engine should see the remote head changed
and use GitHub commit/tree/blob details to prove that the first queued commit is
already present. It may dequeue only after message, parent, complete changed
path set, and exact bytes match.

Also test failure before ref update: unreachable Git objects must not be mistaken
for a pushed commit because the branch head did not change.

**Verify**: both ambiguous cases pass in the reusable forge contract suite.

### Step 5: Add opt-in live tests and enable the adapter

Use a disposable GitHub repository and a token supplied only through test
environment variables. Cover clone, add/commit, push, pull, non-overlapping
merge, conflict/abort, new branch, stale head, and ambiguous-response recovery
where it can be simulated safely. Delete only branches/repositories explicitly
created by the test harness.

Document:

```sh
GEW_TOKEN=... gew login --provider github https://github.com
gew clone OWNER/REPO
```

Document the minimum fine-grained token permission: repository Contents read
and write. Mention Enterprise base URL behavior.

**Verify**: live suite passes, then normal full/race/shuffled suites pass with
network disabled.

## Test plan

- Provider fixtures for every endpoint and error status used.
- URL matrix including GitHub Enterprise subpaths and `.git` suffixes.
- Multi-file commit with text, binary, delete, and mode preservation.
- Stale branch between commit creation and ref update.
- Lost response after successful ref update.
- Recursive tree truncation fallback.
- Zip redirect with no credential leakage to an untrusted host.
- Empty repository clone plus non-mutating initial-push refusal.

## Done criteria

- [ ] GitHub fixture and forge-contract tests pass under `-race`.
- [ ] Full, shuffled, vet, and existing Gitea tests pass.
- [ ] One queued multi-file commit creates exactly one GitHub commit.
- [ ] Branch updates always use `force:false`.
- [ ] Stale refs preserve the local queue and remote work.
- [ ] Ambiguous successful ref updates reconcile without duplicate commits.
- [ ] Truncated tree responses never produce partial workspaces.
- [ ] Empty-repository push fails before creating Git objects and preserves the
  local queue.
- [ ] GitHub tokens do not appear in workspace state, output, redirects, or
  error bodies.
- [ ] README lists supported GitHub and Enterprise forms and permissions.
- [ ] `docs/plans/README.md` status row is updated.

## STOP conditions

- Plan 001 lacks a way to express expected head, returned parents, or
  conditional reference updates.
- A required operation would need `force:true`.
- GitHub's API returns a truncated tree and no complete fallback can be built.
- Redirect handling would send credentials to a host not explicitly trusted.
- A multi-file local commit would require multiple remote commits (empty
  repositories must use the documented non-mutating refusal instead).
- Live stale-ref testing shows remote work can be overwritten.

## Maintenance notes

GitHub's REST API version and Enterprise compatibility must be centralized and
covered by fixtures. Reviewers should pay special attention to ref-update
atomicity, redirect authentication, tree truncation, file modes, and empty
repositories. If GitHub changes API-version requirements, update one adapter
constant and its compatibility tests rather than generic HTTP code.
