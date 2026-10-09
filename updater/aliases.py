from __future__ import annotations

import re
from typing import Final


CATALOG_IDS: Final[tuple[str, ...]] = (
    "glm-5.3-flash",
    "deepseek-v4-flash",
    "qwen-3.8-flash-next",
    "mimo-v2.6-flash",
    "gpt-5.6-luna",
    "gpt-6-luna",
    "gpt-5.6-terra",
    "glm-5.3",
    "kimi-k3",
    "claude-sonnet-5",
    "deepseek-v4-pro",
    "qwen-3.8-max",
    "minimax-m3",
    "gpt-6-astra",
    "gpt-6-sol",
    "gpt-5.6-sol",
    "claude-opus-5",
    "claude-opus-5-5",
    "claude-fable-5",
    "claude-fable-5-1",
    "gpt-6.1-sol",
    "claude-sonnet-5-5",
    "claude-haiku-5-5",
    "mimo-v2.6-pro",
    "qwen-3.7-flash",
)

BENCHMARKS: Final[dict[str, dict[str, str]]] = {
    "arena-webdev": {
        "source": "hf:lmarena-ai/leaderboard-dataset/webdev",
        "unit": "elo",
    },
    "arena-coding": {
        "source": "hf:lmarena-ai/leaderboard-dataset/text_style_control#coding",
        "unit": "elo",
    },
    "arena-math": {
        "source": "hf:lmarena-ai/leaderboard-dataset/text_style_control#math",
        "unit": "elo",
    },
    "arena-creative-writing": {
        "source": "hf:lmarena-ai/leaderboard-dataset/text_style_control#creative_writing",
        "unit": "elo",
    },
    "arena-instruction-following": {
        "source": "hf:lmarena-ai/leaderboard-dataset/text_style_control#instruction_following",
        "unit": "elo",
    },
    "arena-hard-prompts": {
        "source": "hf:lmarena-ai/leaderboard-dataset/text_style_control#hard_prompts",
        "unit": "elo",
    },
    "arena-overall": {
        "source": "hf:lmarena-ai/leaderboard-dataset/text_style_control#overall",
        "unit": "elo",
    },
    "arena-agent": {
        "source": "hf:lmarena-ai/leaderboard-dataset/agent",
        "unit": "agent-score",
    },
    "terminal-bench-4": {
        "source": "https://www.tbench.ai/",
        "unit": "pct",
    },
    "terminal-bench-4-software": {
        "source": "eee:vals-ai/terminal-bench-4.software",
        "unit": "pct",
    },
    "terminal-bench-4-security": {
        "source": "eee:vals-ai/terminal-bench-4.security",
        "unit": "pct",
    },
    "swe-bench-pro-v2": {
        "source": "https://labs.scale.com/leaderboard/swe_bench_pro_public_v2",
        "unit": "pct",
    },
    "swe-atlas-qna": {
        "source": "https://labs.scale.com/leaderboard/sweatlas-qna",
        "unit": "pct",
    },
    "swe-atlas-test-writing": {
        "source": "https://labs.scale.com/leaderboard/sweatlas-tw",
        "unit": "pct",
    },
    "swe-atlas-refactoring": {
        "source": "https://labs.scale.com/leaderboard/sweatlas-refactoring",
        "unit": "pct",
    },
    "deepswe": {
        "source": "https://epoch.ai/data/benchmark_data.zip#deepswe_external.csv",
        "unit": "pct",
    },
    "gpqa-diamond": {
        "source": "https://openrouter.ai/api/v1/benchmarks#gpqa_diamond",
        "unit": "pct",
    },
    "aa-intelligence-index": {
        "source": "openrouter:artificial-analysis#intelligence_index",
        "unit": "index",
    },
    "aa-coding-index": {
        "source": "openrouter:artificial-analysis#coding_index",
        "unit": "index",
    },
    "aa-coding-agent-index": {
        "source": "openrouter:artificial-analysis#agentic_index",
        "unit": "index",
    },
    "gdpval-aa": {
        "source": "eee:artificial-analysis-llms#gdpval",
        "unit": "pct",
    },
    "cursorbench": {
        "source": "https://epoch.ai/data/benchmark_data.zip#cursorbench_external.csv",
        "unit": "pct",
    },
    "frontiercode": {
        "source": "https://epoch.ai/data/benchmark_data.zip#frontiercode_external.csv",
        "unit": "pct",
    },
}

