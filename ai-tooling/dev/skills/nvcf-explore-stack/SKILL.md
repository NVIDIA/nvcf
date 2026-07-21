---
name: nvcf-explore-stack
description: >-
  Navigate and explain the NVCF self-hosted stack inside the monorepo. Maps
  helmfile releases to their charts, image-source subtrees, helm hooks,
  namespaces, and `needs:` dependency chains. Reads
  deploy/stacks/self-managed/helmfile.d/*.yaml.gotmpl and
  deploy/stacks/nvcf-compute-plane/helmfile.d/*.yaml.gotmpl as the source of
  truth for ordering and versions, with imports.yaml for upstream provenance.
  Use when a user or developer asks "what deploys X", "what does X depend
  on", "what hooks run for X", "walk me through deployment order", "which
  subtree do I edit to change X", "what namespaces does the stack use", or
  mentions stack topology, helmfile stages, deployment DAG, or dependency map.
license: Apache-2.0
compatibility: Requires a local checkout of the NVCF monorepo with deploy/stacks/self-managed/ and deploy/stacks/nvcf-compute-plane/ present
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags: [nvcf, self-managed, self-hosted, helmfile, deployment, stack-topology]
tools: [Read, Grep, Glob]
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0.0"
  tags: [nvcf, self-managed, self-hosted, helmfile, deployment, stack-topology, dependencies, hooks]
  languages: [yaml, markdown]
  frameworks: [helmfile, helm, kubernetes]
  domain: cloud-infrastructure
---

# NVCF Explore Stack

Help a user or developer navigate the NVCF self-hosted stack inside the monorepo. Identify what a release is, what installs it, what it depends on, and which subtree owns the chart and the runtime image.

## Instructions

This skill is for both NVCF users and NVCF developers. Users use it to understand stack dependencies, deployment order, and CI/CD migration questions. Developers use it to find the owning chart, source subtree, hook, or helmfile stage before making changes.

Use this skill long enough to answer the question, then hand off to the right execution skill. Always cite the source file path so the user can verify.

## Required inputs

Read these from the monorepo root (the directory containing `imports.yaml`):

Authoritative (always read first when answering):

- `deploy/stacks/self-managed/helmfile.d/000-prepare.yaml.gotmpl`
- `deploy/stacks/self-managed/helmfile.d/01-dependencies.yaml.gotmpl`
- `deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl`
- `deploy/stacks/self-managed/helmfile.d/03-observability.yaml.gotmpl`
- `deploy/stacks/nvcf-compute-plane/helmfile.d/01-dependencies.yaml.gotmpl`
- `deploy/stacks/nvcf-compute-plane/helmfile.d/02-nvca.yaml.gotmpl`

Provenance (when asked which subtree is monorepo-native vs. upstream-owned):

- `imports.yaml`

Chart-level (when the chart is checked into the monorepo):

- `deploy/helm/<chart>/Chart.yaml`
- `deploy/helm/<chart>/values.yaml`

If workspace routing metadata is available and disagrees with the helmfile, the
helmfile wins. Update stale routing metadata in the same change rather than
guessing.

## Common questions

What deploys X
: Look up release `X` in the helmfile stage files. Return chart name, version, namespace, and which gotmpl file declares it. If the chart is checked in, also point at `deploy/helm/<chart>/`.

What does X depend on
: Return the `needs:` chain for that release plus the stage gate it sits behind (control-plane stages 1 -> 2 -> 3, then compute-plane). Include any `condition:` that gates whether X deploys at all.

What hooks run for X
: Read the checked-in chart under `deploy/helm/<chart>/` when available. Search its `templates/` directory for Helm hook annotations, weights, hook events (pre-install / post-install), images used, and purpose. Cite the chart-relative template file path. If the chart is consumed from OCI only and the local workspace has routing metadata, use the workspace metadata and cite it.

Walk me through the full deployment order
: Summarize control-plane stages 0 through 3 from the self-managed gotmpl file headers. Then summarize the compute-plane stage from `deploy/stacks/nvcf-compute-plane/helmfile.d/01-dependencies.yaml.gotmpl` and `deploy/stacks/nvcf-compute-plane/helmfile.d/02-nvca.yaml.gotmpl`. Call out which releases run in parallel inside a stage and which are serialized by `needs:`.

Which subtree do I edit to change X
: Two answers, both are important. For chart wiring (Helm hooks, manifests, values, hook weights) point at `deploy/helm/<chart>/` if the chart is checked in, or note `oci-only` (the chart is consumed from the OCI registry and does not live in the monorepo). For runtime application logic, use the chart image repository, imports.yaml, and any available workspace routing metadata. `authoritative_source: native` means edits land here. `upstream` means edits also flow back to the upstream repo through the internal source-sync workflow.

What namespaces does the stack use
: Return the list from the helmfile (`namespace:` per release). If workspace routing metadata includes a destroy namespace list, include it as supplemental context and cite that source.

## Subtree mapping

The stack lives in three layers across the monorepo:

| Concern | Lives at |
|---------|----------|
| Helmfile orchestration (stage ordering, env wiring, secrets flow) | `deploy/stacks/self-managed/` |
| Chart manifests, helm hooks, values | `deploy/helm/<chart>/` (when vendored) or OCI registry only |
| Runtime application code, migrations | `src/`, `infra/`, `migrations/` (per `imports.yaml`) |

When a question crosses layers, answer by layer and tell the user the order to edit (chart wiring first if the deploy contract changes, image source if behavior changes).

## Tone

Assume the user is onboarding to the stack. Be concise. Always include the chart name, version, and the gotmpl path when referencing a release. Prefer one short paragraph plus a code-block citation over prose.

## Skill handoff candidates

After exploring, suggest the next skill when applicable:

- `nvcf-self-managed-installation` for installing, upgrading, or tearing down the stack
- `docs/dev/local-development.md` for k3d / local cluster work
- `nvcf-self-managed-cli` for `nvcf-cli` usage against an installed stack
- `docs/AGENTS.md` and `fern/versions/main.yml` for routing the user to a published docs page
- `tools/ci/check-doc-version-sync` for keeping the documentation manifest in sync with the docs version catalog
