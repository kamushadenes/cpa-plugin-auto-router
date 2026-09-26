const TIER_RANK = { flash: 0, mid: 1, top: 2 };

export function parseRouterTransition(routerHeader, tierHeader, previousTier = "") {
  const parts = String(routerHeader || "").split(";");
  const model = (parts.shift() || "").replace(/\([^)]*\)$/, "");
  const reason = parts.shift() || "unknown";
  const effectiveTier = String(tierHeader || "").trim();
  const eligible = reason === "escalate-tier"
    && TIER_RANK[previousTier] !== undefined
    && TIER_RANK[effectiveTier] > TIER_RANK[previousTier];
  return { model, reason, previousTier, effectiveTier, eligible };
}

export function isEligibleTierUpgrade(transition) {
  return Boolean(transition?.eligible)
    && TIER_RANK[transition.previousTier] !== undefined
    && TIER_RANK[transition.effectiveTier] > TIER_RANK[transition.previousTier];
}

export function noticeText(previousModel, effectiveModel, reason) {
  return `Router changed the model from ${previousModel} to ${effectiveModel} (reason: ${reason}). Preserve the user's goals, constraints, verified facts, and working tree; reassess unverified hypotheses and the failed approach.`;
}
