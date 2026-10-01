# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

### Changed
- Sessions now expire after 10 idle minutes instead of 1 hour, so trivial follow-ups can downgrade.
- Moved production deployment to the dedicated cliproxy LXC (`root@10.23.23.12:/opt/cliproxy`); `make install` now copies there, and the production runbook documents the new host.

### Fixed
- Retry explicit request timeouts and stalled streams before any output is emitted (#9).
- Kept uncertain difficulty decisions within one band of the classifier without lowering existing sessions (#5).
- Corrected extreme difficulty confidence for requests classified directly by average effort (#5).
