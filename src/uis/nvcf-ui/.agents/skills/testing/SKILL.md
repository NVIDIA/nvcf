---
name: testing
description: >-
  Write frontend tests for the nvcf-ui app with Vitest, Testing Library, MSW,
  and the project's render helper. Use when creating test files, testing a page
  or component, mocking API responses, choosing how to render, or when a UI
  component is added or changed and tests have not been mentioned (tests are
  required). TypeScript and React only; Go backend tests follow standard Go
  practice.
license: Apache-2.0
allowed-tools:
  - Bash
  - Read
  - Edit
---

# Testing

Adding or changing a component means adding or updating its tests.

## How to render

| Testing | Use | Notes |
| --- | --- | --- |
| A page (route file) | `renderWithRouter({ initialLocation: "/functions" })` | Renders the app's generated route tree, so the real loader, search validation, and page run. |
| A component that uses router hooks but is not a route | `renderWithRouter({ routeTree })` with a test-local tree | Build a small root and stub routes in the test. See `AccountSwitcher.test.tsx`. |
| A plain component | `render` from `@testing-library/react` | Pass data through props. |
| Pure logic | A plain unit test | No rendering. |

- `renderWithRouter` lives in `~/testing/render`. It creates a fresh
  `QueryClient`, memory history, and router context.
- Never mock `@tanstack/react-router`. Render a real router instead.
- The helper sets only the router options tests assert on (today
  `defaultErrorComponent`). Add another option only when a test needs it.

## Where tests live

- Next to the file they test: `FunctionCard.test.tsx` beside
  `FunctionCard.tsx`. Never in a `__tests__` folder.
- Page tests sit beside their route file and are named after the page
  component: `ui/src/routes/functions/FunctionDetail.test.tsx`. Route
  generation ignores `*.test.*` files.

## Mocking API responses

`ui/vitest.setup.ts` starts one MSW server backed by the seeded mock store and
calls `resetHandlers()` after each test.

Never call `setupServer` in a test file. A local server replaces the global one,
loses the store baseline, and fails on any endpoint you did not mock.

Layer overrides on top with `server.use(...)`:

```typescript
import { server } from "~/mocks/server";
import { handlers } from "~/mocks/scenarios/functions/empty-list";

beforeEach(() => {
  server.use(...handlers);
});
```

Scenario files export a `handlers` array. Reuse them instead of duplicating
handler logic.

## Guidelines

- Prefer accessible queries (`getByRole`, `getByText`) over test IDs.
- Use `userEvent` over `fireEvent`.
- Cover the happy path, error state, and empty state.
- Use `findBy` queries or `waitFor` for async results.
- Assert user-visible behavior, not implementation details.

## Validate

1. Run `task verify`.
2. Fix failures and rerun until clean.
