# AGENTS.md

## Repo Role
- Repo: `nvcf-invocation-service`
- Workspace(s): `self-hosted-nvcf`
- Tier: `image-source`
- Team: `@NVIDIA/nvcf-dev`
- Default owner: `@NVIDIA/nvcf-dev`.
- Manifest description: Invocation service application source

## Use `nvcf-agentic-dev` As The Routing Layer
Before making changes, use the `nvcf-agentic-dev` workspace repo to confirm whether this repo is actually the right place for the task. Treat that repo as the source of truth for workspace membership, repo ownership, deployment dependencies, and available agent skills.

Check these files first when they exist in your local workspace:
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/repos.yaml`: repo ownership and workspace membership
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/skills.yaml`: related agent skills and sourced commands
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/docs/deployment-sequence.md`: deployment order and stage gates
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/docs/deployment-dependencies-with-links.yaml`: release dependencies and upstream/downstream links

## Routing Rules
- Stay in this repo for application logic, APIs, migrations, worker behavior, container build logic, and tests for the shipped image.
- If the request is only about Helm values, templates, hook jobs, or Kubernetes manifests, route to the colocated chart repo.
- If the request is about environment composition or multi-service rollout ordering, route to `nvcf-self-managed-stack`.

## Working In This Repo
- Read this repo’s top-level `README*`, build files, and CI config before making assumptions about language or tooling.
- Search for existing patterns with `rg` before adding new structure.
- Keep changes scoped to the owning repo once routing is confirmed; only fan out when the workspace docs show an explicit dependency.

## Completion Expectations
- Validate with the repo-native command set if one exists (`make`, Maven, Helm, npm, etc.).
- If you change cross-repo behavior, mention the adjacent repo(s) that may also need follow-up.
- In your final summary, state that routing was confirmed through `nvcf-agentic-dev` and name the workspace context used.
