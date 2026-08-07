# git ew (`gew`) v0.4.0

`gew` v0.4.0 expands the REST-only client into a multi-forge tool and adds an
opt-in standards-compliant local Git workspace without introducing Git
transport.

## Highlights

- Provider-neutral REST engine with adapters for Gitea, GitHub, GitLab,
  Bitbucket Cloud, and Azure DevOps Services.
- Safe GitHub Git-database pushes for non-empty repositories.
- Exact-commit snapshots and stable provider repository identities.
- Opt-in `gew clone --backend git` hybrid workspaces with a real local `.git`.
- Local Git OID to provider commit-ID receipts and crash-safe export journals.
- Stale-head refusal and accepted-but-lost response reconciliation.
- Hybrid clean pull, queued-work linearization, conflict continue/abort, and
  branch creation.
- Transactional `gew migrate --to git --dry-run` and migration of version-1,
  version-2, and version-3 Gew workspaces with retained legacy data.

## Safety and provider scope

- All remote access continues through forge REST APIs. The production binary
  does not execute `git`, hooks, filters, credential helpers, or Git transport.
- GitLab and Bitbucket Cloud push remain disabled until their provider-specific
  live stale-write gates pass; clone and pull are available.
- GitHub initial push to an empty repository is refused because GitHub's REST
  API cannot create the first reference. Existing non-empty repositories push
  normally.
- Azure DevOps Services push uses exact `oldObjectId` concurrency control.
  Azure DevOps Server is outside this release's compatibility scope.
- Hybrid mode remains opt-in.

## Release gate

A new empty private Gitea repository passed initial hybrid push with text,
binary, empty, and Unicode-path files; fresh clone; staging/reset; stale-head
refusal; queued-work pull; conflict abort/continue; new-branch creation; real
v0.3.0 state-version-2 migration; accepted-but-lost response reconciliation;
and `git fsck --full` inspection.

## Packages

This release contains macOS and Linux builds for amd64 and arm64. Each archive
contains the `gew` executable and README. Verify downloads with `SHA256SUMS`.

## Requirements

- Go 1.22 or newer when building from source.
- A supported forge REST API and token with the required repository scope.
- The system `git` executable is not required at runtime.
