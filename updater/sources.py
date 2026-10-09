from __future__ import annotations

from dataclasses import dataclass
import csv
import io
import json
import logging
import re
import zipfile
from typing import Any, Callable

try:
    from . import aliases
except ImportError:  # pragma: no cover - supports ``cd updater`` invocation
    import aliases  # type: ignore


log = logging.getLogger(__name__)
Fetch = Callable[..., bytes]
UNMAPPED_COUNT = 0

EEE_TREE = "https://huggingface.co/api/datasets/evaleval/EEE_datastore/tree/main/data"
EEE_RESOLVE = "https://huggingface.co/datasets/evaleval/EEE_datastore/resolve/main"
OPENROUTER_URL = "https://openrouter.ai/api/v1/benchmarks"
EPOCH_URL = "https://epoch.ai/data/benchmark_data.zip"
ARENA_URL = (
    "https://huggingface.co/datasets/lmarena-ai/leaderboard-dataset/resolve/"
    "refs%2Fconvert%2Fparquet/{config}/latest/0000.parquet"
)
MODELSDEV_URL = "https://models.dev/api.json"
TBENCH_URL = "https://www.tbench.ai/"
SCALE_URLS = {
    "swe-bench-pro-v2": "https://labs.scale.com/leaderboard/swe_bench_pro_public_v2",
    "swe-atlas-qna": "https://labs.scale.com/leaderboard/sweatlas-qna",
    "swe-atlas-test-writing": "https://labs.scale.com/leaderboard/sweatlas-tw",
    "swe-atlas-refactoring": "https://labs.scale.com/leaderboard/sweatlas-refactoring",
}
REQUIRED_FALLBACKS = {"terminal-bench-4", *SCALE_URLS}


@dataclass(frozen=True)
class Row:
    model: str
    bench: str
    effort: str | None
    value: float
    margin: float
    date: str
    note: str


def _warn_unmapped(source: str, label: str) -> None:
    global UNMAPPED_COUNT
    UNMAPPED_COUNT += 1
    log.warning("unmapped %s %s", source, label)


def _warn_uncertainty(source: str, label: str) -> None:
    log.warning("missing published uncertainty %s %s", source, label)


def _date(value: Any) -> str:
    if not value:
        return ""
    return str(value)[:10]


def _number(value: Any) -> float | None:
    if value is None or value == "":
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _effort(value: Any) -> str | None:
    if value is None:
        return None
    normal = str(value).strip().lower().replace("extra high", "xhigh")
    normal = normal.replace("extra-high", "xhigh").replace("x-high", "xhigh")
    return normal if normal in {"low", "medium", "high", "xhigh", "max"} else None


def _scaled(value: float, unit: Any) -> float:
    return value * 100 if str(unit).lower() in {"proportion", "fraction"} else value


def _uncertainty(score: dict[str, Any], scale: float = 1.0) -> tuple[float, str] | None:
    uncertainty = score.get("uncertainty")
    if not isinstance(uncertainty, dict):
        return None
    standard_error = uncertainty.get("standard_error")
    if isinstance(standard_error, dict):
        value = _number(standard_error.get("value"))
        if value is not None:
            return value * scale, "published standard error"
    for key, label in (
        ("confidence_interval_95", "published 95% confidence interval"),
        ("confidence_interval", "published confidence interval"),
    ):
        interval = uncertainty.get(key)
        if not isinstance(interval, dict):
            continue
        lower = _number(interval.get("lower"))
        upper = _number(interval.get("upper"))
        if lower is not None and upper is not None:
            return (upper - lower) * scale / 2, label
        half_width = _number(interval.get("half_width"))
        if half_width is not None:
            return half_width * scale, label + " half-width"
    return None
def _eee_effort(result: dict[str, Any], document: dict[str, Any]) -> str | None:
    score = result.get("score_details") or {}
    details = score.get("details") or {}
    generation = (result.get("generation_config") or {}).get("additional_details") or {}
    declared = _effort(details.get("reasoning_effort") or generation.get("reasoning_effort"))
    if declared is not None:
        return declared
    model = document.get("model_info") or {}
    for value in (model.get("name"), model.get("id")):
        if isinstance(value, str):
            declared = aliases.arena_effort(value)
            if declared is not None:
                return declared
    return None




