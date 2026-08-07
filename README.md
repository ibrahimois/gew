# git ew ("gew")

<p align="center">
  <img src="assets/gew-logo.svg" width="760" alt="git ew (gew) — Git-like, REST-only">
</p>

**git ew**, invoked as `gew`, is a Git-like, REST-only workspace client for
hosted Git forges. It is made for environments where the normal `git`
executable or Git transport cannot be used, but HTTPS access to a forge's API
is available. Gitea, GitHub, GitLab, Bitbucket Cloud, and Azure DevOps use the same
provider-neutral workspace engine. GitLab and Bitbucket push remain
safety-gated as described below.

Version 0.2 added a real local staging area and local commit queue. Staged file
content is snapshotted, so editing a file after `gew add` does not silently alter
the commit. Each local commit is pushed to the configured forge separately, in order, with its
own message.

Version 0.3 adds a three-way merge engine to `pull`. It automatically combines
non-overlapping text edits, creates standard conflict markers for overlapping
edits, preserves binary conflict sides, and supports merge continue/abort.

Version 0.4 adds provider-neutral REST adapters for Gitea, GitHub, GitLab,
Bitbucket Cloud, and Azure DevOps, plus the opt-in hybrid backend where a real
local `.git` repository works alongside Gew's REST synchronization journal.

`gew` is not a reimplementation of Git's object database. It does not support
rebase, cherry-pick, tags, submodules, or arbitrary historical checkouts.

## A quick sample

```sh
# Download a repository through the Gitea REST API — no .git directory.
gew clone acme/widgets
cd widgets

# Make a change, stage its exact contents, and create a local queued commit.
printf '\nREST all the things.\n' >> README.md
gew add README.md
gew diff --staged
gew commit -m "Document the REST workflow"

# Merge any newer changes from main, then publish your queued commit.
gew pull
gew push
```

If `pull` finds overlapping changes, resolve the marked files and finish with
`gew merge --continue -m "Resolve merge"`, or return to the exact pre-merge
workspace with `gew merge --abort`.

## Build

Go 1.22 or newer is required. The optional hybrid backend uses the reviewed
pure-Go `go-git` engine; it does not require the `git` executable.

```sh
go test ./...
go build -o gew .
./gew version
```

## Configure

Create a Gitea personal access token with repository read/write permission. Use
an environment variable during login to keep the token out of the process
argument list:

```sh
export GEW_TOKEN=your-token
./gew login --provider gitea https://gitea.example.com
unset GEW_TOKEN
./gew doctor
```

The saved token lives in the operating system's user configuration directory
with file mode `0600`. For an ephemeral environment, skip `login` and provide
`GEW_SERVER`, `GEW_TOKEN`, and optionally `GEW_PROVIDER`, `GEW_AUTH_KIND`, and
`GEW_USERNAME` when running commands. Older profiles migrate in memory to the
`gitea` provider.

For GitHub.com, use a fine-grained token with repository Contents read/write
permission:

```sh
export GEW_TOKEN=your-github-token
./gew login --provider github https://github.com
unset GEW_TOKEN
./gew clone OWNER/REPOSITORY
```

GitHub Enterprise profiles use the instance's web base URL; `gew` maps it to
the instance's `/api/v3` REST base. GitHub authentication uses Bearer tokens.

GitLab.com and self-managed GitLab profiles support Bearer/OAuth tokens and
`private-token` authentication. Nested namespaces resolve to stable numeric
project IDs:

```sh
export GEW_TOKEN=your-gitlab-token
./gew login --provider gitlab --auth-kind private-token https://gitlab.com
unset GEW_TOKEN
./gew clone group/subgroup/repository
```

Bitbucket Cloud supports Bearer access tokens and Atlassian API tokens through
Basic authentication. Basic authentication requires the Atlassian account
email as `--username`:

```sh
export GEW_TOKEN=your-atlassian-api-token
./gew login --provider bitbucket --auth-kind basic \
  --username account@example.com https://bitbucket.org
unset GEW_TOKEN
./gew clone workspace/repository
```

Azure DevOps Services supports Microsoft Entra bearer tokens and PATs. A PAT
uses Azure's Basic authentication form and needs repository read/write scope:

```sh
export GEW_TOKEN=your-azure-token
./gew login --provider azure --auth-kind pat \
  https://dev.azure.com/my-organization
unset GEW_TOKEN
./gew clone my-project/my-repository
```

The Azure adapter supports the hosted service at `dev.azure.com` and documented
legacy `{organization}.visualstudio.com` URLs. Azure DevOps Server/on-premises
is intentionally outside this adapter's compatibility scope.

## Git-like workflow

```sh
# Clone an existing or empty repository with the default .gew-only backend.
./gew clone acme/widgets
cd widgets

# Edit files, inspect the working tree, and stage selected paths.
gew status
gew diff
gew add src/config.go README.md

# Inspect exactly what is staged, then create a local commit.
gew diff --staged
gew commit -m "Update widget configuration"

# Create more local commits if desired, inspect them, then push in order.
gew log --oneline
gew push

# Safely download a newer remote snapshot.
gew pull
```

