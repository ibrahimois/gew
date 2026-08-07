# git ew (`gew`) v0.3.0

`gew` is a Git-like, REST-only workspace client for Gitea environments where
the normal Git executable or Git transport is unavailable.

## Highlights

- Git-like staging with immutable content snapshots.
- Local queued commits that are pushed to Gitea individually and in order.
- REST-only clone, pull, and atomic multi-file push.
- Three-way text merge with diff3-style conflict markers.
- Recoverable `gew merge --continue` and `gew merge --abort` workflows.
- Binary conflict preservation under `.gew/conflicts/`.
- Safe refusal when the remote branch advances before a push.
- Ambiguous-push reconciliation after a dropped network response.

## Packages

This release contains macOS and Linux builds for amd64 and arm64. Each archive
contains the `gew` executable and README. Verify downloads with `SHA256SUMS`.

## Requirements

- A Gitea instance exposing the branch, archive, recursive tree, blob, commit,
  and atomic multi-file contents APIs.
- A Gitea access token with repository read/write permission.
