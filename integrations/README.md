# Native handoff adapters

These files stay outside OMP and Hermes core.

## OMP

Install `integrations/omp/auto-router-handoff.mjs` as a user extension, or load it for a run with:

```sh
omp --extension /path/to/auto-router-handoff.mjs
```

The extension reads `X-Auto-Router` and `X-Auto-Router-Tier` after a provider response. It records state in the OMP session branch and queues one hidden `nextTurn` agent-facing transition message after `agent_end`. The first request that receives an upgraded router response cannot be changed retroactively. Automatic compaction is not claimed: `after_provider_response` fires before the stream body is consumed, `before_agent_start` is already entering the next turn, and native testing did not establish `agent_end` as an idle compaction boundary. The notice tells the agent to preserve verified state and reports that `/compact` is required when a fresh summary is needed.

For persistent use, enroll the file in the installed OMP global extension allowlist. A loose file is not automatically loaded by Coding Agent; the Coding Agent repository must add the path to its explicit six-extension allowlist. Do not weaken extension discovery or steering guards.

The extension was loaded through the installed OMP CLI with `--extension` and a real non-stream inference. The adapter tests use the installed Node runtime.

## Hermes
The plugin uses the documented `post_api_request` and `pre_llm_call` hooks and profile-scoped `ctx.state`. Hermes exposes `response_model` but not the router response headers or reason in these hooks, so the plugin stores a factual model-change notice and returns it as context on the next real LLM request. It does not claim or attempt automatic compaction. It does not call `inject_message`: that API needs a gateway session key and explicit per-plugin gateway consent, and it can interrupt an active CLI turn. The next-turn hook avoids that unsafe boundary.

Install the directory and enable it, then start a fresh Hermes process:

```sh
cp -a /path/to/integrations/hermes ~/.hermes/plugins/auto-router-handoff
hermes plugins doctor auto-router-handoff --ci
hermes plugins enable auto-router-handoff
```

