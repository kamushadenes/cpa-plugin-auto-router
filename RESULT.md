# Router handoff result

Scope: bounded transient-failure failover, a tier header, a model-change notice injected into the proxy request body, and a conservative difficulty raise driven by explicitly marked tool failures. No harness plugins.

## Delivered

- `retryableHostFailure` recognizes explicit `request_timeout` and `stream stalled` markers and keeps the authentication and invalid-request exclusions. Streaming still retries only before the first emitted chunk.
- Buffered and streaming responses carry `X-Auto-Router-Tier` next to `X-Auto-Router`.
- `internal/notice` appends a notice to the outgoing body as a trailing user turn in the request's own format: a `user` message for `chat-completions`, a `message` item with one `input_text` part for `responses`.
  - A retry after a transport failure says the previous request did not complete. It never claims the previous model was incapable.
  - A first attempt carries a notice only when the router moved the session to a different model, and that wording says the session moved.
  - A raise caused by repeated tool failures reports the reason `tool-error-bump`.
  - The original body is never mutated, so routing and Jev never see a notice. A body that does not parse, lacks the turn list, or arrives in another format is forwarded unchanged.
- Difficulty rises one band after three consecutive tool results that carry an explicit error marker and answer a call issued in the same request. The raise is latched to the episode, identified by the call the trailing failure run started with, so the same run never raises twice and a fresh run after progress can raise once more. Session floors still prevent any decrease.

## Removed

- `integrations/omp` and `integrations/hermes`, their tests, and their documentation. No harness plugin ships from this repository.

## What counts as a tool failure

Only structural markers on the tool result: `is_error: true`, `status` of `failed` or `error`, or a non-empty `error` field. Free-form text is never read as failure, so an unmarked `{"error": ...}` payload, a message that merely mentions an error, and ordinary user text are all inert. A result that answers no call issued in the same request is ignored. Marked failures naming authentication, permission, quota, or billing problems are excluded because no tier resolves them.

## Verification

- `go vet ./...`: clean.
- `go test ./...`: passed, including the new `internal/notice` package.
- `make test`: passed; updater pytest reported `71 passed`.
- `make build`: passed; produced `bin/auto-router.so`.
- `internal/notice` covers the chat-completions append, the responses append, tool-pairing preservation, unsupported and unparsable bodies, and large-integer precision.
- `internal/snippet` covers marked streaks in both formats, uncorrelated results, unmarked error text, successful output, user text mentioning errors, access failures, an explicit `is_error: false`, and reset after progress.
- `plugin_test.go` covers the latch: below threshold does not raise, the threshold raises one band once, a longer run of the same episode does not raise again, a new episode raises once more, and unmarked results clear the latch without demoting.
- `host_test.go` covers notice selection: unchanged sessions and same-model routes are untouched, a capability move and a failover retry produce different wording, the first attempt carries no failover notice on either the buffered or streaming path, and the original body is unmutated.

## Client coverage caveat

The detector is proven against fixtures for the two formats this executor accepts. Whether OMP or Hermes emits these markers on the wire was not observed in the lab, so real-client coverage is unverified. A client that reports failures only as text never raises difficulty.

## Race investigation

External review reported two failures under `go test -race ./...`:
`TestPluginShutdownClosesStalledHostStream` and
`TestPluginShutdownClosesHostStreamRegisteredDuringShutdown`.

Both are pre-existing and unrelated to this work. `resetPluginLifecycleForTest`
(shutdown_test.go:18) writes the package-level `streamLifecycle` from `t.Cleanup`
while the forwarding goroutine `executeStream` started still reads it through
`endPluginStream` and `finishPluginShutdownLocked`. The test returns before that
goroutine finishes.

Before and after comparison, same command on both trees:

- `origin/main` in a detached worktree: `go test -race . -run TestPluginShutdown -count=1` exits 1 with those two test names and that race site.
- Branch head: the same command exits 1 with the same two test names and the same race site.

The two runs differ only in the checkout path printed in the trace. Nothing in
this branch changed shutdown behavior, so the failures are not a regression and
shutdown was left untouched. The defect is filed as issue #11.

Everything else is clean under the detector:

- `go test -race ./internal/...`: all six packages pass, including the new `internal/notice`.
- `go test -race .` with only those two known-failing tests skipped: passes, covering every test added here.

## Remaining gates

- `X-Auto-Router` and `X-Auto-Router-Tier` reach a client only when the proxy sets `passthrough-headers: true`; otherwise the JSON decision log stays authoritative.
- A `responses` request whose `input` is a plain string is forwarded unchanged, because converting it to an array would change the request shape.
- Lab router acceptance still needs a reachable authorized endpoint. `127.0.0.1:8318` refused connections and `127.0.0.1:8317` answered `401`, so no end-to-end router receipt was captured.
- The shutdown-test race in issue #11 is still open.
- No production service was restarted, and no credentials were copied or printed.
- The task-owned Hermes lab plugin was disabled and removed from `~/.hermes/plugins/auto-router-handoff`.

## Rollback

- Revert the tool-failure raise commit to drop `ErrorEpisode` and the streak detector.
- Revert the notice commit to restore untouched request bodies and drop `internal/notice`.
- Revert `d77e889d8f4e4f96064c76551214796fdbe50947` to remove the tier header.
- Revert `44d9cc144936743c223e4844b3b31211b05ded05` to remove the explicit retry markers.
