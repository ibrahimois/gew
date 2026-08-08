# git ew (`gew`) v0.7.0

`gew` v0.7.0 makes REST-only synchronization scale with the work that actually
changed while preserving exact-revision reads, atomic expected-head writes,
and accepted-but-lost mutation reconciliation.

## Highlights

- Unchanged pulls now return after one HEAD request without scanning local
  repository content.
- Clean default-backend pulls compare a persisted state-v5 manifest with one
  exact remote Tree, download only changed blobs, and apply them through a
  rollback-first filesystem journal.
- Exact-revision archives and fallback blobs spool through owned mode-`0600`
  artifacts instead of repository-sized byte slices. Azure now uses its native
  exact-commit Items ZIP endpoint; Tree+Blob fallback reads are concurrency
  capped per provider.
- Gitea, GitHub, and Azure pushes checkpoint from explicit REST mutation,
  tree, and changed-byte evidence. Healthy pushes no longer download or verify
  a complete repository after every queued commit.
- Hybrid Git exports derive deltas from go-git object trees and read content
  only for created or modified paths.
- `clone`, `pull`, and `push` add `--progress=auto|always|never` and `--timings`
  on stderr, including sanitized phase, request, retry, file, and byte counts.

## Safety and compatibility

- Legacy workspace state versions 1–4 still load; the next explicit write
  upgrades them to state v5 without network access.
- Interrupted delta applies restore the old base and byte-identical tracked
  files before new network work.
- Mutation requests remain single-shot. Ambiguous responses are inspected and
  proven before checkpointing or replay.
- GitLab and Bitbucket push remain safety-gated. All synchronization continues
  to use documented HTTPS REST endpoints—never Git transport or provider
  subprocesses.

## Verification

- full, race, focused, shuffled stress, format, vet, and diff checks
- exact unchanged/delta pull and healthy push request-count fixtures
- artifact ownership/cleanup, state-v5 planner, pull-journal recovery, and
  provider proof validation tests
- four cross-platform archive membership and SHA-256 checks

Each archive contains exactly `gew` and `README.md`. Go 1.22 or newer is
required when building from source; the system `git` executable is not required
at runtime.
