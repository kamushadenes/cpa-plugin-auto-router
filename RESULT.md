# Router handoff result

## Delivered commits

- `44d9cc144936743c223e4844b3b31211b05ded05`: signed retry marker change.
- `d77e889d8f4e4f96064c76551214796fdbe50947`: signed router tier header and native adapter integration.

## Delivered

- `retryableHostFailure` now recognizes explicit `request_timeout` and `stream stalled` markers while preserving authentication and invalid-request exclusions.
- Buffered and streaming executor responses expose `X-Auto-Router-Tier`; tests cover both response paths.
- OMP adapter reads `X-Auto-Router` plus `X-Auto-Router-Tier`, persists per-session transition state, deduplicates transitions, and queues one hidden `nextTurn` guidance message for a confirmed tier upgrade.
- Hermes plugin registers `post_api_request` and `pre_llm_call`. It persists the observed response model per Hermes session and returns one factual model-change notice on the next real LLM request. It does not call `inject_message` and does not compact history.

## Verification

All commands ran in the isolated worktree unless stated otherwise.

- `go test ./...`: passed.
- `make test`: passed; Go tests passed and updater pytest reported `71 passed`.
- `make build`: passed; produced `bin/auto-router.so`.
- `node integrations/omp/test_handoff_policy.mjs`: passed.
- `node integrations/omp/test_extension.mjs`: passed.
- `python3 integrations/hermes/test_handoff_policy.py`: passed, 2 tests.
- `python3 integrations/hermes/test_plugin.py`: passed, 1 test.
- `HERMES_HOME=/tmp/router-handoff-hermes-home-final2 hermes plugins doctor integrations/hermes --ci`: passed; 2 hooks registered.
- OMP native CLI import and inference: `timeout 45s omp -p --no-session --no-tools --extension integrations/omp/auto-router-handoff.mjs --model gpt-5.6-sol "Reply exactly ok"`; returned `ok`.
- Hermes lab plugin doctor: passed on `hermes@10.23.23.144`; manifest loaded and 2 hooks registered.
- Hermes lab native inference: `timeout 90s hermes -z "Reply exactly ok"`; returned `ok`.
- OMP bounded `agent_end` compaction probe: `{"idle":false,"pending":false,"compact":"error","error":"Compaction cancelled"}`.
- OMP bounded `before_provider_request` compaction probe: `{"idle":false,"pending":false,"compact":"error","error":"Nothing to compact (session too small)"}`; the process aborted after the probe. A deferred `agent_end` callback produced no evidence file before process exit.
- No OMP context replacement implementation was retained: deterministic tail extraction and one-user-message rewriting were rejected as lossy and unsafe for tool history.

## Lab surface and model evidence

- Lab Hermes reported configured provider `custom:cliproxy-lab` and model `kimi-k3`; the benign lab inference returned `ok`.
- Lab `127.0.0.1:8318` was unavailable (`000`); `127.0.0.1:8317` returned `401`. No lab router inference or effective `X-Auto-Router` model was observed.
- The local OMP smoke requested `gpt-5.6-sol` and returned `ok`; this proves extension loading and inference, not router header delivery.
- No production service or production proxy was restarted.

## Remaining gates

- The Coding Agent repository must explicitly enroll the OMP extension in its global extension allowlist. A loose file is not auto-loaded.
- OMP automatic pre-generation compaction remains blocked. The public `context` replacement hook is request-only, but a safe semantic summary requires the native summarizer; the tested extension lifecycle did not provide a verified transaction boundary to run it before the promoted request.
- Hermes automatic compaction remains blocked. The public `register_context_engine` seam exists, but replacing the configured built-in compressor without preserving its full construction/session lifecycle would be unsafe; documented request hooks expose no confirmed router tier/reason pair. Hermes remains guidance-only.
- Conservative loop escalation was not enabled. The inspected protocols do not provide one uniform, reliable explicit failure signal across chat-completions, Anthropic, OpenAI tool messages, and Responses without provider-specific parsing. Repeated calls alone are insufficient; stale or arbitrary error-shaped output must not escalate.
- Lab router acceptance remains blocked until an authorized lab router endpoint is available. No credentials were copied and no production endpoint was mutated.

## Rollback

- Revert signed commit `d77e889d8f4e4f96064c76551214796fdbe50947` to remove tier headers and adapters.
- Revert signed commit `44d9cc144936743c223e4844b3b31211b05ded05` to remove the explicit retry markers.
- Remove the OMP extension from the enrolled allowlist and remove the Hermes plugin directory from the target Hermes profile.
