# cliproxy-guarded

A Hermes model-provider plugin for the local CLIProxyAPI guarded alias. It sends every request with streaming off.

| Setting | Value |
|-|-|
| Provider name | `cliproxy-guarded` |
| Endpoint | `http://127.0.0.1:8317/v1`, `chat_completions` |
| API key | `CLIPROXY_API_KEY` from the Hermes environment |
| Streaming | off (`supports_streaming=False`) |
| Default model | `claude-opus-5-5-guarded` |

The guarded alias checks for prose refusals only when a request has `stream: false` (#27). Hermes streams on `custom:cliproxyapi`. `supports_streaming=False` on a provider profile is how Hermes turns streaming off for a single provider (#29).

## What it leaves alone

- `custom:cliproxyapi` keeps streaming, even though it uses the same endpoint and key.
- The plugin does not claim `127.0.0.1` for URL-based model-metadata lookups. Without this, other loopback endpoints would be treated as this provider.
- A set `CLIPROXY_API_KEY` never makes Hermes pick this provider on its own. `model.provider: auto` skips it, and the auxiliary fallback chain skips it too.
- It does not replace an existing provider or register any aliases.

## Limit

`supports_streaming` covers only the main conversation calls. Hermes auxiliary calls do not read it. Context compression installs a progress hook, and while that hook is active the summary request is sent with `stream: true` (`agent/auxiliary_client.py`, `agent/conversation_compression.py`). If a long run of the pinned job compresses its context and the compression model resolves to this provider, its summary requests still stream.

## Install

Copy the directory into the Hermes home:

```bash
mkdir -p ~/.hermes/plugins/model-providers
cp -R integrations/hermes/cliproxy-guarded ~/.hermes/plugins/model-providers/
hermes plugins doctor ~/.hermes/plugins/model-providers/cliproxy-guarded --ci
```

Running Hermes processes pick up the new directory the next time they look up `cliproxy-guarded`, so no restart is needed.

## Pin the cron job

Pin only cron job `75db9b8c762b` to this provider:

```bash
hermes cron edit 75db9b8c762b --model claude-opus-5-5-guarded --provider cliproxy-guarded
```

Every other job and the main agent keep their current provider. The job must not have its own `base_url`: the scheduler passes a job-level `base_url` to runtime resolution, and it overrides the plugin's endpoint. To undo, run `hermes cron edit 75db9b8c762b` again with the job's previous `--model` and `--provider` values.

## Self-check

```bash
~/.hermes/hermes-agent/venv/bin/python integrations/hermes/cliproxy_guarded_selfcheck.py
```

The self-check runs in a throwaway `HOME` and `HERMES_HOME`, with a synthetic key. It loads the plugin through real Hermes discovery and checks that `_should_stream` is False for `cliproxy-guarded` and True for `custom:cliproxyapi`. It also checks that the plugin claims no metadata host and is not chosen by `auto`. Last, it runs a real Hermes turn on each route against a local receiver. `cliproxy-guarded` must send its chat request without `stream: true`, and `custom:cliproxyapi` must send `stream: true`.
