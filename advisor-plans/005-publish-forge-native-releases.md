# Plan 005: Publish tags and hosted releases through Gew

> **Executor instructions**: Execute only after Plans 002 and 003 are `DONE`.
> This plan adds external mutations; never run live tests against `main` or an
> existing release tag. Follow all gates and STOP conditions. Mark Plan 005
> `DONE` in the index when complete.
>
> **Drift check (run first)**:
>
> ```sh
> git diff --stat e8a7b47..HEAD -- \
>   internal/forge internal/cli/command.go internal/cli/command_test.go \
>   internal/cli/cli.go internal/cli/main_test.go README.md
> shasum -a 256 internal/forge/forge.go internal/forge/http.go \
>   internal/forge/gitea/gitea.go internal/forge/github/github.go \
>   internal/cli/command.go internal/cli/cli.go README.md
> ```
>
> Reconcile all planned changes from 002/003. The pre-plan v0.5.0 fingerprints
> are recorded in those plans. STOP if another release/tag feature now exists.

## Status

- **Status**: DONE
- **Priority**: P1
- **Effort**: L (multi-day, including live conformance)
- **Risk**: HIGH — creates public tags/releases and uploads binary assets
- **Depends on**: Plans 002 and 003
- **Category**: direction / release / DX
- **Planned at**: commit `e8a7b47`, 2026-08-08, against published v0.5.0 source

## Why this matters

Gew pushed all v0.5.0 source and artifacts, but both forge sidebars still
showed v0.4.1 because Gew cannot create tags or hosted Release objects. The
operator had to finish with `gh release` and direct Gitea API calls, including
manual multipart retry/reconciliation. A REST-only forge client should be able
to publish and safely resume its own GitHub/Gitea releases.

## Current state

- README explicitly lists tags as unsupported and exposes no release command.
- `Forge` has reader roles and optional commit writer/inspector roles, but no
  release-publisher role.
- `internal/cli/command.go:82-97` defines the complete command graph; release
  help/completion must be added there.
- GitHub and Gitea adapters already own authentication, repository identity,
  bounded HTTP, error redaction, and URL escaping. Release endpoints belong in
  those adapters, not CLI conditionals.
- The successful v0.5.0 convention is: tag `v0.5.0`, title `gew v0.5.0`, notes
  from `release/v0.5.0/RELEASE_NOTES.md`, four archives, and `SHA256SUMS`.
- During manual Gitea publication, multipart uploads stalled until HTTP/1.1
  was used with `Expect: 100-continue` disabled. Plan 003's characterized
  transport result is authoritative; do not cargo-cult curl behavior.

## Public command contract

Implement this first-release surface:

```text
gew release create TAG --title TITLE --notes-file PATH \
  --asset PATH [--asset PATH ...] [--draft] [--prerelease] [--resume]
```

- Must run inside an existing synchronized workspace.
- Refuse staged, unstaged, queued, merge, or prepared-export state. Require
  remote Head to equal the workspace's exact `BaseCommit`; that immutable ID is
  the release/tag target. No branch-name target is accepted in v0.6.0.
- `TAG` and title are nonblank; tag rejects whitespace/control characters and
  unsafe ref segments. Notes must be a regular file of at most 1 MiB.
- Require at least one `--asset`. Assets must be readable regular files, at
  most 1 GiB each, have unique basenames, and may not be symlinks. Compute
  SHA-256 and size before any mutation.
- A stable non-draft release requests Latest behavior. Draft/prerelease never
  does. GitHub's explicit latest field and Gitea's latest selection must be
  tested rather than assumed.
- Without `--resume`, an existing tag/release is an error. With `--resume`, the
  target, title, notes, draft/prerelease state, and every existing same-name
  asset must match exactly. Skip exact assets and upload only missing ones.
- Within one invocation, a lost create/upload response is reconciled
  automatically even without `--resume`; the flag is for a later invocation.
- Never delete/replace a tag, release, or asset in this initial command.

