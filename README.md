# git ew (`gew`)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/gew-logo-minimal-on-dark.png">
    <source media="(prefers-color-scheme: light)" srcset="assets/gew-logo-minimal.png">
    <img src="assets/gew-logo-minimal.png" width="760" alt="gew — Git-like, REST-only">
  </picture>
</p>

`gew` is a Git-like workspace client that talks to hosted Git forges through
their HTTPS REST APIs. It is designed for environments where the `git`
executable or Git transport is unavailable, while keeping familiar commands
such as `clone`, `add`, `commit`, `pull`, and `push`.

It supports Gitea, GitHub, GitLab, Bitbucket Cloud, and Azure DevOps through one
workspace engine. GitLab and Bitbucket push are intentionally disabled until
their concurrency behavior passes live safety verification.

`gew` is not a complete Git replacement. It focuses on everyday file changes,
local commits, safe synchronization, and recoverable merges—not tags, rebase,
cherry-pick, submodules, or arbitrary history operations.

## Quick start

```sh
# Configure credentials once.
export GEW_TOKEN=your-token
gew login --provider gitea https://gitea.example.com
unset GEW_TOKEN

# Work with a repository without Git transport.
gew clone acme/widgets
cd widgets

printf '\nREST all the things.\n' >> README.md
gew add README.md
gew diff --staged
gew commit -m "Document the REST workflow"

# Merge newer remote work, then publish queued commits.
gew pull
gew push
```

Staged content is snapshotted when you run `gew add`, so later edits do not
silently change the queued commit. Multiple local commits are pushed in order,
one remote commit at a time.

## Install or build

Use a prebuilt release archive, or build with Go 1.22 or newer:

```sh
go test ./...
go build -o gew ./cmd/gew
./gew version
./gew --version
./gew -v
```

The optional local `.git` backend uses the pure-Go `go-git` library. The system
`git` executable is not required at runtime.

## Configure a provider

Pass tokens through `GEW_TOKEN` during login to avoid putting them in the
process argument list. When `--token` is omitted, `gew login` reads
`GEW_TOKEN`; it does not prompt interactively. Saved credentials use the
operating system's user configuration directory with file mode `0600`.

| Provider | Login example | Repository form | Push |
| --- | --- | --- | --- |
| Gitea | `gew login --provider gitea https://gitea.example.com` | `owner/repo` | Enabled |
| GitHub | `gew login --provider github https://github.com` | `owner/repo` | Enabled for non-empty repos |
| GitLab | `gew login --provider gitlab --auth-kind private-token https://gitlab.com` | `group/subgroup/repo` | Safety-gated |
| Bitbucket Cloud | `gew login --provider bitbucket --auth-kind basic --username you@example.com https://bitbucket.org` | `workspace/repo` | Safety-gated |
| Azure DevOps | `gew login --provider azure --auth-kind pat https://dev.azure.com/my-org` | `project/repo` | Enabled |

Authentication notes:

- Gitea accepts token or Bearer authentication.
- GitHub uses Bearer tokens and supports GitHub Enterprise web URLs.
- GitLab supports Bearer/OAuth and `private-token` authentication, including
  self-managed instances and nested namespaces.
- Bitbucket supports Bearer tokens or Basic authentication with an Atlassian
  account email. Only Bitbucket Cloud is supported.
- Azure DevOps supports Microsoft Entra Bearer tokens and PATs. Only Azure
  DevOps Services is supported, not Azure DevOps Server.

For ephemeral use, skip `login` and set `GEW_SERVER`, `GEW_TOKEN`, and, when
needed, `GEW_PROVIDER`, `GEW_AUTH_KIND`, and `GEW_USERNAME`.

Confirm the selected profile before cloning:

```sh
gew doctor
```

## Everyday workflow

```sh
gew status
gew diff
gew add src/config.go README.md
gew diff --staged
gew commit -m "Update widget configuration"
gew log --oneline
gew pull
gew push
```

Useful variations:

```sh
gew add -A                         # Stage all changes, including deletions
gew reset src/config.go            # Unstage one path
gew reset                          # Unstage everything
gew pull --ff-only                 # Refuse local merges
gew push --new-branch feature/api  # Create and switch to a remote branch
```

Commands work from workspace subdirectories. Paths are interpreted relative to
the current directory, similar to Git pathspecs.

### Pull conflicts

`gew pull` performs a three-way merge when local and remote files both changed.
Non-overlapping text edits merge automatically. For overlapping edits, resolve
the conflict markers and continue, or restore the exact pre-merge workspace:

```sh
gew status
# Edit files containing <<<<<<< ours / ======= / >>>>>>> theirs.
gew merge --continue -m "Resolve merge conflicts"

# Or abandon the merge:
gew merge --abort
```

Binary conflict sides are saved under `.gew/conflicts/` with `.base`, `.ours`,
and `.theirs` suffixes.

## Command reference

