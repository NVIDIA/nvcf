# AGENTS.md

Context and conventions for AI agents working in this codebase. Read before making changes.

This file covers cross-cutting rules that apply to every task. Verify file paths, function names, and code patterns against the actual codebase before following them.

## Project Structure

```
ui/src/
├── main.tsx          # Entry point — mounts providers, stays thin
├── rootRoute.tsx     # Root route — app shell and layout
├── router.tsx        # Route tree composition — imports feature routes, wires tree
├── components/       # Shared UI components (feature-agnostic)
├── features/         # One folder per domain — self-contained modules
├── generated/        # Orval output — read-only, never edit
├── hooks/            # Shared hooks (feature-agnostic)
├── lib/              # Preconfigured instances (fetch client, queryClient)
├── mocks/            # MSW setup, baseline handlers, custom scenarios
├── types/            # Shared TypeScript types
└── utils/            # Pure utility functions
```

## TypeScript

- `strict: true` — do not use `any`, non-null assertions, or disable strict checks
- Path alias: `~/` maps to `src/` (configured in `tsconfig.json`)
- Use `import type` for type-only imports (`verbatimModuleSyntax: true`)

## Import Rules

- Features may import directly from other features, but imports must be **unidirectional** — no circular dependencies
- **Graduation rule**: graduate shared code to `src/components/`, `src/hooks/`, etc. only when the concept is genuinely domain-agnostic (e.g. `StatusBadge`, `useDebounce`). Domain-specific code stays in its owning feature
- **Component ownership**: ask "would the source feature's team recognize this as theirs?" If yes, it belongs in the source feature. If shaped by the consumer's layout, it belongs with the consumer
- Before building new components, check if similar logic or UI already exists — extract and graduate rather than duplicating

## State Management

- TanStack Query owns all server state — no Redux, Zustand, or global stores
- Local UI state lives in the component or a feature-scoped hook

## Data Loading

- Always use the generated React Query hooks — do not write manual `fetch` calls for endpoints with generated hooks
- In route components, use suspense hook variants (`useGetFunctionsSuspense`) — not `useQuery` + `isPending` checks
- Route loaders use `ensureQueryData` with `revalidateIfStale: true` to avoid flashing stale content
- Use generated query options factories (e.g. `getGetFunctionsQueryOptions`) in loaders
- Use generated Zod schemas for route search param validation via `validateSearch`

## Fetch and Error Handling

- `lib/fetch.ts` provides a custom fetch mutator used by all generated Orval hooks
- Non-2xx responses throw an `HttpError` automatically — do not add manual response checks in feature code
- Route errors surface through the router's global `defaultErrorComponent` (`RouteErrorFallback`), whose retry calls `router.invalidate()` to re-run the loader — don't add per-route `errorComponent` lambdas. Use `AsyncBoundary` for component-level (non-route) suspense/error isolation (e.g. dashboard panels)

## Testing

- Tests are not optional — when adding or modifying a component, add or update the corresponding tests
- Vitest + Testing Library; test files live alongside code (`Widget.test.tsx` next to `Widget.tsx`)
- Test user-visible behavior, not implementation details

## Styling

- Tailwind CSS 4 + KUI (NVIDIA design system)
- Always prefer KUI components (`Text`, `Heading`, `Panel`, `Button`, etc. from `@nvidia/foundations-react-core`) over plain HTML elements
- The KUI Tailwind plugin overrides default theme values — standard Tailwind classes already use KUI design tokens
- KUI vendor CSS is committed in `vendor/kui-foundations/` — run `task ui:vendor-css` after bumping the package

## Backend

- Go code lives in `backend/` — a self-contained module with its own `go.mod`

## Linting and License

- **UI**: Biome — `task ui:lint` / `task ui:lint:fix`
- **Backend**: golangci-lint — `task backend:lint` / `task backend:lint:fix`
- **All**: `task lint` runs everything including license check
- All source files require an Apache 2.0 license header — `task license:add`
- Run `task lint:fix` after making changes

## Code Generation

- Edit the spec files in `spec/` (`control-plane-openapi.yaml`, `nvcf-openapi.yaml`, `sis-openapi.yaml`) directly — never edit anything in `ui/src/generated/`
- Run `task generate` after spec changes (Orval generates React Query hooks, TypeScript model interfaces, and MSW mock handlers from each spec)
- The generated models are the source of truth — import types and status enums (e.g. `FunctionDtoStatus`, `GetClusterResponseStatus`) from `~/generated/model/...`; don't hand-roll status constants

## Mocking (dev + tests)

- The default MSW baseline is a **seeded in-memory store** (`ui/src/mocks/store/`): stable, referentially-consistent data (list ids match detail, keyed by the active account `ncaId`). Both the browser worker and the Node test server use it
- `VITE_SCENARIO=feature:file` files in `mocks/scenarios/` layer on top — MSW is first-match-wins and scenarios are prepended, so they override the baseline
- Fix mock-data realism at the **source** — the `mock.properties` overrides in `orval.config.ts`, then `task generate` — never by patching generated output or the store
