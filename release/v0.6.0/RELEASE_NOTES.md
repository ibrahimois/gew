# git ew (`gew`) v0.6.0

`gew` v0.6.0 makes forge operations resilient to transient network failures,
self-heals ambiguous pushes, falls back when native archives fail, and can
publish its own GitHub and Gitea hosted releases.

## Highlights

- Read-only HTTP requests retry bounded transient transport, timeout, rate
  limit, and 5xx failures. Mutations remain single-shot until remote state has
  been reconciled. The request timeout is configurable from `1s` to `30m`.
- Accepted commits whose response is lost are reconciled in the same `gew push`
  invocation for both the native and hybrid workspace backends.
- Gitea uses HTTP/1.1 for large atomic commit and multipart release operations,
  reports raw and estimated Base64 payload sizes, and returns a typed 413 error.
- `gew uncommit` safely restores the newest unpushed commit to the staging index
  so an oversized commit can be split without rewriting remote history.
- Clone, pull, merge, and hybrid verification fall back to deterministic,
  bounded Tree+Blob snapshots when a provider archive endpoint fails.
- `gew release create` creates an exact-commit tag and hosted release on GitHub
  or Gitea, uploads assets, and supports byte-verified `--resume` recovery.

## Safety and compatibility

- The `.gew` state schema remains version 4. No workspace migration is needed.
- POST, PATCH, PUT, and DELETE requests are never blindly retried.
- Release publication requires a clean synchronized workspace and refuses to
  replace any existing tag, release, or asset.
- GitLab, Bitbucket Cloud, and Azure DevOps hosted releases remain unsupported.
- Existing provider, workspace, merge, and command behavior remains compatible.

## Verification

- `test -z "$(gofmt -l cmd internal)"`
- `go vet ./...`
- `go test ./...`
- `go test -race ./...`
- focused retry, push-reconciliation, archive-fallback, release, and resume tests
- host build and help/version smoke tests
- four-archive checksum, membership, and Go module metadata verification
- live GitHub and Gitea source push and hosted-release conformance using Gew

## Packages

This release contains macOS and Linux builds for amd64 and arm64. Each archive
contains exactly the `gew` executable and README. Verify downloads with
`SHA256SUMS`.

Go 1.22 or newer is required when building from source. The system `git`
executable is not required at runtime.
