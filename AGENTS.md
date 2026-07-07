# AGENTS.md - Guide for AI Coding Agents

Quick reference for NVCF (NVIDIA Cloud Functions) in this repository.

## Repo Layout

This repo is an umbrella layout: upstream services appear as ordinary
directories arranged under `src/`, `deploy/`, `infra/`, and `migrations/`
according to `imports.yaml`. Goal: over time, land and maintain code here
natively while upstream-owned sources are still tracked by commit pin. Tooling
lives under `tools/` and `tests/`.

Use `python3`, not `python`, when Python is needed. Use the nearest nested `AGENTS.md` for subtree-specific guidance.

Useful pointers:
- `BAZEL.md` for the contributor-facing Bazel build path
- `tools/AGENTS.md` for repo tooling
- `deploy/helm/AGENTS.md` for native Helm chart guidance
- `migrations/AGENTS.md` for native migration image guidance
- `imports.yaml` for subtree ownership and commit pins
- `.cursor/skills/documentation-style/SKILL.md` for docs style
- `.cursor/skills/nvcf-gitlab-subproject-ci/SKILL.md` for native subproject CI
  (read before editing `.gitlab-ci.yml`, `subproject-validations.yaml`, or
  `generated-release-jobs.yml`)
- `.cursor/skills/` for root dev-skill symlink fanout
- `ai-tooling/user/skills/` and `ai-tooling/dev/skills/` for public skills

If a referenced skill is outdated, update it before finishing.

## Cross-repo and stack routing

Use `imports.yaml` to decide whether a subtree is monorepo-native or still
owned by an upstream repo. Native subprojects are edited here. Upstream-owned
subtrees usually need the change in the upstream repo and a later commit-pin
update.

For self-managed stack ownership, deployment order, chart/image-source mapping,
or "which subtree owns this" questions, use:
- `.cursor/skills/nvcf-explore-stack/SKILL.md` for the in-repo Helmfile,
  dependency, chart, hook, and image-source map
- the `nvcf/nvcf-internal` repository when private workspace routing metadata
  is relevant

Do not copy workspace routing boilerplate into subtree `AGENTS.md` files. Local
guidance should only record subtree-specific ownership exceptions, adjacent
subtrees that commonly need follow-up, and commands that must be run from that
subtree.

## OSS Snapshot Hygiene

Treat every file matched by the root or nested `.oss-allowlist` files as public
GitHub content. Do not add private tracker IDs, private bug IDs, private
merge-request links or ref names, internal hostnames or URLs, private service
names, registry endpoints, vault endpoints, or debugging context that external
readers cannot access.

Keep private context in the internal Merge Request/Pull Request description
outside the public commit section, in the `nvcf/nvcf-internal` repository, or
in a non-allowlisted runbook. If a change requires public wording, generalize
it to the user-visible behavior and remove the private evidence trail from the
allowlisted file.

## Local QA and Testing Environment Safety

For new local QA and testing, including one-click or self-hosted install
validation, treat stale local state as a test hazard. Start from a fresh
worktree at the target ref, inventory existing `k3d`, `kubectl`, Helm, and
local artifact state, then ask the user whether cleanup or a net-new isolated
environment is appropriate. Never delete clusters, Helm releases, worktrees,
secrets, or artifact directories without explicit user confirmation.

Use the `nvcf/nvcf-internal` repository for detailed internal k3d workflows.
If creating a net-new environment, use unique cluster names, ports, Helmfile
environments, secrets files, CLI configs, and artifact directories.

## GitLab CI Manual Actions

Treat manual GitLab CI actions as remote write operations. Before triggering a
manual job, retrying or restarting a job or pipeline, canceling CI, or using a
push/API side effect to retrigger CI, ask the user for explicit approval in the
current thread. Do this even when the user asks an agent to fix CI, restart a
pipeline or job, get a release unstuck, or investigate a failed pipeline.

The approval request must name the exact project, ref, pipeline or job name and
ID when available, and the expected side effect. Do not treat a broad request
such as "fix CI", "restart the pipeline", or "get this released" as permission
to press a manual CI button or retrigger CI.

## Writing AGENTS.md Files

Every subtree that an agent may work in should have its own `AGENTS.md` with build commands, test commands, code style, and any subtree-specific conventions. Keep each file under 400 lines; split into separate docs or skills when it grows past that.

`AGENTS.md` is the source of truth for agent guidance. Cursor and Codex read `AGENTS.md` directly. Claude Code reads `CLAUDE.md`, so every directory that has an `AGENTS.md` also has a sibling `CLAUDE.md` that is a regular file containing the single line `@AGENTS.md`. That import line tells Claude Code to load the adjacent `AGENTS.md`, so all three tools end up on the same content. When creating a new `AGENTS.md`, create the companion `CLAUDE.md` in the same commit: `printf '@AGENTS.md\n' > CLAUDE.md`. Do not use a symlink, and never put unique content in `CLAUDE.md`.

