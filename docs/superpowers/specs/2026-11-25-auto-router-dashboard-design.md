# cpa-plugin-auto-router: "Auto Router" management dashboard (design)

Date: 2026-11-25. Status: approved by Kamus in conversation (DM 25/11); revised after a read-only builder check (auto-router, 25/11) whose findings are folded in below. Base: `origin/main` = `6d05e97` (v0.3.0). Host in production: CLIProxyAPI 8.0.79 (commit `6faa8d20`), LXC 139.

Approved visual reference (standalone HTML, real data snapshot from 25/11): `docs/superpowers/mockups/2026-11-25-auto-router-dashboard.html`. It is the layout to reproduce. Its numbers are a snapshot, not fixtures.

## Intent

Kamus wants to see, without opening logs, what the auto-router chooses, how often the guarded alias falls from opus to canopus and auto-cyber, and how those models compare in cost, latency and failures. The immediate use is the canopus (Asteras) vs auto-cyber (OrcaRouter) comparison that started on 25/11. Today the only evidence is the proxy journal and `usage.jsonl`, which he cannot read at a glance.

## Decisions (Kamus)

1. A screen named **Auto Router** in the proxy's own `management.html`, served by this plugin. No separate service, no new port.
2. Charts, not tables. All five sections of the approved mock: Router, Guarded, Comparar, Qualidade, Operação.
3. Show only what passes through the proxy. No ARTEX data (no Postgres access from the plugin).
4. No "remaining credit" anywhere unless the upstream returns it. Asteras has no balance endpoint (11 paths gave 404 on 25/11), and OrcaRouter is not queried for it either.
5. No benchmark panel. A benchmark over historical conversations is separate future work and is not part of this dashboard.
6. Approved after the playback ("aprovado"), with the screen named **Auto Router**.

Coordinator decisions (Chloe, reversible, not discussed with Kamus): 90-day history with a 7 / 30 / 90 day selector (matches the 90-day `usage.jsonl` rotation on LXC 139); management key asked once per browser session; session counts persisted as the 4-byte hashes the plugin already logs.

## Goals

- One menu entry "Auto Router" in the management UI, which opens a page with the five sections of the mock.
- Data covers up to 90 days (selector 7 / 30 / 90, default 7) and survives a proxy restart or a plugin hot reload.
- Works with the existing auth: no new secret, no unauthenticated data.

## Non-goals

- Historical backfill before the plugin version that ships this (the page starts empty and fills up).
- Quality scoring of answers, ARTEX metrics, credit or balance, alerts, export.
- Editing config from the page.

## Anti-goals (failure even if it works)

- Any metric or count reachable without the management key. Resource routes are unauthenticated and `remote-management.allow-remote` is `true` on this proxy.
- Request or response bodies, prompts, API keys or session identifiers in plain text written to disk or returned by the API.
- Anything that adds latency to the request path. Recording must not block a request or a stream.

## What the host offers (verified at `6faa8d20`)

