from __future__ import annotations

import json
from pathlib import Path

import pytest
import yaml

try:
    from updater import __main__ as cli
    from updater import merge, sources
except ImportError:  # pragma: no cover - Makefile runs pytest from updater/
    import sys

    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
    from updater import __main__ as cli
    from updater import merge, sources


ROOT = Path(__file__).resolve().parents[1]
TESTDATA = Path(__file__).with_name("testdata")


def row(*args):
    return sources.Row(*args)




def test_load_exposes_typed_models_and_scores():
    table = merge.load(TESTDATA / "old.yaml")
    score = table.models["gpt-6-astra"].scores["terminal-bench-4"][0]
    assert score.value == 57.88
    assert score.margin == 2.7
    assert table.models["gpt-6-astra"].tier == "top"


def test_merge_date_rule_keeps_older_and_accepts_equal_or_newer():
    old = merge.load(TESTDATA / "old.yaml")
    catalog = {"gpt-6-astra"}
    tiers = {"gpt-6-astra": "top"}

    older = row("gpt-6-astra", "terminal-bench-4", "xhigh", 10, 1, "2026-08-01", "")
    equal = row("gpt-6-astra", "terminal-bench-4", "xhigh", 60, 1, "2026-09-03", "equal")
    newer = row("gpt-6-astra", "terminal-bench-4", "xhigh", 61, 1, "2026-09-22", "new")

    kept = merge.merge(old, [older], catalog, tiers, {})
    assert kept.models["gpt-6-astra"].scores["terminal-bench-4"][0].value == 57.88

    replaced_equal = merge.merge(old, [equal], catalog, tiers, {})
    assert replaced_equal.models["gpt-6-astra"].scores["terminal-bench-4"][0].value == 60

    replaced_newer = merge.merge(old, [newer], catalog, tiers, {})
    assert replaced_newer.models["gpt-6-astra"].scores["terminal-bench-4"][0].value == 61


def test_source_failure_retains_old_scores():
    old = merge.load(TESTDATA / "old.yaml")
    new = merge.merge(
        old,
        rows=[],
        catalog_ids={"gpt-6-astra"},
        tiers={"gpt-6-astra": "top"},
        caps={},
    )
    assert new.models["gpt-6-astra"].scores == old.models["gpt-6-astra"].scores


def test_merge_warns_for_untiered_catalog_model(caplog):
    old = merge.load(TESTDATA / "old.yaml")
    new = merge.merge(
        old,
        rows=[],
        catalog_ids={"gpt-6-astra", "new-model"},
        tiers={"gpt-6-astra": "top"},
        caps={},
    )
    assert "new-model" in caplog.text
    assert "no tier" in caplog.text
    assert "new-model" not in new.models


def test_merge_filters_catalog_and_capability_exclusions():
    rows = [
        row("gpt-6-astra", "terminal-bench-4", "xhigh", 60, 1, "2026-09-22", ""),
        row("gpt-image-1", "terminal-bench-4", "xhigh", 99, 1, "2026-09-22", ""),
        row("abliterated-foo", "terminal-bench-4", "xhigh", 99, 1, "2026-09-22", ""),
        row("codex-auto-review", "terminal-bench-4", "xhigh", 99, 1, "2026-09-22", ""),
        row("not-in-catalog", "terminal-bench-4", "xhigh", 99, 1, "2026-09-22", ""),
    ]
    tiers = {
        "gpt-6-astra": "top",
        "gpt-image-1": "top",
        "abliterated-foo": "top",
        "codex-auto-review": "top",
    }
    new = merge.merge(
        old=None,
        rows=rows,
        catalog_ids={"gpt-6-astra", "gpt-image-1", "abliterated-foo", "codex-auto-review"},
        tiers=tiers,
        caps={
            "gpt-6-astra": {"vision": True, "cost": {"input": 10, "output": 50}},
            "gpt-image-1": {"vision": True, "cost": {"input": 1, "output": 1}},
            "abliterated-foo": {"vision": True, "cost": {"input": 1, "output": 1}},
            "codex-auto-review": {"vision": True, "cost": {"input": 1, "output": 1}},
        },
    )
    assert set(new.models) == {"gpt-6-astra"}


def test_missing_capabilities_preserve_old_and_skip_fresh(caplog):
    old = merge.load(TESTDATA / "old.yaml")
    kept = merge.merge(
        old,
        rows=[],
        catalog_ids={"gpt-6-astra"},
        tiers={"gpt-6-astra": "top"},
        caps={},
    )
    old_model = kept.models["gpt-6-astra"]
    assert old_model.vision is True
    assert old_model.cost.input == 10
    assert old_model.cost.output == 50

    fresh = merge.merge(
        old=None,
        rows=[row("fresh-model", "terminal-bench-4", None, 1, 1, "2026-09-22", "")],
        catalog_ids={"fresh-model"},
        tiers={"fresh-model": "top"},
        caps={},
    )
    assert fresh.models == {}
    assert "fresh-model" in caplog.text
    assert "capab" in caplog.text.lower()

