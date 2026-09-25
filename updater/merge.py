"""Typed table merge, validation, and atomic publication for the updater."""

from __future__ import annotations

from dataclasses import asdict, dataclass, field, is_dataclass
from datetime import date, datetime, timezone
import copy
import logging
import math
from pathlib import Path
import os
import tempfile
from typing import Any, Iterable, Mapping

import yaml

try:
    from . import aliases
except ImportError:  # pragma: no cover - supports ``cd updater`` test runs
    import aliases  # type: ignore


log = logging.getLogger(__name__)

VALID_TIERS = {"flash", "mid", "top"}
VALID_EFFORTS = {None, "", "low", "medium", "high", "xhigh", "max"}
EXCLUDED_PREFIXES = ("gpt-image-", "abliterated-")
EXCLUDED_IDS = {"codex-auto-review"}


@dataclass(frozen=True)
class Score:
    effort: str | None
    value: float
    margin: float
    date: str
    note: str = ""


@dataclass(frozen=True)
class Cost:
    input: float
    output: float


@dataclass
class Model:
    tier: str
    vision: bool
    cost: Cost
    scores: dict[str, list[Score]] = field(default_factory=dict)
    context_window: int = 0

@dataclass
class Table:
    generated_at: str = ""
    benchmarks: dict[str, Any] = field(default_factory=dict)
    models: dict[str, Model] = field(default_factory=dict)


class ValidationError(ValueError):
    """The table does not satisfy the plugin loader's schema rules."""


def _excluded(model_id: str) -> bool:
    return model_id in EXCLUDED_IDS or model_id.startswith(EXCLUDED_PREFIXES)


def _normalise_effort(value: Any) -> str | None:
    return None if value in (None, "") else str(value)