- `ManagementAPI` (`sdk/pluginapi/types.go`, `RegisterManagement`): `Routes` are management-authenticated, under `/v0/management/` (`Authorization: Bearer <key>` or `X-Management-Key`). `Resources` are GET pages under `/v0/resource/plugins/<plugin id>/`, **unauthenticated**, with a `Menu` label the management UI lists.
- The management UI at `/plugin-pages/:pluginId/:menuIndex` loads the resource in an iframe (`referrerPolicy: no-referrer`).
- `UsagePlugin` (`HandleUsage(ctx, UsageRecord)`): per request, with `RequestID`, `Model`, `Alias`, `ResponseModel`, `Provider`, `Failed`, `Failure`, `Latency`, `TTFT`, `Detail` (input/output/cache read/cache creation tokens), `RequestedAt`, `Stream`.
- The RPC registration enables both through capabilities `usage_plugin` and `management_api` (`internal/pluginhost/rpc_schema.go`). The plugin registers today with `pluginabi.SchemaVersion` and capabilities `model_registrar`, `model_router` and `executor` only (`plugin.go` `registration`).
- Pairing key, to verify on TEST before building the aggregates: in the usage queue the proxy already exports (`/var/log/cliproxy-usage/usage.jsonl` on LXC 139, written by `cliproxy-usage-collector.py`), the primary and fallback records of one guarded call share the same `request_id` (4,484 opus + auto-cyber pairs and 157 opus + canopus pairs between 18/11 and 25/11; the queue has no `trace_id` field). The ABI documents `UsageRecord.RequestID` as unique per execution and `TraceID` as the parent inbound request (`sdk/pluginapi/types.go:1854-1857`), so the queue's `request_id` may be either. Capture one real guarded refusal on TEST, log both `UsageRecord`s and pair on the field that repeats. Keep the pairing rule in one function so it can change.
- The plugin never sees a request ID in `ModelRouteRequest` or `ExecutorRequest`. Correlate a usage record with this plugin by its `Alias`/`Model` (see Recording), not by a decision ID.

## Data the page needs, by section (from the mock)

Window: 7, 30 or 90 days (selector), by day in America/Sao_Paulo, plus hourly buckets for the last 24 h.

**Router** (decisions of the `auto-router` model, which the plugin already computes in `routeResponse`): decisions per day by chosen model; tier split; decision reason, every value the code emits (`keep`, `new`, `escalate-tier`, `escalate-thinking`, `fallback`, `model-gone`, `jev-unavailable`, `reclassified`, `failover`, `context_overflow_risk`; see `internal/decide/decide.go:392-430`, `host.go:329-331` and `host.go:520-583`) plus an `other` bucket for anything new; Jev latency p50/p95 per day; distinct sessions per day (count only, the session hash is never stored in clear).

**Guarded** (refusal_fallback aliases): calls per model in the chain, from `HandleUsage` (a call the primary answers produces no decision or log line today: `plugin.go:329-331` returns early, and `logRefusalFallback` fires only on a switch); switches per day by destination; switches by reason (`refusal-error`, `refusal-fallback`, `prose-refusal`, `fallback-error`, `empty-answer`, `tool-error-escalation`, `refusal_after_output`); opus refusal rate per day = switches / primary calls.

**Comparar** (per model that answered: the primary and each fallback of every alias, and every model the auto-router chose): estimated cost over the selected period; latency p50/p95; first token p50; cost per 1,000 calls; output tokens per call; cache hit %; for an upstream router (auto-cyber), the `ResponseModel` mix; latency p95 per day (per week for 90 days).

**Qualidade**: % of primary refusals the fallback answered (fallback record not failed); count of fallback errors and empty answers; count of tool-error escalations.

**Operação**: non-200 count by status per model; calls per hour for 24 h; primary 400s split into "refusal, rescued" and "other 400"; 402 count; tokens per model (input, cache, output).

## Design

### Recording

- One in-memory aggregate keyed by day (BRT) × dimension. Router decisions come from `routeResponse` and the executor's reason rewrites; guarded switches from `logRefusalFallback`. Everything about calls (counts, tokens, latency, status, primary calls the guard did not switch) comes from `HandleUsage`.
- `HandleUsage` is the source for tokens, latency, TTFT, status and `ResponseModel`. It keeps a record when its `Alias` or `Model` is `auto-router`, a configured guarded alias, or a primary or fallback of a guarded alias; for `auto-router` the chosen model is `Model`/`ResponseModel`. Guarded primary and fallback records of the same call are paired by the key verified on TEST (see "What the host offers"). Records unrelated to this plugin are ignored.
- Percentiles: keep a fixed-size per-day histogram (log buckets) per model, not raw samples.
- The plugin keeps no bodies. Distinct sessions per day are counted from the 4-byte hash `hashSession` already produces and logs (`plugin.go:590-596`); the daily file stores that set of hashes so the count survives a restart. No raw session ID is stored.