If local and remote files both changed, `pull` performs a three-way merge using
the last synchronized commit as the base. For a conflict:

```sh
# Inspect files containing <<<<<<< ours / ======= / >>>>>>> theirs.
gew status

# Edit each conflicted file, then continue with a local merge commit.
gew merge --continue -m "Resolve merge conflicts"

# Or restore the exact pre-merge workspace and local commit queue.
gew merge --abort
```

Binary conflict versions are preserved under `.gew/conflicts/` with `.base`,
`.ours`, and `.theirs` suffixes. Choose or replace the working file before
continuing.

Use `gew pull --ff-only` when automation must refuse all local merges.

To stage all created, modified, and deleted files:

```sh
gew add -A
```

To unstage one path or the entire index without changing working files:

```sh
gew reset src/config.go
gew reset
```

To create and switch the workspace to a new remote branch while pushing queued
commits:

```sh
gew push --new-branch feature/config
```

Commands work from any subdirectory because `gew` searches parent directories
for `.gew/state.json`. Path arguments are interpreted relative to the current
directory, like Git pathspecs.

## Command mapping

| Git | gew |
| --- | --- |
| `git clone URL` | `gew clone OWNER/REPO` |
| `git status` | `gew status` |
| `git diff` | `gew diff` |
| `git diff --staged` | `gew diff --staged` |
| `git add PATH` | `gew add PATH` |
| `git add -A` | `gew add -A` |
| `git reset PATH` | `gew reset PATH` |
| `git commit -m MSG` | `gew commit -m MSG` |
| `git log --oneline` | `gew log --oneline` |
| `git push` | `gew push` |
| `git pull` | `gew pull` |
| `git pull --ff-only` | `gew pull --ff-only` |
| `git merge --continue` | `gew merge --continue` |
| `git merge --abort` | `gew merge --abort` |
| Create a local `.git` workspace | `gew clone --backend git OWNER/REPO` |
| Migrate a clean v3 workspace | `gew migrate --to git --dry-run` then `gew migrate --to git` |

## Local model

`gew` keeps private metadata under `.gew/`:

```text
.gew/
  state.json       provider/repository identity, branch, remote base, queue, local history
  index.json       currently staged paths
  objects/         immutable staged and baseline file snapshots
  commits/         local commit records and remote push results
  merge.json       recoverable in-progress merge state
  conflicts/       binary base/ours/theirs files during conflicts
```

The local flow is:

```text
working files -> gew add -> staging index -> gew commit -> local queue -> gew push -> forge REST API
```

The `.gew` directory and any `.git` directory are excluded from status, staging,
and pushes. Tokens are never written into a workspace.

## Local workspace backends

The default `gew` backend keeps its index, immutable objects, local commit
queue, and merge recovery under `.gew/`; it creates no `.git` directory. The
opt-in hybrid backend creates a standards-compliant local `.git` repository:

```sh
gew clone --backend git acme/widgets widgets
cd widgets
export GEW_AUTHOR_NAME="Example User"
export GEW_AUTHOR_EMAIL="user@example.invalid"
gew add README.md
gew commit -m "Update documentation"
gew push
```

In hybrid mode, `.git` owns the local index, objects, commits, branch, and
worktree history. `.gew` remains mandatory: it owns the provider identity,
actual hosted head, crash-recovery journal, and one receipt mapping each local
Git OID to its provider-created commit ID. Those IDs often differ because the
forge creates its own commit metadata. Deleting `.gew` leaves readable local
Git history but destroys the synchronization mappings needed for safe push and
pull.

All network access still uses the provider REST adapter. Production code does
not invoke `git`, Git hooks, filters, credential helpers, submodule commands,
SSH, or smart HTTP. Git-aware editors may inspect and edit the local repository,
but `gew push` accepts only history descending from its private tracking ref in
first-parent order and fails closed on unsupported graphs. Gew records a local
merge's final tree as a linear provider commit; it does not claim the hosted
repository has the same merge topology.

To migrate an existing version-1, version-2, or version-3 `.gew` workspace,
empty the staging index, make the worktree clean, and ensure the remote still
matches the recorded base:

```sh
gew migrate --to git --dry-run \
  --author-name "Example User" --author-email user@example.invalid
gew migrate --to git \
  --author-name "Example User" --author-email user@example.invalid
```

The dry-run performs all reads, object/hash checks, queue reconstruction, and
remote-head validation without writing. Migration refuses any existing `.git`,
replays every queued Gew commit separately, and retains checksummed source data
under `.gew/legacy/v<source-version>-<migration-id>/`. Reverse migration and
implicit adoption of an existing Git repository are not supported.

## Provider adapter contract

Remote adapters resolve a canonical repository identity and implement branch
head, exact-revision snapshot, tree, blob, commit-detail, and atomic-commit
operations. Each adapter advertises capabilities such as branch creation and
conditional reference updates. The shared push engine supplies an expected
head and verifies returned commit parents whenever a provider cannot guarantee
an atomic compare-and-swap. Staging, diff, local commits, and three-way merge
do not contain provider-specific behavior.