def test_summary_counts_replaced_score_as_updated_not_dropped():
    old = merge.load(TESTDATA / "old.yaml")
    changed = merge.merge(
        old,
        [row("gpt-6-astra", "terminal-bench-4", "xhigh", 60, 1, "2026-09-22", "")],
        {"gpt-6-astra"},
        {"gpt-6-astra": "top"},
        {},
    )
    summary = cli._summary(
        old,
        changed,
        [row("gpt-6-astra", "terminal-bench-4", "xhigh", 60, 1, "2026-09-22", "")],
        {"eee": 1},
        {"gpt-6-astra"},
        {"gpt-6-astra": "top"},
    )
    assert "updated=1" in summary
    assert "dropped=0" in summary

@pytest.mark.parametrize(
    "score",
    [
        {"effort": None, "value": 1, "margin": 0},
        {"effort": None, "value": 1, "date": "2026-09-24"},
        {"effort": None, "value": 1, "margin": -1, "date": "2026-09-24"},
    ],
)
def test_validate_rejects_missing_or_negative_score_fields(score):
    value = {
        "benchmarks": {"x": {"source": "s", "unit": "pct"}},
        "models": {
            "m": {
                "tier": "top",
                "vision": True,
                "cost": {"input": 1, "output": 1},
                "scores": {"x": [score]},
            }
        },
    }
    with pytest.raises(merge.ValidationError):
        merge.validate(value)


def test_write_atomic_serializes_without_dataclass_internals(tmp_path):
    source = merge.load(TESTDATA / "old.yaml")
    out = tmp_path / "models.yaml"
    merge.write_atomic(out, source)
    raw = out.read_text()
    assert "__dict__" not in raw
    assert "_margin" not in raw
    parsed = yaml.safe_load(raw)
    assert parsed["models"]["gpt-6-astra"]["scores"]["terminal-bench-4"][0]["value"] == 57.88


def test_write_atomic_validation_failure_leaves_existing_file_untouched(tmp_path):
    out = tmp_path / "models.yaml"
    out.write_bytes(b"sentinel\n")
    invalid = {
        "benchmarks": {"x": {"source": "s", "unit": "pct"}},
        "models": {
            "m": {
                "tier": "top",
                "vision": True,
                "cost": {"input": 1, "output": 1},
                "scores": {"x": [{"effort": None, "value": 1, "margin": 0}]},
            }
        },
    }
    with pytest.raises(merge.ValidationError):
        merge.write_atomic(out, invalid)
    assert out.read_bytes() == b"sentinel\n"


def test_cli_unavailable_catalog_returns_exit_2(monkeypatch, tmp_path):
    def unavailable(*args, **kwargs):
        raise OSError("catalog unavailable")

    monkeypatch.setattr(cli, "fetch", unavailable)
    out = tmp_path / "models.yaml"
    rc = cli.main(
        [
            "--catalog",
            "http://proxy.invalid/v1/models",
            "--catalog-key-env",
            "CLIPROXY_API_KEY",
            "--tiers",
            str(ROOT / "table/tiers.yaml"),
            "--out",
            str(out),
        ]
    )
    assert rc == 2
    assert not out.exists()


def test_cli_accepts_catalog_key_file_alongside_env(monkeypatch, tmp_path):
    calls = []
    key_file = tmp_path / "catalog-key"
    key_file.write_text("file-secret\n")
    tiers = tmp_path / "tiers.yaml"
    tiers.write_text("top: [gpt-6-astra]\nmid: []\nflash: []\n")
    out = tmp_path / "models.yaml"
    monkeypatch.setenv("CLIPROXY_API_KEY", "env-secret")

    def fake_fetch(url, key=None):
        calls.append((url, key))
        if url.endswith("/v1/models"):
            return json.dumps({"data": [{"id": "gpt-6-astra"}]}).encode()
        if "models.dev" in url:
            return b"{}"
        return b"[]"
    monkeypatch.setattr(
        cli.sources,
        "modelsdev",
        lambda fetcher, catalog_ids: {
            "gpt-6-astra": {"vision": True, "cost": {"input": 10, "output": 50}}
        },
    )
    monkeypatch.setattr(cli, "fetch", fake_fetch)
    rc = cli.main(
        [
            "--catalog",
            "http://proxy.test/v1/models",
            "--catalog-key-env",
            "CLIPROXY_API_KEY",
            "--catalog-key-file",
            str(key_file),
            "--tiers",
            str(tiers),
            "--out",
            str(out),
            "--only",
            "eee",
        ]
    )
    assert rc == 0
    catalog_call = next(call for call in calls if call[0].endswith("/v1/models"))
    assert catalog_call[1] == "file-secret"
    assert out.exists()
    assert "file-secret" not in out.read_text()
    assert "env-secret" not in out.read_text()