def eee(fetch: Fetch, catalog_ids: set[str]) -> list[Row]:
    newest: dict[
        tuple[str, str, str, str | None],
        tuple[tuple[str, str], dict[str, Any], dict[str, Any]],
    ] = {}
    for model_id in aliases.CATALOG_IDS:
        if model_id not in catalog_ids:
            continue
        entries = aliases.EEE_MODELS[model_id]
        if entries is None:
            continue
        for source, org, model_dir in entries:
            tree_url = f"{EEE_TREE}/{source}/{org}/{model_dir}"
            try:
                listing = json.loads(fetch(tree_url))
                if not isinstance(listing, list):
                    raise ValueError("tree response is not a list")
            except Exception as exc:
                log.warning("source failed: eee %s/%s/%s: %s", source, org, model_dir, exc)
                continue
            for item in listing:
                if not isinstance(item, dict):
                    continue
                path = item.get("path")
                if not isinstance(path, str) or not path.endswith(".json"):
                    continue
                snapshot_url = f"{EEE_RESOLVE}/{path}"
                try:
                    document = json.loads(fetch(snapshot_url))
                    if not isinstance(document, dict):
                        raise ValueError("snapshot is not an object")
                except Exception as exc:
                    log.warning("source failed: eee %s: %s", path, exc)
                    continue
                source_details = (
                    (document.get("source_metadata") or {}).get("additional_details") or {}
                )
                source_date = source_details.get("benchmark_updated") or source_details.get(
                    "cron_run_date"
                )
                retrieved = str(document.get("retrieved_timestamp") or "")
                for result in document.get("evaluation_results") or []:
                    if not isinstance(result, dict):
                        continue
                    name = result.get("evaluation_name")
                    if not isinstance(name, str) or name not in aliases.EEE_EVALS:
                        continue
                    details = (result.get("score_details") or {}).get("details") or {}
                    result_date = details.get("benchmark_updated") or source_date
                    rank = (_date(result_date), retrieved)
                    effort = _eee_effort(result, document)
                    key = (model_id, source, name, effort)
                    if key not in newest or rank >= newest[key][0]:
                        newest[key] = (rank, result, document)

    rows: list[Row] = []
    for (model_id, source, name, effort), (_, result, document) in newest.items():
        score = result.get("score_details") or {}
        value = _number(score.get("score"))
        if value is None:
            continue
        unit = (result.get("metric_config") or {}).get("metric_unit")
        scale = 100.0 if str(unit).lower() in {"proportion", "fraction"} else 1.0
        published = _uncertainty(score, scale)
        if published is None:
            label = f"{model_id} effort={effort or 'any'} {name}"
            _warn_uncertainty(f"eee:{source}", label)
            continue
        margin, semantics = published
        source_details = (
            (document.get("source_metadata") or {}).get("additional_details") or {}
        )
        details = score.get("details") or {}
        date = _date(
            details.get("benchmark_updated")
            or source_details.get("benchmark_updated")
            or source_details.get("cron_run_date")
        )
        if not date:
            log.warning("missing published date eee:%s %s", source, name)
            continue
        rows.append(
            Row(
                model_id,
                aliases.EEE_EVALS[name],
                effort,
                value * scale,
                margin,
                date,
                f"eee:{source}; {semantics}",
            )
        )
    return rows