EEE_MODELS: Final[dict[str, tuple[tuple[str, str, str], ...] | None]] = {
    "glm-5.3-flash": (("vals-ai", "zai", "glm-5.3-flash"),),
    "deepseek-v4-flash": (
        ("vals-ai", "deepseek", "deepseek-v4-flash"),
        ("artificial-analysis-llms", "deepseek", "deepseek-v4-flash"),
    ),
    "qwen-3.8-flash-next": None,
    "mimo-v2.6-flash": (
        ("vals-ai", "xiaomi", "mimo-v2.6-flash"),
        ("llm-stats", "xiaomi", "mimo-v2.6-flash"),
    ),
    "gpt-5.6-luna": (
        ("vals-ai", "openai", "gpt-5.6-luna"),
        ("llm-stats", "openai", "gpt-5.6-luna"),
    ),
    "gpt-6-luna": (
        ("vals-ai", "openai", "gpt-6-luna"),
        ("llm-stats", "openai", "gpt-6-luna"),
        ("artificial-analysis-llms", "openai", "gpt-6-luna"),
    ),
    "gpt-5.6-terra": (
        ("vals-ai", "openai", "gpt-5.6-terra"),
        ("llm-stats", "openai", "gpt-5.6-terra"),
    ),
    "glm-5.3": (("vals-ai", "zai", "glm-5.3"),),
    "kimi-k3": (
        ("vals-ai", "moonshotai", "kimi-k3"),
        ("llm-stats", "moonshotai", "kimi-k3"),
    ),
    "claude-sonnet-5": (
        ("vals-ai", "anthropic", "claude-sonnet-5"),
        ("llm-stats", "anthropic", "claude-sonnet-5"),
        ("artificial-analysis-llms", "anthropic", "claude-sonnet-5"),
    ),
    "deepseek-v4-pro": (
        ("vals-ai", "deepseek", "deepseek-v4-pro"),
        ("artificial-analysis-llms", "deepseek", "deepseek-v4-pro"),
    ),
    "qwen-3.8-max": (("vals-ai", "alibaba", "qwen3.8-max"),),
    "minimax-m3": (("vals-ai", "minimax", "MiniMax-M3"),),
    "gpt-6-astra": (
        ("vals-ai", "openai", "gpt-6-astra"),
        ("llm-stats", "openai", "gpt-6-astra"),
        ("artificial-analysis-llms", "openai", "gpt-6-astra"),
    ),
    "gpt-6-sol": (
        ("vals-ai", "openai", "gpt-6-sol"),
        ("llm-stats", "openai", "gpt-6-sol"),
        ("artificial-analysis-llms", "openai", "gpt-6-sol"),
    ),
    "gpt-5.6-sol": (
        ("vals-ai", "openai", "gpt-5.6-sol"),
        ("llm-stats", "openai", "gpt-5.6-sol"),
    ),
    "claude-opus-5": (
        ("vals-ai", "anthropic", "claude-opus-5"),
        ("llm-stats", "anthropic", "claude-opus-5"),
        ("artificial-analysis-llms", "anthropic", "claude-opus-5"),
    ),
    "claude-opus-5-5": (
        ("vals-ai", "anthropic", "claude-opus-5-5"),
        ("llm-stats", "anthropic", "claude-opus-5-5"),
        ("artificial-analysis-llms", "anthropic", "claude-opus-5-5"),
    ),
    "claude-fable-5": (
        ("vals-ai", "anthropic", "claude-fable-5"),
        ("llm-stats", "anthropic", "claude-fable-5"),
        ("artificial-analysis-llms", "anthropic", "claude-fable-5"),
    ),
    "claude-fable-5-1": (
        ("vals-ai", "anthropic", "claude-fable-5-1"),
        ("llm-stats", "anthropic", "claude-fable-5-1"),
        ("artificial-analysis-llms", "anthropic", "claude-fable-5-1"),
    ),
    "gpt-6.1-sol": (
        ("vals-ai", "openai", "gpt-6.1-sol"),
        ("llm-stats", "openai", "gpt-6.1-sol"),
        ("artificial-analysis-llms", "openai", "gpt-6-1-sol"),
    ),
    "claude-sonnet-5-5": (
        ("vals-ai", "anthropic", "claude-sonnet-5-5"),
        ("llm-stats", "anthropic", "claude-sonnet-5-5"),
        ("artificial-analysis-llms", "anthropic", "claude-sonnet-5-5"),
    ),
    "claude-haiku-5-5": (
        ("llm-stats", "anthropic", "claude-haiku-5-5"),
        ("artificial-analysis-llms", "anthropic", "claude-haiku-5-5"),
    ),
    "mimo-v2.6-pro": (
        ("vals-ai", "xiaomi", "mimo-v2.6-pro"),
        ("llm-stats", "xiaomi", "mimo-v2.6-pro"),
        ("artificial-analysis-llms", "xiaomi", "mimo-v2-6-pro"),
    ),
    "qwen-3.7-flash": None,
}