## Skills

Skills are reusable, on-demand agent instructions for specific workflows. They follow the [Agent Skills specification](https://agentskills.io/specification) and are compatible with the [Vercel Skills CLI](https://github.com/vercel-labs/skills). Skills are invoked when relevant, not auto-applied (auto-applied guidance belongs in rules, not skills).

Keep durable skills focused on current behavior, stable prerequisites, and reusable workflows. Do not put in-progress Merge Request/Pull Request tables, merge-order checklists, branch-specific references, or temporary cross-repo coordination status in skills. Put that information in Merge Request/Pull Request descriptions, comments on the ticket, or temporary runbooks instead.

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

For public skills under `ai-tooling/user/skills/` or `ai-tooling/dev/skills/`,
follow `ai-tooling/AGENTS.md`. That file is the full public skill authoring contract
and includes the required public metadata fields.

### Where skills live

Skills are split by visibility and audience:

- `ai-tooling/user/skills/`: public user-facing NVCF skills.
- `ai-tooling/dev/skills/`: public developer workflow skills.
- Private skill source trees follow the same `user/skills/` and `dev/skills/`
  split in the `nvcf/nvcf-internal` repository.
- `.cursor/skills/`: root dev-skill fanout only. Each entry is a symlink to a
  public dev skill source directory in this repository.

Cross-tool symlinks make root dev skills available to all agents. The root fanout directories must contain symlinks only, never source skill directories or regular skill files:
- `.cursor/skills/<name>` -> symlink to the dev skill source directory.
- `.codex/skills/<name>` -> symlink to the same dev skill source directory.
- `.claude/skills/<name>` -> symlink to the same dev skill source directory.

The project hook `.cursor/hooks/validate-skill-fanout.py` audits this before an agent finishes. If it reports a fanout error, fix the symlinks or source placement before responding.

When adding a skill:
1. Decide visibility (public or private) and audience (`user/skills` or `dev/skills`).
2. Create the `SKILL.md` with valid frontmatter.
3. For root-wide dev skills, create matching `.cursor/skills/<name>`, `.codex/skills/<name>`, and `.claude/skills/<name>` symlinks to the same source directory.
4. Update the relevant public or private skills table.

### Where hooks live

Hooks follow the same source-and-fanout pattern as root dev skills. The root
hook directories are agent-facing fanouts only; hook implementation scripts
must live in their owning public source tree in this repository.

- `ai-tooling/dev/hooks/`: public hook source scripts.
- `.cursor/hooks/`: Cursor hook script fanout only. Entries must be symlinks.
- `.codex/hooks/`: Codex hook script fanout only. Entries must be symlinks.
- `.claude/hooks/`: Claude hook script fanout only. Entries must be symlinks.

Tool-specific hook config files, such as `.cursor/hooks.json`,
`.codex/hooks.json`, `.codex/config.toml`, and `.claude/settings.json`, may be
regular files. Hook implementation scripts must live in `ai-tooling/dev/hooks/`
and be exposed through matching symlinks in all three root hook fanouts.

Internal-only stop hooks and private snapshot hygiene checks live in the
`nvcf/nvcf-internal` repository. Still follow the OSS Snapshot Hygiene policy
manually before finishing.

### Public skills

| Skill | Location | Purpose |
|-------|----------|---------|
| `bazel-go-gazelle` | `ai-tooling/dev/skills/` | Wire Go modules into Bazel with rules_go and Gazelle |
| `bazel-java-maven` | `ai-tooling/dev/skills/` | Wire Java and Spring Boot services into Bazel with Maven artifacts |
| `bazel-gitlab-child-pipelines` | `ai-tooling/dev/skills/` | Generic Bazel parent-child pipeline pattern; for this repo prefer `nvcf-gitlab-subproject-ci` |
| `bazel-monorepo-bootstrap` | `ai-tooling/dev/skills/` | Bootstrap Bazel in an existing polyglot monorepo |
| `bazel-oci-images` | `ai-tooling/dev/skills/` | Build multi-arch OCI images from Bazel binaries |
| `bazel-rust-crate-universe` | `ai-tooling/dev/skills/` | Wire Rust services into Bazel with crate_universe |
| `nvcf-gitlab-subproject-ci` | `ai-tooling/dev/skills/` | Native subproject CI via generated child pipeline (not root `.gitlab-ci.yml`) |
| `documentation-style` | `ai-tooling/dev/skills/` | NVCF documentation conventions (no bold, no emojis, no em-dash) |
| `nvcf-explore-stack` | `ai-tooling/dev/skills/` | Navigate the self-hosted stack topology and dependency graph |
| `official-docs-style` | `ai-tooling/dev/skills/` | External-facing NVCF user documentation voice and structure |
| `nvcf-self-managed-cli` | `ai-tooling/user/skills/` | Install, operate, and manage self-managed NVCF through `nvcf-cli` |
| `nvcf-self-managed-installation` | `ai-tooling/user/skills/` | Install and deploy the self-managed NVCF stack |
| `nvcf-self-managed-prerequisite` | `ai-tooling/user/skills/` | Install cluster-level prerequisites such as KAI Scheduler and SMB CSI driver |

## GitLab CI for native subprojects

The umbrella parent pipeline (`.gitlab-ci.yml`) is not where native service
build, test, helm-lint, or release validation jobs go.

| Job kind | Where to declare it |
|---|---|
| `<id>-bazel`, Go checks, custom `checks:` | `tools/ci/subproject-validations.yaml` (runs in `subproject-validations` child pipeline) |
| Image push, semantic-release, helm publish | `release:` in same YAML, output in `tools/ci/generated-release-jobs.yml` |
| License scan, bazel-smoke, docs, OSS snapshot | root `.gitlab-ci.yml` only |

Before adding or moving any CI job for a path under `src/`, `deploy/helm/`, or
`migrations/`, read `nvcf-gitlab-subproject-ci` (`.cursor/skills/` symlink).
Do not add per-service jobs to root `.gitlab-ci.yml` and do not recreate
`src/**/.gitlab-ci.yml`, `deploy/helm/**/.gitlab-ci.yml`, or
`migrations/**/.gitlab-ci.yml` for native subprojects. Helm CI-only values
belong in `tools/ci/helm-validate-values/`, not under chart `ci/` dirs.

## Commit Messages

Use Conventional Commits v1.0.0. Include issue references in the footer when required by the change type; use `NO-REF` only when acceptable.

Format:

```
<type>(<scope>): <short description>

[optional body]
```

Types with customer impact (appear in release notes): `feat`, `fix`, `perf` (scope required).
Foundational types (not in release notes): `docs`, `build`, `test`, `refactor`, `ci`, `chore`, `style`, `revert`.

When a commit adds or updates a third-party dependency, call out the dependency name and version in the body so reviewers can assess license and security impact.

## Merge Requests/Pull Requests

Use Conventional Commit format for Merge Request/Pull Request titles, as
described in the Commit Messages section. Release automation depends on this
format. Examples: `feat:`, `fix:`, `chore:`, `docs:`.

Before creating a Merge Request/Pull Request, confirm there is a bug, issue, or
ticket reference. If none was provided, ask for one. Use `None` or `NO-REF` only
after explicit confirmation that no issue exists or one is not required.

Use a structured Merge Request/Pull Request description. Subtree repos may define
their own Merge Request/Pull Request template; fall back to this shape when none
exists. Do not include a test plan checklist unless explicitly requested.

Every Merge Request/Pull Request description must explain why the change is
needed, not only what changed. Include enough context that a reviewer can
understand the motivation without doing detective work. Always include:
- the problem, requirement, review comment, or CI blocker driving the change
- what changed and how the changed pieces connect
- links to upstream Merge Requests/Pull Requests, tickets, bugs, or related
  reviews when relevant

```
## Why
<context and motivation for the change>

## What changed
<high-level summary of changes, not a commit log>

## Customer Release Notes
<short customer-facing summary for feat/fix/perf, or "Not customer visible">

## Plan Summary
<resource summary for infrastructure, chart, deploy, or Terraform-like changes; otherwise "Not applicable">

## Usage
<relevant commands or operator notes, or "Not applicable">

## Testing
<what you ran and whether QA is needed>

## Notes
<caveats, follow-up work, or reviewer context>

## References
<bug, issue, or ticket links at the bottom, or "None">

## Related Merge Requests/Pull Requests
<links or "None">

## Dependencies
<new or updated third-party packages, license review result, NOTICE update status, or "None">
```

## Cross-subtree impact

NVCF clients in this repo (notably `src/clis/nvcf-cli`) are hand-written against control-plane and invocation-plane APIs. When changing public API surfaces (request/response shapes, auth flow, new endpoints, removed fields), evaluate whether `src/clis/nvcf-cli` needs a matching change, and list affected clients in the Merge Request/Pull Request "Related Merge Requests/Pull Requests" section. If the CLI needs an update, file a follow-up issue.

## Tests

Code changes must include tests. If a change does not include tests, explain why in the Merge Request/Pull Request description (for example: pure documentation, CI-only change, or refactor with full existing coverage).

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
- The Merge Request/Pull Request description calls out the observability impact if dashboards or alerts may need updating.

## Diagrams

When a change modifies runtime behavior, data flow, or component interactions, ask whether architecture or sequence diagrams need updating. If the change modifies an existing documented flow, update that diagram instead of creating a conflicting copy. Prefer ASCII or Mermaid; avoid binary image formats when text is sufficient.

## Issue Tracking

Reference related issues in commits and Merge Request/Pull Request descriptions. When creating follow-up work, file a ticket rather than leaving a TODO in code.
