---
name: routing
description: >-
  Add or change routes in the nvcf-ui app with TanStack Router file-based
  routing: route files in ui/src/routes, loaders, search params, path params,
  navigation, pending and error states, layout routes, and code splitting. Use
  when adding a page, editing a loader or search schema, linking between pages,
  or loading the official TanStack Router skills for this project.
license: Apache-2.0
allowed-tools:
  - Bash
  - Read
  - Edit
---

# Routing

nvcf-ui uses TanStack Router file-based routing with its default layout.
TanStack's docs and skills apply as written. This skill covers only what is
specific to this project.

## How it works here

- Route files live in `ui/src/routes/`. Their names follow TanStack's
  file-naming conventions, so the folder structure is the URL structure.
- `@tanstack/router-plugin` generates `ui/src/routeTree.gen.ts` whenever Vite
  or Vitest starts, and on file changes during `task ui:dev`. The file is
  committed (TanStack FAQ). Never edit it.
- `autoCodeSplitting` is on. The page component is split into its own chunk;
  `loader`, `pendingComponent`, and the rest of the route options stay in the
  main bundle.
- Files ending in `.test.*` or `.spec.*` are ignored by route generation
  (`routeFileIgnorePattern` in `ui/vite.config.ts`).

## What a route file holds

A route file holds the route options and the page component:

```tsx
export const Route = createFileRoute("/functions/")({
  head: () => ({ meta: [{ title: "Functions · NVCF" }] }),
  validateSearch: functionsSearchSchema,
  pendingComponent: FunctionsListPending,
  loader: async ({ context: { queryClient } }) => { ... },
  component: FunctionsList,
});
```

- Do not export the page component or pending component. The code splitter
  requires route properties to stay unexported.
- Inside the route file, read route data with `Route.useLoaderData()`,
  `Route.useSearch()`, `Route.useParams()`, and `Route.useNavigate()`.
- In other files, use `getRouteApi("<route id>")` instead of importing `Route`.
  Importing the route object risks circular imports.
- Page scaffolding (heading, layout, composition, table `columns`, URL-state
  wiring) stays in the route file. Domain building blocks come from
  `~/features/<domain>/`. See `ARCHITECTURE.md` for placement rules.

## Project conventions

- Loaders call `queryClient.ensureQueryData({ ...getXQueryOptions(...),
  revalidateIfStale: true })` for blocking data and `void
  queryClient.prefetchQuery(...)` for data below the fold.
- Page components read data with the generated suspense hooks
  (`useGetXSuspense`).
- `validateSearch` uses a Zod schema. List pages extend
  `baseTableSearchSchema` from `~/components/DataTable/utils/tableSearchSchema`.
- Errors render through the router's global `defaultErrorComponent`
  (`RouteErrorFallback`). Do not add a per-route `errorComponent` unless the
  page needs recovery the global fallback cannot give, and record that in
  `ARCHITECTURE.md`.
- Account-specific detail pages set
  `staticData: { account: { redirectOnChange: "<list path>" } }` so switching
  accounts leaves the page.
- Shared layout for every page under a URL section goes in a layout route,
  `ui/src/routes/<section>/route.tsx`, rendering `<Outlet />`.
- Top-level pages need a nav entry in `navItems` in `ui/src/routes/__root.tsx`.

## Official TanStack skills

The `@tanstack/intent` CLI prints the skills that ship with the installed
TanStack packages, matched to the installed version. Run from `ui/`:

```sh
pnpm exec intent list
pnpm exec intent load @tanstack/router-core#router-core
pnpm exec intent load @tanstack/router-core#router-core/data-loading
pnpm exec intent load @tanstack/router-core#router-core/search-params
pnpm exec intent load @tanstack/router-core#router-core/path-params
pnpm exec intent load @tanstack/router-core#router-core/navigation
pnpm exec intent load @tanstack/router-core#router-core/not-found-and-errors
pnpm exec intent load @tanstack/router-core#router-core/code-splitting
pnpm exec intent load @tanstack/router-core#router-core/type-safety
pnpm exec intent load @tanstack/react-router#compositions/router-query
```

Load the skill that matches the task before changing route code. Where a
TanStack skill and this file disagree, this file wins for project conventions.

## Examples

- List page: `ui/src/routes/functions/index.tsx`
- Detail page with path params: `ui/src/routes/functions/$functionId.versions.$versionId.tsx`
- Page composing several features: `ui/src/routes/index.tsx`
- Root route and app shell: `ui/src/routes/__root.tsx`

## Validate

1. Run `task lint:fix`.
2. Run `task verify`.