OPENROUTER_MODELS: Final[dict[str, tuple[str, ...] | None]] = {
    "glm-5.3-flash": ("z-ai/glm-5.3-flash-20260826",),
    "deepseek-v4-flash": (
        "deepseek/deepseek-v4-flash-20260423",
        "deepseek/deepseek-v4-flash-20260731",
    ),
    "qwen-3.8-flash-next": None,
    "mimo-v2.6-flash": ("xiaomi/mimo-v2.6-flash-20260921",),
    "gpt-5.6-luna": ("openai/gpt-5.6-luna-20260709",),
    "gpt-6-luna": ("openai/gpt-6-luna-20260922",),
    "gpt-5.6-terra": ("openai/gpt-5.6-terra-20260709",),
    "glm-5.3": ("z-ai/glm-5.3-20260816",),
    "kimi-k3": ("moonshotai/kimi-k3-20260715",),
    "claude-sonnet-5": ("anthropic/claude-sonnet-5-20260630",),
    "deepseek-v4-pro": (
        "deepseek/deepseek-v4-pro-20260423",
        "deepseek/deepseek-v4-pro-20260813",
    ),
    "qwen-3.8-max": (
        "qwen/qwen3.8-max-20260803",
        "qwen/qwen3.8-max-20260902",
    ),
    "minimax-m3": ("minimax/minimax-m3-20260531",),
    "gpt-6-astra": ("openai/gpt-6-astra-20260903",),
    "gpt-6-sol": ("openai/gpt-6-sol-20260922",),
    "gpt-5.6-sol": ("openai/gpt-5.6-sol-20260709",),
    "claude-opus-5": ("anthropic/claude-opus-5-20260723",),
    "claude-opus-5-5": ("anthropic/claude-opus-5.5-20260921",),
    "claude-fable-5": ("anthropic/claude-5-fable-20260609",),
    "claude-fable-5-1": ("anthropic/claude-fable-5.1-20260831",),
    "gpt-6.1-sol": ("openai/gpt-6.1-sol-20260929",),
    "claude-sonnet-5-5": ("anthropic/claude-sonnet-5.5-20260928",),
    "claude-haiku-5-5": ("anthropic/claude-haiku-5.5-20261007",),
    "mimo-v2.6-pro": ("xiaomi/mimo-v2.6-pro-20260921",),
    "qwen-3.7-flash": None,
}