Workspace state version 3 stores the provider and provider-neutral repository
identity, but never authentication credentials. State version 4 adds the local
backend and hybrid synchronization pointers. Existing version-1/2/3 workspaces
default to the original backend without rewriting their queued commits,
history, or object snapshots.

## Safety behavior

- `pull` refuses staged changes. Unstaged changes are merged when the remote has
  advanced. Additional unstaged changes on top of queued commits must be
  committed or restored first.
- Non-overlapping text edits merge automatically. Conflicting edits receive
  diff3-style base/ours/theirs markers.
- `merge --abort` restores the exact pre-merge files, state, queue, and an empty
  staging index.
- When remote changes overlap queued local commits, successful pulls replace the
  queue with one synthetic merge commit. Superseded commits remain visible in
  `gew log`.
- `push` refuses when the remote branch has advanced since clone or pull.
- Existing remote files are updated or deleted using their current blob SHA.
- A partially successful multi-commit push is checkpointed after each remote
  commit. Retrying continues with the remaining queue.
- Archive extraction rejects path traversal and symbolic links.
- Staging rejects paths outside the workspace and internal `.gew`/`.git` paths.
- Empty repositories are cloneable. Gitea can create the initial branch and
  commit, as can Azure DevOps through its documented all-zero ref update;
  GitHub initial push is intentionally refused as described below.

## Gitea compatibility

Push uses the atomic multi-file endpoint:

```text
POST /api/v1/repos/{owner}/{repo}/contents
```

Your instance's `swagger.v1.json` should contain the operation
`repoChangeFiles`. Clone and pull also require the repository archive, branch,
and recursive tree endpoints.

## GitHub compatibility

GitHub pushes use the Git database API: blobs are created first, then one tree
and one commit, followed by a non-force reference update. This preserves one
queued local commit as one GitHub commit and rejects a concurrently advanced
branch. Recursive tree truncation falls back to walking each subtree, and
archive redirects cannot forward credentials to another host.

GitHub REST cannot create a reference in an empty repository. Empty GitHub
repositories can be cloned as prepared workspaces, but an initial `gew push`
is refused before any Git object is created and leaves every local commit
queued. Initialize the repository through GitHub or another Git client, then
run `gew pull` before pushing with `gew`.

## GitLab compatibility

GitLab clone and pull use exact-commit ZIP archives, paginated recursive trees,
and stable numeric project IDs, including projects in nested namespaces. The
adapter contains a one-request batched commit implementation with real
per-file `last_commit_id` locks for updates and deletes.

GitLab does not document a branch-wide compare-and-swap field for its Commits
API. Push therefore remains disabled until an opt-in live concurrency suite
proves that a stale same-file write cannot overwrite remote bytes. `gew push`
returns a provider-capability error without dequeuing local commits.

## Bitbucket Cloud compatibility

Bitbucket Cloud clone and pull walk paginated Source API directories pinned to
one immutable commit, fetch exact file bytes, and synthesize the safe ZIP
snapshot consumed by the shared workspace engine. Symlinks, subrepositories,
untrusted pagination URLs, and inconsistent commit metadata are rejected.

The adapter fixture-tests one streamed multipart request containing binary
create/update parts, repeated delete fields, a message, target branch, and the
expected `parents` value. Bitbucket Cloud push remains disabled until a live
stale-parent and lost-response suite proves it cannot overwrite concurrent
work or duplicate commits. This adapter does not support Bitbucket Data Center.

## Azure DevOps compatibility

Azure clone and pull resolve stable project and repository IDs, then list and
fetch content at one exact commit. Push sends every queued commit as one Pushes
API request containing all file changes and the fully qualified target ref.
Every existing-branch update carries the exact workspace head in
`refUpdates[].oldObjectId`; Azure rejects stale updates atomically rather than
silently rebasing them. Binary content is Base64 encoded and deletes omit
`newContent`.

Microsoft Entra bearer tokens are preferred. PATs are supported with
`--auth-kind pat`. Branch policies and missing repository permissions are
reported as provider errors without dequeuing the local commit. This adapter is
for Azure DevOps Services only, not Azure DevOps Server.

## Current limitations

- The merge engine is line-oriented. It does not perform semantic language-aware
  merges or rename detection.
- Synthetic merge commits are linear Gitea commits because the REST contents API
  cannot create a native two-parent Git merge commit.
- `log` shows commits created locally by this `gew` workspace. It is not a full
  remote-history browser.
- Symbolic links, submodules, executable-bit-only changes, and empty directories
  are not tracked.
- Renames are represented as delete plus create.
- Large repositories are downloaded as complete ZIP snapshots.
- Hybrid mode is opt-in. Live export/pull conformance has passed on Gitea and
  GitHub; providers whose push capability is safety-gated remain read-only, and
  Azure live conformance requires operator-supplied Azure credentials.
- Hybrid pull linearizes queued local work onto a new remote anchor. Arbitrary
  DAG export, octopus merges, hooks, filters, LFS, worktrees, and Git transport
  are unsupported.
- GitHub empty repositories require their first branch to be initialized
  outside `gew`.
- The diff engine is line-oriented and intentionally simpler than Git's diff.
