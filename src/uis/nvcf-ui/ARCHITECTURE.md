# Architecture

The structure of nvcf-ui and the rules that keep it that way. For commands and
conventions see `AGENTS.md`. For step by step procedures see the skills listed
there.

## System shape

A React single page application, compiled by Vite to static assets, served by a
Go backend for frontend (BFF) in `backend/`. The browser never calls upstream
NVCF services. Every request goes through the BFF, which authenticates it,
proxies it upstream, and serves the SPA itself.

`backend/` is a Go module with its own `go.mod` and builds two binaries:

- `cmd/server`, the BFF. It reverse proxies to the NVCF and NVCT APIs, applies
  middleware, refreshes credentials through `internal/token-watcher`, and
  serves the SPA as static files.
- `cmd/control-plane`, a monitor driven by a components manifest. The same
  binary is the Kubernetes exec liveness probe when run with `-liveness`.

`task build` builds the UI into `backend/static`, then compiles both binaries.
They ship in one OCI image with the SPA at `/app/static`.

Credentials, upstream hostnames, and anything else the browser must not hold
live in the BFF. Nothing in `ui/src/` talks to an upstream service directly.

## Layers

`ui/src/` has three layers. Imports only point down the list.

1. Pages: `routes/`. One file per URL. Pages compose features and shared code.
2. Features: `features/<domain>/`. Domain code. A feature imports shared code
   only, never another feature and never `routes/`.
3. Shared: `components/`, `hooks/`, `lib/`, `utils/`, `types/`, plus the
   generated client in `generated/`. Shared code imports neither features nor
   pages.

`mocks/` and `testing/` support development and tests and may import shared
code and the generated client.

This follows the layering in bulletproof-react ("shared -> features -> app").
It is enforced by review (CodeRabbit and `AGENTS.md` review priorities), not by
lint.

## Placement

Decide where code goes by what it is, not by how many places use it.

- Page scaffolding goes in the route file: the page heading, layout,
  composition of feature and shared components, page configuration such as a
  list's table `columns`, and URL-state wiring.
- Domain building blocks go in their feature from the start: anything that
  displays or works with domain data, such as cards, badges, stat panels,
  domain hooks, constants, and types.
- Domain-agnostic code needed by more than one feature graduates to shared.
  The test is whether the owning feature's team would still call it theirs.

When scaffolding is reused:

- Across every page under one URL section, move it to a layout route
  (`routes/<section>/route.tsx` rendering `<Outlet />`).
- Across sections, graduate a generic shared component, for example
  `PageHeading({ icon, title, actions })`.

Do not extract ahead of a second use.

### UI that combines domains

A view that needs data from several domains, such as a chart of functions and
tasks, splits into three parts:

1. Presentation: a data-source-agnostic component in `components/`.
2. Data: a hook per domain in its feature, each returning the shared shape.
3. Join: combining the two in the page.

If a substantial join repeats across pages, raise it and add a layer above
features at that point (Feature-Sliced Design calls it "widgets"). Do not break
feature isolation to share it.

## Routing

Routing uses TanStack Router file-based routing with its default layout.

- Route files live in `ui/src/routes/` and follow TanStack's file-naming
  conventions, so the folder structure is the URL structure.
- `@tanstack/router-plugin` generates `ui/src/routeTree.gen.ts` when Vite or
  Vitest starts and on file changes in dev. It is committed, per the TanStack
  FAQ, and never edited.
- `autoCodeSplitting` splits each page component into its own chunk. Loaders
  and pending components stay in the main bundle.
- A route file holds the route options and the page component. The page
  component is not exported. Inside the file it reads route data with
  `Route.useLoaderData()`, `Route.useSearch()`, and `Route.useParams()`. Other
  files use `getRouteApi()`.
- Page components are named by view type, such as `FunctionsList` and
  `ClusterDetails`. Never use a `Page` suffix.
- `routes/__root.tsx` is the root route: the app shell, navigation, and router
  context.
- `router.tsx` creates the router from the generated tree and sets
  application-wide defaults.

## Data loading

