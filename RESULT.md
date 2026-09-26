# Router handoff result

Scope for this branch: bounded transient-failure failover, an authoritative router tier header, and safe model-transition guidance for OMP and Hermes. Context handling and loop escalation are out of scope and are not implemented here.

## Delivered commits

- `44d9cc144936743c223e4844b3b31211b05ded05`: retry explicit transient host failures.
- `d77e889d8f4e4f96064c76551214796fdbe50947`: router tier header and native handoff adapters.
- `00d0d8241a77825a258200ded5e224e0167fefe1`: first evidence report.
- `20d4035bf755d17660047cbd6ee041d284930832`: feasibility findings.
- `26c500df555712741d0418d4201479b4c3ef2c30`: partial-status note.

## Delivered behavior

- `retryableHostFailure` recognizes explicit `request_timeout` and `stream stalled` markers and keeps the authentication and invalid-request exclusions. Streaming retries stay restricted to the window before the first emitted chunk.
- Buffered and streaming executor responses carry `X-Auto-Router-Tier` alongside `X-Auto-Router`. Tests assert both response paths.
- The OMP extension tracks the per-session effective model and tier in the session branch and queues exactly one hidden `nextTurn` notice per real model change. The notice reports the previous model, the effective model, and the router's own reason.
- The Hermes plugin registers `post_api_request` and `pre_llm_call`. It stores the observed effective model per session and returns one factual model-change notice as context on the next real LLM request. It never calls `inject_message`.

## Verification

All commands ran in the isolated worktree unless stated otherwise.

- `make test`: passed. Go packages green; updater pytest reported `71 passed`.
- `make build`: passed; produced `bin/auto-router.so`.
- `go test ./...`: passed.
- `node integrations/omp/test_handoff_policy.mjs`: passed.
- `node integrations/omp/test_extension.mjs`: passed.
- `python3 integrations/hermes/test_handoff_policy.py`: passed, 2 tests.
- `python3 integrations/hermes/test_plugin.py`: passed, 1 test.
- `hermes plugins doctor integrations/hermes --ci`: passed with an isolated `HERMES_HOME`; 2 hooks registered.
- OMP native load and inference: `omp -p --no-session --no-tools --extension integrations/omp/auto-router-handoff.mjs --model gpt-5.6-sol "Reply exactly ok"` returned `ok`.
- Hermes lab plugin doctor on `hermes@10.23.23.144`: passed; 2 hooks registered.
- Hermes lab inference: `hermes -z "Reply exactly ok"` returned `ok`.

## Lab surface and model evidence

- Lab Hermes reported provider `custom:cliproxy-lab` and model `kimi-k3`; the benign lab inference returned `ok`.
- Lab `127.0.0.1:8318` did not accept connections; `127.0.0.1:8317` answered `401`. No lab router inference was executed, so no effective `X-Auto-Router` model was observed end to end.
- The local OMP smoke requested `gpt-5.6-sol` and returned `ok`. That proves extension loading and live inference, not router header delivery.
- No production service or production proxy was restarted, and no credentials were copied or printed.
- The task-owned Hermes lab plugin was disabled and removed from `~/.hermes/plugins/auto-router-handoff`.

## Remaining gates

- Coding Agent must explicitly enroll the OMP extension in its global extension allowlist. A loose file is not loaded automatically.
- End-to-end router header delivery still needs a reachable authorized lab router endpoint.
- `X-Auto-Router` and `X-Auto-Router-Tier` reach a client only when the proxy sets `passthrough-headers: true`; otherwise the JSON decision log remains authoritative.
- The router header arrives after the upstream call, so a consumer observes a model change only after that response. Both adapters report the change on the following request.

## Rollback

- Revert `d77e889d8f4e4f96064c76551214796fdbe50947` to remove the tier header and both adapters.
- Revert `44d9cc144936743c223e4844b3b31211b05ded05` to remove the explicit retry markers.
- Remove the OMP extension from the enrolled allowlist and delete the Hermes plugin directory from the target profile.
