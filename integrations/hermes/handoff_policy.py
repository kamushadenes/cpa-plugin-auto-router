def observe_transition(previous_model, effective_model, reason):
    previous_model = str(previous_model or "").strip()
    effective_model = str(effective_model or "").strip()
    reason = str(reason or "").strip()
    if not previous_model or not effective_model or previous_model == effective_model or not reason:
        return None
    return {
        "previous_model": previous_model,
        "effective_model": effective_model,
        "reason": reason,
    }
def notice_text(previous_model, effective_model):
    return (
        f"Observed a provider model change from {previous_model} to {effective_model}; "
        "the router reason is unavailable in Hermes post_api_request. Preserve the user's "
        "goals, constraints, verified facts, and working tree; reassess unverified hypotheses "
        "and the failed approach."
    )
