# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- `make install` now makes the production host hot-reload the new plugin binary.

## [0.1.7] - 2026-10-01

### Added
- Added offline context-window filtering before model selection (#7).
- Documented the 1M auto-router context override for OMP clients (#7).
- Bounded Jev classification snippets to preserve input capacity (#7).
- Added an `X-Auto-Router-Tier` response header naming the routed tier (#9).
- Added a model-change notice to retry request bodies after a failover (#9).
- Raised difficulty one band after three explicitly failed tool calls in a row (#9).
- Raised state-changing requests (production, credentials, billing, shared infrastructure) to at least hard difficulty.
- Kept new sessions at routine or higher when the prompt tries to pick the model or tier.
- Logged the `sensitive`, `claim`, and `guard` values in each routing decision.
- Retried a firewall-blocked Jev call once with commands, paths and URLs removed.

### Changed
- Sessions now expire after 10 idle minutes instead of 1 hour, so trivial follow-ups can downgrade.
- Jev now sees the latest request (up to 4000 characters), two earlier turns, and session context.
- Secrets and harness wrappers are removed before text reaches Jev.
- Code blocks are replaced by a one-line description before text reaches Jev.
- Raised the `snippet_chars` default and cap from 1500 to 4000.
- Decision logs now report the effective difficulty, after guards and the tool-failure raise.
- A missing guard answer from Jev counts as zero instead of failing the classification.
- Moved production deployment to the dedicated cliproxy LXC (`root@10.23.23.12:/opt/cliproxy`); `make install` now copies there, and the production runbook documents the new host.

### Fixed
- Retry explicit request timeouts and stalled streams before any output is emitted (#9).
- Kept uncertain difficulty decisions within one band of the classifier without lowering existing sessions (#5).
- Corrected extreme difficulty confidence for requests classified directly by average effort (#5).
