# git ew (`gew`) v0.5.0

`gew` v0.5.0 replaces its handwritten command parser with
`github.com/urfave/cli/v3` v3.10.1 while preserving workspace and provider
safety semantics.

## Highlights

- One declarative command graph now owns commands, flags, aliases, required
  options, positional arity, and generated root and per-command help.
- Bash, zsh, fish, and PowerShell completion scripts are available through
  `gew completion`.
- `RunContext` carries cancellation into remote clone, pull, push, merge,
  migration, recovery, and profile-probe requests. The executable cancels work
  on an interrupt signal.
- `gew -v` joins `gew version` and `gew --version` as an exact version alias.
- Flags may appear after positional arguments; `--` terminates flag parsing for
  paths beginning with `-`.
- Merge modes are required and mutually exclusive, merge messages are accepted
  only with `--continue`, commit messages must be nonblank, and migration
  targets are validated as `git` before workspace access.

## Safety and compatibility

- The `.gew` state schema remains version 4. No workspace migration is needed.
- Provider URLs, request behavior, authentication methods, saved profiles, and
  environment precedence are unchanged.
- Gitea, GitHub, and Azure DevOps push remain enabled. GitLab and Bitbucket
  Cloud push remain safety-gated.
- Existing command names, long flags, `-A`, and `-m` remain supported.
- Errors are still returned by the CLI library; only `cmd/gew` prints the final
  `gew:` prefix and selects exit status 1.

## Verification

- `test -z "$(gofmt -l cmd internal)"`
- `go vet ./...`
- `go test ./...`
- `go test -race ./...`
- `go test -shuffle=on -count=20 ./...`
- `go test -cover ./...`
- host build and help/version/completion smoke tests
- four-archive checksum, membership, and Go module metadata verification

## Packages

This release contains macOS and Linux builds for amd64 and arm64. Each archive
contains exactly the `gew` executable and README. Verify downloads with
`SHA256SUMS`.

Go 1.22 or newer is required when building from source. The system `git`
executable is not required at runtime.
