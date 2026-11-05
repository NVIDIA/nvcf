---
name: add-feature
description: >-
  Add new functionality to the nvcf-ui app: a new page, a new domain feature
  folder, or an extension of an existing feature. Covers deciding where code
  goes, scaffolding, wiring a route, tests, mocks, and the completion checklist.
  Use when building a new screen, view, or page, adding a feature folder, or
  deciding whether to extend an existing feature, even if the user does not
  say "feature".
license: Apache-2.0
---

# Adding a feature

Read `ARCHITECTURE.md` first. It defines the layers this procedure follows:
pages in `ui/src/routes/`, domain code in `ui/src/features/`, shared code in
`ui/src/components/`, `ui/src/hooks/`, `ui/src/lib/`, and `ui/src/utils/`.

## Steps

1. Decide what you are adding.
   - A new domain (new data, concepts, or API surface): create
     `ui/src/features/<domain>/`.
   - More of an existing domain: extend that feature folder.
   - A new page: add a route file. A page is not a feature; it composes
     features.
2. Check for an existing component or hook before writing a new one.
3. If an endpoint has no generated hook, update the spec and regenerate. Use
   the `codegen` skill.
4. Put each piece where it belongs:
   - Domain building blocks (cards, badges, panels, domain hooks, constants,
     types) go in the feature: `components/`, `hooks/`, `types/`, and
     `constants.ts`, created only when needed.
   - Page scaffolding (heading, layout, composition, table columns, URL-state
     wiring) goes in the route file.
   - Domain-agnostic code needed by several features graduates to shared code.
5. Do not import one feature from another. Compose features in the route file.
6. Add the route with the `routing` skill. Add a nav entry in
   `ui/src/routes/__root.tsx` for a new top-level page.
7. For UI, use the `kaizen-ui` skill. Prefer KUI components over plain HTML.
8. Add tests with the `testing` skill.
9. Add MSW scenarios for key states (empty, error, a status that changes the
   view) in `ui/src/mocks/scenarios/<feature>/`. See the `codegen` skill.
10. Import anything outside the current folder with `~/`, including within a
    feature. `./` is fine for files in the same folder; never use `../`.

## Examples

- Page: `ui/src/routes/functions/index.tsx`
- Feature: `ui/src/features/functions/`
- Page composing several features: `ui/src/routes/index.tsx`

## Validate

1. Run `task lint:fix` to apply formatting and license headers.
2. Run `task verify`.
3. Fix failures and rerun until clean.
