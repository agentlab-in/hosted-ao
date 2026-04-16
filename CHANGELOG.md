# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Changed
- Improve lifecycle assessment to use evidence-driven `detecting` retries before declaring sessions dead when runtime, process, and activity signals disagree.

### Fixed
- Fix recovery validation so probe uncertainty and signal disagreement escalate for human review instead of being flattened into cleanup-safe dead states.