def openrouter(fetch: Fetch, key: str | None = None) -> list[Row]:
    payload = json.loads(fetch(OPENROUTER_URL, key) if key else fetch(OPENROUTER_URL))
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), list):
        raise ValueError("OpenRouter benchmark response has no data list")
    as_of = _date((payload.get("meta") or {}).get("as_of"))
    rows: list[Row] = []
    for item in payload["data"]:
        if not isinstance(item, dict):
            continue
        slug = item.get("model_permaslug")
        if not isinstance(slug, str):
            continue
        model_id = aliases.resolve("openrouter", slug)
        if model_id is None:
            _warn_unmapped("openrouter", slug)
            continue
        source = item.get("source")
        if source == "openrouter" and item.get("benchmark_type") == "gpqa_diamond":
            value = _number(item.get("accuracy"))
            margin = _number(item.get("accuracy_stddev"))
            date = _date(item.get("last_run_timestamp"))
            if value is None or not date:
                continue
            if margin is None:
                _warn_uncertainty("openrouter", f"{slug} gpqa_diamond")
                continue
            rows.append(
                Row(
                    model_id,
                    "gpqa-diamond",
                    None,
                    value * 100,
                    margin * 100,
                    date,
                    "openrouter; published standard deviation",
                )
            )
            continue
        if source == "artificial-analysis":
            for field, bench in (
                ("intelligence_index", "aa-intelligence-index"),
                ("coding_index", "aa-coding-index"),
                ("agentic_index", "aa-coding-agent-index"),
            ):
                if _number(item.get(field)) is not None:
                    _warn_uncertainty("openrouter", f"{slug} {field}")
            continue
        if source == "design-arena" and item.get("category") in {
            "website",
            "uicomponent",
            "dataviz",
        }:
            if _number(item.get("elo")) is not None:
                _warn_uncertainty("openrouter", f"{slug} design-arena:{item['category']}")
            continue
        if source == "openrouter" and as_of and item.get("benchmark_type"):
            # Unsupported OpenRouter benchmark types are intentionally not part of BENCHMARKS.
            continue
    return rows


def _epoch_identity(model_version: str, explicit_effort: Any = None) -> tuple[str, str | None]:
    effort = _effort(explicit_effort)
    base = model_version
    if "_" in model_version:
        candidate, suffix = model_version.rsplit("_", 1)
        suffix_effort = _effort(suffix)
        if suffix_effort is not None or suffix.lower() in {"unknown", "none"}:
            base = candidate
            if effort is None:
                effort = suffix_effort
    return base, effort


def epoch(fetch: Fetch) -> list[Row]:
    archive = zipfile.ZipFile(io.BytesIO(fetch(EPOCH_URL)))
    configs = (
        (
            "deepswe_external.csv",
            "deepswe",
            "Pass@1",
            "Reasoning effort",
            "95% CI half-width",
            None,
            100.0,
        ),
        (
            "webdev_arena_external.csv",
            "arena-webdev",
            "Arena Score",
            None,
            None,
            ("95% CI Low", "95% CI High"),
            1.0,
        ),
        (
            "cursorbench_external.csv",
            "cursorbench",
            "Score",
            "Reasoning level",
            None,
            None,
            100.0,
        ),
        (
            "frontiercode_external.csv",
            "frontiercode",
            "Main score",
            "Reasoning effort",
            None,
            None,
            100.0,
        ),
    )
    rows: list[Row] = []
    for member, bench, score_field, effort_field, half_field, bounds, scale in configs:
        try:
            stream = io.TextIOWrapper(archive.open(member), encoding="utf-8-sig", newline="")
        except KeyError:
            log.warning("epoch archive missing %s", member)
            continue
        for item in csv.DictReader(stream):
            raw_name = item.get("Model version") or ""
            base, effort = _epoch_identity(raw_name, item.get(effort_field) if effort_field else None)
            model_id = aliases.resolve("epoch", base)
            if model_id is None:
                _warn_unmapped("epoch", base)
                continue
            value = _number(item.get(score_field))
            date = _date(item.get("Release date"))
            if value is None or not date:
                continue
            margin: float | None = None
            note = ""
            if half_field:
                margin = _number(item.get(half_field))
                if margin is not None:
                    margin *= scale
                    note = f"epoch:{member}; published {half_field}"
            elif bounds:
                lower = _number(item.get(bounds[0]))
                upper = _number(item.get(bounds[1]))
                if lower is not None and upper is not None:
                    margin = (upper - lower) / 2
                    note = f"epoch:{member}; published 95% CI bounds"
            if margin is None:
                _warn_uncertainty("epoch", f"{member} {raw_name}")
                continue
            rows.append(Row(model_id, bench, effort, value * scale, margin, date, note))
    return rows