| Git-style task | `gew` command |
| --- | --- |
| Clone | `gew clone OWNER/REPO [DIRECTORY]` |
| Clone with a local `.git` | `gew clone --backend git OWNER/REPO [DIRECTORY]` |
| Inspect changes | `gew status`, `gew diff`, `gew diff --staged` |
| Stage | `gew add PATH...`, `gew add -A` |
| Unstage | `gew reset [PATH...]` |
| Commit | `gew commit -m MESSAGE` |
| View local history | `gew log --oneline` |
| Pull | `gew pull`, `gew pull --ff-only` |
| Resolve a merge | `gew merge --continue`, `gew merge --abort` |
| Push | `gew push`, `gew push --new-branch BRANCH` |
| Migrate to the local `.git` backend | `gew migrate --to git --dry-run`, then `gew migrate --to git` |

Generated help is available for the whole command tree and for each command:

```sh
gew help
gew help commit
gew commit --help
```

Flags may appear before or after positional arguments. Use `--` to terminate
flag parsing when a path begins with `-`, for example `gew add -- -notes.md`.
Print the release version with `gew version`, `gew --version`, or `gew -v`.

### Shell completion

Generate completion directly from the installed command:

```sh
source <(gew completion bash)
source <(gew completion zsh)
gew completion fish > ~/.config/fish/completions/gew.fish
gew completion pwsh > gew-completion.ps1
```

## Workspace backends

The default backend stores its index, immutable objects, commit queue, and
merge recovery data under `.gew/` and creates no `.git` directory:

```text
working files -> gew add -> staging index -> gew commit -> local queue -> gew push -> forge API
```

The opt-in hybrid backend creates a standards-compliant local `.git` repository
for editors and Git-aware tools:

```sh
gew clone --backend git acme/widgets widgets
cd widgets
export GEW_AUTHOR_NAME="Example User"
export GEW_AUTHOR_EMAIL="user@example.invalid"
gew add README.md
gew commit -m "Update documentation"
gew push
```

In hybrid mode, `.git` owns local history while `.gew` remains the remote
synchronization journal. Provider-created commit IDs may differ from local Git
OIDs, so deleting `.gew` removes the mappings required for safe push and pull.
All network access still uses REST APIs; `gew` does not invoke Git transport,
hooks, filters, credential helpers, SSH, or smart HTTP.

To migrate an older clean `.gew` workspace:

```sh
gew migrate --to git --dry-run \
  --author-name "Example User" --author-email user@example.invalid
gew migrate --to git \
  --author-name "Example User" --author-email user@example.invalid
```

Migration refuses an existing `.git`, validates the remote head, replays queued
commits, and keeps checksummed source data under `.gew/legacy/`. Reverse
migration and implicit adoption of an existing Git repository are unsupported.

## Safety model

- Push refuses a changed remote head; provider policy and validation errors are
  not misreported as concurrency failures.
- Every enabled push is one atomic multi-file remote commit per queued local
  commit. Partial queues checkpoint after each confirmed commit.
- Accepted-but-lost responses are reconciled before retrying, preventing
  duplicate remote commits.
- Pull refuses staged changes. Unstaged work is merged only when it can be
  recovered or explicitly resolved.
- Archive extraction rejects path traversal and symbolic links. Staging rejects
  paths outside the workspace and internal `.gew` or `.git` files.
- Tokens are never stored in a workspace.

Empty repositories are cloneable. Gitea and Azure can create the first commit.
GitHub's REST Git database API cannot create the first reference, so initialize
an empty GitHub repository elsewhere, then run `gew pull` before pushing.

## Provider status

| Provider | Clone/pull strategy | Push safety |
| --- | --- | --- |
| Gitea | Native exact-revision archive and recursive tree | Atomic multi-file endpoint; enabled |
| GitHub | Native archive and Git tree API | Non-force conditional ref update; enabled for non-empty repos |
| GitLab | Native archive and paginated tree | Disabled pending live branch-wide concurrency proof |
| Bitbucket Cloud | Exact-commit tree and shared ZIP fallback | Disabled pending live stale-parent/lost-response proof |
| Azure DevOps | Exact-commit item listing and shared ZIP fallback | Exact `oldObjectId` ref update; enabled |

## Current limitations

- No rebase, cherry-pick, tags, submodules, LFS, worktrees, or arbitrary DAG
  export.
- Merges and diffs are line-oriented, with no semantic merge or rename
  detection. Renames appear as delete plus create.
- Symlinks, submodules, executable-bit-only changes, and empty directories are
  not tracked.
- `gew log` shows commits created by the current workspace, not full remote
  history.
- Hybrid merges are exported as linear provider commits; hosted topology may
  differ from local Git topology.
- Large repositories require full snapshot downloads.

## Adding a provider

Adapters share one small contract: identity, connection probing, repository
resolution, and exact-revision head/tree/blob reads. A new provider should:

1. Add one `ForgeKind` and one entry in `internal/forge/registry`.
2. Implement the required reader methods with the shared HTTP requester.
3. Implement native snapshots only when the archive is safely pinned to an
   exact revision; otherwise use the shared Tree+Blob fallback.
4. Enable the writer only after atomic multi-file, stale-head, and
   accepted-but-lost behavior pass disposable-repository tests.
5. Run the shared conformance tests plus provider-specific HTTP fixtures.
