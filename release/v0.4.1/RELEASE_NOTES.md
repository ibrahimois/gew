# git ew (`gew`) v0.4.1

`gew` v0.4.1 hardens the five-provider adapter boundary while preserving the
v0.4 workspace format, command syntax, and provider safety gates.

## Highlights

- Smaller structural roles for repository reads, native snapshots, commit
  inspection, and safe writes.
- One validated writer boundary for request normalization, path/operation
  validation, capability gates, and expected-parent result checks.
- Provider-local stale-head classification: HTTP 409/412/422 responses are no
  longer globally misreported as concurrency failures.
- A deterministic, size-bounded Tree+Blob ZIP fallback shared by Azure DevOps
  and Bitbucket Cloud, with executable-mode and binary-content preservation.
- One ordered provider catalog for factories, default authentication, provider
  normalization, and help/error output.
- Reusable conformance contracts run by Gitea, GitHub, GitLab, Bitbucket Cloud,
  and Azure DevOps fixtures.

## Safety and compatibility

- Gitea, GitHub, and Azure DevOps push remain enabled with their existing
  atomicity and concurrency behavior.
- GitLab and Bitbucket Cloud push remain disabled pending live stale-write
  verification; clone and pull remain available.
- Candidate mutation conflicts become stale-head errors only when a fresh
  branch read proves the expected head changed. The sanitized provider error is
  retained for diagnosis.
- The `.gew` state schema remains version 4. No workspace migration is needed.
- No provider API version, supported host, authentication scheme, or command
  syntax changed.

## Verification

- `go test -race ./...`
- `go vet ./...`
- `go test -shuffle=on -count=20 ./...`
- `go test -cover ./...` — 69.9% statement coverage
- `gofmt`, build, and diff-hygiene gates

## Packages

This release contains macOS and Linux builds for amd64 and arm64. Each archive
contains the `gew` executable and README. Verify downloads with `SHA256SUMS`.

Go 1.22 or newer is required when building from source. The system `git`
executable is not required at runtime.
