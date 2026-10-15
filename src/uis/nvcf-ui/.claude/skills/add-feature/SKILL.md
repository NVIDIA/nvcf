---
name: add-feature
description: >
  Add a new UI feature to the nvcf-ui app — folder structure, routing, data loading,
  lazy-loading, testing, and completion checklist. Use when creating a new feature folder,
  adding a page or view, wiring routes, building a new screen, or deciding whether to extend
  an existing feature vs create a new one. Also use when the user asks to build something new
  in the UI, even if they don't say "feature" explicitly.
license: Apache-2.0
allowed-tools:
  - Bash
  - Read
  - Edit
---

# Adding a UI Feature

For all component selection, layout decisions, and styling — invoke the `kaizen-ui` skill.

## Gotchas

- This project uses **code-based routing**, not file-based. Do not use `createFileRoute` or the TanStack Router Vite plugin — use `createRoute` and `createLazyRoute`.
- View components are named by view type (e.g. `WidgetsList`, `WidgetDetail`), never with a `Page` suffix.
- `router.tsx` is pure composition — it only imports and wires route trees. Do not put route definitions or loaders in `router.tsx`.
- `rootRoute.tsx` is separate from `router.tsx` to avoid circular imports. Layout routes that span multiple features live at `src/` root, not in a feature folder.
- Generated hooks, schemas, and mocks live in `ui/src/generated/` — never edit these files. If the endpoint you need doesn't have generated hooks, update the appropriate spec in `spec/` (`control-plane-openapi.yaml`, `nvcf-openapi.yaml`, or `sis-openapi.yaml`) then run `task generate`.
- All source files need Apache 2.0 license headers. Run `task license:add` before committing.

## When to Create a New Feature Folder

- **New domain** (new data, new concepts, new API surface) → new feature folder
- **Composition of existing domains** (e.g. a home page assembling widgets) → new thin feature folder
- **Extension of an existing domain** (e.g. a "Create Function" page) → new route/component in the existing feature folder

## Feature Folder Structure

A feature is a **domain boundary**, not a route. Each feature is self-contained under `ui/src/features/<name>/`:

```
features/<name>/
├── components/         # Smaller building blocks (badges, cards)
├── hooks/              # Feature-scoped hooks
├── types/              # Feature-scoped types
├── routes.tsx          # Route definitions, loaders, search validation (optional)
├── <View>.tsx          # Route-level view components (PascalCase, named by view type)
└── utils.ts            # Feature-scoped helpers (when needed)
```

- Not every subfolder is required — add `components/`, `hooks/`, `types/` only when needed
- A feature may not have routes at all — it may only export hooks and components for other features
- Helpers go inline in the component file by default. Extract to a feature-root file only when shared across multiple files in the same feature

## Route Splitting Pattern

Each route is split across two files:

**`routes.tsx`** — route definition, search params, loader. In the main bundle.

```tsx
import { createRoute } from "@tanstack/react-router";
import { appLayoutRoute } from "~/appLayout";
import { queryClient } from "~/lib/queryClient";
import { getGetWidgetsQueryOptions } from "~/generated/api/widgets/widgets";
import { GetWidgetsParams } from "~/generated/model/getWidgetsParams.zod";

const widgetsRoute = createRoute({
  getParentRoute: () => appLayoutRoute,
  path: "widgets",
  validateSearch: GetWidgetsParams.catch({ status: "active" }),
  loaderDeps: ({ search }) => search,
  loader: ({ deps }) =>
    queryClient.ensureQueryData({
      ...getGetWidgetsQueryOptions(deps),
      revalidateIfStale: true,
    }),
}).lazy(() => import("./WidgetsList").then((m) => m.WidgetsListRoute));

export const widgetsRouteTree = widgetsRoute;
```

- Use generated Zod schemas for `validateSearch` when params map to API query params. For UI-only search params, define inline.
- `loaderDeps` declares which search params the loader depends on — the loader re-runs when these change.
- `ensureQueryData` with `revalidateIfStale: true` avoids flashing stale content.
- For multiple data sources: `ensureQueryData` (awaited) for primary data, `prefetchQuery` (no await) for below-the-fold data.

**`<View>.tsx`** — lazy-loaded component, pending, and error states.

```tsx
import { createLazyRoute } from "@tanstack/react-router";
import { useGetWidgetsSuspense } from "~/generated/api/widgets/widgets";

function WidgetsList() {
  const { status } = widgetsRoute.useSearch();
  const { data } = useGetWidgetsSuspense({ status });
  // render...
}

export const WidgetsListRoute = createLazyRoute("/widgets")({
  component: WidgetsList,
  pendingComponent: () => <div>Loading...</div>,
});
```

- Use suspense hook variants (`useGetWidgetsSuspense`) — the loader already ensured data, so these resolve immediately or suspend if cache is stale.
- For prefetched (non-blocking) data, wrap the consumer in a `<Suspense>` boundary with a fallback.

## Lazy-Loading Rules

- Page routes with view code → always lazy-load via `createLazyRoute`
- Layout routes that just render `<Outlet />` → keep in main bundle
- Components within a page → never lazy-load; already in the route's chunk

## Layout Routes

- Single feature → in that feature's `routes.tsx`
- Multiple features → at `src/` root alongside `rootRoute.tsx`

**Tabbed views**: use a layout route with `<Outlet />`, each tab is a child route with its own loader and lazy-loaded component.

## Steps

1. Check for existing components/patterns that could be reused or graduated before building new ones
2. Create `features/<name>/` with the relevant files — a view component and `routes.tsx` if the feature has routes, or just `components/`/`hooks/` if routeless
3. If the feature has routes: define routes in `routes.tsx` using `createRoute` with `.lazy()`, then wire the subtree into `router.tsx`. If it introduces a new top-level route, add a `NavItem` entry in `rootRoute.tsx`
4. Use generated hooks and query options for data fetching
5. Add tests for view components and any non-trivial logic. Use `renderWithRouter` from `~/testing/render` for route-level views, standard `render` for standalone components
6. Add MSW scenarios in `mocks/scenarios/<name>/` for key UI states
7. Run `task license:add` to apply license headers to new files

## Validate Before Finishing

1. Run `task ui:lint:fix` and fix any errors
2. Run `task ui:test` and verify tests pass
3. If either fails, fix the issues and rerun until clean
