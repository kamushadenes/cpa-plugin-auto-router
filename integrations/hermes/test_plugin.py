import importlib.util
import pathlib
import sys
import unittest


ROOT = pathlib.Path(__file__).parent
SPEC = importlib.util.spec_from_file_location(
    "auto_router_handoff", ROOT / "__init__.py", submodule_search_locations=[str(ROOT)]
)
MODULE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


class State:
    def __init__(self):
        self.values = {}

    def get(self, key, default=None):
        return self.values.get(key, default)

    def set(self, key, value):
        self.values[key] = value


class Context:
    def __init__(self):
        self.state = State()
        self.hooks = {}

    def register_hook(self, name, callback):
        self.hooks[name] = callback


class PluginTest(unittest.TestCase):
    def test_sessions_are_isolated_and_guidance_is_next_turn_only(self):
        ctx = Context()
        MODULE.register(ctx)
        post = ctx.hooks["post_api_request"]
        pre = ctx.hooks["pre_llm_call"]
        post(session_id="one", response_model="model-a")
        post(session_id="two", response_model="model-x")
        post(session_id="one", response_model="model-b")
        self.assertIn("model-a to model-b", pre(session_id="one")["context"])
        self.assertIsNone(pre(session_id="one"))
        self.assertIsNone(pre(session_id="two"))


if __name__ == "__main__":
    unittest.main()
