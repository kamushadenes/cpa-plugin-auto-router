# Context window truncation on auto-router

## Problem
`GET /v1/models` omits the context length field entirely for `auto-router`
entries. Without that field, OMP falls back to a default of 128000 tokens,
even though the model advertises a 1,000,000-token context window. Callers
that size their context budget from this fallback under-allocate and
trigger compaction well before the advertised window.

## Verified path
- `plugin.go` (`modelRegistration`) declares `ContextLength: 1000000` for
  `auto-router`.
- CLIProxyAPI copies that value into its internal model registry:
  `/home/hermes/projects/CLIProxyAPI/internal/pluginhost/adapters.go:120-141`.
- The `/v1/models` handler discards it:
  `/home/hermes/projects/CLIProxyAPI/sdk/api/handlers/openai/openai_handlers.go:71-95`
  rebuilds each entry with only `id`, `object`, `created`, and `owned_by`,
  dropping `context_length`.

This is an upstream formatter limitation in CLIProxyAPI's OpenAI-compatible
`/v1/models` response, not a loss of the plugin's model registration. The
plugin advertises a 1,000,000-token window for the virtual `auto-router`
model, but `auto-router` routes each request to a concrete backing model
chosen by an offline capacity filter (see Routing behavior below); that
model's real context window may be smaller. If every candidate would
overflow, the router still fails open, so 1,000,000 tokens is an upper
bound, not a per-request guarantee.

## Routing behavior
`auto-router` estimates input tokens as `ceil(raw_bytes / 4) + images * 1000`.
A missing or zero `context_window` is unlimited (fail-open). Candidates
above 90% of their context window are removed before benchmark ranking.
The router selects the best fitting candidate in the requested tier, then
tries higher tiers if needed. Session difficulty, tier, and thinking floors
never decrease; there is no sticky context-window floor. If none fits, it
selects the largest eligible context window at or above the tier floor and
records `reason: context_overflow_risk`. Decision logs add `est_tokens` and
`context_filtered`.

## Jev schema limits
The local Jev schema imposes no input cap of its own. Published docs
(https://www.jevtypesafeai.com/how-to-use) state limits of up to ~64k
tokens for state plus questions, and 32k tokens for state plus the longest
single question. The local regression caps `snippet_chars` at 1500 and
asserts the actual serialized request body stays under 32000 bytes under
worst-case escaping. This is a conservative wire-size check, not proof that
the request stays within the published token budget.

## Workaround
Set a model override on the consumer side instead of relying on
`/v1/models`. In OMP:

```yaml
providers:
  cliproxyapi:
    modelOverrides:
      auto-router:
        contextWindow: 1000000
```

This override is already applied in the active `~/.omp/agent/models.yml`.

Restart or reload the OMP session for the override to take effect; already
running sessions keep their prior context window until then.

## Verification
On an OMP smoke instance, with the override applied `auto-router` resolved
to `contextWindow=1000000` and `maxTokens=32768`, and a 600k-character
request did not trigger compaction. Without the override, the same request
resolved `contextWindow=128000` and compacted.
