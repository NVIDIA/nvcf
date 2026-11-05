# NVIDIA Cloud Functions UI

A web interface for managing NVIDIA Cloud Functions (NVCF), built as a single-page application (SPA).

## Prerequisites

- Go 1.26+ (see `backend/go.mod`)
- Node 22.13+ (required by pnpm 11; see `engines` in `ui/package.json`)
- pnpm 11 (pinned by `packageManager` in `ui/package.json`)
- [Task] 3+
- [golangci-lint] 2.x (backend lint in `task lint` and `task verify`; not installed by `task setup`)

[Task]: https://taskfile.dev
[golangci-lint]: https://golangci-lint.run/welcome/install/

## Quick Start

```sh
task setup              # install Go tooling and UI dependencies
task ui:dev MOCK=true   # start Vite dev server on :5173 with mocked APIs
```

## Build with Bazel

Bazel is the canonical build path. The `task` commands below stay available for
local iteration outside CI.

Requires [bazelisk](https://github.com/bazelbuild/bazelisk) (`bazel` on PATH
delegating to the version pinned in the repository root `.bazelversion`). Run
these from the repository root.

```bash
# Build every nvcf-ui target.
bazel build //src/uis/nvcf-ui/...

# Run all nvcf-ui tests.
bazel test //src/uis/nvcf-ui/... --flaky_test_attempts=3

# Build the multi-arch OCI image index (linux/amd64 + linux/arm64).
bazel build //src/uis/nvcf-ui:image_index

# Load the host-arch image into the local docker daemon.
bazel run //src/uis/nvcf-ui:image_load

# Refresh backend/BUILD.bazel files after Go source changes.
bazel run //src/uis/nvcf-ui:gazelle
```

Image publishing to the staging registry is performed by the CI pipeline, not
a local Bazel push target.

> The `//ui` target carries `tags = ["no-sandbox"]`, required for Vite 8's Rolldown
> bundler under Bazel ([rules_js#2888](https://github.com/aspect-build/rules_js/issues/2888)).
> Do not remove it.

## Project Structure

```text
├── AGENTS.md       # guidance for AI coding agents
├── ARCHITECTURE.md # layers, placement, routing, data loading
├── .agents/skills/ # agent skills (symlinked into .claude/ and .cursor/)
├── backend/       # Go BFF: proxy, control-plane, static SPA serving
├── ui/            # React SPA: Vite, TanStack Router/Query, Tailwind, KUI
├── spec/          # OpenAPI specifications (source of truth for codegen)
├── helm/          # Helm chart for Kubernetes deployment
├── BUILD.bazel    # Bazel targets: build, lint, typecheck, test, OCI image
├── biome.json     # Biome config (extends ui/biome.json)
└── Taskfile.yml   # Root task runner (orchestrates ui/ and backend/ tasks)
```

## Development

Start your coding agent in this directory. Claude Code reads
`.claude/settings.json` only from the directory a session starts in, so a
session started at the monorepo root works but loses the permission allowlist
and the format-on-edit hook. See `AGENTS.md`.

### Commands

| Command                            | Description                                  |
| ---------------------------------- | -------------------------------------------- |
| `task setup`                       | Bootstrap a fresh clone                      |
| `task ui:dev`                      | Start Vite dev server                        |
| `task ui:dev MOCK=true`            | Start with mock API responses (no live API)  |
| `task generate`                    | Regenerate API hooks and mocks from spec     |
| `task typecheck`                   | Type-check the UI                            |
| `task build`                       | Production build (UI + Go binaries)          |
| `task lint`                        | Run all linters and license check            |
| `task lint:fix`                    | Auto-fix lint and formatting issues          |
| `task test`                        | Run all tests                                |
| `task ui:vendor-css`               | Fetch KUI CSS (after bumping foundations)     |

### Dev Modes

Mock mode runs the UI standalone with mocked API responses. No live API or cluster required.

```sh
task ui:dev MOCK=true
```

By default, mock responses come from a seeded in-memory store built on the OpenAPI schemas (via Orval), so data is stable and referentially consistent. List rows match their detail pages and everything is keyed by the active account. To test specific UI states (empty lists, error conditions, etc.), use scenarios, which are named overrides layered on top of the default handlers:

```sh
task ui:dev MOCK=true SCENARIO=functions:empty-list
```

Multiple scenarios can be combined with commas: `SCENARIO=functions:empty-list,tasks:launching-task`. Scenarios live in `ui/src/mocks/scenarios/<feature>/<name>.ts`.

Live API mode uses the backend-for-frontend server, which lives in `backend/` and is compiled by `task build`. Running it against live upstreams requires in-cluster access and mounted secrets, so for day-to-day UI work use mock mode.

### Code Generation

API client hooks, TypeScript models, and MSW mock handlers are generated from the OpenAPI specs via Orval. After modifying the specs in `spec/` (`control-plane-openapi.yaml`, `nvcf-openapi.yaml`, `sis-openapi.yaml`), run:

```sh
task generate
```

Generated code lives in `ui/src/generated/` and should not be edited by hand.

The route tree, `ui/src/routeTree.gen.ts`, is generated by the TanStack Router
Vite plugin whenever the dev server, a build, or the tests start. It is
committed and should not be edited by hand either.

### Testing

Tests use [Vitest](https://vitest.dev/) with [Testing Library](https://testing-library.com/). Test files live alongside the code they test (for example `FunctionCard.test.tsx` next to `FunctionCard.tsx`). Page tests sit beside their route file in `ui/src/routes/` and are named after the page component (`FunctionDetail.test.tsx`).

```sh
task test
```

## Architecture

The application follows a backend-for-frontend (BFF) pattern: the React SPA never talks to upstream services directly. A BFF handles authentication, proxies API requests to upstream NVCF services, and serves the built SPA as static files. That server lives in `backend/`.

See `ARCHITECTURE.md` for how the UI is structured.

## License

[Apache 2.0](../../../LICENSE)
