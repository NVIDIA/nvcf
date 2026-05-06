# AGENTS.md - Guide for AI Coding Agents

Quick reference for NVCF (NVIDIA Cloud Functions) in this repository.

## Repo Layout

This repo is an umbrella layout: upstream services appear as ordinary directories (synthetic imports), arranged under `src/`, `deploy/`, `infra/`, and `migrations/` according to `imports.yaml`. Goal: over time, land and maintain code here natively; synthetic imports are a bridge while sources still live in separate GitLab projects. Tooling lives under `tools/` and `tests/`.

Use `python3`, not `python`, when Python is needed. Use the nearest nested `AGENTS.md` for subtree-specific guidance.

Useful pointers:
- `tools/AGENTS.md` for repo tooling
- `.cursor/skills/add-synthetic-import/SKILL.md` for synthetic imports
- `.cursor/skills/documentation-style/SKILL.md` for docs style
- `.cursor/skills/` for root dev-skill symlink fanout
- `ai-tooling/user/skills/` and `ai-tooling/dev/skills/` for public skills
- `nvidia-internal/user/skills/` and `nvidia-internal/dev/skills/` for private skills

If a referenced skill is outdated, update it before finishing.

## Writing AGENTS.md Files

Every subtree that an agent may work in should have its own `AGENTS.md` with build commands, test commands, code style, and any subtree-specific conventions. Keep each file under 400 lines; split into separate docs or skills when it grows past that.

`AGENTS.md` is the source of truth for agent guidance. Cursor and Codex read `AGENTS.md` directly. Claude Code reads `CLAUDE.md`, so every directory that has an `AGENTS.md` also has a sibling `CLAUDE.md` that is a regular file containing the single line `@AGENTS.md`. That import line tells Claude Code to load the adjacent `AGENTS.md`, so all three tools end up on the same content. When creating a new `AGENTS.md`, create the companion `CLAUDE.md` in the same commit: `printf '@AGENTS.md\n' > CLAUDE.md`. Do not use a symlink, and never put unique content in `CLAUDE.md`.

## Skills

