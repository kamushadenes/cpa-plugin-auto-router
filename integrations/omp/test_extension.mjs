import assert from "node:assert/strict";
import extension from "./auto-router-handoff.mjs";

const handlers = new Map();
const entries = [];
const messages = [];
const api = {
  on(name, handler) { handlers.set(name, handler); },
  appendEntry(customType, data) { entries.push({ type: "custom", customType, data }); },
  sendMessage(message, options) { messages.push({ message, options }); },
};
extension(api);
const ctx = {
  sessionManager: { getBranch: () => entries },
  isIdle: () => true,
  hasPendingMessages: () => false,
};
await handlers.get("session_start")({}, ctx);
await handlers.get("after_provider_response")({ headers: { "x-auto-router": "mid-model(high);new", "x-auto-router-tier": "mid" } }, ctx);
await handlers.get("agent_end")({ willContinue: false }, ctx);
assert.equal(messages.length, 0);
await handlers.get("after_provider_response")({ headers: { "x-auto-router": "top-model(xhigh);escalate-tier", "x-auto-router-tier": "top" } }, ctx);
await handlers.get("agent_end")({ willContinue: false }, ctx);
assert.equal(messages.length, 1);
assert.equal(messages[0].options.deliverAs, "nextTurn");
assert.match(messages[0].message.content, /mid-model to top-model/);
assert.doesNotMatch(messages[0].message.content, /compact|compaction|fresh summary/i);
await handlers.get("agent_end")({ willContinue: false }, ctx);
assert.equal(messages.length, 1);
console.log("OMP handoff extension tests passed");
