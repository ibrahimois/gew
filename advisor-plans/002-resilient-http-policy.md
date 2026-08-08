# Plan 002: Add retry-safe HTTP policy and configurable timeouts

> **Executor instructions**: Follow this plan step by step. Run every
> verification command before moving on. If a STOP condition occurs, stop and
> report; do not improvise. When done, mark Plan 002 `DONE` in
> `advisor-plans/README.md`.
>
> **Drift check (run first)**:
>
> ```sh
> git diff --stat e8a7b47..HEAD -- \
>   internal/forge/http.go internal/forge/forge.go internal/forge/registry \
>   internal/forge/gitea internal/forge/github internal/forge/gitlab \
>   internal/forge/bitbucket internal/forge/azure \
>   internal/cli/cli.go internal/cli/command.go internal/cli/command_test.go \
>   internal/cli/main_test.go README.md
> shasum -a 256 internal/forge/http.go internal/forge/forge.go \
>   internal/cli/cli.go internal/cli/command.go README.md
> ```
>
> This plan was written against the published v0.5.0 working tree, which is
> still structurally dirty relative to local commit `e8a7b47`. Expected
> SHA-256 prefixes are `1fc9948` (`http.go`), `6b03146` (`forge.go`),
> `5f9425a` (`cli.go`), `fad4e79` (`command.go`), and `08f27ad`
> (`README.md`). STOP on a semantic mismatch.

## Status

- **Status**: DONE
- **Priority**: P1
- **Effort**: M (one to two days)
- **Risk**: MED — retry mistakes can duplicate work or hide auth failures
- **Depends on**: Plan 001
- **Category**: bug / reliability / DX
- **Planned at**: commit `e8a7b47`, 2026-08-08, plus the fingerprinted v0.5.0 working tree

## Why this matters

The v0.5.0 publication repeatedly hit connection resets, broken reads, HTTP
500 responses, and the requester's fixed 90-second deadline. Every read is
currently attempted once, so a transient failure aborts clone, pull, or push
even when replay is safe. Gew needs one audited retry policy for idempotent
reads and a configurable per-attempt timeout while remaining fail-closed for
mutations and credentials.

## Current state

- `internal/forge/http.go:37-58` constructs
  `http.Client{Timeout: 90 * time.Second}` with no retry policy.
- `internal/forge/http.go:86-143` sends each JSON request/download once.
- `internal/forge/forge.go:46-54` has no request-timeout setting.
- `internal/cli/cli.go:691-730` implements profile/environment precedence.
- `internal/cli/command.go:136-157` is the urfave/cli login schema to extend.
- `HTTPRequester.SanitizeError` owns token redaction, and redirect handling
  strips credentials on cross-origin hops. Preserve both invariants.

## Target contract

1. GET and HEAD retry only connection-level transient errors and HTTP 408,
   425, 429, 500, 502, 503, and 504.
2. POST, PUT, PATCH, and DELETE are attempted exactly once. Callers reconcile
   ambiguity; transport code never replays them.
3. Cancellation/deadline, TLS failures, malformed URLs, malformed successful
   JSON, and HTTP 400, 401, 403, and 404 are never retried.
4. Idempotent requests get at most four attempts with bounded exponential
   waits (250 ms, 500 ms, 1 s before jitter). Honor valid `Retry-After`, capped
   at 30 seconds. All waits must be context-aware and injectable in tests.
5. Default per-attempt timeout remains 90 seconds. Saved profiles accept a
   `request_timeout` Go-duration string; `gew login --request-timeout` sets it,
   and `GEW_HTTP_TIMEOUT` overrides saved or ephemeral profiles. Accept 1s
   through 30m and reject invalid values before network access.
6. Timeout metadata never enters `.gew`; legacy profiles continue to work.
7. Every response body is closed and bounded on every attempt. Final errors
   may name attempt counts but never tokens, auth headers, or query strings.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Baseline | `go test ./...` | all packages pass |
| Focused | `go test -race -run 'HTTP|Retry|Timeout|Profile|Login|Cancellation' ./...` | pass, no races |
| Full race | `go test -race ./...` | pass |
| Vet | `go vet ./...` | no diagnostics |
| Format | `test -z "$(gofmt -l cmd internal)"` | exit 0 |

