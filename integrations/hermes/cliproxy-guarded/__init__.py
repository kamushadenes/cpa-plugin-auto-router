"""Hermes provider for the local CLIProxyAPI guarded alias, with streaming off (#29)."""

from providers import register_provider
from providers.base import ProviderProfile


class _GuardedProfile(ProviderProfile):
    def get_hostname(self) -> str:
        # The default derives "127.0.0.1" from base_url, and agent.model_metadata matches hosts by
        # substring: every loopback endpoint, custom:cliproxyapi included, would read as this provider.
        return ""


register_provider(_GuardedProfile(
    name="cliproxy-guarded",
    env_vars=("CLIPROXY_API_KEY",),
    base_url="http://127.0.0.1:8317/v1",
    api_mode="chat_completions",
    supports_streaming=False,
    fallback_models=("claude-opus-5-5-guarded",),
    # CLIPROXY_API_KEY also serves custom:cliproxyapi: never auto-select this provider from it.
    ambient_credentials=True,
))