EPOCH_MODELS: Final[dict[str, tuple[str, ...] | None]] = {
    "glm-5.3-flash": ("glm-5.3-flash",),
    "deepseek-v4-flash": ("deepseek-v4-flash", "deepseek-v4-flash-0731"),
    "qwen-3.8-flash-next": None,
    "mimo-v2.6-flash": None,
    "gpt-5.6-luna": ("gpt-5.6-luna",),
    "gpt-6-luna": None,
    "gpt-5.6-terra": ("gpt-5.6-terra",),
    "glm-5.3": ("glm-5.3",),
    "kimi-k3": ("kimi-k3",),
    "claude-sonnet-5": ("claude-sonnet-5",),
    "deepseek-v4-pro": ("deepseek-v4-pro", "deepseek-v4-pro-0813"),
    "qwen-3.8-max": None,
    "minimax-m3": None,
    "gpt-6-astra": ("gpt-6-astra",),
    "gpt-6-sol": None,
    "gpt-5.6-sol": ("gpt-5.6-sol",),
    "claude-opus-5": ("claude-opus-5",),
    "claude-opus-5-5": None,
    "claude-fable-5": ("claude-fable-5",),
    "claude-fable-5-1": ("claude-fable-5-1",),
    "gpt-6.1-sol": ("gpt-6.1-sol",),
    "claude-sonnet-5-5": ("claude-sonnet-5-5",),
    "claude-haiku-5-5": None,
    "mimo-v2.6-pro": ("mimo-v2.6-pro",),
    "qwen-3.7-flash": None,
}

ARENA_MODELS: Final[dict[str, tuple[str, ...] | None]] = {
    "glm-5.3-flash": ("glm-5.3-flash",),
    "deepseek-v4-flash": (
        "deepseek-v4-flash",
        "deepseek-v4-flash-high",
        "deepseek-v4-flash-high-preview",
    ),
    "qwen-3.8-flash-next": ("qwen3.8-flash-next", "Qwen3.8 Flash Next"),
    "mimo-v2.6-flash": None,
    "gpt-5.6-luna": ("gpt-5.6-luna-xhigh", "gpt-5.6-luna-xhigh (codex-harness)"),
    "gpt-6-luna": None,
    "gpt-5.6-terra": ("gpt-5.6-terra-xhigh", "gpt-5.6-terra-xhigh (codex-harness)"),
    "glm-5.3": ("glm-5.3-max",),
    "kimi-k3": ("kimi-k3-max",),
    "claude-sonnet-5": ("claude-sonnet-5-high", "Claude Sonnet 5 (High)"),
    "deepseek-v4-pro": (
        "deepseek-v4-pro",
        "deepseek-v4-pro-high-20260813",
        "deepseek-v4-pro-high-preview",
    ),
    "qwen-3.8-max": (
        "qwen3.8-max",
        "qwen3.8-max-0902",
        "Qwen3.8 Max",
    ),
    "minimax-m3": ("minimax-m3",),
    "gpt-6-astra": ("gpt-6-astra-max", "GPT 6 Astra (Max)"),
    "gpt-6-sol": ("gpt-6-sol-max",),
    "gpt-5.6-sol": ("gpt-5.6-sol-xhigh", "gpt-5.6-sol-xhigh (codex-harness)", "GPT 5.6 Sol (xHigh)"),
    "claude-opus-5": ("claude-opus-5-high", "claude-opus-5-max", "Claude Opus 5 (High)", "Claude Opus 5 (Max)"),
    "claude-opus-5-5": ("claude-opus-5.5-max",),
    "claude-fable-5": ("claude-fable-5", "claude-fable-5-high", "Claude Fable 5 (High)"),
    "claude-fable-5-1": ("claude-fable-5.1-max", "Claude Fable 5.1 (Max)"),
    "gpt-6.1-sol": ("gpt-6.1-sol-max", "GPT 6.1 Sol (Max)"),
    "claude-sonnet-5-5": ("claude-sonnet-5.5-xhigh", "claude-sonnet-5.5-high", "Claude Sonnet 5.5 (Max)"),
    "claude-haiku-5-5": None,
    "mimo-v2.6-pro": ("mimo-v2.6-pro",),
    "qwen-3.7-flash": None,
}

