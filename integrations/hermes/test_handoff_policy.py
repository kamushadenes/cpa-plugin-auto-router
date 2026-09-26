import unittest

from handoff_policy import observe_transition, notice_text


class HandoffPolicyTest(unittest.TestCase):
    def test_model_change_notice_uses_observed_models(self):
        self.assertEqual(
            observe_transition("old", "new", "provider response"),
            {"previous_model": "old", "effective_model": "new", "reason": "provider response"},
        )
        self.assertTrue(notice_text("old", "new").startswith("Observed a provider model change from old to new;"))

    def test_unknown_or_repeated_transition_is_ignored(self):
        self.assertIsNone(observe_transition("", "new", "provider response"))
        self.assertIsNone(observe_transition("old", "old", "provider response"))
        self.assertIsNone(observe_transition("old", "new", ""))


if __name__ == "__main__":
    unittest.main()
