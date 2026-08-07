# Plan 004: Add a Bitbucket Cloud adapter with live-tested parent safety

> **Executor instructions**: Complete Plan 001 first. Treat Bitbucket's
> `parents` field as an unproven concurrency control until a disposable live
> repository demonstrates stale-parent rejection. Ship clone/pull-only if that
> gate fails or remains ambiguous.
>
> **Drift check (run first)**:
> `git diff --stat 2f1a3fc6723ded372aa5f94cf863c6a66b08c866..HEAD -- forge.go forge_http.go forge_registry.go main.go main_test.go README.md`

## Status

- **Priority**: P2
- **Effort**: L
- **Risk**: HIGH
- **Depends on**: `plans/001-provider-core-and-gitea.md`
- **Category**: direction / integration
- **Planned at**: GitHub `2f1a3fc6723ded372aa5f94cf863c6a66b08c866`, Gitea `1a860b47b8b26597ca3c1ab91971c63b621e5baf`, 2026-08-07

## Why this matters

Bitbucket Cloud's Source API can create one commit containing multiple file
adds, updates, and deletions without invoking Git. That fits `gew`'s queued
commit model, but the endpoint is multipart rather than JSON and its documented
`parents` input is not an explicit compare-and-swap guarantee. Clone also needs
a paginated source-tree walk unless an authenticated archive route proves
stable. This is therefore the highest-risk adapter in the initial roadmap.

## Current state

- Workspace identity is currently an owner/repository pair
  (`main.go:48-53`), which maps reasonably to Bitbucket workspace/repo slug but
  still needs a provider tag and stable repository UUID.
- Existing push code sends JSON per-file operations (`main.go:650-668`); it
  cannot be reused to construct a streamed multipart request.
- Plan 001 must centralize parent verification and permit an adapter to expose
  clone/pull while declaring push unsupported.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Format | `gofmt -w *.go` | exit 0 |
| Adapter tests | `go test -race -run 'Bitbucket|ForgeContract' ./...` | all pass |
| Full tests | `go test -race ./...` | all pass |
| Static analysis | `go vet ./...` | exit 0 |
| Order independence | `go test -shuffle=on -count=20 ./...` | all pass |

## Suggested executor toolkit

