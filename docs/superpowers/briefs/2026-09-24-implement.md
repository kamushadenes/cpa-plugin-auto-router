# Brief — implement cpa-plugin-auto-router (Tasks 0–10)

## Objective and visible result
Implement the plan in `docs/superpowers/plans/2026-09-24-auto-router.md`, task by task, in order, in THIS repository (`git@github.com:chloeassistant/cpa-plugin-auto-router.git`, branch `main`, base = current HEAD). Visible result: `bin/auto-router.so` built, `go test ./...` and the updater's pytest green, the plugin loaded in the TEST proxy on port 8318 answering `auto-router` requests with decision log lines, the weekly timer installed against the TEST instance, and every task committed on a branch `feat/auto-router` pushed to origin with a PR opened against `main`.

## Read first, in this order
1. `docs/superpowers/specs/2026-09-24-auto-router-design.md` (the design; normative)
2. `docs/superpowers/plans/2026-09-24-auto-router.md` (the plan; follow it literally, tick checkboxes as you go, commit per task)
3. `/home/hermes/projects/CLIProxyAPI` at tag `v7.2.159` — `sdk/pluginapi/types.go`, `sdk/pluginabi/types.go`, `examples/plugin/claude-web-search-router/go/` (copy its cgo shell and stream forwarder), `internal/thinking/validate.go`, `sdk/cliproxy/auth/selector.go:1587`.

## Hard constraints (violations = stop and report)
- **NEVER touch the production proxy**: do not restart `cliproxyapi.service`, do not edit `/home/hermes/cliproxyapi/config.yaml`, do not write into `/home/hermes/cliproxyapi/plugins/`, do not call `make install`. The orchestrator that dispatched you is served by that proxy (port 8317); killing it kills you. Use ONLY the TEST instance: `cliproxyapi-test.service`, port 8318, `/home/hermes/cliproxyapi-test/` (config.yaml, plugins/, api-key, env). `systemctl --user` needs `XDG_RUNTIME_DIR=/run/user/1000`.
- Task 10 is a runbook only; do not execute it.
- Never print, echo, or commit secrets (API keys, the `env` files, `api-key`). Copy secrets file-to-file (`grep ... > file && chmod 600`), never through stdout. Never commit `/home/hermes/cliproxyapi-test/*`.
- No personal model policy in code or tables; decisions are 100% benchmark + capability.
- Go 1.27.1 is on the host (`/usr/local/go/bin/go`). Python via `uv run --with pyyaml --with pyarrow --with pytest --python 3.12`. No global installs.
- Follow TDD as written in the plan: failing test → implement → pass → commit. One commit per task, message as given in the plan, signed (repo has `commit.gpgsign=true`).
- Ponytail: shortest working diff, no speculative abstractions, `// ponytail:` comments only where the plan puts them. Do not add features not in the plan.
- Do not modify `/home/hermes/projects/CLIProxyAPI` (read-only reference checkout).

## If the plan is wrong
When a step cannot work as written (API mismatch, test that cannot pass, missing file), do NOT improvise a different design: fix the narrow step, note the deviation in the commit body as `Plan deviation: …`, and continue. If the deviation changes an interface another task depends on, stop and ask (native `ask`), naming the task and the exact conflict.

## Acceptance
- Task 0 checks recorded with real journal/curl output (test instance).
- `go test ./...` green; `cd updater && uv run … pytest -q` green; `make build` produces `bin/auto-router.so`.
- Test instance journal shows `pluginhost` loading `auto-router` and `GET http://127.0.0.1:8318/v1/models` lists `auto-router`.
- Smoke (Task 9 step 3): trivial chat → log line with `difficulty=trivial tier=flash thinking=low`; hard Responses stream → SSE `response.created` … and log line with `tier=top`; second trivial turn with the same `prompt_cache_key` → `reason=keep`. Paste the three log lines verbatim into README "Verified".
- `cpa-auto-router-update.timer` enabled and one manual `start` of the service exits 0 with the summary line.
- Branch `feat/auto-router` pushed; PR opened with `gh pr create --base main --fill`; PR URL is the deliverable. Final message: PR URL + list of `Plan deviation:` commits (or "none") + anything from Task 0 that failed.

## Non-goals
Production rollout (Task 10 runbook only). Claude Code / Codex CLI clients. Local classifier. Management Center UI. Persisting session state.
