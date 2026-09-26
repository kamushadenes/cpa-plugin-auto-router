# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Added offline context-window filtering before model selection (#7).
- Documented the 1M auto-router context override for OMP clients (#7).
- Bounded Jev classification snippets to preserve input capacity (#7).
- Added installable OMP and Hermes model-transition guidance adapters.

### Fixed
- Retry explicit request timeouts and stalled streams before any output is emitted (#9).
- Kept uncertain difficulty decisions within one band of the classifier without lowering existing sessions (#5).
- Corrected extreme difficulty confidence for requests classified directly by average effort (#5).
