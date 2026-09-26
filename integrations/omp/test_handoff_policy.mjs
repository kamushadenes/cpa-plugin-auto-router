import assert from "node:assert/strict";
import { parseRouterTransition, noticeText } from "./handoff_policy.mjs";

assert.deepEqual(parseRouterTransition("gpt-6-astra(xhigh);escalate-tier", "top", "mid"), {
  model: "gpt-6-astra",
  reason: "escalate-tier",
  previousTier: "mid",
  effectiveTier: "top",
});
assert.deepEqual(parseRouterTransition("mid-model;failover", "mid", "mid"), {
  model: "mid-model",
  reason: "failover",
  previousTier: "mid",
  effectiveTier: "mid",
});
assert.deepEqual(parseRouterTransition("bare-model", "", ""), {
  model: "bare-model",
  reason: "unknown",
  previousTier: "",
  effectiveTier: "",
});
assert.equal(noticeText("mid-model", "top-model", "failover"),
  "Router changed the model from mid-model to top-model (reason: failover). Preserve the user's goals, constraints, verified facts, and working tree; reassess unverified hypotheses and the failed approach.");
console.log("OMP handoff policy tests passed");
