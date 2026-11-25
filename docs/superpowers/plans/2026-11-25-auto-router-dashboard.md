# cpa-plugin-auto-router: Auto Router dashboard, implementation plan

**Goal:** the "Auto Router" screen in the proxy management UI, as approved in the design.

**Spec:** `docs/superpowers/specs/2026-11-25-auto-router-dashboard-design.md` (the design). **Visual reference:** `docs/superpowers/mockups/2026-11-25-auto-router-dashboard.html` (the mock). Executors read both; this plan cites design sections instead of restating them.

**Version:** 0.4.0. Base `origin/main` = `6d05e97` (v0.3.0). One PR, signed commits.

## Global Constraints

- Frozen decisions: design "Decisions (Kamus)" and "Coordinator decisions". Not reopened here.
- Standard library only: no new Go module, no JS dependency, no CDN. The page is embedded in the `.so` with `embed`.
- Production is never touched (LXC 139, `/opt/cliproxy`, `make install`, ssh to 10.23.23.12). TEST only: `cliproxyapi-test.service`, port 8318, `make install-test`, manual restart.
- No request or response bodies, prompts, keys or raw session IDs logged, persisted or returned (design "Anti-goals", "Security").
- Recording never blocks the request path: no disk I/O on a request goroutine, no lock held across I/O (design "Anti-goals").
- The go.mod `replace` to `~/projects/CLIProxyAPI` stays as is. Host behaviour is read from the frozen copy at `6faa8d20`.

## File Structure

```
stats.go           aggregate (day x dimension), histograms, pairing rule, snapshot JSON, persistence
stats_test.go      unit tests from design "Testing"
management.go      management.register / management.handle: stats route + resource shell, CSP
dashboard/index.html  page shell (HTML + inline JS + SVG charts), embedded
plugin.go          capabilities usage_plugin + management_api, config keys prices / stats_dir, usage.handle, v0.4.0
host.go, refusal.go  record router decisions and guarded switches (one call each at the existing log sites)
main.go            flush on shutdown
```

## Task 0: TEST verifications before the aggregates (design "What the host offers")

- [ ] Build a probe that declares `usage_plugin` and `management_api` and writes every received `UsageRecord` (fields only) to a TEST-local file.
- [ ] Throwaway guarded alias on TEST (`deepseek-v4-flash` → `orcarouter/auto-cyber`), one non-streaming request with `max_tokens: 12`. Pair on the field that repeats (`RequestID` or `TraceID`). Remove the alias.
- [ ] Confirm `HandleUsage` fires for `auto-router` and guarded alias calls; note which records carry `Alias`.
- [ ] Confirm the menu entry in `management.html` and the iframe load.
- [ ] Record the per-provider cache rule (design "Cost", uncached input).

## Task 1: Aggregate and persistence (design "Recording", "Persistence")

- [ ] Per-day BRT buckets; router decisions with the full reason list plus `other`; guarded switches by destination and reason; per-model call stats (status, tokens, latency/TTFT log histograms, `ResponseModel` mix, hourly counts).
- [ ] Pairing rule in one function, using the key found in Task 0.
- [ ] Session hashes from `hashSession` only.
- [ ] One JSON file per day, 0600, temp + rename, flushed every minute off the request path and on shutdown; 91-day retention; corrupt file logged and skipped.

## Task 2: Cost (design "Cost")

- [ ] Price lookup: config `prices` wins over `models.yaml` cost; missing price, subscription, cache-read fallback to input price; upstream routers priced by `ResponseModel`.

## Task 3: API and page (design "API", "Page", "Security")

- [ ] `GET /v0/management/auto-router/stats?days=7|30|90`, grouped for drawing.
- [ ] Resource shell with `Menu: "Auto Router"`, no data, CSP and `nosniff`.
- [ ] Page: key prompt in `sessionStorage`, 401 clears it; five sections as the mock; SVG charts; empty states; 7/30/90 selector; weekly buckets at 90 days; 60 s auto-refresh.

## Task 4: Docs and release

- [ ] `plugin.go` version 0.4.0; CHANGELOG `[0.4.0]`; README "Auto Router dashboard".

## Task 5: Acceptance (design "Testing", brief "Acceptance")

- [ ] `go test ./...`, `go test -race ./...`, `make smoke`.
- [ ] TEST: 401 without key / 200 with key through the host middleware; persistence across a restart; menu and page; screenshots at 1440 px per section, compared with the mock.
- [ ] PR with data-flow diagram; no merge, no deploy.
