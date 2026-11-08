#!/usr/bin/env python3
"""Drive a built auto-router.so through the plugin ABI with a synthetic host.

The host side is faked in this process: `host.model.execute` returns a canned
completion instead of calling a provider, and every call the plugin makes is
printed. Nothing is deployed and no provider is contacted.

The request carries the entry protocol identifier a real CLIProxyAPI host sends
(`openai`, `openai-response`), not the alias names the plugin declares, so the
refusal guards are exercised exactly as production would.

Jev is either a local fake (default, no key needed) or the configured endpoint
(`--jev-base-url`). The key is never read from a file or printed: the plugin
reads it in-process from the environment variable named by `--jev-api-key-env`,
which the caller must export.

Examples:
    make build
    python3 scripts/plugin_smoke.py --case refusal-en
    python3 scripts/plugin_smoke.py --case refusal-pt --source-format openai-response
    python3 scripts/plugin_smoke.py --case structured --prose-refusal false
    OPENROUTER_API_KEY=... python3 scripts/plugin_smoke.py --case refusal-en \
        --jev-base-url https://api.orcarouter.ai --jev-endpoint-path /v1/systemone
"""

import argparse
import base64
import ctypes
import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

CHAT, RESPONSES = "chat", "responses"
WIRE_FORMATS = {
    "openai": CHAT,
    "chat-completions": CHAT,
    "openai-response": RESPONSES,
    "responses": RESPONSES,
}

# Synthetic assistant answers, in the shapes the CLIProxyAPI translators emit.
ANSWERS = {
    "refusal-en": "I'm sorry, but I can't help with that request.",
    "refusal-pt": "Desculpe, não posso ajudar com esse pedido.",
    "answer": "Here are the curated learnings.",
    "tool-call": "I can't help with that until I read the file.",
}
REASONING = "weighing the request"


