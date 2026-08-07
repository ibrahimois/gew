# Plan 003: Add a GitLab REST adapter with parent-verified batch commits

> **Executor instructions**: Complete Plan 001 first. Do not claim branch-wide
> compare-and-swap semantics that GitLab's Commits API does not provide. Keep
> push disabled until file-level locks and post-commit parent recovery pass live
> tests.
>
> **Drift check (run first)**:
> `git diff --stat 2f1a3fc6723ded372aa5f94cf863c6a66b08c866..HEAD -- forge.go forge_http.go forge_registry.go main.go main_test.go README.md`

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED-HIGH
- **Depends on**: `docs/plans/001-provider-core-and-gitea.md`
- **Category**: direction / integration
- **Planned at**: GitHub `2f1a3fc6723ded372aa5f94cf863c6a66b08c866`, Gitea `1a860b47b8b26597ca3c1ab91971c63b621e5baf`, 2026-08-07
- **Execution result**: Completed in clone/pull-only mode. The adapter and
  locked batch-commit request are fixture-tested, but no live GitLab sandbox
  credentials were available for the mandatory same-file concurrency gate, so
  `Push` remains false in the registry.

## Why this matters

GitLab provides an atomic batch commit endpoint whose `actions[]` map naturally
to `gew` create/update/delete operations. It supports GitLab.com and self-managed
instances through the same `/api/v4` shape. The primary risk is concurrency:
GitLab offers `last_commit_id` for changed files, but no documented
branch-wide expected-head field for committing to an existing branch. The
adapter therefore needs both file-level optimistic locks and generic
post-commit parent verification.

## Current state

- Existing workspace identity assumes one owner and one repository
  (`main.go:48-53`); GitLab permits nested namespaces and should use a canonical
  numeric project ID after resolution.
- Current Gitea push prechecks the branch head (`main.go:609-625`) and attaches
  remote blob SHAs to changed files (`main.go:704-728`). GitLab's analogous
  lock is a file's `last_commit_id`, not its blob SHA.
- Plan 001 must expose nonconditional commit results and unexpected-parent
  recovery before this adapter can enable push.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Format | `gofmt -w *.go` | exit 0 |
| Adapter tests | `go test -race -run 'GitLab|ForgeContract' ./...` | all pass |
| Full tests | `go test -race ./...` | all pass |
| Static analysis | `go vet ./...` | exit 0 |
| Order independence | `go test -shuffle=on -count=20 ./...` | all pass |

## Suggested executor toolkit

