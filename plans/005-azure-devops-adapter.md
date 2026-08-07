# Plan 005: Add an Azure DevOps adapter with oldObjectId concurrency control

> **Executor instructions**: Complete Plan 001 first. Implement Azure DevOps
> Services before considering Server. Preserve the fully qualified ref name and
> send the exact expected head as `refUpdates[].oldObjectId` on every push.
>
> **Drift check (run first)**:
> `git diff --stat 2f1a3fc6723ded372aa5f94cf863c6a66b08c866..HEAD -- forge.go forge_http.go forge_registry.go main.go main_test.go README.md`

## Status

- **Priority**: P2
- **Effort**: L
- **Risk**: MED
- **Depends on**: `plans/001-provider-core-and-gitea.md`
- **Category**: direction / integration
- **Planned at**: GitHub `2f1a3fc6723ded372aa5f94cf863c6a66b08c866`, Gitea `1a860b47b8b26597ca3c1ab91971c63b621e5baf`, 2026-08-07

## Why this matters

Azure Repos exposes a Pushes API that accepts a branch update plus a batch of
file changes in one request. Its `oldObjectId` is an explicit optimistic lock,
making safe push semantics stronger than providers that expose only a parent
hint. Supporting Azure also tests whether Plan 001's identity model can handle
organization, project, repository UUID, and fully qualified refs instead of a
simple owner/repository pair.

## Current state

- Existing profiles and workspace state contain only Gitea base URL, owner,
  repository, and branch (`main.go:31-65`). Azure requires organization,
  project, repository ID, and canonical web URL.
- Existing remote writes are Gitea Contents API requests (`main.go:650-668`),
  whereas Azure expects a single push JSON document with `refUpdates` and
  `commits`.
- The generic adapter contract in Plan 001 should represent Azure's strict
  conditional update as `ConditionalRef:true`.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Format | `gofmt -w *.go` | exit 0 |
| Adapter tests | `go test -race -run 'Azure|ForgeContract' ./...` | all pass |
| Full tests | `go test -race ./...` | all pass |
| Static analysis | `go vet ./...` | exit 0 |
| Order independence | `go test -shuffle=on -count=20 ./...` | all pass |

## Suggested executor toolkit

