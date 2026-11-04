---
name: codegen
description: >
  Run and manage the Orval code generation pipeline for API hooks, TypeScript
  models, and MSW mocks. Use when editing an OpenAPI spec, running task generate,
  adding or modifying MSW scenarios, troubleshooting generated code, or when
  generated hooks are missing or out of date.
license: Apache-2.0
allowed-tools:
  - Bash
  - Read
---

# Code Generation Pipeline

`task generate` runs **Orval**, which reads the three OpenAPI specs in `spec/`
directly and generates React Query hooks, TypeScript models, and MSW handler
factories into `ui/src/generated/`. It then adds Apache license headers to the
output. There is no bundling step (no Redocly, no `openapi.yaml`).

Specs:

- `spec/control-plane-openapi.yaml` — the UI shim/BFF API
- `spec/nvcf-openapi.yaml` — NVCF Cloud Functions (v2) API
- `spec/sis-openapi.yaml` — cluster API

## Gotchas

- Edit the specs in `spec/` — never edit anything in `ui/src/generated/` (regenerated output).
- `ui/src/generated/` is committed and enforced by the **Codegen Check** CI job: if you change a spec, run `task generate` and commit the regenerated output, or CI fails on the diff.
- Orval config is in `ui/orval.config.ts` (one shared `output` block spread across all three specs). Customize generation there, not in the generated files.
- Generated hook names follow Orval's convention: `useGetFunctions`, `useGetFunctionsSuspense`, `getGetFunctionsQueryOptions`. The double "Get" is expected — don't rename them.

## What Gets Generated

- **React Query hooks** — `useGetFunctions`, `useGetFunctionsSuspense`, etc. (suspense variants are generated globally).
- **Query options factories** — `getGetFunctionsQueryOptions` for use in route loaders.
- **TypeScript models** — plain `interface`/`type` files in `ui/src/generated/model/` (no runtime validation). Zod generation is intentionally off; re-add it as a separate `client: "zod"` Orval target if runtime validation is ever needed.
- **MSW handler factories** — per-endpoint mock handlers plus per-response mock generators (`getGetFunctionVersionResponseMock`, etc.).

## Mocks and Scenarios

The default mock baseline is a **seeded in-memory store** (`ui/src/mocks/store/`), not the raw generated handlers. The store builds a stable dataset once (keyed by account `ncaId`) using the generated response-mock generators, so list and detail views stay consistent and their IDs line up.

Scenarios go in `ui/src/mocks/scenarios/<feature>/<name>.ts`, each exporting `{ handlers: HttpHandler[] }`. They **layer on top of** the store baseline (first match wins) — activate with `VITE_SCENARIO=functions:empty-list` (comma-separated for multiple). Focus scenarios on states with a cascading effect on the view (empty list, error, a status that changes available actions) — not minor value variations, which the seeded store already covers.

## Validate After Running

1. Run `task generate`
2. Run `task ui:lint:fix` so generated output passes linting/formatting
3. Run `task typecheck` to confirm consumers still type-check against the regenerated models