def _parquet_rows(data: bytes) -> list[dict[str, Any]]:
    import pyarrow as pa
    import pyarrow.parquet as pq

    return pq.read_table(pa.BufferReader(data)).to_pylist()


def _arena_row(
    source_name: str,
    item: dict[str, Any],
    bench: str,
    value_field: str,
    lower_field: str,
    upper_field: str,
    scale: float,
) -> Row | None:
    name = item.get("model_name")
    if not isinstance(name, str):
        return None
    model_id = aliases.resolve_arena(name)
    if model_id is None:
        _warn_unmapped("arena", name)
        return None
    value = _number(item.get(value_field))
    lower = _number(item.get(lower_field))
    upper = _number(item.get(upper_field))
    date = _date(item.get("leaderboard_publish_date"))
    if value is None or lower is None or upper is None or not date:
        return None
    if source_name == "agent":
        note = "arena:agent; published score_ci lower/upper interval; score x1000"
    else:
        note = f"arena:{source_name}; published rating lower/upper interval"
    return Row(
        model_id,
        bench,
        aliases.arena_effort(name),
        value * scale,
        (upper - lower) * scale / 2,
        date,
        note,
    )


def arena(fetch: Fetch) -> list[Row]:
    rows: list[Row] = []
    webdev = _parquet_rows(fetch(ARENA_URL.format(config="webdev")))
    for item in webdev:
        if item.get("category") != "webdev":
            continue
        row = _arena_row(
            "webdev", item, "arena-webdev", "rating", "rating_lower", "rating_upper", 1.0
        )
        if row is not None:
            rows.append(row)

    text = _parquet_rows(fetch(ARENA_URL.format(config="text_style_control")))
    for item in text:
        bench = aliases.ARENA_CATEGORIES.get(str(item.get("category")))
        if bench is None:
            continue
        row = _arena_row(
            "text_style_control", item, bench, "rating", "rating_lower", "rating_upper", 1.0
        )
        if row is not None:
            rows.append(row)

    agent = _parquet_rows(fetch(ARENA_URL.format(config="agent")))
    for item in agent:
        if item.get("category") != "overall":
            continue
        row = _arena_row(
            "agent", item, "arena-agent", "score", "score_ci_lower", "score_ci_upper", 1000.0
        )
        if row is not None:
            rows.append(row)
    return rows


def modelsdev(fetch: Fetch, catalog_ids: set[str]) -> dict[str, dict[str, Any]]:
    payload = json.loads(fetch(MODELSDEV_URL))
    if not isinstance(payload, dict):
        raise ValueError("models.dev response is not an object")
    reverse: dict[str, str] = {}
    for model_id, source_ids in aliases.MODELSDEV_MODELS.items():
        if source_ids is not None:
            for source_id in source_ids:
                reverse[source_id] = model_id
    result: dict[str, dict[str, Any]] = {}
    for provider in payload.values():
        if not isinstance(provider, dict) or not isinstance(provider.get("models"), dict):
            continue
        for source_id, model in provider["models"].items():
            if source_id in aliases.KNOWN_UNMAPPED["modelsdev"]:
                _warn_unmapped("modelsdev", source_id)
                continue
            model_id = reverse.get(source_id)
            if model_id is None or model_id not in catalog_ids or model_id in result:
                continue
            if not isinstance(model, dict):
                continue
            cost = model.get("cost") or {}
            input_cost = _number(cost.get("input"))
            output_cost = _number(cost.get("output"))
            modalities = model.get("modalities")
            inputs = modalities.get("input") if isinstance(modalities, dict) else None
            if (
                input_cost is None
                or output_cost is None
                or input_cost < 0
                or output_cost < 0
                or not isinstance(inputs, list)
            ):
                log.warning("incomplete modelsdev capabilities %s %s", model_id, source_id)
                continue
            capability: dict[str, Any] = {
                "vision": "image" in inputs,
                "cost": {"input": input_cost, "output": output_cost},
            }
            limit = model.get("limit")
            context_window = limit.get("context") if isinstance(limit, dict) else None
            if isinstance(context_window, int) and not isinstance(context_window, bool) and context_window > 0:
                capability["context_window"] = context_window
            result[model_id] = capability
    return result