- [Azure DevOps Pushes - Create](https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pushes/create?view=azure-devops-rest-7.1)
- [Azure DevOps Items - List](https://learn.microsoft.com/en-us/rest/api/azure/devops/git/items/list?view=azure-devops-rest-7.1)
- [Azure DevOps REST authentication guidance](https://learn.microsoft.com/en-us/azure/devops/integrate/how-to/call-rest-api?view=azure-devops)

## Scope

**In scope**:

- Azure DevOps Services at `dev.azure.com` and documented legacy
  `{organization}.visualstudio.com` URLs
- REST API version `7.1`, centralized in one adapter constant
- `forge_azure.go` (new)
- `forge_azure_test.go` (new)
- repository/ref resolution, exact-revision snapshots, conditional pushes,
  recovery, README, and help
- Microsoft Entra bearer tokens and PAT authentication

**Out of scope**:

- Azure DevOps Server/on-premises until its base path and supported API version
  receive a separate compatibility plan.
- Pipelines, pull requests, work items, policies, LFS, or project management.
- Force pushes, branch-policy bypass, and credential acquisition/storage beyond
  Plan 001's profile mechanism.
- Executable-bit-only and symlink changes while unsupported by the core.

## Git workflow

- Branch: `next/azure-devops-adapter` from completed Plan 001.
- Conventional commit example: `feat: add Azure DevOps REST adapter`.
- Preserve one queued local commit to one Azure commit.

## Steps

### Step 1: Parse Azure URLs and resolve stable repository identity

Support canonical URLs such as:

`https://dev.azure.com/{organization}/{project}/_git/{repository}`

and normalize documented legacy organization hosts. Resolve the repository via
the Repositories API, then persist organization, project ID/name, repository
ID/name, canonical remote URL, and default branch. Use repository and project
IDs for API calls after resolution so renames do not invalidate workspaces.

Keep `refs/heads/...` intact internally; strip or add the prefix only at CLI
boundaries. Correctly encode project and repository path segments and reject
ambiguous URLs rather than guessing whether a segment is an organization or
project.

**Verify**: parser fixtures cover spaces, Unicode, IDs, legacy hosts, `.git`,
renames, missing projects, default branch, and non-Services hosts.

### Step 2: Implement explicit authentication and shared HTTP behavior

Prefer Microsoft Entra access tokens through `Authorization: Bearer`. Support
PATs through Azure's documented Basic authentication form as a separate auth
kind. Never serialize either credential into repository identity, URLs, `.gew/`,
errors, or debug bodies. Preserve Azure correlation headers while redacting
authorization and signed query parameters.

Handle JSON error envelopes, 401/403 scope failures, 409 ref conflicts, 429 and
5xx retry metadata, cancellation, and bounded response bodies through Plan
001's shared client. Keep `api-version=7.1` on every endpoint.

**Verify**: exact-header tests cover Bearer/PAT, redirects cannot leak auth to a
different host, errors are actionable, and diagnostics are secret-free.

### Step 3: Resolve heads and fetch exact-revision snapshots

Use the Refs API filtered to the fully qualified branch to obtain its
`objectId`. Resolve the commit before downloading. Use Items List with
`recursionLevel=Full`, the exact version descriptor, and `$format=zip` where
supported to retrieve a snapshot. If ZIP behavior is unsuitable for a response
or server variant, fall back to paginated item metadata plus bounded content
fetches, still pinned to the exact commit ID.

Feed ZIP content through the existing safe archive extractor. Normalize Azure
root paths, reject traversal/symlinks under current policy, preserve binary
bytes, and verify that metadata did not silently resolve the moving branch.

**Verify**: tests cover ZIP/list forms, folder recursion, binary/empty content,
root paths, paging, moving branches, missing refs, empty repositories, and size
limits.

### Step 4: Translate one queued commit into one conditional push

POST to:

`.../_apis/git/repositories/{repositoryId}/pushes?api-version=7.1`

with one ref update and one commit:

```json
{
  "refUpdates": [
    {"name": "refs/heads/main", "oldObjectId": "EXPECTED_HEAD"}
  ],
  "commits": [
    {
      "comment": "message",
      "changes": [
        {
          "changeType": "edit",
          "item": {"path": "/path/file"},
          "newContent": {
            "content": "BASE64_BYTES",
            "contentType": "base64encoded"
          }
        }
      ]
    }
  ]
}
```

Map local add/update/delete to Azure `add`, `edit`, and `delete`. Omit
`newContent` for delete. Use Base64 for arbitrary bytes and normalized
repository-absolute paths. Return the new commit/ref IDs and
`ConditionalRef:true` only after confirming the ref update succeeded.

For a new branch or empty repository, implement only behavior documented and
proven by a live test, including the required all-zero `oldObjectId` and commit
base semantics. Do not guess or fall back to updating another ref.

**Verify**: exact JSON tests cover all operations, binary bytes, deletes,
branch names, new branches, empty repositories, duplicate paths, hostile paths,
and server validation failures.

### Step 5: Reconcile conflicts and ambiguous responses

Treat rejected `oldObjectId` as an ordinary stale-head result: do not retry the
same push automatically, leave the local commit queued, fetch the new head, and
enter the generic pull/merge workflow. Never replace `oldObjectId` with the new
head without recomputing the merge.

When the connection fails after request transmission, query the branch head,
push/commit metadata, parent, changed paths, and final bytes. If the exact
intended commit is present once and parented by `ExpectedHead`, record success;
if not, preserve recovery state and require synchronization. A retry must not
create a duplicate Azure commit.

**Verify**: forge contract tests cover strict stale-head rejection, branch
policy failure, same/unrelated changes, lost 2xx, delayed reads, duplicate retry
prevention, and malformed push responses.

### Step 6: Run live Azure Services conformance and document it

Use a disposable Azure DevOps project/repository to test exact snapshot,
create/update/delete, non-ASCII paths, new branch, empty repository if
available, stale `oldObjectId`, branch policies, lost response reconciliation,
and pull merge/abort/continue. Keep tests opt-in via `GEW_TEST_AZURE_*` and make
normal CI entirely offline.

Document login and clone forms, required scopes/permissions, Entra preference,
PAT support, Services-only scope, and branch-policy errors. Example:

```sh
gew login --provider azure https://dev.azure.com/my-organization
gew clone my-project/my-repository
```

**Verify**: live tests pass repeatedly; help examples have parser tests; README
capabilities match the provider registry.

## Test plan

- Canonical/legacy URL and repository/project identity matrix.
- Entra/PAT authentication, redirect isolation, and secret redaction.
- Ref resolution and exact-commit ZIP/list snapshots.
- Exact Pushes API JSON for mixed binary add/edit/delete.
- Strict `oldObjectId` conflict and branch-policy responses.
- New branch, empty repository, ambiguous response, and duplicate prevention.
- Optional live tests controlled by `GEW_TEST_AZURE_*` variables.

## Done criteria

- [ ] Azure and forge-contract tests pass with `-race`.
- [ ] Full, shuffled, vet, and earlier provider tests pass.
- [ ] Stable project/repository IDs survive rename fixtures.
- [ ] Snapshots are pinned to one immutable commit.
- [ ] One local queued commit produces at most one Azure commit.
- [ ] Every existing-branch push carries the exact expected `oldObjectId`.
- [ ] A stale head never overwrites remote content or silently rebases.
- [ ] Ambiguous results reconcile without duplicate commits.
- [ ] Entra/PAT credentials never enter `.gew/` or diagnostics.
- [ ] README states Azure DevOps Services scope accurately.
- [ ] `plans/README.md` status row is updated.

## STOP conditions

- Any existing-branch push omits or weakens `oldObjectId`.
- A conflict path retries against a new head without running merge logic.
- New-branch behavior requires updating or resetting an unrelated ref.
- Snapshot content cannot be pinned to the resolved commit ID.
- Authentication can cross-host redirect boundaries or appear in diagnostics.
- Azure DevOps Server support begins without a separate compatibility matrix.

## Maintenance notes

Keep the API version and Azure URL normalization centralized. Re-run live tests
when Microsoft changes Pushes, Items, identity, or authentication guidance.
Azure commit IDs, ref object IDs, repository IDs, and project IDs are distinct;
reviewers should reject code that treats them as interchangeable strings even
when fixtures happen to use SHA-shaped values.
