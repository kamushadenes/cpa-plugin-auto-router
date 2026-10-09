from __future__ import annotations

import copy
import io
import json
from pathlib import Path
from urllib.error import HTTPError
import zipfile

import pytest

try:
    from updater import aliases, sources
except ImportError:  # pragma: no cover - Makefile runs pytest from updater/
    import aliases  # type: ignore
    import sources  # type: ignore


TESTDATA = Path(__file__).with_name("testdata")
CAPTURE_DATE = "2026-09-24"
SOURCE_URLS = {
    "eee": "https://huggingface.co/datasets/evaleval/EEE_datastore",
    "openrouter": "https://openrouter.ai/api/v1/benchmarks",
    "epoch": "https://epoch.ai/data/benchmark_data.zip",
    "arena": "https://huggingface.co/datasets/lmarena-ai/leaderboard-dataset",
    "modelsdev": "https://models.dev/api.json",
    "tbench": "https://www.tbench.ai/",
    "scale": "https://labs.scale.com/leaderboard/",
}


def _fixture_fetch():
    calls: list[str] = []

    def fetch(url: str, key: str | None = None) -> bytes:
        calls.append(url)
        if "EEE_datastore/tree" in url:
            if "/vals-ai/openai/gpt-6-astra" in url:
                name = "eee_tree_vals_astra.json"
            elif "/llm-stats/openai/gpt-6-astra" in url:
                name = "eee_tree_llm_astra.json"
            elif "/artificial-analysis-llms/openai/gpt-6-astra-high" in url:
                name = "eee_tree_aa_astra_high.json"
            elif "/artificial-analysis-llms/openai/gpt-6-astra" in url:
                name = "eee_tree_aa_astra.json"
            else:
                return b"[]"
            return (TESTDATA / name).read_bytes()
        if "EEE_datastore/resolve/main" in url:
            names = {
                "205007fc-fce0-4e52-9d7b-7136cbe76409.json": "eee_vals_astra_old.json",
                "1d5ffc47-4821-42e4-b9fe-7129ada79bc8.json": "eee_vals_astra_new.json",
                "6941f422-b02a-46f8-89ec-3cf756718be5.json": "eee_llm_astra.json",
                "66a35221-2cb5-45aa-85f8-833b54f778a7.json": "eee_aa_astra.json",
                "82bf6db7-273c-48e0-b67b-57b78c6f7034.json": "eee_aa_astra_high.json",
            }
            for suffix, name in names.items():
                if url.endswith(suffix):
                    return (TESTDATA / name).read_bytes()
            raise AssertionError(f"unexpected EEE snapshot URL: {url}")
        if url == SOURCE_URLS["openrouter"]:
            assert key == "fixture-key"
            return (TESTDATA / "openrouter.json").read_bytes()
        if "epoch.ai/data/" in url:
            assert url == SOURCE_URLS["epoch"]
            archive = io.BytesIO()
            with zipfile.ZipFile(archive, "w") as zf:
                for member, name in {
                    "deepswe_external.csv": "epoch_deepswe.csv",
                    "webdev_arena_external.csv": "epoch_webdev.csv",
                    "cursorbench_external.csv": "epoch_cursorbench.csv",
                    "frontiercode_external.csv": "epoch_frontiercode.csv",
                }.items():
                    zf.writestr(member, (TESTDATA / name).read_bytes())
            return archive.getvalue()
        if "leaderboard-dataset" in url:
            if "/webdev/" in url:
                name = "arena_webdev.parquet"
            elif "/text_style_control/" in url:
                name = "arena_text.parquet"
            elif "/agent/" in url:
                name = "arena_agent.parquet"
            else:
                raise AssertionError(f"unexpected Arena URL: {url}")
            return (TESTDATA / name).read_bytes()
        if url == SOURCE_URLS["modelsdev"]:
            return (TESTDATA / "modelsdev.json").read_bytes()
        if url == SOURCE_URLS["tbench"]:
            return (TESTDATA / "tbench4.html").read_bytes()
        scale = {
            "sweatlas-qna": "scale_sweatlas_qna.html",
            "sweatlas-tw": "scale_sweatlas_tw.html",
            "sweatlas-refactoring": "scale_sweatlas_refactoring.html",
            "swe_bench_pro_public_v2": "scale_swe_bench_pro_v2.html",
        }
        for suffix, name in scale.items():
            if url == SOURCE_URLS["scale"] + suffix:
                return (TESTDATA / name).read_bytes()
        raise AssertionError(f"unexpected source URL: {url}")

    return fetch, calls




