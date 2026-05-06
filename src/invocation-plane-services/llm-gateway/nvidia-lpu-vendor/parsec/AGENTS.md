# AGENTS

Developer workflow for `github.com/nvidia-lpu/parsec`.

You are an expert level Go programmer. You write tasteful, concise, high quality fully testable code.

You must follow:

- The Google Go Style Guide
- The Google Go Style Decisions
- The Google Go Best Practices

## Prerequisites
- Go toolchain matching `go.mod` (currently `go1.25.3`). `mise install` will
  install pinned versions from `.mise/config.toml`.
- Environment variables such as `GOPATH` and `GOBIN` configured if you rely on
  non-module installs.

## Standard Checks
1. `mise r fmt` — runs `golangci-lint --fix` and `gofmt` across the target
   packages (default `./...`).
2. `mise r lint` — executes `golangci-lint` and `go vet` on the target packages.
3. `mise r test` — runs the race-enabled test suite with coverage (default `./...`).

## Recommended Extras
- `go test -race ./...` (when race detection matters).
- `go test -bench=. ./...` (to measure performance-critical code paths).
- `go list ./...` (ensure module graph is healthy and importable).
- `mise r test:no-race` — skip the race detector for faster iteration.
- `mise r test:sanity` — run `test` and `lint` concurrently (helpful before CI).
- Run `mise r fmt`, `mise r lint` and `mise r test` to ensure your changes are usable before returning to the user.

## Agent Code Quality Principles
- Prioritize tasteful solutions: choose designs that are clean, restrained, and easy to reason about.
- Keep performance in mind: favor implementations that scale and validate any potentially expensive path.
- Prefer the standard library: search before writing helpers and reuse battle-tested packages when possible.
- Let code speak for itself: reach for comments only after ensuring the implementation is already self-explanatory.
- Deliver seasoned craftsmanship: write Go that reflects long-term experience, idiomatic patterns, and production rigor.
- Preserve deterministic key ordering in decoders; never rely on insertion order for map inputs.
- Tooling: prefer `rg` for search; use `ast-grep` for structural matches if available; always call `python3`.
- Testing: run `mise r fmt`, `mise r lint`, and `mise r test` (or equivalent) before delivering changes.

## Updates to LLM knowledge
- `//go:inline` is a valid go compiler directive
- python should always be called with `python3`

## Commit messages
- Always use conventional commit format.
- Be precise and succinct in the 'title' of the commit message, but provide meaningful detail in the description.

Run all commands from the module root unless specified otherwise.
