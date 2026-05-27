# NVCF Public Skills

Public agent skills for users and developers working with NVIDIA Cloud Functions (NVCF). Public user skills live under `user/skills/`; public dev skills live under `dev/skills/`.

## Skills

| Skill | Description |
|-------|-------------|
| [bazel-go-gazelle](dev/skills/bazel-go-gazelle/SKILL.md) | Wire Go modules into Bazel with rules_go and Gazelle |
| [bazel-java-maven](dev/skills/bazel-java-maven/SKILL.md) | Wire Java and Spring Boot services into Bazel with Maven artifacts |
| [bazel-monorepo-bootstrap](dev/skills/bazel-monorepo-bootstrap/SKILL.md) | Bootstrap Bazel in an existing polyglot monorepo |
| [bazel-oci-images](dev/skills/bazel-oci-images/SKILL.md) | Build multi-arch OCI images from Bazel binaries |
| [bazel-rust-crate-universe](dev/skills/bazel-rust-crate-universe/SKILL.md) | Wire Rust services into Bazel with crate_universe |
| [bazel-synthetic-import-strategy](dev/skills/bazel-synthetic-import-strategy/SKILL.md) | Plan Bazel rollout for NVCF synthetic imports |
| [documentation-style](dev/skills/documentation-style/SKILL.md) | NVCF documentation conventions for public repo prose |
| [nvcf-explore-stack](dev/skills/nvcf-explore-stack/SKILL.md) | Navigate the self-hosted stack topology, helmfile dependency graph, chart ownership, and deployment order |
| [nvcf-self-managed-installation](user/skills/nvcf-self-managed-installation/SKILL.md) | Install and deploy the nvcf-self-managed-stack helmfile bundle: installation, teardown, values overrides, pull secrets, debugging |
| [nvcf-self-managed-cli](user/skills/nvcf-self-managed-cli/SKILL.md) | Standalone NVCF CLI (`nvcf-cli`) for self-managed/self-hosted deployments: install, status, add compute plane, teardown, function lifecycle, invocation, and API keys |
| [nvcf-self-managed-prerequisite](user/skills/nvcf-self-managed-prerequisite/SKILL.md) | Install the cluster-level prerequisites NVCA needs — KAI Scheduler (with queue-quota patch) and the SMB CSI driver. Cloud-neutral helm installs pinned to NVCF-validated versions |

## Prerequisites

Before using the self-managed user skills, ensure you have:

1. The `nvcf-cli` binary or the source checkout that builds it.
2. An extracted `nvcf-self-managed-stack` bundle when installing a stack.
3. Access to the target self-managed environment.

## Quick Start

Once installed, your coding agent will automatically discover these skills when you work on NVCF-related tasks. You can also explicitly invoke them:

- "Help me create a new cloud function"
- "Deploy my function to production"
- "Install the self-managed NVCF stack"
- "Explain what deploys the API service in the stack"

## References
- [Agent Skills Specification](https://agentskills.io)