MODELSDEV_MODELS: Final[dict[str, tuple[str, ...] | None]] = {
    "glm-5.3-flash": ("zai-org/GLM-5.3-Flash", "glm-5.3-flash"),
    "deepseek-v4-flash": ("deepseek-ai/DeepSeek-V4-Flash", "deepseek-v4-flash"),
    "qwen-3.8-flash-next": ("qwen/qwen3.8-flash-next", "qwen3.8-flash-next"),
    "mimo-v2.6-flash": (
        "XiaomiMiMo/MiMo-V2.6-Flash",
        "xiaomi/mimo-v2.6-flash",
        "mimo-v2.6-flash",
    ),
    "gpt-5.6-luna": ("gpt-5.6-luna", "openai/gpt-5.6-luna"),
    "gpt-6-luna": ("gpt-6-luna", "openai/gpt-6-luna"),
    "gpt-5.6-terra": ("gpt-5.6-terra", "openai/gpt-5.6-terra"),
    "glm-5.3": ("zai-org/GLM-5.3", "glm-5.3"),
    "kimi-k3": ("moonshotai/Kimi-K3", "kimi-k3"),
    "claude-sonnet-5": ("claude-sonnet-5", "anthropic/claude-sonnet-5"),
    "deepseek-v4-pro": ("deepseek-ai/DeepSeek-V4-Pro", "deepseek-v4-pro"),
    "qwen-3.8-max": ("Qwen/Qwen3.8-Max", "qwen3.8-max"),
    "minimax-m3": ("MiniMaxAI/MiniMax-M3", "MiniMax-M3", "minimax-m3"),
    "gpt-6-astra": ("gpt-6-astra", "openai/gpt-6-astra"),
    "gpt-6-sol": ("gpt-6-sol", "openai/gpt-6-sol"),
    "gpt-5.6-sol": ("gpt-5.6-sol", "openai/gpt-5.6-sol"),
    "claude-opus-5": ("claude-opus-5", "anthropic/claude-opus-5"),
    "claude-opus-5-5": ("claude-opus-5-5", "anthropic/claude-opus-5.5"),
    "claude-fable-5": ("claude-fable-5", "anthropic/claude-fable-5"),
    "claude-fable-5-1": ("claude-fable-5-1", "anthropic/claude-fable-5.1"),
    "gpt-6.1-sol": ("gpt-6.1-sol", "openai/gpt-6.1-sol"),
    "claude-sonnet-5-5": ("claude-sonnet-5-5", "anthropic/claude-sonnet-5.5"),
    "claude-haiku-5-5": ("claude-haiku-5-5", "anthropic/claude-haiku-5.5"),
    "mimo-v2.6-pro": (
        "XiaomiMiMo/MiMo-V2.6-Pro",
        "xiaomi/mimo-v2.6-pro",
        "mimo-v2.6-pro",
    ),
    "qwen-3.7-flash": ("qwen/qwen3.7-flash", "qwen3.7-flash"),
}