- [GitLab Commits API](https://docs.gitlab.com/api/commits/)
- [GitLab Repositories API](https://docs.gitlab.com/api/repositories/)
- GitLab Branches, Repository Files, and Projects API documentation for the
  exact server version targeted by live tests.

## Scope

**In scope**:

- `forge_gitlab.go` (new)
- `forge_gitlab_test.go` (new)
- `forge_contract_test.go` for generic nonconditional-parent cases
- provider registry and URL/profile parsing from Plan 001
- generic push recovery only if Plan 001's specified path is incomplete
- `main_test.go`
- `README.md`

**Out of scope**:

- GitLab merge requests, issues, CI, releases, LFS uploads, or group management.
- Force-pushing/resetting an existing branch.
- Treating a GitLab blob ID as `last_commit_id`.
- GitHub, Bitbucket, or Azure code.

## Git workflow

- Branch: `next/gitlab-adapter` from completed Plan 001.
- Conventional commit example: `feat: add GitLab REST adapter`.
- Keep the adapter clone/pull-only until the live concurrency gate passes.

## Steps

### Step 1: Resolve GitLab servers and nested projects

Support:

- `https://gitlab.com/group/subgroup/repository`
- explicitly configured self-managed GitLab URLs;
- numeric project IDs when returned by the API;
- `group/subgroup/repository` under an active GitLab profile.

Map the API base to `<server>/api/v4`. URL-encode the complete namespace/project
path as one project identifier during initial resolution, then persist the
numeric project ID and canonical `path_with_namespace`. Do not split the path
into a fixed owner/repository pair.

Support bearer/OAuth and `PRIVATE-TOKEN` authentication as explicit profile
auth kinds. Never send both. Probe via `/user` and resolve via `/projects/:id`.

**Verify**: URL tests cover nested namespaces, spaces/escaping, self-managed
base paths, numeric IDs, moved projects, bad credentials, and redaction.

### Step 2: Implement read operations and archive snapshots

Implement branch head, commit details/parents, recursive repository tree with
pagination, blob retrieval, and ZIP archive download pinned to an exact commit
SHA. Prefer the stable individual blob endpoint; do not depend on GitLab's beta
batch-blob endpoint. Handle GitLab's documented tree `404` behavior for missing
paths and repository archive rate limits.

The archive extractor must continue rejecting traversal and symbolic links
according to existing project policy. A provider archive prefix may differ from
Gitea's and must be normalized by tests, not assumptions.

**Verify**: fixtures cover recursive pagination, nested namespaces, binary
blobs, archive formats/prefixes, rate-limit errors, empty projects, and missing
paths.

### Step 3: Translate `RemoteChange` into one `actions[]` commit

POST one JSON request to `/projects/:id/repository/commits`:

- `branch`: target branch;
- `commit_message`: local commit message;
- `actions`: `create`, `update`, or `delete` entries;
- `content`: Base64 and `encoding: base64` for create/update;
- `last_commit_id`: the last known **file commit ID** for every update/delete;
- `start_sha`: exact base when creating a new branch.

Fetch and cache `last_commit_id` from Repository Files metadata for every
existing changed path. Do not substitute tree blob IDs. Keep request-size/rate
limit errors actionable and leave the local queue intact.

Return GitLab's commit ID and `parent_ids` with `ConditionalRef:false`.

**Verify**: exact JSON tests cover all actions, binary data, nested paths,
file-lock IDs, new branch, empty repository, malformed responses, and request
limits.

### Step 4: Enforce concurrency and recover an unexpected parent

Immediately before POST, require current head equals `ExpectedHead`. Supply all
available file-level locks. After POST:

- If first parent equals `ExpectedHead`, proceed normally.
- If the first parent differs, do not report a clean push. The remote commit may
  have safely appended to a concurrently advanced branch. Verify the created
  commit's complete path set and bytes, persist an accepted-but-needs-sync push
  recovery record, dequeue only after exact verification, then invoke the
  generic pull/three-way merge from the old base.
- If any remotely changed target file was overwritten or cannot be verified,
  stop, preserve recovery metadata, and do not advance the local base.

Tests must include an unrelated concurrent remote change and a concurrent
change to the same target file. The latter must be rejected by `last_commit_id`
or keep GitLab push disabled.

**Verify**: reusable forge tests cover same-parent success, unrelated
concurrent append recovery, conflicting-file rejection, and ambiguous lost
response.

### Step 5: Run a live GitLab conformance gate

Against a disposable GitLab.com or self-managed sandbox project, test:

- clone and exact-SHA snapshot;
- create/update/delete in one commit;
- new branch from exact SHA;
- remote unrelated change between precheck and commit;
- remote same-file change between precheck and commit;
- dropped/ignored response after commit creation;
- pull merge and merge abort/continue.

Enable push in the registry only if no test silently overwrites a same-file
remote change and unexpected-parent recovery leaves a synchronized workspace.
Otherwise ship the adapter as clone/pull-only with an explicit capability and
error message.

**Verify**: live contract suite passes; normal tests remain offline and pass.

### Step 6: Document GitLab use and limitations

Document:

```sh
GEW_TOKEN=... gew login --provider gitlab https://gitlab.com
gew clone group/subgroup/repository
```

Describe token scopes (`read_repository` plus API/write capability required by
the commit endpoint), self-managed `/api/v4`, archive rate limits, and the
concurrency capability actually proven by tests. Do not claim GitLab push if it
remains gated.

**Verify**: documented forms have parser tests and all help text matches CLI.

## Test plan

- Nested namespace and numeric project identity matrix.
- Tree pagination and 404 behavior.
- Batched create/update/delete, Base64 binary content, and file modes supported
  by GitLab actions.
- Distinguish blob ID from file `last_commit_id` in fixtures.
- Same-file race, unrelated-file race, and unexpected-parent recovery.
- Empty project and new branch behavior.
- Authentication modes and secret redaction.
- Optional live tests controlled by `GEW_TEST_GITLAB_*` variables.

## Done criteria

- [ ] GitLab and forge-contract tests pass with `-race`.
- [ ] Full, shuffled, vet, Gitea, and any completed GitHub tests pass.
- [ ] One local queued commit produces one GitLab commit.
- [ ] Existing-file actions carry real file `last_commit_id` values.
- [ ] Same-file races cannot silently overwrite remote bytes.
- [ ] Unexpected parents trigger deterministic sync/recovery, not a clean
  status.
- [ ] Nested namespaces and self-managed base URLs work.
- [ ] GitLab tokens never enter `.gew/` or diagnostics.
- [ ] README describes only capabilities proven live.
- [ ] `docs/plans/README.md` status row is updated.

## STOP conditions

- Plan 001 cannot represent a nonconditional commit or unexpected parent.
- GitLab accepts a stale `last_commit_id` and overwrites the changed remote file.
- Parent mismatch recovery would discard local or remote changes.
- Implementing safety appears to require `force:true` or branch reset.
- A project path cannot be resolved without losing nested namespace identity.
- The only workable design would create multiple remote commits for one local
  commit.

## Maintenance notes

GitLab.com and self-managed versions can differ in limits and newer optional
fields. Keep compatibility parsing tolerant but request generation conservative.
Reviewers should scrutinize the difference between blob SHA, commit SHA, and
`last_commit_id`, as well as project-path encoding. Re-run live concurrency
tests when changing the Commit API version assumptions.