## Provider-neutral contract

Add an optional role rather than widening base `Forge`:

```go
type ForgeReleasePublisher interface {
    FindReleaseByTag(context.Context, RepositoryRef, string) (RemoteRelease, error)
    CreateRelease(context.Context, CreateReleaseRequest) (RemoteRelease, error)
    ListReleaseAssets(context.Context, RepositoryRef, string) ([]RemoteReleaseAsset, error)
    UploadReleaseAsset(context.Context, RepositoryRef, string, string, int64, io.Reader) (RemoteReleaseAsset, error)
    DownloadReleaseAsset(context.Context, RepositoryRef, RemoteReleaseAsset) (io.ReadCloser, error)
}
```

Names may vary, but semantics may not. Remote types carry opaque release/asset
IDs, URL, tag, exact target commit, state, name, and size—never provider DTOs,
tokens, or local paths. CLI code owns file opening and hashing so an ambiguous
upload can reopen a fresh reader after remote reconciliation.

Only GitHub and Gitea implement the role in v0.6.0. Other providers return a
clear `ErrUnsupported` before mutation.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Focused | `go test -race -run 'Release|Asset|Tag|Latest|Resume|Redact' ./...` | pass |
| Full | `go test -race ./...` | pass |
| Stress | `go test -shuffle=on -count=20 -run 'Release|Asset|Resume' ./...` | pass |
| Vet/format | `go vet ./... && test -z "$(gofmt -l cmd internal)"` | exit 0 |

## Scope

**In scope**:

- `internal/forge/release.go` and tests (create)
- `internal/forge/forge.go` only for optional role/types/errors
- `internal/forge/http.go` only for bounded streaming bodies established by Plans 002/003
- `internal/forge/gitea/gitea.go`, `gitea_test.go`
- `internal/forge/github/github.go`, `github_test.go`
- Registry/contract tests for optional capability consistency
- `internal/cli/release.go`, `release_test.go` (create)
- `internal/cli/forge_bridge.go`, `command.go`, `command_test.go`, `cli.go`
- `README.md`
- `advisor-plans/README.md` (status only)

**Out of scope**:

- Standalone tag CRUD, tag deletion, signed/annotated tag options
- Release edit/delete, asset replacement/deletion, generated notes
- GitLab, Bitbucket, or Azure release implementation
- Git transport, GitHub Actions, changelog generation, package registries
- Publishing a real project release without a separate explicit operator command

## Git workflow

- Branch: `advisor/005-forge-native-releases`
- Suggested commits: `test: define release publisher contract`,
  `feat: publish github and gitea releases`, `feat: add gew release command`,
  `docs: document release workflow`.
- Do not publish v0.6.0 merely because implementation tests pass.

## Steps

### Step 1: Lock CLI safety and publisher contracts

Write command tests for help/arity, dirty/staged/queued/merge/prepared refusal,
head mismatch, unsafe tags, invalid notes/assets, duplicate basenames, provider
unsupported, stable/draft/prerelease modes, and token-free errors. Build shared
publisher contract fixtures covering find/create/list/upload/download and typed
not-found/unsupported errors.

Fixtures must count mutation requests and simulate lost responses. Normal test
runs remain offline.

**Verify**: focused tests fail only for missing implementation.

### Step 2: Implement Gitea release publishing

Use Gitea REST endpoints to find by tag, create a release/tag at the exact
commit, list assets, upload multipart attachments, and download assets for hash
verification. Validate the 1.27 response fields; treat IDs as opaque strings in
provider-neutral code.

Construct multipart bodies in a 0600 temporary spool so Content-Length is
known and the body can be reopened once remote listing proves a retry safe.
Apply Plan 003's tested HTTP protocol choice. Disable `Expect` only through a
documented requester mechanism if Go would otherwise emit it. Clean all temp
files.

**Verify**: Gitea tests assert exact methods/escaped paths/query/body fields,
target SHA, mutation counts, multipart filename/content, and redaction.

