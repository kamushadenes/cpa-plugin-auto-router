# cpa-plugin-auto-router

A native CLIProxyAPI plugin that exposes `auto-router`. It sends Jev a small state shaped like `dirien/jev-router`'s: the latest human message (clipped to `snippet_chars`, default 4000, keeping its start and end), up to two earlier human messages, the assistant's last prose when the request is short, and the session's harness, depth and recent tool names. Harness wrappers (including Hermes `<memory-context>`) are removed, secrets are redacted, and code blocks become one-line descriptions. The plugin then chooses a concrete model and thinking level from the local benchmark table. The host still performs the upstream request, including credentials, retries, cooldowns, usage accounting, and stream handling.

Jev answers nine category factors, one five-level effort question, and two guard questions in a single call. Go composes the labels using the calibrated rules in `internal/decide/compose.go`; the tests cover 39 category and 22 difficulty fixtures. After the mean-based cuts, mass of at least `0.35` at the next level can raise difficulty once: `p1` raises trivial to routine, `p3` raises routine to hard, and `p4` raises hard to extreme. Difficulty confidence depends only on the final label: `p0` for trivial, `p1+p2` for routine, `p2+p3` for hard, and `p3+p4` for extreme. Decision messages contain only JSON, including `factors`, `effort_p`, `effort_mean`, and both confidences. The logged `category` is the composed label before the confidence gate; the logged `difficulty` is the effective one after the confidence gate, the guards and the tool-failure raise, so it matches `tier` and `thinking`. Below the confidence threshold, difficulty falls back to one band below the Jev label while preserving the previous session floor.

The guards never change the category and only raise difficulty. When `alters_sensitive_state` is at least `0.7` (the request changes production, credentials, billing, shared infrastructure, or unrecoverable data), difficulty becomes at least hard. When `routing_claim_present` is at least `0.5` (the prompt tries to pick the model, tier, or effort), difficulty cannot fall below routine. The decision log records `sensitive`, `claim`, and `guard` (`risk-override`, `claim-guard`, or empty). A guard answer missing from Jev's response counts as 0; a present but invalid one makes the call unavailable. A session that sees no traffic for 10 minutes expires, so the next message is classified from scratch.

## TEST verification