def test_aliases_are_exact_and_never_transfer_scores_across_model_versions():
    assert aliases.resolve("arena", "gpt-6-astra-max") == "gpt-6-astra"
    assert aliases.resolve("openrouter", "qwen/qwen3.8-flash-20260826") is None
    assert aliases.resolve("arena", "prefix-gpt-6-astra-max") is None
    assert aliases.resolve("openrouter", "openai/gpt-6-astra-20260903") == "gpt-6-astra"
    assert aliases.resolve("openrouter", "deepseek/deepseek-v4.1-flash-20260910") is None
    assert aliases.resolve("arena", "deepseek-v4.1-flash-max") is None
    assert aliases.arena_effort("GPT 6 Astra (xHigh)") == "xhigh"
    assert aliases.arena_effort("gpt-6-astra-max") == "max"
    assert aliases.arena_effort("claude-fable-5") is None


@pytest.mark.parametrize(
    ("label", "expected"),
    [
        ("qwen3.8-max", None),
        ("qwen3.8-max-0902", None),
        ("Qwen3.8 Max", None),
        ("qwen3.8-max (xhigh)", "xhigh"),
        ("gpt-6-astra-max", "max"),
        ("GPT 6 Astra (xHigh)", "xhigh"),
    ],
)
def test_arena_effort_strips_the_matched_alias_before_parsing(label, expected):
    assert aliases.arena_effort(label) == expected


@pytest.mark.parametrize(
    ("label", "expected"),
    [
        ("qwen3.8-max", None),
        ("qwen3.8-max-0902", None),
        ("Qwen3.8 Max", None),
        ("qwen3.8-max (xhigh)", "xhigh"),
    ],
)
def test_arena_row_uses_alias_aware_effort(label, expected):
    row = sources._arena_row(
        "webdev",
        {
            "model_name": label,
            "rating": 1660.0,
            "rating_lower": 1650.0,
            "rating_upper": 1670.0,
            "leaderboard_publish_date": CAPTURE_DATE,
        },
        "arena-webdev",
        "rating",
        "rating_lower",
        "rating_upper",
        1.0,
    )
    assert row is not None
    assert row.model == "qwen-3.8-max"
    assert row.effort == expected


def test_eee_keeps_newest_snapshot_and_labels_standard_error(caplog):
    fetch, _ = _fixture_fetch()
    rows = sources.eee(fetch, {"gpt-6-astra"})
    overall = [
        row
        for row in rows
        if row.model == "gpt-6-astra" and row.bench == "terminal-bench-4"
    ]
    assert len(overall) == 1
    assert overall[0].effort == "max"
    assert overall[0].value == pytest.approx(57.071)
    assert overall[0].margin == pytest.approx(3.072)
    assert overall[0].date == "2026-09-22"
    assert "standard error" in overall[0].note.lower()
    assert "95%" not in overall[0].note
    assert {
        row.bench
        for row in rows
        if row.model == "gpt-6-astra"
    } == {
        "terminal-bench-4",
        "terminal-bench-4-software",
        "terminal-bench-4-security",
    }
    assert "missing published uncertainty" in caplog.text.lower()


def test_eee_source_failure_keeps_healthy_sibling_rows(caplog):
    fixture_fetch, _ = _fixture_fetch()

    def partially_unavailable(url: str, key: str | None = None) -> bytes:
        if "/llm-stats/openai/gpt-6-astra" in url:
            raise OSError("fixture directory unavailable")
        if url.endswith("205007fc-fce0-4e52-9d7b-7136cbe76409.json"):
            raise OSError("fixture snapshot unavailable")
        return fixture_fetch(url, key)

    rows = sources.eee(partially_unavailable, {"gpt-6-astra"})
    assert any(row.bench == "terminal-bench-4" for row in rows)
    assert "source failed" in caplog.text.lower()
    assert "llm-stats/openai/gpt-6-astra" in caplog.text
    assert "205007fc-fce0-4e52-9d7b-7136cbe76409.json" in caplog.text


def test_eee_tracks_newest_snapshot_per_declared_effort(monkeypatch):
    document = json.loads((TESTDATA / "eee_vals_astra_new.json").read_bytes())
    max_document = copy.deepcopy(document)
    high_document = copy.deepcopy(document)
    high_document["evaluation_id"] += "-high"
    high_document["retrieved_timestamp"] = str(
        float(high_document["retrieved_timestamp"]) + 1
    )
    for result in high_document["evaluation_results"]:
        result["score_details"]["details"]["reasoning_effort"] = "high"
        result["generation_config"]["additional_details"]["reasoning_effort"] = "high"

    monkeypatch.setitem(
        aliases.EEE_MODELS,
        "gpt-6-astra",
        (("vals-ai", "openai", "gpt-6-astra"),),
    )

    def fetch(url: str, key: str | None = None) -> bytes:
        if "EEE_datastore/tree" in url:
            return json.dumps(
                [
                    {"type": "file", "path": "data/max.json"},
                    {"type": "file", "path": "data/high.json"},
                ]
            ).encode()
        if url.endswith("data/max.json"):
            return json.dumps(max_document).encode()
        if url.endswith("data/high.json"):
            return json.dumps(high_document).encode()
        raise AssertionError(url)

    rows = sources.eee(fetch, {"gpt-6-astra"})
    overall = [row for row in rows if row.bench == "terminal-bench-4"]
    assert {row.effort for row in overall} == {"max", "high"}