def _plain(value: Any) -> Any:
    """Convert our dataclasses to the public YAML shape, never internals."""
    if isinstance(value, Table):
        return {
            "generated_at": value.generated_at,
            "benchmarks": _plain(value.benchmarks),
            "models": _plain(value.models),
        }
    if isinstance(value, Model):
        return {
            "tier": value.tier,
            "vision": value.vision,
            "cost": _plain(value.cost),
            "scores": _plain(value.scores),
            "context_window": value.context_window,
        }
    if isinstance(value, Cost):
        return {"input": value.input, "output": value.output}
    if isinstance(value, Score):
        result: dict[str, Any] = {
            "effort": value.effort,
            "value": value.value,
            "margin": value.margin,
            "date": value.date,
        }
        if value.note:
            result["note"] = value.note
        return result
    if is_dataclass(value):
        return _plain(asdict(value))
    if isinstance(value, Mapping):
        return {str(k): _plain(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [_plain(v) for v in value]
    return value


def _raw(value: Any) -> dict[str, Any]:
    if isinstance(value, Table):
        return _plain(value)
    if is_dataclass(value):
        return _plain(value)
    if not isinstance(value, Mapping):
        raise ValidationError("table must be a mapping or Table")
    return copy.deepcopy(dict(value))

def _number(value: Any, field_name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValidationError(f"{field_name} must be numeric")
    result = float(value)
    if not math.isfinite(result):
        raise ValidationError(f"{field_name} must be finite")
    return result


def validate(value: Any) -> None:
    """Validate a mapping or typed Table using the Go loader's rules."""
    raw = _raw(value)
    models = raw.get("models")
    if not isinstance(models, Mapping) or not models:
        raise ValidationError("no models")
    benchmarks = raw.get("benchmarks")
    if not isinstance(benchmarks, Mapping):
        raise ValidationError("benchmarks must be a mapping")

    for model_id, model in models.items():
        if not isinstance(model, Mapping):
            raise ValidationError(f"model {model_id}: must be a mapping")
        context_window = model.get("context_window", 0)
        if (
            isinstance(context_window, bool)
            or not isinstance(context_window, int)
            or context_window < 0
        ):
            raise ValidationError(
                f"model {model_id}: context_window must be a nonnegative integer"
            )
        tier = model.get("tier")
        if tier not in VALID_TIERS:
            raise ValidationError(f"model {model_id}: invalid tier {tier!r}")
        scores = model.get("scores", {})
        if not isinstance(scores, Mapping):
            raise ValidationError(f"model {model_id}: scores must be a mapping")
        for benchmark_id, entries in scores.items():
            if benchmark_id not in benchmarks:
                raise ValidationError(
                    f"model {model_id}: unknown benchmark {benchmark_id!r}"
                )
            if not isinstance(entries, list):
                raise ValidationError(
                    f"model {model_id}: scores for {benchmark_id} must be a list"
                )
            for entry in entries:
                if not isinstance(entry, Mapping):
                    raise ValidationError(
                        f"model {model_id}: score in {benchmark_id} must be a mapping"
                    )
                effort = _normalise_effort(entry.get("effort"))
                if effort not in VALID_EFFORTS:
                    raise ValidationError(
                        f"model {model_id}: invalid effort {entry.get('effort')!r}"
                    )
                score_date = entry.get("date")
                if not isinstance(score_date, str) or not score_date:
                    raise ValidationError(
                        f"model {model_id}: score in {benchmark_id} has no date"
                    )
                try:
                    date.fromisoformat(score_date)
                except ValueError as exc:
                    raise ValidationError(
                        f"model {model_id}: score in {benchmark_id} has invalid date {score_date!r}"
                    ) from exc
                if "margin" not in entry or entry.get("margin") is None:
                    raise ValidationError(
                        f"model {model_id}: score in {benchmark_id} has no margin"
                    )
                margin = _number(entry["margin"], "margin")
                if margin < 0:
                    raise ValidationError(
                        f"model {model_id}: score in {benchmark_id} has negative margin"
                    )
                _number(entry.get("value"), "value")


def _benchmark_map(raw: Mapping[str, Any]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for benchmark_id, info in (raw.get("benchmarks") or {}).items():
        if isinstance(info, Mapping):
            result[str(benchmark_id)] = {
                "source": info.get("source", ""),
                "unit": info.get("unit", ""),
            }
        else:
            result[str(benchmark_id)] = info
    return result


def _score(entry: Mapping[str, Any]) -> Score:
    return Score(
        effort=_normalise_effort(entry.get("effort")),
        value=float(entry["value"]),
        margin=float(entry["margin"]),
        date=str(entry["date"]),
        note=str(entry.get("note", "") or ""),
    )


def _model(model_id: str, value: Mapping[str, Any]) -> Model:
    cost = value.get("cost") or {}
    return Model(
        tier=str(value.get("tier", "")),
        vision=bool(value.get("vision", False)),
        cost=Cost(float(cost.get("input", 0)), float(cost.get("output", 0))),
        scores={
            str(benchmark_id): [_score(entry) for entry in entries]
            for benchmark_id, entries in (value.get("scores") or {}).items()
        },
        context_window=int(value.get("context_window", 0)),
    )


def _table(raw: Mapping[str, Any]) -> Table:
    return Table(
        generated_at=str(raw.get("generated_at", "") or ""),
        benchmarks=_benchmark_map(raw),
        models={
            str(model_id): _model(str(model_id), value)
            for model_id, value in (raw.get("models") or {}).items()
        },
    )


def load(path: str | os.PathLike[str]) -> Table:
    """Load and validate one YAML table."""
    source = Path(path)
    try:
        raw = yaml.safe_load(source.read_text()) or {}
    except OSError:
        raise
    except yaml.YAMLError as exc:
        raise ValidationError(f"{source}: invalid YAML: {exc}") from exc
    validate(raw)
    return _table(raw)


def _capability(
    caps: Mapping[str, Any], model_id: str
) -> tuple[bool, Cost, int | None] | None:
    value = caps.get(model_id)
    if not isinstance(value, Mapping):
        return None
    vision = value.get("vision")
    cost = value.get("cost")
    if not isinstance(vision, bool) or not isinstance(cost, Mapping):
        return None
    input_cost = cost.get("input")
    output_cost = cost.get("output")
    if (
        isinstance(input_cost, bool)
        or not isinstance(input_cost, (int, float))
        or isinstance(output_cost, bool)
        or not isinstance(output_cost, (int, float))
    ):
        return None
    context_window = value.get("context_window")
    if (
        isinstance(context_window, bool)
        or not isinstance(context_window, int)
        or context_window <= 0
    ):
        context_window = None
    return vision, Cost(float(input_cost), float(output_cost)), context_window


def _declared_benchmarks(old: Table | None) -> dict[str, Any]:
    declared = getattr(aliases, "BENCHMARKS", {}) or {}
    result = _benchmark_map({"benchmarks": declared})
    if old is not None:
        for benchmark_id, info in old.benchmarks.items():
            result.setdefault(benchmark_id, _plain(info))
    return result


def _clone_model(model: Model) -> Model:
    return Model(
        tier=model.tier,
        vision=model.vision,
        cost=Cost(model.cost.input, model.cost.output),
        scores={benchmark: list(scores) for benchmark, scores in model.scores.items()},
        context_window=model.context_window,
    )


def merge(
    old: Table | None,
    rows: Iterable[Any],
    catalog_ids: set[str] | Iterable[str],
    tiers: Mapping[str, str],
    caps: Mapping[str, Any],
) -> Table:
    """Merge fresh source rows into the last valid table without regressions."""
    catalog = {str(model_id) for model_id in catalog_ids}
    untiered = sorted(model_id for model_id in catalog if model_id not in tiers)
    for model_id in untiered:
        log.warning("catalog model %s has no tier", model_id)

    eligible = {
        model_id
        for model_id in catalog
        if model_id in tiers and not _excluded(model_id)
    }
    models: dict[str, Model] = {}
    if old is not None:
        for model_id in eligible:
            previous = old.models.get(model_id)
            if previous is not None:
                model = _clone_model(previous)
                model.tier = tiers[model_id]
                capability = _capability(caps, model_id)
                if capability is not None:
                    model.vision, model.cost, context_window = capability
                    if context_window is not None:
                        model.context_window = context_window
                models[model_id] = model

    for model_id in sorted(eligible):
        if model_id in models:
            continue
        capability = _capability(caps, model_id)
        if capability is None:
            log.warning(
                "skipping fresh model %s: missing complete capability/cost data",
                model_id,
            )
            continue
        vision, cost, context_window = capability
        models[model_id] = Model(
            tier=tiers[model_id],
            vision=vision,
            cost=cost,
            scores={},
            context_window=context_window or 0,
        )

    benchmark_info = _declared_benchmarks(old)
    accepted_rows = 0
    for row in rows:
        model_id = str(getattr(row, "model", ""))
        benchmark_id = str(getattr(row, "bench", ""))
        if model_id not in models:
            continue
        if benchmark_id not in benchmark_info:
            log.warning("dropping row with unknown benchmark %s", benchmark_id)
            continue
        score = Score(
            effort=_normalise_effort(getattr(row, "effort", None)),
            value=float(getattr(row, "value")),
            margin=float(getattr(row, "margin")),
            date=str(getattr(row, "date")),
            note=str(getattr(row, "note", "") or ""),
        )
        scores = models[model_id].scores.setdefault(benchmark_id, [])
        existing_index = next(
            (
                index
                for index, existing in enumerate(scores)
                if _normalise_effort(existing.effort) == score.effort
            ),
            None,
        )
        if existing_index is None:
            scores.append(score)
            accepted_rows += 1
        elif score.date >= scores[existing_index].date:
            scores[existing_index] = score
            accepted_rows += 1

    generated_at = datetime.now(timezone.utc).replace(microsecond=0).isoformat()
    return Table(
        generated_at=generated_at,
        benchmarks=benchmark_info,
        models=models,
    )


def write_atomic(path: str | os.PathLike[str], value: Any) -> None:
    """Validate, serialize, and atomically replace *path*."""
    raw = _raw(value)
    validate(raw)
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{target.name}.", dir=target.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            yaml.safe_dump(
                _plain(raw),
                handle,
                sort_keys=False,
                default_flow_style=False,
                allow_unicode=True,
            )
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, target)
    except Exception:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise
