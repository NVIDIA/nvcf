# AGENTS.md - Tools Guide for AI Coding Agents

Scope: everything under `tools/`.

## Tool choice

- Prefer `bash` for small repo-local wrappers, CI entrypoints, and scripts that mostly orchestrate existing CLI tools.
- Prefer `Go` for non-trivial tooling: structured parsing, manifest scanning, larger file transforms, concurrency, or logic that benefits from unit tests and a compiled binary.
- Avoid adding new Python-based repo tooling when `bash` or `Go` is a reasonable fit. Some CI environments in this repo do not guarantee Python.
- If Python is still required for a one-off command, use `python3`, not `python`.

## Layout

- Put general executable wrapper scripts in `tools/scripts/`.
- Put CI-focused entrypoint scripts in `tools/ci/`.
- Put larger Go tools in their own directory under `tools/`, for example `tools/collect-dependencies/` or `tools/sync-synthetic-imports/`.
- If a Go tool needs a stable repo entrypoint for CI or docs, add a thin wrapper in `tools/ci/` or `tools/scripts/` as appropriate, or document the exact `go run` or `go build -C` command.

## Naming

- Use hyphens, not underscores, for filenames under `tools/`, especially under `tools/scripts/`.
- Examples: `collect-notices`, `check-license-headers`, `check-dependency-licenses`, `test-collect-notices`.
- Keep test entrypoints aligned with the tool name, for example `test-<tool-name>`.

## Implementation

- Keep tools easy to run in CI and local development. Prefer standard library code and common Unix tools over extra dependencies.
- For bash scripts, prefer `#!/usr/bin/env bash` and `set -euo pipefail` unless there is a clear reason not to.
- Keep user-facing output concise and actionable. Error messages should say what failed and how to fix it when possible.
- When replacing a tool or changing its entrypoint, update callers, CI jobs, and docs in the same change.

## Tests

- Add or update focused tests when changing tool behavior.
- For bash tools, prefer shell-based tests under `tools/scripts/test/`.
- For Go tools, prefer `_test.go` unit tests next to the implementation.
