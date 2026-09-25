"""Command line updater for the auto-router benchmark table."""

from __future__ import annotations

import argparse
import json
import logging
import os
from pathlib import Path
import time
from typing import Any, Callable
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit, urlunsplit
from urllib.request import Request, build_opener, HTTPRedirectHandler

import yaml

try:
    from . import merge, sources
except ImportError:  # pragma: no cover - supports ``cd updater`` invocation
    import merge  # type: ignore
    import sources  # type: ignore
LOG = logging.getLogger("updater")
USER_AGENT = "cpa-auto-router-updater/0.1"
OPENROUTER_ORIGIN = ("https", "openrouter.ai", 443)
_AUTHORIZED_ORIGINS: set[tuple[str, str, int | None]] = set()


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise HTTPError(req.full_url, code, "credentialed redirect refused", headers, fp)


class _PublicRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        redirected = super().redirect_request(req, fp, code, msg, headers, newurl)
        if redirected is not None:
            redirected.remove_header("Authorization")
            redirected.remove_header("Cookie")
        return redirected


def _opener(authenticated: bool):
    return build_opener(_NoRedirect if authenticated else _PublicRedirect)


def _origin(url: str) -> tuple[str, str, int | None]:
    parsed = urlsplit(url)
    scheme = parsed.scheme.lower()
    host = (parsed.hostname or "").lower()
    port = parsed.port
    if port is None:
        port = 443 if scheme == "https" else 80
    return scheme, host, port


def _auth_allowed(url: str) -> bool:
    return _origin(url) in _AUTHORIZED_ORIGINS or _origin(url) == OPENROUTER_ORIGIN


def fetch(url: str, key: str | None = None) -> bytes:
    """Fetch bytes with bounded retries and origin-scoped optional auth."""
    authenticated = bool(key and _auth_allowed(url))
    opener = _opener(authenticated)
    last: Exception | None = None
    for attempt in range(3):
        headers = {"User-Agent": USER_AGENT, "Accept": "*/*"}
        if authenticated:
            headers["Authorization"] = f"Bearer {key}"
        request = Request(url, headers=headers)
        try:
            with opener.open(request, timeout=60) as response:
                return response.read()
        except HTTPError as exc:
            last = exc
            if (exc.code != 429 and not 500 <= exc.code < 600) or attempt == 2:
                raise
        except (URLError, OSError) as exc:
            last = exc
            if attempt == 2:
                raise
        time.sleep(0.5 * (2**attempt))
    assert last is not None
    raise last


def _catalog_url(value: str) -> str:
    parsed = urlsplit(value)
    path = parsed.path.rstrip("/")
    if not path.endswith("/v1/models"):
        path += "/v1/models"
    return urlunsplit((parsed.scheme, parsed.netloc, path, parsed.query, parsed.fragment))


def _read_secret(env_name: str | None, file_name: str | None) -> str | None:
    if file_name:
        return Path(file_name).read_text(encoding="utf-8").strip() or None
    if env_name:
        return os.environ.get(env_name)
    return None


def _catalog_ids(payload: Any) -> set[str]:
    entries = payload.get("data", []) if isinstance(payload, dict) else payload
    if not isinstance(entries, list):
        raise ValueError("catalog response has no data list")
    result: set[str] = set()
    for entry in entries:
        if isinstance(entry, dict) and isinstance(entry.get("id"), str):
            result.add(entry["id"])
    if not result:
        raise ValueError("catalog response contains no model ids")
    return result


def _load_tiers(path: str | os.PathLike[str]) -> dict[str, str]:
    raw = yaml.safe_load(Path(path).read_text(encoding="utf-8")) or {}
    if not isinstance(raw, dict):
        raise ValueError("tiers file must be a mapping")
    result: dict[str, str] = {}
    for tier, models in raw.items():
        if not isinstance(models, list):
            continue
        for model_id in models:
            if isinstance(model_id, str):
                result[model_id] = str(tier)
    return result


def _score_map(table: merge.Table | None) -> dict[tuple[str, str, str | None], merge.Score]:
    if table is None:
        return {}
    return {
        (model_id, benchmark_id, score.effort): score
        for model_id, model in table.models.items()
        for benchmark_id, scores in model.scores.items()
        for score in scores
    }


def _score_equal(a: merge.Score, b: merge.Score) -> bool:
    return (
        a.effort == b.effort
        and a.value == b.value
        and a.margin == b.margin
        and a.date == b.date
        and a.note == b.note
    )


def _row_key(row: Any) -> tuple[str, str, str | None]:
    effort = getattr(row, "effort", None) or None
    return str(getattr(row, "model", "")), str(getattr(row, "bench", "")), effort


def _score_count(table: merge.Table | None) -> int:
    return len(_score_map(table))


