"""Self-check for integrations/hermes/cliproxy-guarded against a real Hermes checkout.

Run with Hermes' own interpreter:
    ~/.hermes/hermes-agent/venv/bin/python integrations/hermes/cliproxy_guarded_selfcheck.py

It uses a throwaway HOME and HERMES_HOME, a synthetic CLIPROXY_API_KEY and a receiver on
127.0.0.1, so it never reads the real Hermes home or reaches the real proxy.
"""
import atexit
import json
import os
import shutil
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from types import SimpleNamespace

HERMES_AGENT = Path(os.environ.get("HERMES_AGENT_DIR") or Path.home() / ".hermes" / "hermes-agent")
PLUGIN = Path(__file__).resolve().parent / "cliproxy-guarded"
PROXY_URL = "http://127.0.0.1:8317/v1"
MODEL = "claude-opus-5-5-guarded"
KEY = "synthetic-selfcheck-key"

home = Path(tempfile.mkdtemp(prefix="cliproxy-guarded-selfcheck-"))
atexit.register(shutil.rmtree, home, True)
shutil.copytree(PLUGIN, home / ".hermes" / "plugins" / "model-providers" / PLUGIN.name)
path = os.environ.get("PATH", "")
os.environ.clear()  # no real credential can reach Hermes or the receiver
# HERMES_DISABLE_LAZY_INSTALLS: a fresh HERMES_HOME would otherwise build its own dependency set.
os.environ.update(PATH=path, HOME=str(home), HERMES_HOME=str(home / ".hermes"), CLIPROXY_API_KEY=KEY,
                  HERMES_DISABLE_LAZY_INSTALLS="1")
sys.path.insert(0, str(HERMES_AGENT))

import run_agent  # noqa: E402
from agent.model_metadata import _infer_provider_from_url  # noqa: E402
from agent.turn_api_call import _should_stream  # noqa: E402
from hermes_cli.auth import AuthError, resolve_provider  # noqa: E402
from hermes_cli.runtime_provider import resolve_runtime_provider  # noqa: E402
from providers import provider_source  # noqa: E402


def stream_decision(provider):
    # Headless like a cron run: no display consumers, a real (non-Mock) client.
    agent = SimpleNamespace(_disable_streaming=False, base_url=PROXY_URL, provider=provider, client=None,
                            _has_stream_consumers=lambda: False)
    return _should_stream(agent)


assert provider_source("cliproxy-guarded") == "user", "plugin was not discovered from HERMES_HOME"
assert stream_decision("cliproxy-guarded") is False
assert stream_decision("custom:cliproxyapi") is True
assert _infer_provider_from_url(PROXY_URL) != "cliproxy-guarded", "plugin claimed 127.0.0.1 for metadata"
try:
    auto = resolve_provider("auto")
except AuthError:
    auto = None
assert auto != "cliproxy-guarded", "CLIPROXY_API_KEY alone auto-selected the plugin"
print("ok: discovery, _should_stream, metadata host and auto-detection")

requests = []


class Receiver(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, content_type, payload):
        self.send_response(200)
        self.send_header("content-type", content_type)
        self.send_header("content-length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):  # catalog probes
        self.reply("application/json", json.dumps({"object": "list", "data": [{"id": MODEL}]}).encode())

    def do_POST(self):  # chat completions; also local context-length probes ({"name": model})
        body = json.loads(self.rfile.read(int(self.headers["content-length"])))
        if self.path.endswith("/chat/completions"):
            requests.append((self.headers.get("authorization"), body))
        base = {"id": "c", "created": 0, "model": MODEL}
        if body.get("stream"):
            chunk = {**base, "object": "chat.completion.chunk", "choices": [
                {"index": 0, "delta": {"role": "assistant", "content": "pong"}, "finish_reason": "stop"}]}
            self.reply("text/event-stream", f"data: {json.dumps(chunk)}\n\ndata: [DONE]\n\n".encode())
        else:
            done = {**base, "object": "chat.completion", "choices": [
                {"index": 0, "message": {"role": "assistant", "content": "pong"}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}
            self.reply("application/json", json.dumps(done).encode())


server = ThreadingHTTPServer(("127.0.0.1", 0), Receiver)
threading.Thread(target=server.serve_forever, daemon=True).start()
receiver_url = f"http://127.0.0.1:{server.server_port}/v1"


def run_turn(**route):
    requests.clear()
    agent = run_agent.AIAgent(model=MODEL, quiet_mode=True, skip_context_files=True, skip_memory=True,
                              enabled_toolsets=[], max_iterations=1, **route)
    result = agent.run_conversation("ping", conversation_history=[], task_id="selfcheck")
    assert result["final_response"] == "pong", result
    assert requests, "no chat completion request reached the receiver"
    return requests[:]


# The cron path: resolve the pinned provider, with base_url pointed at the receiver.
rt = resolve_runtime_provider(requested="cliproxy-guarded", explicit_base_url=receiver_url, target_model=MODEL)
guarded = run_turn(api_key=rt["api_key"], base_url=rt["base_url"], provider=rt["provider"], api_mode=rt["api_mode"])
assert all(auth == f"Bearer {KEY}" for auth, _ in guarded), "CLIPROXY_API_KEY was not sent"
assert all(body.get("stream", False) is False for _, body in guarded), guarded
# Same receiver, same model, existing route: still streams, so the harness can see stream=true.
custom = run_turn(api_key=KEY, base_url=receiver_url, provider="custom:cliproxyapi")
assert all(body.get("stream") is True for _, body in custom), custom
server.shutdown()
print(f"ok: cliproxy-guarded sent {len(guarded)} request(s) without stream; "
      f"custom:cliproxyapi sent {len(custom)} with stream=true")