## Scope

**In scope**:

- `internal/forge/http.go`
- `internal/forge/http_test.go` (create)
- `internal/forge/forge.go`
- `internal/forge/registry/registry.go` and tests
- Adapter constructors under `internal/forge/{gitea,github,gitlab,bitbucket,azure}` only as required for requester construction
- `internal/cli/cli.go`, `internal/cli/command.go`
- `internal/cli/command_test.go`, `internal/cli/main_test.go`
- `README.md`
- `advisor-plans/README.md` (status only)

**Out of scope**:

- Mutation reconciliation and queue state (Plan 003)
- Archive fallback (Plan 004)
- Release/tag APIs (Plan 005)
- Automatic mutation retries, proxy management, adaptive concurrency, or new dependencies
- Workspace state-version changes

## Git workflow

- Branch: `advisor/002-resilient-http-policy`
- Suggested commits: `test: characterize transient forge failures`,
  `fix: retry transient forge reads`, `feat: configure request timeouts`.
- Do not push or publish without operator instruction.

## Steps

### Step 1: Characterize the policy

Create `internal/forge/http_test.go` with `httptest.Server` cases for every
status/error class above. Inject the sleeper and jitter source through
unexported requester options; tests must use no real delays. Assert exact
attempt counts, `Retry-After`, cancellation during backoff, body closure,
bounded reads, redirect redaction, and token-free errors.

**Verify**: new tests compile and fail only for the missing policy; existing
tests remain green.

### Step 2: Centralize idempotent retries

Route `DoJSON` and `Download` through one internal execution loop. Build a
fresh request for every read attempt. Retry eligible status responses before
decoding; never retry malformed 2xx JSON. Preserve the final `RemoteError`
status and cancellation identity. Determine replay safety from the method, not
from whether a request body happens to be nil.

**Verify**:

```sh
go test -race -run 'HTTP|Retry|Cancellation|Redirect' ./internal/forge
```

Expected: exact attempt counts pass and every mutation fixture records one
request.

### Step 3: Add validated timeout configuration

Add the profile field and one parser used by saved profiles and
`GEW_HTTP_TIMEOUT`. Make requester construction reject invalid values and
update all adapter constructors explicitly. Add the declarative
`--request-timeout` login flag. Precedence is environment override, saved
profile, then 90 seconds.

**Verify**:

```sh
go test -race -run 'Timeout|Profile|Login|CommandHelp' ./internal/cli ./internal/forge/...
```

Expected: `5m` reaches the requester; invalid, sub-second, and over-30m values
fail before the server receives a request; config mode remains 0600; no timeout
appears in workspace JSON.

### Step 4: Document and run final gates

Document the flag/environment variable, per-attempt semantics, read retries,
and the guarantee that writes are never automatically replayed.

Run all commands above plus:

```sh
go test -shuffle=on -count=20 ./...
git diff --check
```

## Test plan

- Table-test every retryable and non-retryable status.
- Simulate reset before headers and a truncated response body.
- Prove POST executes once on 500 and network failure.
- Prove cancellation interrupts both active requests and backoff.
- Prove token redaction on every attempt.
- Test saved, ephemeral, overridden, invalid, and legacy profiles.

## Done criteria

- [ ] Transient idempotent reads recover within four attempts.
- [ ] Mutations are never automatically retried.
- [ ] Default timeout is 90s; valid 1s–30m overrides work.
- [ ] Invalid timeout fails before network access.
- [ ] Context and credential-safety tests pass under `-race`.
- [ ] Full, shuffled, vet, format, and diff checks pass.
- [ ] No workspace schema change or new dependency.
- [ ] Plan 002 is `DONE`.

## STOP conditions

- A retry requires buffering an unbounded response or replaying a mutation.
- Cancellation identity cannot be preserved through `errors.Is`.
- Timeout policy would have to be stored in `.gew`.
- One adapter needs a non-idempotent exception; defer it to its provider plan.

## Maintenance notes

All future adapter reads must use this policy. Reviewers should look for
accidental POST replay, body leaks between attempts, real sleeps in tests, and
retry loops layered again in Plans 003–005.