### Step 3: Implement GitHub release publishing

Use GitHub's release-by-tag, create-release, list-assets, upload, and download
APIs. Validate any API-provided upload URL against the expected GitHub upload
origin (`uploads.github.com`, or the documented same-enterprise origin) before
attaching credentials. Reject arbitrary cross-origin upload URLs.

Set explicit Latest behavior for stable releases. Preserve GitHub API version
and media headers. Stream assets as `application/octet-stream` with known size.

**Verify**: GitHub fixtures cover github.com and enterprise URL shapes,
cross-origin rejection, latest/draft/prerelease payloads, and lost response.

### Step 4: Implement idempotent release orchestration

In provider-neutral orchestration, create once, then reconcile by tag before
considering another create. Upload one asset at a time. After an upload error,
list assets; if the name exists, download and SHA-256 it. Exact bytes count as
success; mismatched bytes are a hard conflict. If absent, one new upload may be
attempted only under the Plan 002/003 mutation rule.

For `--resume`, verify the complete release identity before adding anything.
Do not use name+size alone as integrity proof because equal-size binaries can
differ.

**Verify**: tests prove at most one successful create and one successful upload
per asset across reset, timeout, 500, and lost-2xx scenarios.

### Step 5: Add `gew release create`

Add nested urfave commands using per-invocation option storage so flag values
do not leak between runs. Perform all local validation and workspace cleanliness
checks before creating the publisher or sending a mutation. Print the final
release URL and an uploaded/skipped asset summary; never print credentials or
authenticated download URLs.

Add shell-completion/help assertions and update the root command list.

**Verify**: command tests pass, including two sequential invocations in one
process with different flags.

### Step 6: Live conformance and docs

Add opt-in GitHub and Gitea tests that create unique disposable tags/releases
in disposable repositories, upload a small binary and checksum file, resume,
download/verify, and delete only resources created by the test during cleanup.
Cleanup code must verify repository and tag prefix before deletion.

Update README quick reference, safety model, provider matrix, and limitations:
Gew supports release-created tags on GitHub/Gitea but still has no standalone
tag management. Document the exact release command and resume behavior.

Run all gates plus `git diff --check`. With operator approval, use the candidate
binary to publish a disposable prerelease on both forges; do not publish v0.6.0
as part of verification.

## Test plan

- Validation fails before mutation for every unsafe input/state.
- Create/upload ambiguity reconciles without duplicates.
- Resume compares exact target, metadata, and downloaded asset SHA-256.
- Stable is Latest; draft/prerelease is not.
- Gitea multipart and GitHub binary upload paths are bounded and resumable.
- Cross-origin credentials and token-shaped sentinels never leak.
- Unsupported providers fail before any write.

## Done criteria

- [ ] `gew release create` publishes a synchronized exact commit on GitHub and Gitea.
- [ ] Release-created tag, title, notes, state, Latest behavior, and all assets verify remotely.
- [ ] Lost responses and `--resume` never duplicate releases/assets.
- [ ] Mismatched existing metadata/assets fail closed without replacement.
- [ ] Other providers return `ErrUnsupported` before mutation.
- [ ] Full race/stress/vet/format/diff gates pass.
- [ ] Opt-in disposable live conformance passes on both forges.
- [ ] README no longer claims all tags/releases are unsupported.
- [ ] Plan 005 is `DONE`.

## STOP conditions

- A provider cannot target an exact commit when creating the tag/release.
- Safe resume would require trusting only asset name/size without bytes/digest.
- An upload URL cannot be constrained to an authenticated trusted origin.
- A retry would repeat create/upload without first checking remote state.
- Live verification requires touching an existing tag/release or default branch.

## Maintenance notes

Release publication is a mutation workflow, not a thin API wrapper. Reviewers
should audit exact-target validation, upload-origin checks, multipart temp-file
cleanup, and mutation counts. Add edit/delete or more providers only through
separate plans with the same reconciliation contract.