### Persistence

- A small JSON (or similar) file per day in a configurable directory, default `plugins/auto-router/stats/`, written atomically (temp + rename) every minute and on plugin shutdown (hot reload calls it: `main.go:138-140`; on process exit the minute flush bounds the loss), and loaded at start. Keep 91 days and delete older files. A corrupt file is logged and skipped, never fatal.
- Hot reload: the new plugin version loads the same files.

### Cost

- Price per 1M tokens: input and output come from the existing table `models.yaml` `cost` where present (it has only `input`/`output`: `internal/table/table.go:62`). A new optional config map `prices` keyed by model ID gives input, output and cache read, and wins over the table; it covers the cache-read price and the models the table lacks (canopus, the `orca/orcacyber-*` IDs that auto-cyber returns, `z-ai/*`). A model with no cache-read price is billed with cache reads at the input price and the panel says "cache ao preço de entrada". For an upstream router the price is looked up by `ResponseModel`.
- A model with no price shows "sem preço" instead of US$ 0. Subscription models (opus via Claude OAuth) can be marked `subscription: true` and show "assinatura".
- Uncached input = input − cache read, when the provider reports cache inside input (OpenAI-compatible). Claude reports them apart. The builder must check per provider type with real records and document the rule.

### API

- `GET /v0/management/auto-router/stats?days=7|30|90` (management-authenticated) returns the aggregates as JSON, already grouped the way the page draws them. No raw records.

### Page

- One resource `GET /v0/resource/plugins/auto-router/` with `Menu: "Auto Router"`. It is a static HTML/JS shell with no data inside, embedded in the `.so` (no files to deploy), and it fetches the stats route.
- Auth: the page asks for the management key once and keeps it in `sessionStorage` (cleared with the tab), sending it as `X-Management-Key`. It never reads the parent panel's storage. Reason: `management.html` is downloaded at runtime from `panel-github-repository` and self-updates (`internal/api/server_management.go:318-328`); its storage scheme (observed live on 25/11 as `localStorage` key `cli-proxy-auth`, `enc::v2::` obfuscation) is not a contract. A 401 clears the stored key and asks again.
- The resource sets `Content-Security-Policy` that allows only its own inline script and `connect-src 'self'`, plus `X-Content-Type-Options: nosniff`.
- Charts in plain SVG generated by the page's JS, matching the mock (dark theme, the palette per model, stacked bars, lines, donuts, horizontal bars). No external CDN and no new JS dependency.
- Empty state: "sem dados ainda" per panel when the window has no data.
- Period selector 7 / 30 / 90 days at the top; daily charts aggregate per day for 7 and 30, per week for 90.

## Security

- Data only behind the management key; the resource shell holds no numbers.
- The stats files may contain model names, counts and costs; written with mode 0600, owned by the proxy user.
- No prompt text, no keys, no session IDs in clear anywhere.

## Testing

- Unit: aggregation per dimension, BRT day boundaries, histogram percentiles against a known sample, persistence round-trip, corrupt-file skip, 91-day retention, price lookup (table, config map, missing, subscription), usage-record filtering by alias/model, guarded primary/fallback pairing (with the key found on TEST), every decision reason plus `other`.
- The resource returns no data and carries the CSP header; the stats route rejects requests without the management key (test through the host's management middleware where possible, otherwise document the manual check).
- TEST instance (port 8318) live check: the menu entry appears in `management.html`, the page renders with data after a few `auto-router` and guarded requests, and a plugin hot reload keeps the numbers. Screenshots at 1440 px.

## Rollout and rollback

- Version 0.4.0. `make install-test`, TEST check, then `make install` (hot reload).
- Rollback: install 0.3.0 again. The stats files stay and are ignored.

## Left to the builder

File layout inside the repo, aggregation structure, the exact JSON shape, histogram bucket layout, chart code, refresh interval on the page (suggest manual reload plus 60 s auto-refresh).
