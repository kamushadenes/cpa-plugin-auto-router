# cpa-plugin-auto-router

A native CLIProxyAPI plugin that exposes `auto-router`. It sends a bounded snippet of the last user message (up to `snippet_chars`, default 1500) and local request signals to Jev, then chooses a concrete model and thinking level from the local benchmark table. The host still performs the upstream request, including credentials, retries, cooldowns, usage accounting, and stream handling.

Jev answers nine category factors and one five-level effort question in a single call. Go composes the labels using the calibrated rules in `internal/decide/compose.go`; the tests cover 39 category and 22 difficulty fixtures. After the mean-based cuts, mass of at least `0.35` at the next level can raise difficulty once: `p1` raises trivial to routine, `p3` raises routine to hard, and `p4` raises hard to extreme. Hard confidence includes `p2+p3`; extreme includes `p3` only when raised. Decision messages contain only JSON, including `factors`, `effort_p`, `effort_mean`, and both confidences. Logged labels describe the composition before the confidence gate; `tier` and `thinking` describe the actual routing decision. A low-confidence difficulty keeps the previous session difficulty or uses `routine` for a new session.

## TEST verification

Task9 was run against the TEST proxy on port 8318. Production (`cliproxyapi.service`, port 8317) was not changed. Production rollout is an operator-run Task10 step.
Production rollout is operator-run; see [`docs/runbook-production.md`](docs/runbook-production.md).

## Install

From this checkout:

```bash
make install-test
mkdir -p /home/hermes/cliproxyapi-test/plugins/auto-router
install -m 0644 table/models.yaml /home/hermes/cliproxyapi-test/plugins/auto-router/models.yaml
```

Add the plugin entry below to the TEST proxy's `plugins.configs` and restart only `cliproxyapi-test`:

```yaml
auto-router:
  enabled: true
  priority: 10
  jev_api_key_env: OPENROUTER_API_KEY
  table_path: /home/hermes/cliproxyapi-test/plugins/auto-router/models.yaml
```

The TEST service reads `OPENROUTER_API_KEY` from `/home/hermes/cliproxyapi-test/env`, a mode `0600` file. Copy the value file-to-file from the operator's environment; do not put the key in YAML, source control, or logs.

## Configuration

The plugin accepts these fields under `plugins.configs.auto-router`:

- `enabled`: enable routing for requests whose model is `auto-router`.
- `priority`: host plugin ordering.
- `jev_api_key_env`: environment variable containing the Jev key. Default: `OPENROUTER_API_KEY`.
- `jev_base_url`: Jev service base URL. Default: `https://openrouter.ai`.
- `jev_endpoint_path`: Jev endpoint. Default: `/api/alpha/decisions`.
- `jev_model`: classifier model. Default: `typesafe/jev-1.13`.
- `confidence_threshold`: label confidence threshold. Default: `0.6`.
- `table_path`: benchmark YAML path. The default points at the production plugin directory; set it explicitly for TEST.
- `snippet_chars`: maximum user-message snippet sent to Jev. Default: `1500`.
- `jev_timeout_ms`: Jev request timeout. Default: `2000`.

## Model failover

The executor tries at most three ranked models when the host returns an allow-listed retryable error. Each retry excludes the models that already failed. Streaming can retry only before the first emitted chunk; the executor holds at most one chunk while selecting the effective response headers.

A successful retry stores the effective session model. The decision log and `X-Auto-Router` identify that model with `reason: failover` and the ordered `failed_from` list. This host version exposes callback errors as text, so the plugin matches explicit rate-limit, overload, cooldown, unavailable-auth, API-error, selected status, and premature-stream-close markers. Invalid requests and authentication errors return immediately. No host patch is required; numeric status propagation is tracked in issue #3.

## Benchmark table and tiers

`table/tiers.yaml` is operator-owned. It declares the `flash`, `mid`, and `top` sets; the updater never changes it. The TEST catalog had 50 models, of which all 20 tier entries were present in the generated table:

- flash (4): `deepseek-v4-flash`, `glm-5.3-flash`, `mimo-v2.6-flash`, `qwen-3.8-flash-next`
- mid (9): `claude-sonnet-5`, `deepseek-v4-pro`, `glm-5.3`, `gpt-5.6-luna`, `gpt-5.6-terra`, `gpt-6-luna`, `kimi-k3`, `minimax-m3`, `qwen-3.8-max`
- top (7): `claude-fable-5`, `claude-fable-5-1`, `claude-opus-5`, `claude-opus-5-5`, `gpt-5.6-sol`, `gpt-6-astra`, `gpt-6-sol`

The generated seed has 252 score rows across 17 benchmarks. Six declared benchmarks had no usable rows because the sources did not publish uncertainty: `aa-coding-agent-index`, `aa-coding-index`, `aa-intelligence-index`, `cursorbench`, `frontiercode`, and `gdpval-aa`. Those gaps are reported, not filled with guesses. A single EEE snapshot returned HTTP 499; the run kept the other EEE rows.