def chat_body(text, *, tool_call=False, content_filter=False):
    message = {"role": "assistant", "content": text, "reasoning_content": REASONING}
    if tool_call:
        message["tool_calls"] = [
            {"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "{}"}}
        ]
    finish = "content_filter" if content_filter else ("tool_calls" if tool_call else "stop")
    return json.dumps({"id": "c1", "object": "chat.completion",
                       "choices": [{"index": 0, "message": message, "finish_reason": finish}]})


def responses_body(text, *, tool_call=False, content_filter=False):
    output = [
        {"id": "rs_1", "type": "reasoning", "status": "completed",
         "summary": [{"type": "summary_text", "text": REASONING}]},
        {"id": "msg_1", "type": "message", "status": "completed", "role": "assistant",
         "content": [{"type": "output_text", "annotations": [], "text": text}]},
    ]
    if tool_call:
        output.append({"id": "fc_1", "type": "function_call", "status": "completed",
                       "call_id": "call_1", "name": "read_file", "arguments": "{}"})
    body = {"id": "r1", "object": "response", "status": "completed",
            "incomplete_details": None, "output": output}
    if content_filter:
        body["status"] = "incomplete"
        body["incomplete_details"] = {"reason": "content_filter"}
    return json.dumps(body)


def primary_body(case, shape):
    build = chat_body if shape == CHAT else responses_body
    if case == "structured":
        return build("", content_filter=True)
    return build(ANSWERS[case], tool_call=case == "tool-call")


def fallback_body(shape):
    return (chat_body if shape == CHAT else responses_body)("fallback answer")


def request_body(shape):
    if shape == CHAT:
        return json.dumps({"stream": False, "messages": [{"role": "user", "content": "curate the learnings"}]})
    return json.dumps({"stream": False,
                       "input": [{"role": "user", "content": [{"type": "input_text", "text": "curate the learnings"}]}]})


class FakeJev(BaseHTTPRequestHandler):
    noul = 0.97
    seen: list = []

    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        FakeJev.seen.append(payload.get("state"))
        body = json.dumps({"answers": {"refuses_task": {"noul": FakeJev.noul}}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass


def start_fake_jev(noul):
    FakeJev.noul = noul
    server = HTTPServer(("127.0.0.1", 0), FakeJev)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return f"http://127.0.0.1:{server.server_port}", "/api/alpha/decisions"


class Buffer(ctypes.Structure):
    _fields_ = [("ptr", ctypes.c_void_p), ("len", ctypes.c_size_t)]


HOST_CALL = ctypes.CFUNCTYPE(ctypes.c_int, ctypes.c_void_p, ctypes.c_char_p,
                             ctypes.POINTER(ctypes.c_uint8), ctypes.c_size_t, ctypes.POINTER(Buffer))
FREE = ctypes.CFUNCTYPE(None, ctypes.c_void_p, ctypes.c_size_t)
PLUGIN_CALL = ctypes.CFUNCTYPE(ctypes.c_int, ctypes.c_char_p,
                               ctypes.POINTER(ctypes.c_uint8), ctypes.c_size_t, ctypes.POINTER(Buffer))


class HostAPI(ctypes.Structure):
    _fields_ = [("abi_version", ctypes.c_uint32), ("host_ctx", ctypes.c_void_p),
                ("call", HOST_CALL), ("free_buffer", FREE)]


class PluginAPI(ctypes.Structure):
    _fields_ = [("abi_version", ctypes.c_uint32), ("call", PLUGIN_CALL),
                ("free_buffer", FREE), ("shutdown", ctypes.c_void_p)]


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--plugin", default="bin/auto-router.so")
    parser.add_argument("--source-format", default="openai", choices=sorted(WIRE_FORMATS))
    parser.add_argument("--case", default="refusal-en", choices=["refusal-en", "refusal-pt", "answer", "tool-call", "structured"])
    parser.add_argument("--prose-refusal", default="true", choices=["true", "false"])
    parser.add_argument("--alias", default="guarded-alias")
    parser.add_argument("--primary", default="primary-model")
    parser.add_argument("--fallback", default="fallback-model")
    parser.add_argument("--fake-jev-noul", type=float, default=0.97,
                        help="answer of the local fake Jev; ignored with --jev-base-url")
    parser.add_argument("--jev-base-url", default="", help="real Jev endpoint; the fake is used when empty")
    parser.add_argument("--jev-endpoint-path", default="/api/alpha/decisions")
    parser.add_argument("--jev-model", default="typesafe/jev-1.13")
    parser.add_argument("--jev-api-key-env", default="OPENROUTER_API_KEY",
                        help="name of the environment variable the plugin reads; the value is never read here")
    parser.add_argument("--jev-timeout-ms", type=int, default=2000)
    args = parser.parse_args()

    shape = WIRE_FORMATS[args.source_format]
    primary, fallback = primary_body(args.case, shape), fallback_body(shape)
    original = request_body(shape).encode()
    key_env = args.jev_api_key_env
    if args.jev_base_url:
        jev_base_url, jev_path = args.jev_base_url, args.jev_endpoint_path
        if not os.environ.get(key_env):
            sys.exit(f"{key_env} is not exported; export it or drop --jev-base-url to use the local fake")
    else:
        # The Go runtime snapshots the environment when the process starts, not when
        # the shared object loads, so the fake key has to be in place before then.
        key_env = "AUTO_ROUTER_SMOKE_JEV_KEY"
        if not os.environ.get(key_env):
            os.execve(sys.executable, [sys.executable, *sys.argv], {**os.environ, key_env: "fake-jev-key"})
        jev_base_url, jev_path = start_fake_jev(args.fake_jev_noul)

    libc = ctypes.CDLL(None)
    libc.malloc.restype = ctypes.c_void_p
    libc.free.argtypes = [ctypes.c_void_p]
    executed, logs = [], []

    def write(buffer, raw):
        ptr = libc.malloc(len(raw))
        ctypes.memmove(ptr, raw, len(raw))
        buffer.contents.ptr, buffer.contents.len = ptr, len(raw)

    @HOST_CALL
    def host_call(_ctx, method, request, length, buffer):
        payload = json.loads(ctypes.string_at(request, length)) if length else {}
        name = method.decode()
        if name == "host.model.execute":
            body = primary if payload["model"] == args.primary else fallback
            executed.append({"model": payload["model"], "entry_protocol": payload.get("entry_protocol"),
                             "body_unchanged": base64.b64decode(payload["body"]) == original})
            result = {"status_code": 200, "headers": {}, "body": base64.b64encode(body.encode()).decode()}
        elif name == "host.log":
            logs.append(payload.get("message"))
            result = {}
        else:
            print(f"unexpected host callback: {name}", file=sys.stderr)
            result = {}
        write(buffer, json.dumps({"ok": True, "result": result}).encode())
        return 0

    @FREE
    def host_free(ptr, _length):
        libc.free(ptr)

    plugin_so = ctypes.CDLL(args.plugin)
    host, plugin = HostAPI(1, None, host_call, host_free), PluginAPI()
    if plugin_so.cliproxy_plugin_init(ctypes.byref(host), ctypes.byref(plugin)) != 0:
        sys.exit("plugin init failed")

    def call(method, payload):
        raw = json.dumps(payload).encode()
        out = Buffer()
        plugin.call(method.encode(), (ctypes.c_uint8 * len(raw)).from_buffer_copy(raw), len(raw), ctypes.byref(out))
        return json.loads(ctypes.string_at(out.ptr, out.len))

    config = (f"enabled: true\njev_api_key_env: {key_env}\njev_base_url: {jev_base_url}\n"
              f"jev_endpoint_path: {jev_path}\njev_model: {args.jev_model}\njev_timeout_ms: {args.jev_timeout_ms}\n"
              f"table_path: /nonexistent/models.yaml\nrefusal_fallback:\n  - alias: {args.alias}\n"
              f"    primary: {args.primary}\n    fallback: {args.fallback}\n    prose_refusal: {args.prose_refusal}\n")
    registration = call("plugin.register", {"config_yaml": base64.b64encode(config.encode()).decode()})
    metadata = registration.get("result", {}).get("metadata", {})

    response = call("executor.execute", {"Model": args.alias, "SourceFormat": args.source_format,
                                         "OriginalRequest": base64.b64encode(original).decode()})
    if not response.get("ok"):
        print(json.dumps({"error": response.get("error")}, indent=2))
        sys.exit(1)
    result = response["result"]
    payload = base64.b64decode(result["Payload"]).decode()
    print(json.dumps({
        "plugin_version": metadata.get("version") or metadata.get("Version"),
        "source_format": args.source_format,
        "case": args.case,
        "x_auto_router": result["Headers"].get("X-Auto-Router"),
        "answered_by": "fallback" if payload == fallback else "primary" if payload == primary else "unknown",
        "host_executions": executed,
        "jev_endpoint": "configured" if args.jev_base_url else "local fake",
        "jev_calls": len(FakeJev.seen) if not args.jev_base_url else "unknown (real endpoint)",
        "jev_state": FakeJev.seen,
        "plugin_logs": logs,
    }, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