def test_openrouter_parses_current_gpqa_wire_and_skips_unpublished_margins(caplog):
    fetch, _ = _fixture_fetch()
    rows = sources.openrouter(fetch, key="fixture-key")
    row = next(
        row
        for row in rows
        if row.model == "gpt-6-astra" and row.bench == "gpqa-diamond"
    )
    assert row.effort is None
    assert row.value == pytest.approx(94.36415317919074)
    assert row.margin == pytest.approx(0.20135000000000014)
    assert row.date == "2026-09-06"
    assert "standard deviation" in row.note.lower()
    assert not any(row.bench.startswith("aa-") for row in rows)
    assert not any(row.bench.startswith("design-arena-") for row in rows)
    assert "unmapped openrouter sakana/fugu-ultra" in caplog.text
    assert "missing published uncertainty" in caplog.text.lower()


def test_epoch_uses_current_archive_and_only_published_ranges(caplog):
    fetch, calls = _fixture_fetch()
    rows = sources.epoch(fetch)
    assert SOURCE_URLS["epoch"] in calls
    deep = next(
        row
        for row in rows
        if row.model == "gpt-5.6-sol" and row.bench == "deepswe"
    )
    assert deep.effort == "max"
    assert deep.value == pytest.approx(72.66666666666667)
    assert deep.margin == pytest.approx(2.829822249837175)
    assert deep.date == "2026-07-09"
    assert "95% CI half-width" in deep.note
    web = next(
        row
        for row in rows
        if row.model == "gpt-6-astra" and row.bench == "arena-webdev"
    )
    assert web.effort == "max"
    assert web.value == pytest.approx(1800.28)
    assert web.margin == pytest.approx((1816.76 - 1783.81) / 2)
    assert web.date == "2026-09-03"
    assert not any(row.bench in {"cursorbench", "frontiercode"} for row in rows)
    assert "cursorbench" in caplog.text and "frontiercode" in caplog.text
    assert "missing published uncertainty" in caplog.text.lower()


def test_arena_parquets_cover_all_routing_categories_with_published_ci():
    fetch, _ = _fixture_fetch()
    rows = sources.arena(fetch)
    assert {
        "arena-webdev",
        "arena-coding",
        "arena-math",
        "arena-creative-writing",
        "arena-instruction-following",
        "arena-hard-prompts",
        "arena-overall",
        "arena-agent",
    } <= {row.bench for row in rows}
    web = next(
        row
        for row in rows
        if row.model == "gpt-6-astra" and row.bench == "arena-webdev"
    )
    assert web.effort == "max"
    assert web.value == pytest.approx(1792.1796354686667)
    assert web.margin == pytest.approx((1804.2223435625672 - 1780.1369273747662) / 2)
    assert web.date == "2026-09-23"
    agent = next(
        row
        for row in rows
        if row.model == "gpt-6-astra" and row.bench == "arena-agent"
    )
    assert agent.effort == "max"
    assert agent.value == pytest.approx(115.36629274970146)
    assert agent.margin == pytest.approx(
        (0.13639321085749467 - 0.09433937464190824) * 500
    )
    assert agent.date == "2026-09-15"
    assert "published score_ci lower/upper interval" in agent.note


def test_modelsdev_extracts_positive_integer_context_windows(caplog):
    fetch, _ = _fixture_fetch()
    caps = sources.modelsdev(fetch, {"gpt-6-astra", "mimo-v2.6-flash"})
    assert caps == {
        "gpt-6-astra": {
            "vision": True,
            "cost": {"input": 10.0, "output": 50.0},
            "context_window": 1050000,
        },
        "mimo-v2.6-flash": {
            "vision": True,
            "cost": {"input": 0.14, "output": 0.28},
            "context_window": 1048576,
        },
    }
    assert "gpt-6-astra-pro" not in caps
    assert "unmapped modelsdev openai/gpt-6-astra-pro" in caplog.text


