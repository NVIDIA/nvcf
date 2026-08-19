# NVIDIA Cloud Functions UI

A web interface for managing NVIDIA Cloud Functions (NVCF), built as a single-page application (SPA).

## Prerequisites

- **Go** 1.24+
- **Node** 22+
- **pnpm** 10+
- **[Task]** 3+

[Task]: https://taskfile.dev

## Quick Start

```sh
task setup              # install Go tooling and UI dependencies
task ui:dev MOCK=true   # start Vite dev server on :5173 with mocked APIs
```

## Build with Bazel

Bazel is the canonical build path. The `task` commands below stay available for
local iteration outside CI.

Requires [bazelisk](https://github.com/bazelbuild/bazelisk) (`bazel` on PATH
delegating to the version pinned in `.bazelversion`).

```bash
# Build every Bazel target.
bazel build //...

# Run all tests.
bazel test //... --flaky_test_attempts=3

# Build the multi-arch OCI image index (linux/amd64 + linux/arm64).
bazel build //:image_index

# Load the host-arch image into the local docker daemon.
bazel run //:image_load

# Refresh backend/BUILD.bazel files after Go source changes.
bazel run //:gazelle
```

Image publishing to the staging registry is performed by the CI pipeline, not
a local Bazel push target.

> The `//ui` target carries `tags = ["no-sandbox"]` — required for Vite 8's Rolldown
> bundler under Bazel ([rules_js#2888](https://github.com/aspect-build/rules_js/issues/2888)).
> Do not remove it.

## Project Structure

```
├── backend/       # Go BFF — proxy, control-plane, static SPA serving
├── ui/            # React SPA — Vite, TanStack Router/Query, Tailwind, KUI
├── spec/          # OpenAPI specification (source of truth for codegen)
├── helm/          # Helm chart for Kubernetes deployment
├── rules/         # Bazel macros (OCI image packaging)
├── platforms/     # Bazel platform definitions (linux amd64/arm64)
├── MODULE.bazel   # Bazel (bzlmod) module — Go, UI, and image build
├── Taskfile.yml   # Root task runner (orchestrates ui/ and backend/ tasks)
└── .gitlab-ci.yml # CI pipeline — lint, typecheck, test, deploy
```

## Development

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

**Mock mode** — Run the UI standalone with mocked API responses. No live API or cluster required.

```sh
task ui:dev MOCK=true
```

By default, mock responses come from a seeded in-memory store built on the OpenAPI schemas (via Orval), so data is stable and referentially consistent — list rows match their detail pages and everything is keyed by the active account. To test specific UI states (empty lists, error conditions, etc.), use **scenarios** — named overrides layered on top of the default handlers:

```sh
task ui:dev MOCK=true SCENARIO=functions:empty-list
```

Multiple scenarios can be combined with commas: `SCENARIO=functions:empty-list,tasks:launching-task`. Scenarios live in `ui/src/mocks/scenarios/<feature>/<name>.ts`.

**Live API mode** — The backend-for-frontend server lives in `backend/` and is compiled by `task build`. Running it against live upstreams requires in-cluster access and mounted secrets, so for day-to-day UI work use mock mode.

### Code Generation

API client hooks, TypeScript models, and MSW mock handlers are generated from the OpenAPI specs via Orval. After modifying the specs in `spec/` (`control-plane-openapi.yaml`, `nvcf-openapi.yaml`, `sis-openapi.yaml`), run:

```sh
task generate
```

Generated code lives in `ui/src/generated/` and should not be edited by hand.

### Testing

Tests use [Vitest](https://vitest.dev/) with [Testing Library](https://testing-library.com/). Test files live alongside the code they test (e.g. `FunctionsList.test.tsx` next to `FunctionsList.tsx`).

```sh
task test
```

## Architecture

The application follows a **backend-for-frontend (BFF)** pattern: the React SPA never talks to upstream services directly. A BFF handles authentication, proxies API requests to upstream NVCF services, and serves the built SPA as static files. That server lives in `backend/`.

## License

[Apache 2.0](LICENSE)
