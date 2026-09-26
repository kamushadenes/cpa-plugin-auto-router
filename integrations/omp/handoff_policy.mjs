export function parseRouterTransition(routerHeader, tierHeader, previousTier = "") {
  const parts = String(routerHeader || "").split(";");
  const model = (parts.shift() || "").replace(/\([^)]*\)$/, "");
  const reason = parts.shift() || "unknown";
  const effectiveTier = String(tierHeader || "").trim();
  return { model, reason, previousTier, effectiveTier };
}

export function noticeText(previousModel, effectiveModel, reason) {
  return `Router changed the model from ${previousModel} to ${effectiveModel} (reason: ${reason}). Preserve the user's goals, constraints, verified facts, and working tree; reassess unverified hypotheses and the failed approach.`;
}