@pytest.mark.parametrize("context_value", [None, 0, -1, 1.5, "1050000", True])
def test_modelsdev_omits_unknown_or_invalid_context_window(context_value):
    payload = json.loads((TESTDATA / "modelsdev.json").read_bytes())
    limit = payload["nano-gpt"]["models"]["openai/gpt-6-astra"]["limit"]
    if context_value is None:
        limit.pop("context")
    else:
        limit["context"] = context_value

    def fetch(url: str, key: str | None = None) -> bytes:
        return json.dumps(payload).encode()

    caps = sources.modelsdev(fetch, {"gpt-6-astra"})
    assert caps["gpt-6-astra"] == {
        "vision": True,
        "cost": {"input": 10.0, "output": 50.0},
    }


def test_modelsdev_preserves_published_zero_cost():
    def fetch(url: str, key: str | None = None) -> bytes:
        assert url == SOURCE_URLS["modelsdev"]
        return (TESTDATA / "modelsdev_zero.json").read_bytes()

    assert sources.modelsdev(fetch, {"mimo-v2.6-flash"}) == {
        "mimo-v2.6-flash": {
            "vision": True,
            "cost": {"input": 0.0, "output": 0.0},
            "context_window": 1048576,
        }
    }


def test_modelsdev_skips_caps_without_explicit_input_modalities(caplog):
    payload = json.loads((TESTDATA / "modelsdev_zero.json").read_bytes())
    model = payload["xiaomi-token-plan-cn"]["models"]["mimo-v2.6-flash"]
    model["modalities"].pop("input")

    def fetch(url: str, key: str | None = None) -> bytes:
        return json.dumps(payload).encode()

    assert sources.modelsdev(fetch, {"mimo-v2.6-flash"}) == {}
    assert "incomplete modelsdev capabilities" in caplog.text


def test_fallback_fetches_only_missing_required_benchmarks():
    fetch, calls = _fixture_fetch()
    rows = sources.fallback(fetch, {"terminal-bench-4"})
    assert SOURCE_URLS["tbench"] not in calls
    assert {
        "swe-bench-pro-v2",
        "swe-atlas-qna",
        "swe-atlas-test-writing",
        "swe-atlas-refactoring",
    } <= {row.bench for row in rows}
    qna = next(
        row
        for row in rows
        if row.model == "gpt-6-astra" and row.bench == "swe-atlas-qna"
    )
    assert qna.effort == "xhigh"
    assert qna.value == pytest.approx(59.14)
    assert qna.margin == pytest.approx(4.88)
    assert qna.date == "2026-09-09"
    assert "reported confidenceInterval_upper" in qna.note
    swe = next(
        row
        for row in rows
        if row.model == "gpt-6-astra" and row.bench == "swe-bench-pro-v2"
    )
    assert swe.effort == "high"
    assert swe.value == pytest.approx(96.9)
    assert swe.margin == pytest.approx(1.1)
    assert swe.date == "2026-09-22"

    tbench_fetch, tbench_calls = _fixture_fetch()
    terminal = sources.fallback(
        tbench_fetch,
        {
            "swe-bench-pro-v2",
            "swe-atlas-qna",
            "swe-atlas-test-writing",
            "swe-atlas-refactoring",
        },
    )
    assert tbench_calls == [SOURCE_URLS["tbench"]]
    assert [(row.effort, row.value, row.margin) for row in terminal] == [
        ("max", 58.18, 2.79),
        ("xhigh", 57.88, 2.72),
        ("high", 57.88, 2.97),
    ]
    assert all("95% CI half-width" in row.note for row in terminal)

    no_fetch, no_calls = _fixture_fetch()
    assert sources.fallback(
        no_fetch,
        {
            "terminal-bench-4",
            "swe-bench-pro-v2",
            "swe-atlas-qna",
            "swe-atlas-test-writing",
            "swe-atlas-refactoring",
        },
    ) == []
    assert no_calls == []


def test_fallback_page_failure_keeps_sibling_pages_and_names_url(caplog):
    fixture_fetch, _ = _fixture_fetch()
    failing = SOURCE_URLS["scale"] + "sweatlas-qna"

    def one_page_missing(url: str, key: str | None = None) -> bytes:
        if url == failing:
            raise HTTPError(url, 404, "Not Found", {}, None)
        return fixture_fetch(url, key)

    rows = sources.fallback(one_page_missing, set())
    assert {row.bench for row in rows} == {
        "terminal-bench-4",
        "swe-bench-pro-v2",
        "swe-atlas-test-writing",
        "swe-atlas-refactoring",
    }
    assert f"source failed: fallback swe-atlas-qna: {failing}: HTTP Error 404: Not Found" in caplog.text
