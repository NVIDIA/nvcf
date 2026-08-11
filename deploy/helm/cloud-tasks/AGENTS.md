# AGENTS.md

## Repo Role
- Repo: `nvct-api-colocated-deploy`
- Workspace(s): `self-hosted-nvcf`
- Tier: `chart`
- Team: `@NVIDIA/nvcf-dev`
- Default owner: `@NVIDIA/nvcf-dev`.
- Manifest description: Helm chart for NVCT API / nvct-service (helm-nvcf-nvct-api)

## Use `nvcf-agentic-dev` As The Routing Layer
Before making changes, use the `nvcf-agentic-dev` workspace repo to confirm whether this repo is actually the right place for the task. Treat that repo as the source of truth for workspace membership, repo ownership, deployment dependencies, and available agent skills.

Check these files first when they exist in your local workspace:
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/repos.yaml`: repo ownership and workspace membership
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/skills.yaml`: related agent skills and sourced commands
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/docs/deployment-sequence.md`: deployment order and stage gates
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/docs/deployment-dependencies-with-links.yaml`: release dependencies and upstream/downstream links

## Routing Rules
- Stay in this repo for Helm templates, values, hooks, chart metadata, and Kubernetes deployment behavior.
- If the request is about application logic, APIs, jobs, or binaries running inside the container, route to the paired image-source repo (`nvct-service`).
- If the request is about cross-chart ordering, stage selection, or environment-wide configuration, route to `nvcf-self-managed-stack`.
- OpenBao paths and JWT roles for this service are provisioned in `nvcf-openbao-migrations` (see `migrations/18_setup_nvct.sh`).

## Working In This Repo
- Read this repo's top-level `README*`, build files, and CI config before making assumptions about language or tooling.
- Search for existing patterns with `rg` before adding new structure.
- Keep changes scoped to the owning repo once routing is confirmed; only fan out when the workspace docs show an explicit dependency.

## Completion Expectations
- Validate with the repo-native command set if one exists (`make`, Maven, Helm, npm, etc.).
- If you change cross-repo behavior, mention the adjacent repo(s) that may also need follow-up.
- In your final summary, state that routing was confirmed through `nvcf-agentic-dev` and name the workspace context used.

## Commit Types for Version Bumps

Match the conventional commit type to the version bump type:

- `fix(<scope>):` for patch version bumps (e.g., 1.2.0 -> 1.2.1)
- `feat(<scope>):` for minor version bumps (e.g., 1.2.0 -> 1.3.0)
- `feat(<scope>)!:` or `BREAKING CHANGE:` footer for major version bumps (e.g., 1.2.0 -> 2.0.0)

The CI tag generation pipeline uses the commit type to determine the release level.
Using `chore` or other non-`fix`/`feat` types will skip tag creation entirely.
