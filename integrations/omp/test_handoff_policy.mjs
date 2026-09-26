import assert from "node:assert/strict";
import { parseRouterTransition, isEligibleTierUpgrade, noticeText } from "./handoff_policy.mjs";

const upgrade = parseRouterTransition("gpt-6-astra(xhigh);escalate-tier", "top", "mid");
assert.deepEqual(upgrade, {
  model: "gpt-6-astra",
  reason: "escalate-tier",
  previousTier: "mid",
  effectiveTier: "top",
  eligible: true,
});
assert.equal(isEligibleTierUpgrade(upgrade), true);
assert.equal(isEligibleTierUpgrade(parseRouterTransition("other;failover", "top", "top")), false);
assert.equal(isEligibleTierUpgrade(parseRouterTransition("other;escalate-tier", "mid", "top")), false);
assert.equal(isEligibleTierUpgrade(parseRouterTransition("other;escalate-tier", "top", "")), false);
assert.equal(isEligibleTierUpgrade(parseRouterTransition("gpt-6-astra;keep", "top", "top")), false);
assert.equal(noticeText("mid-model", "top-model", "failover"),
  "Router changed the model from mid-model to top-model (reason: failover). Preserve the user's goals, constraints, verified facts, and working tree; reassess unverified hypotheses and the failed approach.");
console.log("OMP handoff policy tests passed");