SCALE_MODELS: Final[dict[str, tuple[str, ...] | None]] = {
    "glm-5.3-flash": None,
    "deepseek-v4-flash": None,
    "qwen-3.8-flash-next": None,
    "mimo-v2.6-flash": None,
    "gpt-5.6-luna": None,
    "gpt-6-luna": None,
    "gpt-5.6-terra": ("GPT-5.6-Terra (Codex) xhigh", "GPT-5.6 Terra (Codex) xhigh"),
    "glm-5.3": ("GLM-5.3 (mini-swe-agent) max",),
    "kimi-k3": ("Kimi-K3 (mini-swe-agent) max",),
    "claude-sonnet-5": ("Sonnet 5 (Claude Code) xhigh",),
    "deepseek-v4-pro": None,
    "qwen-3.8-max": None,
    "minimax-m3": None,
    "gpt-6-astra": (
        "GPT 6 Astra (Codex) xHigh*",
        "GPT 6 Astra (Codex) xHigh",
        "GPT-6-Astra (Codex) high",
        "GPT-6 Astra (Codex) high ",
    ),
    "gpt-6-sol": None,
    "gpt-5.6-sol": (
        "GPT-5.6-Sol (Codex) xHigh*",
        "GPT-5.6-Sol (Codex) xHigh*\n",
        "GPT-5.6-sol (Codex) xhigh",
        "GPT-5.6 Sol (Codex) xhigh",
    ),
    "claude-opus-5": ("Opus 5 (Claude Code) xHigh\n", "Opus 5 (Claude Code) xHigh", "Opus 5 (Claude Code) xhigh"),
    "claude-opus-5-5": None,
    "claude-fable-5": ("Fable-5 (Claude Code) xHigh", "Fable-5 (Claude Code) xHigh*"),
    "claude-fable-5-1": ("Fable-5.1 (Claude Code) xHigh*", "Fable-5.1 (Claude Code) xHigh", "Fable 5.1 (Claude Code) high"),
    "gpt-6.1-sol": ("GPT-6.1-Sol (mini-swe-agent) xhigh",),
    "claude-sonnet-5-5": None,
    "claude-haiku-5-5": ("Haiku 5.5 (mini-swe-agent) high",),
    "mimo-v2.6-pro": None,
    "qwen-3.7-flash": None,
}

TBENCH_MODELS: Final[dict[str, tuple[str, ...] | None]] = {
    "glm-5.3-flash": None,
    "deepseek-v4-flash": None,
    "qwen-3.8-flash-next": None,
    "mimo-v2.6-flash": None,
    "gpt-5.6-luna": ("GPT-5.6 Luna",),
    "gpt-6-luna": None,
    "gpt-5.6-terra": ("GPT-5.6 Terra",),
    "glm-5.3": ("GLM-5.3",),
    "kimi-k3": None,
    "claude-sonnet-5": ("Sonnet 5",),
    "deepseek-v4-pro": None,
    "qwen-3.8-max": None,
    "minimax-m3": None,
    "gpt-6-astra": ("GPT-6 Astra",),
    "gpt-6-sol": None,
    "gpt-5.6-sol": ("GPT-5.6 Sol",),
    "claude-opus-5": ("Opus 5",),
    "claude-opus-5-5": None,
    "claude-fable-5": ("Fable 5",),
    "claude-fable-5-1": ("Fable 5.1",),
    "gpt-6.1-sol": ("GPT-6.1 Sol",),
    "claude-sonnet-5-5": ("Sonnet 5.5",),
    "claude-haiku-5-5": None,
    "mimo-v2.6-pro": None,
    "qwen-3.7-flash": None,
}

SOURCE_MODELS: Final[dict[str, dict[str, object]]] = {
    "eee": EEE_MODELS,
    "openrouter": OPENROUTER_MODELS,
    "epoch": EPOCH_MODELS,
    "arena": ARENA_MODELS,
    "modelsdev": MODELSDEV_MODELS,
    "scale": SCALE_MODELS,
    "tbench": TBENCH_MODELS,
}

KNOWN_UNMAPPED: Final[dict[str, set[str]]] = {
    "modelsdev": {"openai/gpt-6-astra-pro"},
}