Skills are reusable, on-demand agent instructions for specific workflows. They follow the [Agent Skills specification](https://agentskills.io/specification) and are compatible with the [Vercel Skills CLI](https://github.com/vercel-labs/skills). Skills are invoked when relevant, not auto-applied (auto-applied guidance belongs in rules, not skills).

### Skill structure

Each skill is a directory named to match its `name` frontmatter field, containing at minimum a `SKILL.md`. Names must be lowercase with hyphens only, no leading/trailing/consecutive hyphens.

```
skill-name/
    SKILL.md              # Required (under 500 lines)
    README.md             # Optional: overview and usage
    examples.md           # Optional: detailed examples
    references/           # Optional: reference docs
    scripts/              # Optional: helper scripts
    assets/               # Optional: images, diagrams
```

### SKILL.md frontmatter

```yaml
---
name: skill-name
description: >-
  What the skill does and when to use it.
  Include trigger keywords for discoverability.
version: "1.0.0"
tags:
  - nvcf
  - relevant-tag
tools:
  - Shell
  - Read
---
```

The `description` must say both what the skill does (actions it enables) and when to use it (trigger phrases, keywords). This is how agents decide whether to invoke the skill.

### Where skills live

Skills are split by visibility and audience:

- `ai-tooling/user/skills/`: public user-facing NVCF skills.
- `ai-tooling/dev/skills/`: public developer workflow skills.
- `nvidia-internal/user/skills/`: private user-facing NVCF skills.
- `nvidia-internal/dev/skills/`: private developer, release-engineering, and monorepo-maintenance skills.
- `.cursor/skills/`: root dev-skill fanout only. Each entry is a symlink to a public dev source under `ai-tooling/dev/skills/` or a private dev source under `nvidia-internal/dev/skills/`.

Cross-tool symlinks make root dev skills available to all agents:
- `.cursor/skills/<name>` -> symlink to the dev skill source directory.
- `.codex/skills/<name>` -> symlink to the same dev skill source directory.
- `.claude/skills/<name>` -> symlink to the same dev skill source directory.

When adding a skill:
1. Decide visibility (`ai-tooling` public or `nvidia-internal` private) and audience (`user/skills` or `dev/skills`).
2. Create the `SKILL.md` with valid frontmatter.
3. For root-wide dev skills, create matching `.cursor/skills/<name>`, `.codex/skills/<name>`, and `.claude/skills/<name>` symlinks to the same source directory.
4. Update the relevant public or private skills table.

### Public skills

| Skill | Location | Purpose |
|-------|----------|---------|
| `documentation-style` | `ai-tooling/dev/skills/` | NVCF documentation conventions (no bold, no emojis, no em-dash) |
| `nvcf-explore-stack` | `ai-tooling/dev/skills/` | Navigate the self-hosted stack topology and dependency graph |
| `nvcf-self-managed-cli` | `ai-tooling/user/skills/` | Manage self-managed NVCF through `nvcf-cli` |
| `nvcf-self-managed-installation` | `ai-tooling/user/skills/` | Install and deploy the self-managed NVCF stack |

Private skill inventory lives in `nvidia-internal/AGENTS.md`.

## Commit Messages

Use Conventional Commits v1.0.0. Include a JIRA or NVBug reference in the footer when one exists; use `NO-REF` only when acceptable for the change type.

Format:

```
<type>(<scope>): <short description>

[optional body]

JIRA: NVCF-XXXX
NVBug: none
```

Types with customer impact (appear in release notes): `feat`, `fix`, `perf` (scope required).
Foundational types (not in release notes): `docs`, `build`, `test`, `refactor`, `ci`, `chore`, `style`, `revert`.

When a commit adds or updates a third-party dependency, call out the dependency name and version in the body so reviewers can assess license and security impact.

## Merge Requests

Use a structured MR description. Subtree repos may define their own MR template; fall back to this shape when none exists.

```
## Summary
<high-level explanation of the change and why it exists>

## Customer Release Notes
<short customer-facing summary for feat/fix/perf, or "Not customer visible">

## Testing
<what you ran and whether QA is needed>

## Related Issues
JIRA: <id or none>
NVBug: <id or none>

## Related MRs
<links or "None">

## Dependencies
<new or updated third-party packages, license review result, NOTICE update status, or "None">
```

## Cross-subtree impact

NVCF clients in this repo (notably `src/clis/nvcf-cli`) are hand-written against control-plane and invocation-plane APIs. When changing public API surfaces (request/response shapes, auth flow, new endpoints, removed fields), evaluate whether `src/clis/nvcf-cli` needs a matching change, and list affected clients in the MR "Related MRs" section. If the CLI needs an update, file a follow-up issue.

## Tests

Code changes must include tests. If a change does not include tests, explain why in the MR description (for example: pure documentation, CI-only change, or refactor with full existing coverage).

Prefer the repo-native test runner (`make test`, `go test`, `cargo test`, etc.). Run tests before committing. Check coverage requirements in the subtree `AGENTS.md` or CI config.

## Code Style

Write self-documenting code. Add comments only when the logic is non-obvious. Match the existing package structure, naming, and error-handling conventions of each subtree instead of imposing a new framework.

Follow the documentation style rules in `.cursor/skills/documentation-style/SKILL.md`:
- No markdown bold for emphasis.
- No emojis.
- No em-dash (U+2014).
- Be succinct. Prefer short sentences and direct wording.
- Use only standard ASCII in committed text.

## Third-Party Dependencies

Before adding a third-party dependency:
1. Verify the license is compatible (check `.allowed-licenses.txt` at the root).
2. Warn the user if the license is not in the allow list.
3. Update `NOTICE` and any license attribution files required by the subtree.

Do not add unmaintained libraries. Do not add a new library for small functionality that can be implemented safely in existing code. Mention the dependency name and version in the commit body.

## Observability

Priority order for contributions: logs, then tracing, then metrics. This is the minimum bar for any service change. If a change touches request handling, all three tiers apply.

### Logs (required)

Every service must produce structured logs. Use the logging library established in the subtree (`logrus` for Go, `zap` for invocation-plan services, `tracing` for Rust). Do not introduce a new logging library without a strong reason.

Required context fields on every log line where available:
- Request ID
- Function ID
- Cluster ID
- Org/NCA ID (when auth context is present)

Log level contract:
- `error`: something failed and needs operator attention. Include the error message, the operation that failed, and enough context to start debugging without grepping other sources.
- `warn`: degraded but recoverable. Rate-limited retries, fallback paths taken, near-quota conditions.
- `info`: normal state transitions. Service startup/shutdown, config reloads, successful deployments, completed queue batches.
- `debug`: internals useful during development. Request/response bodies, cache hits, detailed reconciliation steps. Must be off by default in production.

Do not log secrets, tokens, credentials, or full request bodies containing user data. Redact or omit them.

When logging errors, include the originating error with `%w` or equivalent wrapping so the full chain is visible. Do not log and return the same error (pick one).

### Tracing (required for cross-service paths)

Add OpenTelemetry spans for cross-service calls, queue processing, and any operation that crosses a network boundary. Use the `otelconfig` packages under `src/libraries/go/lib/pkg/otelconfig` when available.

Span naming: use `service.operation` format (for example `nvca.reconcile`, `ratelimiter.check`, `grpc-proxy.forward`). Keep names stable across releases so dashboards and alerts do not break.

Required span attributes:
- `nvcf.function.id` when operating on a function
- `nvcf.cluster.id` when operating on a cluster
- `nvcf.org.id` when auth context is available
- `error` attribute set to `true` on failure, with `otel.status_code` = `ERROR`

When to create spans:
- New span for each inbound request (HTTP handler, gRPC method, queue message).
- New child span for each outbound call (HTTP client, gRPC client, database query, queue publish).
- Do not create spans for pure in-memory computation unless it is a known bottleneck.

Propagate trace context on all outbound HTTP and gRPC calls. Use `W3C Trace Context` headers.

### Metrics (required for request-handling paths)

Use the RED method as the baseline for every request-handling service:
- Rate: request count by endpoint and status.
- Errors: error count by endpoint and error category.
- Duration: latency histogram by endpoint.

Naming: `nvcf_<service>_<metric>_<unit>` (for example `nvcf_ratelimiter_requests_total`, `nvcf_nvca_reconcile_duration_seconds`). Use Prometheus naming conventions: snake_case, `_total` suffix for counters, `_seconds` for durations, `_bytes` for sizes.

Cardinality: keep label sets bounded. Do not use unbounded values (user IDs, request IDs, timestamps) as label values. If a label can take more than ~50 distinct values, reconsider whether it belongs as a label or as a log/trace attribute instead.

Pre-initialize counter metrics to zero so they appear on the first Prometheus scrape. See nvca `internal/metrics/` for the pattern. When adding new label values, update the initialization code and tests. Uninitialized counters cause `absent()` alerts to misfire and `rate()` calculations to produce gaps.

Histogram buckets: use default Prometheus buckets unless the operation has a known latency profile. For sub-millisecond operations, add smaller buckets. For long-running operations (minutes), add larger ones.

### Before merging

When a change affects observability, verify:
- New log lines include the required context fields.
- New spans propagate context and set error attributes on failure.
- New metrics follow the naming convention and are pre-initialized.
- Existing dashboards and alerts are not broken by renamed or removed metrics/spans.
- The MR description calls out the observability impact if dashboards or alerts may need updating.

## Diagrams

When a change modifies runtime behavior, data flow, or component interactions, ask whether architecture or sequence diagrams need updating. If the change modifies an existing documented flow, update that diagram instead of creating a conflicting copy. Prefer ASCII or Mermaid; avoid binary image formats when text is sufficient.

## Issue Tracking

Reference JIRA or NVBug tickets in commits and MR descriptions. When creating follow-up work, file a ticket rather than leaving a TODO in code.