- [Bitbucket Cloud Source API](https://developer.atlassian.com/cloud/bitbucket/rest/api-group-source/)
- [Bitbucket Cloud REST authentication and pagination](https://developer.atlassian.com/cloud/bitbucket/rest/)
- A disposable Bitbucket Cloud repository dedicated to concurrency tests.

## Scope

**In scope**:

- Bitbucket Cloud only, using `https://api.bitbucket.org/2.0`
- `forge_bitbucket.go` (new)
- `forge_bitbucket_test.go` (new)
- shared multipart, pagination, capability, and recovery tests
- workspace/repository UUID persistence from Plan 001
- README and CLI help

**Out of scope**:

- Bitbucket Data Center/Server, Pipelines, pull requests, downloads, LFS, or
  repository administration.
- Deprecated app-password onboarding for new profiles.
- Force-pushing or claiming push safety based only on mocked responses.
- Symlink or executable-bit-only changes while those remain unsupported by the
  product core.

## Git workflow

- Branch: `next/bitbucket-cloud-adapter` from completed Plan 001.
- Conventional commit example: `feat: add Bitbucket Cloud REST adapter`.
- Keep `Push:false` in capabilities until Step 5 passes live.

## Steps

### Step 1: Resolve Cloud URLs, identity, and authentication

Accept `https://bitbucket.org/{workspace}/{repo_slug}` and the shorthand
`{workspace}/{repo_slug}` under a Bitbucket Cloud profile. The API host remains
`https://api.bitbucket.org/2.0`; do not derive it by prefixing `/api` to the web
URL. Resolve repository metadata, persist its UUID, canonical full name, slug,
and default branch, and tolerate repository renames by re-resolving the UUID.

Support explicit authentication kinds:

- OAuth or repository/workspace access tokens as `Authorization: Bearer`;
- Atlassian API tokens through HTTP Basic using the Atlassian account email as
  the username and token as the password.

Never place credentials in clone URLs, workspace state, logs, or errors. Do not
offer app passwords as the recommended setup because Atlassian has deprecated
them for new integrations.

**Verify**: table tests cover URL suffixes, `.git`, UUID braces, renamed repos,
default branches, Basic/Bearer headers, malformed identity, and redaction.

### Step 2: Implement exact-revision snapshots through the Source API

Resolve the target branch to an immutable commit hash before fetching content.
Walk `/repositories/{workspace}/{repo_slug}/src/{commit}/{path}` recursively,
following `next` pagination links and directory entries. Fetch file bodies by
their exact commit/path URLs with bounded concurrency and resource limits.
Honor `max_depth` only as an optimization; never assume one response contains
the entire tree.

Evaluate Bitbucket's commit archive/download route in a live spike. Use it only
if it is officially supported for authenticated private repositories, can pin
an exact commit, and passes the existing archive safety suite. Otherwise retain
the source walk. Reject traversal, symlinks, oversized files, and inconsistent
commit metadata using the provider-neutral snapshot policy.

**Verify**: fixtures cover pagination at every level, binary and empty files,
encoded paths, rate limits, deleted paths, empty repositories, response-size
limits, and a branch advancing during the snapshot.

### Step 3: Stream one multipart request per queued commit

POST to `/repositories/{workspace}/{repo_slug}/src` using `io.Pipe` and
`multipart.Writer` so large commits are not duplicated in memory. Translate:

- add/update: a file part whose field name is the repository-absolute path;
- delete: repeated `files` form fields containing repository-absolute paths;
- message: `message`;
- target branch: `branch`;
- expected parent: `parents` set to `ExpectedHead`;
- author: only when the API accepts a valid, non-secret value.

Normalize every path before writing a MIME header and reject CR/LF, traversal,
duplicates, file/delete collisions, and reserved control fields. Use exactly
one request and require a returned commit hash and parent list. Preserve the
local queue on any HTTP, stream, rate-limit, or response-decoding failure.

**Verify**: parse captured multipart bodies in tests and assert exact fields for
mixed add/update/delete, binary bytes, unusual valid paths, empty files, large
streamed files, canceled requests, and server-side validation errors.

### Step 4: Add ambiguous-result reconciliation and parent verification

Precheck that the remote head equals `ExpectedHead`, send `parents`, and inspect
the created commit after every successful or ambiguous response. Do not infer
safety from a 2xx alone. The generic engine must verify:

- the commit exists and its first parent equals `ExpectedHead`;
- its changed path set and final bytes match the queued change;
- the target branch contains that commit;
- a retried request cannot create a duplicate commit after a lost response.

Return `ConditionalRef:true` only if Step 5 proves stale-parent requests are
rejected atomically. Otherwise return a nonconditional result and use Plan
001's accepted-but-needs-sync recovery only for demonstrated safe cases.

**Verify**: contract tests simulate stale heads, changed target files,
unrelated changes, lost responses, delayed visibility, duplicate retries, and
malformed commit-parent responses.

### Step 5: Run the mandatory live concurrency gate

Against a disposable Bitbucket Cloud repository, automate:

1. exact-revision clone and pull;
2. one commit with create/update/delete;
3. a new branch from an exact parent;
4. an unrelated remote commit between precheck and upload;
5. a same-file remote commit between precheck and upload;
6. a deliberately stale `parents` upload;
7. a response dropped after commit creation;
8. pull merge, merge continue, and merge abort.

Record the HTTP status, returned commit parents, branch head, and final bytes.
Enable push only when a stale parent cannot silently replace either related or
unrelated remote work and lost-response recovery produces no duplicate. If the
API appends to the newest head despite stale `parents`, ship clone/pull-only.

**Verify**: opt-in `GEW_TEST_BITBUCKET_*` tests pass repeatedly; ordinary tests
remain hermetic and offline.

### Step 6: Document capabilities without overclaiming

Document provider login, supported token types, public/private repositories,
Cloud-only scope, expected rate-limit behavior, and whether push passed the
live gate. Show a workflow such as:

```sh
gew login --provider bitbucket https://bitbucket.org
gew clone workspace/repository
```

If push is gated off, `gew push` must return a clear provider-capability error
and leave all local commits queued.

**Verify**: help examples have parser tests and the capability matrix matches
the registry exactly.

## Test plan

- URL, UUID, authentication-kind, and secret-redaction matrix.
- Recursive paginated snapshots pinned to an immutable hash.
- Multipart streaming for every change type and hostile path/header input.
- Request cancellation, body-size limits, 429 retry metadata, and partial reads.
- Empty repository and new-branch behavior.
- Stale-parent, same-file race, unrelated-file race, and lost-response recovery.
- Optional live tests controlled by `GEW_TEST_BITBUCKET_*` variables.

## Done criteria

- [ ] Bitbucket and forge-contract tests pass with `-race`.
- [ ] Full, shuffled, vet, and all earlier provider tests pass.
- [ ] Snapshot content is pinned to one immutable commit despite pagination.
- [ ] One local queued commit produces at most one Bitbucket commit.
- [ ] Multipart paths and headers cannot be injected or traversed.
- [ ] Authentication secrets never enter `.gew/`, output, or diagnostics.
- [ ] Push is enabled only after the live stale-parent gate passes.
- [ ] Failed or ambiguous pushes preserve a recoverable local queue.
- [ ] README says Bitbucket Cloud, not generic Bitbucket.
- [ ] `plans/README.md` status row is updated.

## STOP conditions

- A stale `parents` request can silently overwrite a concurrent same-file edit.
- An ambiguous response cannot be reconciled without creating a second commit.
- Snapshot pagination cannot be pinned to one immutable revision.
- Supporting safe push would require force/reset or multiple remote commits.
- The implementation begins conflating Cloud with Data Center endpoints.
- MIME construction permits untrusted paths to become arbitrary form headers.

## Maintenance notes

Atlassian token products and endpoint behavior evolve independently. Keep auth
kinds explicit, isolate API version/base handling, and re-run the live
concurrency suite before changing `ConditionalRef` or push capability. Review
fixtures against current official Cloud documentation; mocked `parents`
behavior is never sufficient evidence to enable push.
