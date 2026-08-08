# git ew (`gew`) v0.6.1

`gew` v0.6.1 is the corrected reliability and hosted-release build. It contains
all v0.6.0 features plus two fixes found while dogfooding v0.6.0 against the
live Gitea server.

## Fixes since v0.6.0

- HTTP/1.1-only Gitea transports now constrain TLS ALPN to `http/1.1`. The
  original implementation disabled Go's HTTP/2 handler but could still
  negotiate `h2`, causing HTTP/2 frames to be reported as malformed HTTP/1.
- Gitea release resume now downloads assets from the server-provided
  `browser_download_url`, after enforcing the configured same-origin and
  repository release-path boundary. The guessed numeric asset API path was not
  a download endpoint on the live Gitea version.

## v0.6 feature set

- Bounded retry/backoff for idempotent reads and configurable request timeouts.
- Same-invocation reconciliation of accepted commits after lost responses.
- Spool-backed Gitea atomic commits, actionable size errors, and safe
  `gew uncommit` recovery.
- Deterministic Tree+Blob fallback when native archive downloads fail.
- Exact-commit GitHub and Gitea hosted releases with resumable, SHA-256-verified
  assets through `gew release create`.

## Verification

- format, vet, full tests, race tests, focused shuffled stress tests, and diff checks
- four archive membership and SHA-256 checks
- live source pushes to GitHub and Gitea using Gew
- live create and byte-verifying `--resume` on both hosted releases using Gew

Each archive contains exactly the `gew` executable and README. Go 1.22 or newer
is required when building from source; the system `git` executable is not
required at runtime.