Routes orchestrate data. A route's loader starts the queries its page needs,
and the page and the components below it read them with the generated suspense
hooks, such as `useGetFunctionsSuspense`. This follows the router and Query
composition TanStack documents: "static queries and fire-and-forget queries in
loaders, useSuspenseQuery in components"
(`@tanstack/react-router#compositions/router-query`).

- Data the page needs to render is awaited with `queryClient.ensureQueryData`
  and `revalidateIfStale: true`. On a revisit, cached data renders immediately
  and stale data refetches in the background, instead of showing the pending
  state.
- Data the page can render without is started in the loader with `void
  queryClient.prefetchQuery(...)`, which never rejects. Components that read
  it sit inside their own `AsyncBoundary`, as the dashboard panels do, and
  show its error there.
- A page that shows its own loading state defines `pendingComponent` inline in
  its route file. Otherwise the router's default `RouteSpinner` shows.
- Search parameters are validated with `validateSearch` and a Zod schema. List
  pages extend `baseTableSearchSchema`.

## API surface

Three OpenAPI specs in `spec/` generate one client surface under
`ui/src/generated/`:

- `control-plane-openapi.yaml`, the UI shim and BFF API
- `nvcf-openapi.yaml`, the NVCF Cloud Functions v2 API
- `sis-openapi.yaml`, the cluster API

Orval reads all three and emits React Query hooks, query option factories,
TypeScript models, and MSW handler factories. The output is committed.

The specs are the source of truth. Never edit `ui/src/generated/`, and never
hand write a `fetch` for an endpoint that has a generated hook. To change the
client surface, change a spec, run `task generate`, and commit the output with
the spec change.

## Caching and polling

One `QueryClient` in `ui/src/lib/queryClient.ts` configures every query:

- `staleTime` of two minutes, `refetchOnWindowFocus`, and `retry: 1`.
- `LIST_REFETCH_INTERVAL` of 60 seconds and `DETAIL_REFETCH_INTERVAL` of 30
  seconds, used by views that poll.

The application polls rather than subscribing, because function and cluster
state changes without the user acting. Views that poll use those constants
rather than choosing their own interval.

## Active account

Every view is scoped to one account. `features/accounts` owns the concept:
`getActiveNcaId.ts` resolves the active account id for loaders,
`useActiveAccount` exposes it to components, and `AccountSwitcher` changes it.

Switching accounts re-runs the current page's loaders. A page whose data does
not exist under another account sets
`staticData: { account: { redirectOnChange: "<path>" } }` and is redirected
there instead.

## Error handling

Error handling is centralized. Do not add local equivalents.

- `lib/fetch.ts` is the fetch mutator behind every generated hook. A non-2xx
  response throws an `HttpError`, so feature code never inspects response
  status.
- Route errors render through the router's global `defaultErrorComponent`
  (`RouteErrorFallback`), whose retry calls `router.invalidate()` to re-run the
  loader. A route declares its own `errorComponent` only when it needs
  recovery the global fallback cannot give, and that exception is recorded
  here.
- `AsyncBoundary` isolates suspense and errors below the route, for a panel
  that reads non-awaited data and may fail on its own.

## Components

Pages own data and orchestration. Everything below them renders.

- A component takes data through props and reports events through callback
  props, or reads a query its route's loader started (see Data loading).
- No business logic in a component. Deriving a label from props is fine.
  Deciding what a user may do is not.
- State lives as locally as it can. There is no global store; TanStack Query
  owns server state.

## Mocking

The MSW baseline is a seeded in-memory store in `ui/src/mocks/store/`, not the
raw generated handlers. It builds one stable dataset keyed by the active
account `ncaId`, so list and detail views agree on ids. The browser worker and
the Node test server share it.

Scenarios layer over that baseline. MSW is first match wins and scenarios are
prepended, so a scenario overrides the store for the endpoints it covers and
leaves the rest intact.

Mock realism is fixed at the source, in the `mock.properties` overrides in
`ui/orval.config.ts` followed by `task generate`. Never patch generated output
by hand. The store is handwritten: when you add an endpoint, add its handler to
`getStoreHandlers` in `ui/src/mocks/store/handlers.ts`.

## Changing this document

Update this file in the same change that establishes a pattern others must
follow, or that changes one described here.
