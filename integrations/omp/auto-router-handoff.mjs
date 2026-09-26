import { noticeText, parseRouterTransition } from "./handoff_policy.mjs";

const STATE_TYPE = "auto-router-handoff-state";

function header(headers, name) {
  if (!headers) return "";
  if (typeof headers.get === "function") return headers.get(name) || "";
  const key = Object.keys(headers).find((candidate) => candidate.toLowerCase() === name.toLowerCase());
  const value = key ? headers[key] : "";
  return Array.isArray(value) ? value[0] || "" : String(value || "");
}

export default function autoRouterHandoff(pi) {
  let previousModel = "";
  let previousTier = "";
  let pending = null;
  let emitting = false;

  function persist() {
    pi.appendEntry(STATE_TYPE, { previousModel, previousTier, pending });
  }

  function restore(ctx) {
    previousModel = "";
    previousTier = "";
    pending = null;
    for (const entry of ctx.sessionManager.getBranch()) {
      if (entry.type !== "custom" || entry.customType !== STATE_TYPE) continue;
      previousModel = entry.data?.previousModel || "";
      previousTier = entry.data?.previousTier || "";
      pending = entry.data?.pending || null;
    }
  }

  pi.on("session_start", (_event, ctx) => restore(ctx));
  pi.on("session_switch", (_event, ctx) => restore(ctx));
  pi.on("session_branch", (_event, ctx) => restore(ctx));
  pi.on("session_tree", (_event, ctx) => restore(ctx));

  pi.on("after_provider_response", (event) => {
    const routerHeader = header(event.headers, "x-auto-router");
    const tierHeader = header(event.headers, "x-auto-router-tier");
    if (!routerHeader || !tierHeader) return;
    const transition = parseRouterTransition(routerHeader, tierHeader, previousTier);
    if (previousModel && transition.model && transition.model !== previousModel) {
      pending = {
        previousModel,
        effectiveModel: transition.model,
        previousTier,
        effectiveTier: transition.effectiveTier,
        reason: transition.reason,
      };
    }
    previousModel = transition.model || previousModel;
    previousTier = transition.effectiveTier || previousTier;
    persist();
  });

  pi.on("agent_end", (event, ctx) => {
    if (!pending || emitting || event.willContinue || !ctx.isIdle() || ctx.hasPendingMessages()) return;
    const transition = pending;
    pending = null;
    emitting = true;
    try {
      persist();
      pi.sendMessage({
        customType: "auto-router-handoff",
        content: noticeText(transition.previousModel, transition.effectiveModel, transition.reason),
        display: true,
      }, { triggerTurn: false, deliverAs: "nextTurn" });
    } finally {
      emitting = false;
    }
  });
}
