---
name: testing
description: >
  Writing frontend tests for the nvcf-ui app — Vitest, Testing Library, MSW, and
  project-specific render helpers. Use when creating test files, writing component tests,
  setting up MSW handlers in tests, deciding which render helper to use, or when the user
  is adding or modifying a UI component and hasn't mentioned tests yet (tests are required).
  Covers TypeScript/React only — Go backend tests follow standard Go patterns.
license: Apache-2.0
allowed-tools:
  - Bash
  - Read
  - Edit
---

# Testing

Tests are not optional — when adding or modifying a component, add or update the corresponding tests.

## Gotchas

- Route-level views that use `useSearch`, `useParams`, or loaders **must** use `renderWithRouter` from `~/testing/render`, not the standard `render`. Using standard `render` will fail because route hooks need router context.
- MSW handlers from scenarios should be reused in tests — don't duplicate handler logic. Import from `~/mocks/scenarios/`.
- Test files live alongside the code they test (`WidgetsList.test.tsx` next to `WidgetsList.tsx`), not in a separate `__tests__` directory.

## Which Render Helper to Use

- **Route-level views** → `renderWithRouter` from `~/testing/render` (sets up router, query client, memory history)
- **Standalone components** (no route dependency) → standard `render` from `@testing-library/react`
- **Pure utility functions** → test directly, no render needed

## MSW in Tests

```typescript
import { setupServer } from "msw/node";
import { getMyHandlers } from "~/mocks/scenarios/myFeature/myScenario";

const server = setupServer(...getMyHandlers());

beforeAll(() => server.listen());
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
```

## Guidelines

- Prefer `screen.getByRole`, `screen.getByText`, and other accessible queries over test IDs
- Use `userEvent` over `fireEvent` for realistic interaction simulation
- Test loading states, error states, and empty states — not just the happy path
- For async operations, use `waitFor` or `findBy` queries

## Validate

1. Run `task ui:test` and verify tests pass
2. If tests fail, fix the issues and rerun until clean
