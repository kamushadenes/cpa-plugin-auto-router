from .handoff_policy import notice_text, observe_transition


def register(ctx):
    state = ctx.state

    def post_api_request(**kwargs):
        session_id = str(kwargs.get("session_id") or "").strip()
        effective_model = str(kwargs.get("response_model") or "").strip()
        if not session_id or not effective_model:
            return
        key = f"session:{session_id}"
        previous = state.get(key, {})
        previous_model = str(previous.get("model") or "").strip()
        transition = observe_transition(previous_model, effective_model, "provider response")
        state.set(
            key,
            {
                "model": effective_model,
                "pending_notice": (
                    notice_text(transition["previous_model"], transition["effective_model"])
                    if transition else previous.get("pending_notice", "")
                ),
            },
        )

    def pre_llm_call(**kwargs):
        session_id = str(kwargs.get("session_id") or "").strip()
        if not session_id:
            return None
        key = f"session:{session_id}"
        current = state.get(key, {})
        notice = str(current.get("pending_notice") or "").strip()
        if not notice:
            return None
        state.set(key, {"model": current.get("model", ""), "pending_notice": ""})
        return {"context": notice}

    ctx.register_hook("post_api_request", post_api_request)
    ctx.register_hook("pre_llm_call", pre_llm_call)