The live run exited 0:

```text
INFO updated=252 kept=0 dropped=29 unmapped=4065 untiered=['abliterated-model', 'abliterated-model-large', 'abliterated-model-large-v2', 'claude-haiku-3.5', 'claude-haiku-4.5', 'claude-opus-4', 'claude-opus-4.1', 'claude-opus-4.5', 'claude-opus-4.6', 'claude-opus-4.7', 'claude-opus-4.8', 'claude-sonnet-3.7', 'claude-sonnet-4', 'claude-sonnet-4.5', 'claude-sonnet-4.6', 'codex-auto-review', 'deepseek-v4-flash-vision-exp', 'glm-5.2', 'gpt-5.5', 'gpt-image-1.5', 'gpt-image-2', 'gpt-image-2.5', 'gpt-image-2.5-flare', 'gpt-image-2.5-sunburst', 'kimi-k2.7-code', 'mimo-v2.5-pro', 'mimo-v2.6-pro', 'mimo-v2.6-pro-ultraspeed', 'qwen-3.7-flash', 'qwen-3.8-27b'] coverage=arena=145 eee=42 epoch=57 fallback=20 openrouter=17
```

The updater keeps old rows when a source fails and skips rows without a published margin. See [`docs/superpowers/plans/2026-09-24-checks.md`](docs/superpowers/plans/2026-09-24-checks.md) for the earlier ABI, session, and header checks.

## Updater and timer

The updater reads the live TEST catalog, benchmark sources, and models.dev capability data, then atomically writes the table:

```bash
CLIPROXY_API_KEY_FILE=/home/hermes/cliproxyapi-test/api-key
/home/hermes/.local/bin/uv run --with pyyaml --with pyarrow --python 3.12 python -m updater \
  --catalog http://127.0.0.1:8318 \
  --catalog-key-file "$CLIPROXY_API_KEY_FILE" \
  --tiers table/tiers.yaml \
  --out /home/hermes/cliproxyapi-test/plugins/auto-router/models.yaml \
  --openrouter-key-env OPENROUTER_API_KEY
```

Install the TEST-only user units and enable the weekly timer:

```bash
install -m 0644 systemd/cpa-auto-router-update.service systemd/cpa-auto-router-update.timer ~/.config/systemd/user/
XDG_RUNTIME_DIR=/run/user/1000 systemctl --user daemon-reload
XDG_RUNTIME_DIR=/run/user/1000 systemctl --user enable --now cpa-auto-router-update.timer
```

The service uses this checkout as `WorkingDirectory`, reads the TEST proxy key from `/home/hermes/cliproxyapi-test/api-key`, and writes only the TEST plugin table. Production is deliberately not a service target.

## Historical verification

The coordinator ran the September 24 TEST checks recorded in `ce81dd0`. The logs below are historical evidence, not verification of the current commit. The retained report states that production was unchanged. The plugin loaded on port 8318 and `/v1/models` advertised `auto-router`:

```text
Sep 24 22:37:13 hermes-chloe cli-proxy-api[3860320]: [2026-09-24 22:37:13] [--------] [info ] [host.go:352] pluginhost: plugin loaded plugin_id=auto-router path=plugins/auto-router.so
Sep 24 22:37:13 hermes-chloe cli-proxy-api[3860320]: [2026-09-24 22:37:13] [--------] [info ] [host.go:375] pluginhost: plugin registered plugin_id=auto-router plugin_name=auto-router version=0.1.0 path=plugins/auto-router.so
```

`GET /v1/models` returned HTTP 200 with 51 models and `auto-router` present. `make install-test` rebuilt and copied the corrected `.so`; `go test ./internal/table -run 'TestLoad(Generated|Good)$' -count=1` passed after loading the generated fixture.

These are the three decision lines retained from the TEST smoke. The first request was trivial chat. The second was a hard debugging Responses request. The third reused its `prompt_cache_key` with a trivial request, so the router kept the same top-tier model. The complete Responses stream was captured before checking its events: HTTP 200, 387088 bytes, and `response.created`, `response.output_text.delta`, and `response.completed` events were present.