def _rsc_arrays(data: bytes, key: str) -> list[list[Any]]:
    text = data.decode("utf-8", "replace")
    arrays: list[list[Any]] = []
    decoder = json.JSONDecoder()
    for match in re.finditer(r"self\.__next_f\.push\((.*?)\)</script>", text, re.DOTALL):
        try:
            envelope = json.loads(match.group(1))
        except json.JSONDecodeError:
            continue
        if len(envelope) < 2 or not isinstance(envelope[1], str):
            continue
        payload = envelope[1]
        marker = f'"{key}":'
        start = 0
        while True:
            at = payload.find(marker, start)
            if at < 0:
                break
            try:
                value, consumed = decoder.raw_decode(payload[at + len(marker) :])
            except json.JSONDecodeError:
                start = at + len(marker)
                continue
            if isinstance(value, list):
                arrays.append(value)
            start = at + len(marker) + consumed
    return arrays


def _scale_rows(fetch: Fetch, bench: str) -> list[Row]:
    data = fetch(SCALE_URLS[bench])
    entries: list[dict[str, Any]] = []
    if bench == "swe-bench-pro-v2":
        variants = [item for array in _rsc_arrays(data, "variants") for item in array]
        full = next((item for item in variants if isinstance(item, dict) and item.get("key") == "full"), None)
        if isinstance(full, dict):
            entries = [item for item in full.get("entries") or [] if isinstance(item, dict)]
    else:
        entries = [item for array in _rsc_arrays(data, "entries") for item in array if isinstance(item, dict)]
    rows: list[Row] = []
    for item in entries:
        name = item.get("model")
        if not isinstance(name, str):
            continue
        model_id = aliases.resolve("scale", name)
        if model_id is None:
            _warn_unmapped("scale", name)
            continue
        value = _number(item.get("score"))
        margin = _number(item.get("confidenceInterval_upper"))
        date = _date(item.get("createdAt"))
        if value is None or margin is None or not date:
            continue
        rows.append(
            Row(
                model_id,
                bench,
                aliases.arena_effort(name),
                value,
                margin,
                date,
                "scale; reported confidenceInterval_upper",
            )
        )
    return rows


def _tbench_rows(fetch: Fetch) -> list[Row]:
    entries = [
        item
        for array in _rsc_arrays(fetch(TBENCH_URL), "rows")
        for item in array
        if isinstance(item, dict)
    ]
    rows: list[Row] = []
    for item in entries:
        metadata = item.get("metadata") or {}
        name = (metadata.get("model_display") or {}).get("label")
        if not isinstance(name, str):
            continue
        model_id = aliases.resolve("tbench", name)
        if model_id is None:
            _warn_unmapped("tbench", name)
            continue
        metrics = item.get("metrics") or {}
        value = _number(metrics.get("accuracy"))
        margin = _number(metrics.get("accuracy_ci95_half_width"))
        date = _date(metadata.get("date"))
        if value is None or margin is None or not date:
            continue
        rows.append(
            Row(
                model_id,
                "terminal-bench-4",
                _effort(metadata.get("reasoning_effort")),
                value,
                margin,
                date,
                "tbench; published 95% CI half-width",
            )
        )
    return rows


def fallback(fetch: Fetch, present_benchmarks: set[str]) -> list[Row]:
    missing = REQUIRED_FALLBACKS - present_benchmarks
    rows: list[Row] = []
    for bench, url in (("terminal-bench-4", TBENCH_URL), *SCALE_URLS.items()):
        if bench not in missing:
            continue
        try:
            rows.extend(_tbench_rows(fetch) if bench == "terminal-bench-4" else _scale_rows(fetch, bench))
        except Exception as exc:  # one page outage must not drop the other pages
            log.warning("source failed: fallback %s: %s: %s", bench, url, exc)
    return rows
