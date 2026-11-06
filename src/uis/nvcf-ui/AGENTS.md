# AGENTS.md

Guidance for AI agents working in nvcf-ui, the NVCF admin panel: a React SPA
(Vite, TanStack Router and Query, Tailwind 4, KUI, Orval, MSW, Vitest) served by
a Go backend for frontend.

## Start sessions here

Start the agent in `src/uis/nvcf-ui` when the work is in this app.

A session started at the monorepo root still loads this file and the skills
below, the first time it touches a file here. It does not load
`.claude/settings.json`: Claude Code reads project settings only from the
directory the session started in. Without it you lose the permission allowlist
and the format-on-edit hook. Nothing else depends on it.

## Read before changing structure

- `ARCHITECTURE.md`: layers, placement rules, routing, data loading, errors,
  and mocking.
- `README.md`: setup, Bazel targets, and dev modes.
- The root `AGENTS.md`: monorepo, commit, and pull request conventions.

Add any new topic document to this list in the same change. Update
`ARCHITECTURE.md` in the same change that adds or changes a pattern others must
follow.

## Commands

Run from this directory.

```sh
task setup             # bootstrap a fresh clone
task ui:dev MOCK=true  # dev server with mocked APIs (see the run skill)
task generate          # regenerate hooks, models, and mocks from spec/
task lint:fix          # fix lint, formatting, and license headers
task verify            # lint, license, typecheck, and tests (UI and backend)
```

Run `task verify` before finishing. CI runs the equivalent Bazel targets.

## Skills

| Skill | Use for |
| --- | --- |
| `add-feature` | New pages and features, where code goes, the completion checklist |
| `routing` | Route files, loaders, search and path params, navigation, official TanStack skills |
| `testing` | Vitest, Testing Library, MSW, and how to render pages and components |
| `codegen` | OpenAPI specs, `task generate`, MSW scenarios |
| `run` | Starting the dev server |
| `kaizen-ui` | KUI components, layout, and styling |

Skill sources live in `.agents/skills/`. `.claude/skills/` and
`.cursor/skills/` hold one symlink per skill. To add a skill, create
`.agents/skills/<name>/SKILL.md` and add both symlinks
(`ln -s ../../.agents/skills/<name> .claude/skills/<name>`, same for
`.cursor`). `kaizen-ui` is synced from the KUI package on `pnpm install` and is
gitignored.

## Project structure

```text
ui/src/
├── routes/            # pages: one route file per URL (TanStack file-based routing)
├── routeTree.gen.ts   # generated route tree, committed, never edited
├── router.tsx         # router setup and app-wide defaults
├── main.tsx           # entry point, mounts providers
├── features/          # domain code, one folder per domain
├── components/        # shared, domain-agnostic UI
├── hooks/             # shared, domain-agnostic hooks
├── lib/               # fetch client, queryClient
├── utils/             # pure helpers
├── types/             # shared types
├── generated/         # Orval output, never edited
├── mocks/             # MSW store, handlers, scenarios
└── testing/           # render helpers
```

## Core rules

- Layers: pages in `routes/` compose features; features never import other
  features or `routes/`; shared code imports neither. See `ARCHITECTURE.md`.
- Place code by what it is: page scaffolding in the route file, domain
  building blocks in their feature, domain-agnostic code in shared folders.
- Routes orchestrate data: a route's loader starts the queries its page
  needs, and components read them. See `ARCHITECTURE.md`.
- TypeScript is strict. No `any`, no non-null assertions, no disabling strict
  checks. Use `import type` for type-only imports.
- Import anything outside the current folder with the `~/` alias (maps to
  `ui/src/`), including within a feature. `./` is fine for files in the same
  folder. Never use `../`.
- TanStack Query owns server state. There is no global store.
- Never edit `ui/src/generated/` or `ui/src/routeTree.gen.ts`.
- Prefer KUI components from `@nvidia/foundations-react-core` over plain HTML.
  KUI vendor CSS lives in `ui/vendor/kui-foundations/`; run
  `task ui:vendor-css` after bumping the package.
- Tests are required for new or changed components.
- Every source file needs the Apache 2.0 license header. `task lint:fix` adds
  it.

## Backend

The Go BFF in `backend/` is part of this application. It authenticates
requests, proxies them upstream, and serves the built SPA.

- Self-contained Go module with `cmd/` and `internal/`.
- `task backend:test` and `task backend:lint` run `go test ./...` and
  golangci-lint. `task verify` covers both.
- Standard Go conventions; no project-specific style beyond golangci-lint.

## Dependencies

Follow the root `AGENTS.md` and `license-compliance.md`. For the UI:

1. Check the license against `.allowed-licenses.txt`. CI does not check npm
   licenses, so this is manual. Report anything not on the list.
2. Add with `pnpm add [-D]` from `ui/` and commit `package.json` and
   `pnpm-lock.yaml`.
3. Regenerate the root `dependencies.md` from the repo root:
   `GOWORK=off go run -C ./tools/collect-dependencies .` (CI fails when it is
   stale).
4. Name each added or updated dependency and version in the commit body and
   the pull request.

## Pull requests

- Use `.github/PULL_REQUEST_TEMPLATE.md` and link a public `dsx-ai-factory/nvcf`
  issue.
- Conventional Commit titles with scope `nvcf-ui`. `feat`, `fix`, and `perf`
  create a release; `refactor`, `chore`, `docs`, and `test` do not.
- Sign off every commit (`git commit -s`).

## Review priorities

- Code in the wrong layer: domain building blocks in a route file, scaffolding
  in a feature, or code parked in `ui/src/` root.
- A feature importing another feature or `routes/`.
- Page data loaded somewhere other than the route's loader.
- A per-route `errorComponent`, or local handling of an `HttpError` the global
  fallback covers.
- Generated code edited by hand, or a hand-written `fetch` where a generated
  hook exists.
- Page components with a `Page` suffix instead of a view-type name such as
  `FunctionsList`.
- Plain HTML where a KUI component exists, or missing KUI accessibility.
- `any`, a non-null assertion, or a widened type used to silence `tsc`.
- A `../` import instead of `~/`.
- Tests that assert implementation rather than user-visible behavior, or a
  changed component without tests.