def _source_rows(
    selected: set[str], catalog_ids: set[str], openrouter_key: str | None
) -> tuple[list[Any], dict[str, int], int]:
    rows: list[Any] = []
    coverage: dict[str, int] = {}
    failures = 0
    def openrouter_fetch(
        url: str, supplied_key: str | None = None, **kwargs: Any
    ) -> bytes:
        key = supplied_key if supplied_key is not None else kwargs.get("key")
        return fetch(url, key if key is not None else openrouter_key)

    readers: dict[str, Callable[[], list[Any]]] = {
        "eee": lambda: sources.eee(fetch, catalog_ids),
        "openrouter": lambda: sources.openrouter(openrouter_fetch, openrouter_key),
        "epoch": lambda: sources.epoch(fetch),
        "arena": lambda: sources.arena(fetch),
    }
    for name in ("eee", "openrouter", "epoch", "arena"):
        if name not in selected:
            continue
        try:
            source_rows = readers[name]()
        except Exception as exc:  # source outages must not erase old data
            failures += 1
            LOG.warning("source failed: %s: %s", name, exc)
            coverage[name] = 0
            continue
        rows.extend(source_rows)
        coverage[name] = len(source_rows)
        LOG.info("source %s rows=%d", name, len(source_rows))

    if selected == set(readers) and hasattr(sources, "fallback"):
        try:
            fallback_rows = sources.fallback(fetch, {getattr(row, "bench", "") for row in rows})
        except Exception as exc:
            failures += 1
            LOG.warning("source failed: fallback: %s", exc)
            coverage["fallback"] = 0
        else:
            rows.extend(fallback_rows)
            coverage["fallback"] = len(fallback_rows)
            LOG.info("source fallback rows=%d", len(fallback_rows))
    return rows, coverage, failures


def _row_matches(row: Any, score: merge.Score) -> bool:
    return (
        (getattr(row, "effort", None) or None) == score.effort
        and float(getattr(row, "value")) == score.value
        and float(getattr(row, "margin")) == score.margin
        and str(getattr(row, "date")) == score.date
        and str(getattr(row, "note", "") or "") == score.note
    )


def _summary(
    old: merge.Table | None,
    new: merge.Table,
    rows: list[Any],
    coverage: dict[str, int],
    catalog_ids: set[str],
    tiers: dict[str, str],
) -> str:
    old_scores = _score_map(old)
    new_scores = _score_map(new)
    updated = sum(
        1
        for key, score in new_scores.items()
        if key not in old_scores or not _score_equal(old_scores[key], score)
    )
    kept = sum(
        1
        for key, score in new_scores.items()
        if key in old_scores and _score_equal(old_scores[key], score)
    )
    consumed = 0
    for row in rows:
        score = new_scores.get(_row_key(row))
        if score is None or not _row_matches(row, score):
            continue
        previous = old_scores.get(_row_key(row))
        if previous is None or str(getattr(row, "date")) >= previous.date:
            consumed += 1
    dropped = len(set(old_scores) - set(new_scores)) + max(0, len(rows) - consumed)
    unmapped = int(getattr(sources, "UNMAPPED_COUNT", 0) or 0)
    untiered = sorted(catalog_ids - set(tiers))
    source_text = " ".join(f"{name}={count}" for name, count in sorted(coverage.items()))
    return (
        f"updated={updated} kept={kept} dropped={dropped} unmapped={unmapped} "
        f"untiered={untiered} coverage={source_text}"
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="python -m updater")
    parser.add_argument("--catalog", required=True)
    parser.add_argument("--catalog-key-env")
    parser.add_argument("--catalog-key-file")
    parser.add_argument("--tiers", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--openrouter-key-env")
    parser.add_argument("--only", help="comma-separated benchmark sources")
    return parser


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    args = build_parser().parse_args(argv)
    catalog_url = _catalog_url(args.catalog)
    _AUTHORIZED_ORIGINS.clear()
    _AUTHORIZED_ORIGINS.add(_origin(catalog_url))

    catalog_key = _read_secret(args.catalog_key_env, args.catalog_key_file)
    try:
        catalog_payload = json.loads(fetch(catalog_url, catalog_key))
        catalog_ids = _catalog_ids(catalog_payload)
    except Exception as exc:
        LOG.error("catalog unavailable: %s", exc)
        return 2

    if hasattr(sources, "UNMAPPED_COUNT"):
        sources.UNMAPPED_COUNT = 0
    try:
        tiers = _load_tiers(args.tiers)
        old_path = Path(args.out)
        old = merge.load(old_path) if old_path.exists() else None
    except (OSError, merge.ValidationError, ValueError) as exc:
        LOG.error("cannot load updater inputs: %s", exc)
        return 1

    selected = {
        name.strip()
        for name in (args.only.split(",") if args.only else ("eee", "openrouter", "epoch", "arena"))
        if name.strip()
    }
    unknown = selected - {"eee", "openrouter", "epoch", "arena"}
    if unknown:
        LOG.error("unknown source(s): %s", ", ".join(sorted(unknown)))
        return 1

    openrouter_key = _read_secret(args.openrouter_key_env, None)
    rows, coverage, _failures = _source_rows(selected, catalog_ids, openrouter_key)
    try:
        caps = sources.modelsdev(fetch, catalog_ids)
    except Exception as exc:
        LOG.warning("source failed: modelsdev: %s", exc)
        caps = {}

    try:
        table = merge.merge(old, rows, catalog_ids, tiers, caps)
        merge.write_atomic(args.out, table)
    except (merge.ValidationError, OSError, ValueError) as exc:
        LOG.error("table validation/publication failed: %s", exc)
        return 1

    LOG.info("%s", _summary(old, table, rows, coverage, catalog_ids, tiers))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