```text
Sep 24 22:43:39 hermes-chloe cli-proxy-api[3875169]: [2026-09-24 22:43:39] [4f5f4a16] [info ] [host_callbacks.go:370] auto-router decision {"category":"math-data","category_p":{"agentic-terminal":0,"backend":0,"debugging":0,"extraction":0,"math-data":1,"review":0,"spec-design":0,"webdev":0,"writing":0},"confidence":0.99,"difficulty":"trivial","difficulty_p":{"extreme":0,"hard":0,"routine":0.01,"trivial":0.99},"jev_ms":497,"model":"deepseek-v4-flash","reason":"new","session":"h:ddad965b","thinking":"low","tier":"flash"} model=deepseek-v4-flash reason="new"
Sep 24 22:46:05 hermes-chloe cli-proxy-api[3875169]: [2026-09-24 22:46:05] [78dab5b5] [info ] [host_callbacks.go:370] auto-router decision {"category":"debugging","category_p":{"agentic-terminal":0,"backend":0,"debugging":1,"extraction":0,"math-data":0,"review":0,"spec-design":0,"webdev":0,"writing":0},"confidence":0.79,"difficulty":"hard","difficulty_p":{"extreme":0.15,"hard":0.85,"routine":0,"trivial":0},"jev_ms":291,"model":"claude-opus-5","reason":"new","session":"h:1fd53178","thinking":"xhigh","tier":"top"} model=claude-opus-5 reason="new"
Sep 24 22:53:00 hermes-chloe cli-proxy-api[3875169]: [2026-09-24 22:53:00] [125c3022] [info ] [host_callbacks.go:370] auto-router decision {"category":"math-data","category_p":{"agentic-terminal":0,"backend":0,"debugging":0,"extraction":0,"math-data":1,"review":0,"spec-design":0,"webdev":0,"writing":0},"confidence":0.99,"difficulty":"trivial","difficulty_p":{"extreme":0,"hard":0,"routine":0,"trivial":1},"jev_ms":498,"model":"claude-opus-5","reason":"keep","session":"h:1fd53178","thinking":"xhigh","tier":"top"} model=claude-opus-5 reason="keep"
```

The original migration wording selected `claude-fable-5(xhigh)` and received a normal upstream 429 rate-limit response. No routing policy was changed to bypass it; a genuine hard debugging request later selected `claude-opus-5(xhigh)` and completed successfully.

The TEST-only `cpa-auto-router-update.timer` was enabled and active during those checks. The manual service run exited 0 with `ExecMainStatus=0` at `23:12:59 UTC`; it wrote a 54,213-byte TEST table at `2026-09-24 23:12:59 UTC`. Its summary kept all 252 existing rows:

```text
Sep 24 23:12:59 hermes-chloe uv[3906129]: INFO updated=0 kept=252 dropped=29 unmapped=4065 untiered=['abliterated-model', 'abliterated-model-large', 'abliterated-model-large-v2', 'auto-router', 'claude-haiku-3.5', 'claude-haiku-4.5', 'claude-opus-4', 'claude-opus-4.1', 'claude-opus-4.5', 'claude-opus-4.6', 'claude-opus-4.7', 'claude-opus-4.8', 'claude-sonnet-3.7', 'claude-sonnet-4', 'claude-sonnet-4.5', 'claude-sonnet-4.6', 'codex-auto-review', 'deepseek-v4-flash-vision-exp', 'glm-5.2', 'gpt-5.5', 'gpt-image-1.5', 'gpt-image-2', 'gpt-image-2.5', 'gpt-image-2.5-flare', 'gpt-image-2.5-sunburst', 'kimi-k2.7-code', 'mimo-v2.5-pro', 'mimo-v2.6-pro', 'mimo-v2.6-pro-ultraspeed', 'qwen-3.7-flash', 'qwen-3.8-27b'] coverage=arena=145 eee=42 epoch=57 fallback=20 openrouter=17
```

The updater's live reload was then exercised without a proxy restart:

```text
Sep 24 23:13:21 hermes-chloe cli-proxy-api[3875169]: 2026/09/24 23:13:21 auto-router table reloaded path=/home/hermes/cliproxyapi-test/plugins/auto-router/models.yaml
```

The service writes only the TEST table. Production is deliberately not a service target.

The orchestrator ran the September 25 replay at 02:39 UTC against the TEST binary from `641a0f6`. The fixing worker did not run that replay. The journal recorded `hard`, confidence `0.97`, and Fable-to-Opus failover. Those results are historical evidence for `641a0f6`, separate from the 02:32 run and the final review-fix checks.

## Plan deviations

- The updater unit uses this authorized branch checkout as `WorkingDirectory`, not the plan's example path.
- One EEE snapshot returned HTTP 499; healthy rows were retained.
- The planned migration wording hit a normal upstream 429. The successful hard stream used a real debugging prompt and did not change model ranking.
- In the original deployment, the host formatter dropped arbitrary structured fields, so the plugin put the JSON decision in the message. Since `641a0f6`, decision messages contain only JSON and omit duplicate host fields.

## Known limits

- `X-Auto-Router` is filtered unless the proxy has `passthrough-headers: true`; the JSON decision log is authoritative with the current TEST setting.
- Session state is in memory and is lost when the proxy restarts.
- `Available` host cooldown information is not wired into the router in this version.
- Models without a tier are reported and are not routed.
