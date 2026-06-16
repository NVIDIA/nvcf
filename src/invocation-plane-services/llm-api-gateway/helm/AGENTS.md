# AGENTS.md

## Repo Role
- Repo: `llm-api-gateway-colocated-deploy`
- Workspace(s): `self-hosted-nvcf`
- Tier: `chart`
- Owning team: `nvcf`
- Manifest description: Helm chart for LLM API Gateway (llm-api-gateway)

## Routing Rules
- Stay in this repo for Helm templates, values, hooks, chart metadata, and Kubernetes deployment behavior.
- If the request is about application logic, APIs, or binaries running inside the container, route to the paired image-source repo `llm-api-gateway` (see `repos.yaml`).
- If the request is about cross-chart ordering, stage selection, or environment-wide configuration, route to `nvcf-self-managed-stack`.

## Chart Contract
- Chart name: `llm-api-gateway`
- Primary values root: `llmApiGateway`
- Runtime configuration is driven through a ConfigMap (`envFrom`) with optional secrets for Redis password and NVCF auth token.
- A bundled Redis sidecar handles rate limiting; its lifecycle is managed within this chart.

## Working In This Repo
- Search for existing patterns before adding new structure.
- Keep changes scoped to the owning repo once routing is confirmed; only fan out when the workspace docs show an explicit dependency.
- Preserve backwards-compatible values where practical.
- Keep templates minimal and explicit; avoid clever templating that obscures the runtime contract.
- Do not hardcode self-managed environment specifics here when they belong in the stack repo.

## Completion Expectations
- If you change cross-repo behavior, mention the adjacent repo(s) that may also need follow-up.