Task9 was run against the TEST proxy on port 8318. Production (`cliproxyapi.service`, port 8317) was not changed. Production rollout is an operator-run Task10 step.
Production rollout is operator-run; see [`docs/runbook-production.md`](docs/runbook-production.md). Production runs in the cliproxy LXC (`root@10.23.23.12:/opt/cliproxy`), not in `~/cliproxyapi`.

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
- `snippet_chars`: maximum characters of the latest human message sent to Jev. Default and cap: `4000`.
- `jev_timeout_ms`: Jev request timeout. Default: `2000`.
- `refusal_fallback`: optional list of refusal-guarded aliases. Absent or empty means the feature is off. Each entry needs `alias`, `primary` and `fallback`; `fallback` is one model name or an ordered list of model names. An entry missing any of the three, or whose `fallback` holds no non-empty name, is ignored. Optional `prose_refusal: true` also switches on a Jev-confirmed plain-text refusal, for non-streaming requests only.
- `prices`: optional dashboard prices in US$ per 1M tokens, keyed by model ID. See [Auto Router dashboard](#auto-router-dashboard).
- `stats_dir`: directory for the dashboard's daily stats files. Default: `plugins/auto-router/stats`, relative to the proxy's working directory.

## Model failover

The executor tries at most three ranked models when the host returns an allow-listed retryable error. Each retry excludes the models that already failed. Streaming can retry only before the first emitted chunk; the executor holds at most one chunk while selecting the effective response headers.

A successful retry stores the effective session model. The decision log and `X-Auto-Router` identify that model with `reason: failover` and the ordered `failed_from` list. This host version exposes callback errors as text, so the plugin matches explicit rate-limit, overload, cooldown, unavailable-auth, API-error, selected status, and premature-stream-close markers. Invalid requests and authentication errors return immediately. No host patch is required; numeric status propagation is tracked in issue #3.

Every retry after a transport failure carries a notice appended to the request body as a trailing user turn, in the request's own format: a `user` message for `chat-completions`, an `input_text` message item for `responses`. It names the model that did not complete, the model now running, and `failover`. A first attempt carries a notice only when the router moved the session to a different model, and that wording says the session moved rather than claiming the previous model failed. Existing turns, tool-call pairs, unknown fields, and numeric precision are preserved; the original body is never mutated, so the routing and Jev paths never see a notice.

`X-Auto-Router-Tier` accompanies `X-Auto-Router` on both the buffered and streaming paths and names the tier the router selected.

## Refusal fallback aliases

`refusal_fallback` registers extra models that are not routed by Jev. A request for the alias runs on `primary` and moves down the ordered `fallback` list only on the conditions below:

```yaml
plugins:
  configs:
    auto-router:
      refusal_fallback:
        - alias: claude-opus-5-5-guarded
          primary: claude-opus-5-5
          fallback: [asteras/canopus, orcarouter/auto-cyber] # or one model: fallback: asteras/canopus
          prose_refusal: true # optional, non-streaming requests only
```

The alias is listed in `/v1/models`, and `RouteModel` claims it without calling Jev, so there is no classification and no session state. `primary` and every `fallback` entry reach the host verbatim, so a thinking suffix such as `claude-opus-5-5(high)` belongs in the configured value. Fallback entries are trimmed; empty entries and repeats of the primary or of an earlier entry are dropped, so each model runs at most once per request.

A refusal is `finish_reason: content_filter` for OpenAI chat completions requests (what the Anthropic `stop_reason: refusal` becomes in translation) and `status: incomplete` with `incomplete_details.reason: content_filter` for OpenAI responses requests. An error (an error status or a failed host call, including a failed stream open) also counts as a refusal when its text or body contains, case-insensitively, `refusal (cyber)`, `cyber safeguards` or `content_policy_violation`; Claude delivers its cyber refusal this way.

Chain rules:

- The primary moves to the first fallback on a refusal, on a refusal-marked error, on a Jev-confirmed prose refusal when `prose_refusal: true`, or on an empty answer. Any other primary error (429, 5xx, authentication errors, timeouts, other 400s) is returned as it arrives, and the alias path does not retry.
- A fallback that is not the last moves to the next one on the same conditions, and also on any error: an error status, a failed host call, or a failed stream open.
- The last fallback's answer or error is returned as it arrives, without further checks.
- An empty answer is a 2xx non-streaming answer with no visible text and no tool call, or a stream that ends before any visible output. It is logged as `empty-answer`.
- Every model receives the identical original request body; no notice is added.

Tool-failure escalation: when the trailing tool results of a request are three or more explicit failures (the same signal and exclusions as [Difficulty raise from repeated tool failures](#difficulty-raise-from-repeated-tool-failures): structured markers only, authentication, permission, quota and billing failures excluded), the request skips the primary and earlier fallbacks and runs on the last fallback only. Its answer or error is returned as it arrives. No state is kept: once a later tool result succeeds the streak resets, and the next request starts at the primary again. With a single fallback, escalation sends the request to that fallback.

Both guards read the entry protocol the host reports. The identifiers it sends are `openai` and `openai-response`; the names this plugin declares in `executor_input_formats`, `chat-completions` and `responses`, are accepted as aliases. Any other entry protocol, Claude and Gemini included, is left alone: no refusal or empty-answer check applies, so the primary answer is returned as it arrives. Tool-failure escalation still applies.

On the streaming path the plugin buffers the chunks of each model except the last until either the first visible answer output (answer text or a tool call, after which it flushes and streams through) or a terminal refusal or end of stream with nothing emitted yet (it then closes that stream and opens the next model). Role-only deltas, empty deltas and reasoning deltas do not count as visible output, so a model that thinks at length before refusing still switches; the buffered reasoning is dropped and the client hears only the answering model. The buffer holds at most 1 MiB; past that the stream is flushed and runs unguarded. A refusal that arrives after visible output has been flushed cannot be un-sent, so it passes through and is logged as `refusal_after_output`.

`X-Auto-Router` is `claude-opus-5-5;refusal-guard` while the primary answers. After switches it names the answering model and every model tried before it, in order: `orcarouter/auto-cyber;refusal-fallback;failed_from=["claude-opus-5-5","asteras/canopus"]`. A tool-failure escalation reads `orcarouter/auto-cyber;tool-error-escalation`. Each switch logs one JSON line with `alias`, `primary`, `from` (the model that failed), `fallback` (the model switched to), `reason` and `stream`. Reasons are `refusal-fallback`, `refusal-error` (a refusal delivered as an error), `prose-refusal`, `fallback-error`, `empty-answer` and `tool-error-escalation`. Request and response bodies are never logged.

Data egress caveat: every fallback in the list can receive the request. On a switch or an escalation, the full request body, system prompt and conversation history are sent to that fallback's provider. Do not configure an external fallback for an alias that carries data which may not leave your own providers.

### Prose refusals (non-streaming only)

A primary model can refuse in plain text and still end with `finish_reason: stop` or `status: completed`. With `prose_refusal: true` on an alias, such an answer also counts as a refusal on non-streaming requests:

1. The plugin reads only the visible answer text: the `message.content` string for `chat-completions`, and the `output_text` parts of `message` output items for `responses`. Reasoning, tool call arguments and tool results are never read. A response that carries a tool call (`tool_calls`, or a `function_call`, `custom_tool_call`, `web_search_call` or any other non-message output item) never switches, so no tool call is dropped or run twice.
2. A phrase filter looks for first-person refusal wording in English ("I can't help with", "I'm unable to assist", "I won't be creating", "I'm not comfortable producing", "I must decline", "against my guidelines", "content policy") and in Portuguese ("não posso ajudar", "não vou gerar", "me recuso", "contra minhas diretrizes", "política de conteúdo"), with or without accents. Verbs that report a limit rather than a refusal are excluded, so "I can't access the file" and "não posso confirmar isso sem os logs" do not match. Without a match, Jev is not called and the answer passes through.
3. On a match, the plugin asks Jev whether the assistant itself refuses the task on safety, ethics or content-policy grounds. It uses the router's own `jev_base_url`, `jev_endpoint_path`, `jev_model`, `jev_api_key_env` and `jev_timeout_ms`. Jev is told the text is data, never instructions. Quoting a refusal, reporting missing evidence, reporting a technical limit (no access, a failed tool) and declining because the operator's stated scope excludes the task all count as not refused.
4. Only a Jev probability of at least `0.8` switches. A lower probability, a timeout, a non-200 status, or a missing or malformed answer returns the primary response byte for byte; a Jev failure also logs `prose_refusal_unconfirmed`.

The filter is a gate, not a detector: wording it does not list, in any language, never reaches Jev, so a plain refusal that phrases itself another way stays on the primary answer.

A confirmed prose refusal moves to the next model with the identical request body, the same as a `content_filter` refusal. The answer of a fallback that is not the last is checked the same way; the last fallback's answer is not checked. Jev's answer only chooses which model answers the unchanged request: the system prompt and the operator's scope reach the fallback as they were, and nothing Jev returns grants a tool, a permission or an action.

Streaming requests keep the structured-only guard described above, even with `prose_refusal: true`. A consumer that wants prose refusal protection must send `stream: false` for the alias.

Jev egress: on a phrase match, up to `snippet_chars` (default 4000) characters of the primary's visible answer go to the configured Jev endpoint, after secret redaction and with code blocks replaced by one-line descriptions. The request, system prompt and conversation history are not sent to Jev. The default `jev_base_url`, `https://openrouter.ai`, is an external service.

### Checking a build before it ships

`make smoke` loads the built `.so` through the plugin ABI with a synthetic host: `host.model.execute` returns a canned completion in the translator's own shape, the request carries the entry protocol identifier a real host sends, and Jev is a local fake, so no provider and no real Jev endpoint is contacted and no key is needed. It prints the executed models, the `X-Auto-Router` header, whether the primary or the fallback answered, what Jev was shown, and the plugin's log lines.

```bash
make smoke
python3 scripts/plugin_smoke.py --help              # cases, formats, Jev options
python3 scripts/plugin_smoke.py --case refusal-en --fake-jev-noul 0.3   # below the threshold
```

To check against the configured Jev service instead of the fake, export the key variable and pass `--jev-base-url` with the matching `--jev-endpoint-path`, `--jev-model` and `--jev-api-key-env`. The script never reads a key file and never prints the key: the plugin reads the variable itself.

## Difficulty raise from repeated tool failures

Three consecutive tool results that declare failure through a structured field and answer a call issued in the same request raise difficulty by one band. A result declares failure through `is_error: true`, a `status` of `failed` or `error`, a non-empty `error` field, or a tool execution envelope whose numeric `exit_code` is non-zero. Prose is never read as failure, so an unmarked message that merely mentions an error does not count, and `error` values of `null`, `false`, `0`, `""`, `{}` and `[]` are all treated as absence of failure.

The execution envelope is matched by shape, not by guesswork: the payload must carry both `output` and a numeric `exit_code`. A user document that merely contains an `error` key cannot masquerade as execution metadata. Results whose marked failure names an access or environment problem (authentication, permission, quota, billing) are excluded because no tier resolves them.

The raise is latched per episode. An episode is the trailing run of marked failures, identified by the call it started with, so a longer run of the same failures never raises twice and a fresh run after any progress can raise once more. The session floor still applies: difficulty, tier, and thinking never decrease.

The envelope shape was captured on hermes-lab from a real Hermes turn driven against a disposable recorder. A failing command returned `{"output": "", "exit_code": 7, "error": null}` and a passing command returned `{"output": "", "exit_code": 0, "error": null}`, both on a `tool` message carrying only `content`, `role` and `tool_call_id`. OMP's wire shape was not captured, so its coverage is unverified.

## Auto Router dashboard

The plugin adds an "Auto Router" entry to the proxy's management UI (`management.html`, under PLUGINS). The entry opens `/v0/resource/plugins/auto-router/dashboard` in the panel's frame. The page has five sections:

- Router: decisions per day by chosen model, tier split, decision reason (every reason the code emits, plus `other`), Jev latency p50/p95, and the daily count of distinct sessions.
- Guarded: for each `refusal_fallback` alias, calls per model in the chain, switches per day by destination, switches by reason, and the primary's refusal rate (switches / primary calls).
- Comparar: for every model that answered through this plugin, estimated cost, latency p50/p95, first-token p50, cost per 1,000 calls, output tokens per call, cache hit %, the `ResponseModel` mix of an upstream router, and latency p95 per day.
- Qualidade: the share of primary refusals each fallback answered, fallback errors and empty answers, and tool-error escalations.
- Operação: non-200 responses by status per model, calls per hour over the last 24 hours, the primary's 400s split into refusals that switched and other 400s, 402s, and tokens per model.

A selector shows 7, 30 or 90 days. At 90 days the daily charts group by week. Days are counted in America/Sao_Paulo time (UTC-3). The page reloads its data every 60 seconds.

Where the data comes from. Everything comes from traffic that passes through this plugin; nothing is read from ARTEX or a database.

- Routing decisions and guarded switches are counted where the plugin already logs them.
- Calls, tokens, latency, first token, status and `ResponseModel` come from the host's usage records (`usage_plugin` capability). The plugin keeps a record only when the request went through `auto-router` or a guarded alias. It tags each request it executes with the request's trace ID (`trace_id` in the executor metadata). The host stamps the same trace ID on every usage record of that request, so the primary and fallback records of one guarded call pair on it.
- The host emits no usage record for a model served by another plugin's executor (for example `nous-sse` models) when this plugin calls it. Such models appear in the Router section but not in Comparar or Operação.

Cost. Prices are US$ per 1M tokens. The `prices` map wins over the `cost` entry of `models.yaml`. A model with neither shows "sem preço". An upstream router such as `orcarouter/auto-cyber` is priced by the `ResponseModel` it reports, falling back to its own ID. Cache reads without a `cache_read` price are billed at the input price, and the panel says "cache ao preço de entrada". `subscription: true` shows "assinatura" instead of a cost. Uncached input is input minus cache reads, except for the `claude` provider, which reports cache reads apart from input.

```yaml
prices:
  asteras/canopus: {input: 1.0, output: 4.0, cache_read: 0.1}
  orca/orcacyber-flash-1.0: {input: 0.3, output: 1.2}
  claude-opus-5-5: {subscription: true}
```

These values are a format example, not real prices.

Storage and retention. The aggregate is kept in memory. Every minute, and when the plugin shuts down for a hot reload, it is written to one JSON file per day in `stats_dir` (mode 0600, written to a temp file and renamed). The files are loaded again at start, so the numbers survive a proxy restart or a plugin reload; a process exit loses at most the last minute. The plugin keeps 91 days and deletes older files. A corrupt file is logged and skipped. The files hold model names, counts, token totals, costs, latency histograms and the 4-byte session hashes the decision log already prints. They never hold prompts, bodies, keys or raw session IDs. Data starts at the version that ships the dashboard; there is no backfill.

Auth. The numbers are served only by `GET /v0/management/auto-router/stats?days=7|30|90`, behind the proxy's management key. The page itself holds no data, so serving it without auth is safe. It asks for the management key once and keeps it in `sessionStorage` for that tab. It sends the key as `X-Management-Key`, and a 401 clears it and asks again. It never reads the panel's own storage. The page sends `Content-Security-Policy` (only its own inline script, requests to the same origin) and `X-Content-Type-Options: nosniff`.

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

Install the user units and enable the weekly timer:

```bash
install -m 0644 systemd/cpa-auto-router-update.service systemd/cpa-auto-router-update.timer ~/.config/systemd/user/
XDG_RUNTIME_DIR=/run/user/1000 systemctl --user daemon-reload
XDG_RUNTIME_DIR=/run/user/1000 systemctl --user enable --now cpa-auto-router-update.timer
```

`systemd/cpa-auto-router-update.service` is the production unit: it runs the updater against the
production catalog (`http://127.0.0.1:8317`, the TCP forwarder to the LXC), writes
`~/cliproxyapi/plugins/auto-router/models.yaml`, then its `ExecStartPost` publishes that file to the
LXC and the plugin's mtime watcher hot-reloads it with no restart (see
[`docs/runbook-production.md`](docs/runbook-production.md)). A model newly visible in the catalog is
still routed only once it has an entry in both `table/tiers.yaml` and `updater/aliases.py`.

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
- The tool-failure raise fires only on explicit structural markers. A client that reports failures purely as text never raises difficulty, by design.
- `go test -race` fails two shutdown tests because their cleanup helper resets the stream lifecycle while the forwarding goroutine still reads it. The defect predates this work and is tracked in issue #11; every other package and test passes under `-race`.
