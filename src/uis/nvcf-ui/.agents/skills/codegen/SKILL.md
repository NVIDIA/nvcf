---
name: codegen
description: >-
  Run and manage the Orval code generation pipeline for API hooks, TypeScript
  models, and MSW mocks in nvcf-ui. Use when editing an OpenAPI spec, running
  task generate, adding or changing MSW scenarios, troubleshooting generated
  code, or when generated hooks are missing or out of date.
license: Apache-2.0
---

# Code generation

`task generate` runs Orval, which reads the three OpenAPI specs in `spec/` and
generates React Query hooks, TypeScript models, and MSW handler factories into
`ui/src/generated/`. It then adds license headers to the output.

Specs:

- `spec/control-plane-openapi.yaml`: the UI shim and BFF API
- `spec/nvcf-openapi.yaml`: the NVCF Cloud Functions v2 API
- `spec/sis-openapi.yaml`: the cluster API

## Rules

- Edit the specs in `spec/`. Never edit `ui/src/generated/`.
- Commit regenerated output in the same change as the spec. No CI job checks
  generated output for drift today, so a stale diff is caught only in review.
- Orval config is `ui/orval.config.ts`, with one shared `output` block spread
  across all three specs. Customize generation there.
- Hook names follow Orval's convention: `useGetFunctions`,
  `useGetFunctionsSuspense`, `getGetFunctionsQueryOptions`. The double "Get" is
  expected.

## What gets generated

- React Query hooks, including suspense variants.
- Query options factories, used in route loaders.
- TypeScript models in `ui/src/generated/model/`. Zod output is off; add a
  separate `client: "zod"` target if runtime validation is needed.
- MSW handler factories and response mock generators.

## Mocks and scenarios

The default mock baseline is a seeded in-memory store in `ui/src/mocks/store/`,
not the raw generated handlers. It builds one dataset keyed by account `ncaId`,
so list and detail views agree on IDs.

Scenarios live in `ui/src/mocks/scenarios/<feature>/<name>.ts` and export
`handlers`. They layer over the store (first match wins). Activate them with
`task ui:dev MOCK=true SCENARIO=functions:empty-list` (comma-separated for
several). Write scenarios for states that change the view, such as an empty
list, an error, or a status that changes available actions.

Fix mock realism at the source: the `mock.properties` overrides in
`ui/orval.config.ts`, then `task generate`.

## Validate

1. Run `task generate`.
2. Run `task lint:fix`.
3. Run `task verify`.