EEE_EVALS: Final[dict[str, str]] = {
    "vals_ai.terminal-bench-4.overall": "terminal-bench-4",
    "vals_ai.terminal-bench-4.software": "terminal-bench-4-software",
    "vals_ai.terminal-bench-4.security": "terminal-bench-4-security",
    "llm_stats.swe-bench-pro": "swe-bench-pro-v2",
    "llm_stats.swe-bench-pro-v2": "swe-bench-pro-v2",
    "llm_stats.swe-atlas-codebase-qna": "swe-atlas-qna",
    "llm_stats.swe-atlas-qna": "swe-atlas-qna",
    "llm_stats.swe-atlas-test-writing": "swe-atlas-test-writing",
    "llm_stats.swe-atlas-refactoring": "swe-atlas-refactoring",
    "llm_stats.deepswe-1.1": "deepswe",
    "llm_stats.frontiercode-1.1": "frontiercode",
    "artificial_analysis.artificial_analysis_intelligence_index": "aa-intelligence-index",
    "artificial_analysis.artificial_analysis_coding_index": "aa-coding-index",
    "artificial_analysis.artificial_analysis_coding_agent_index": "aa-coding-agent-index",
    "artificial_analysis.gpqa": "gpqa-diamond",
    "artificial_analysis.gdpval_aa": "gdpval-aa",
}

ARENA_CATEGORIES: Final[dict[str, str]] = {
    "overall": "arena-overall",
    "coding": "arena-coding",
    "math": "arena-math",
    "creative_writing": "arena-creative-writing",
    "instruction_following": "arena-instruction-following",
    "hard_prompts": "arena-hard-prompts",
}


def _reverse(mapping: dict[str, tuple[str, ...] | None]) -> dict[str, str]:
    return {
        label: model_id
        for model_id, labels in mapping.items()
        if labels is not None
        for label in labels
    }


EEE: Final[tuple[tuple[str, str, str, str], ...]] = tuple(
    (source, org, model_dir, model_id)
    for model_id, locations in EEE_MODELS.items()
    if locations is not None
    for source, org, model_dir in locations
)
OPENROUTER: Final[dict[str, str]] = _reverse(OPENROUTER_MODELS)
EPOCH: Final[dict[str, str]] = _reverse(EPOCH_MODELS)
ARENA: Final[dict[str, str]] = _reverse(ARENA_MODELS)
MODELSDEV: Final[dict[str, str]] = _reverse(MODELSDEV_MODELS)
SCALE: Final[dict[str, str]] = _reverse(SCALE_MODELS)
TBENCH: Final[dict[str, str]] = _reverse(TBENCH_MODELS)


def resolve(source: str, label: str) -> str | None:
    direct = {
        "openrouter": OPENROUTER,
        "epoch": EPOCH,
        "arena": ARENA,
        "modelsdev": MODELSDEV,
        "scale": SCALE,
        "tbench": TBENCH,
    }
    return direct.get(source, {}).get(label)


def _canonical(value: str) -> str:
    return re.sub(r"[^a-z0-9]", "", value.lower())

def _arena_base(name: str) -> tuple[str, int] | None:
    for model_id in sorted(ARENA_MODELS, key=lambda value: len(_canonical(value)), reverse=True):
        atoms = re.findall(r"[a-z0-9]+", model_id.lower())
        pattern = r"[\s._/-]*".join(map(re.escape, atoms))
        match = re.match(pattern + r"(?=$|[^a-z0-9])", name, re.IGNORECASE)
        if match is not None:
            return model_id, match.end()
    return None


def resolve_arena(name: str) -> str | None:
    direct = ARENA.get(name)
    if direct is not None:
        return direct
    base = re.sub(
        r"\s*(?:\((?:xhigh|extra high|max|high|medium|low)\)|-(?:xhigh|extra-high|max|high|medium|low))\s*$",
        "",
        name,
        flags=re.IGNORECASE,
    )
    return ARENA.get(base)


def _arena_effort(value: str) -> str | None:
    if re.search(r"(?<![a-z])(?:xhigh|extra high)(?![a-z])", value):
        return "xhigh"
    for effort in ("max", "high", "medium", "low"):
        if re.search(rf"(?<![a-z]){effort}(?![a-z])", value):
            return effort
    return None


def arena_effort(name: str) -> str | None:
    match = _arena_base(name)
    if match is not None:
        return _arena_effort(name[match[1] :].lower())
    return _arena_effort(name.lower())
