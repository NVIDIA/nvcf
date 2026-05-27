# Agent Skills Repository Guidelines

This subtree contains public Agent Skills for NVIDIA Cloud Functions (NVCF). All skills must comply with the [Agent Skills specification](https://agentskills.io/specification) with NVCARPS-specific extensions. This repository is compatible with the [Vercel Skills CLI](https://github.com/vercel-labs/skills).

## Instructions

Use this file as the authoring contract for every public skill in `user/skills/` and `dev/skills/`. Keep private skills in the private subtree.

When adding or editing a skill, keep the external skill spec fields at the top level of the frontmatter, retain the required NVCARPS fields under `metadata`, and include a concise `## Instructions` section in the body.

## Skill Structure

Source skills live under `user/skills/` or `dev/skills/`. Each skill must be in its own subdirectory containing a `SKILL.md` file. The directory name must match the `name` field in the frontmatter.

```
user/skills/
- user-skill-name/
  - SKILL.md

dev/skills/
- dev-skill-name/
  - SKILL.md
```

Public hook source scripts live under `dev/hooks/`. Do not place hook implementation scripts directly under `.cursor/hooks/`, `.codex/hooks/`, or `.claude/hooks/`; those root directories are fanouts only and must contain matching symlinks to `dev/hooks/`.

## SKILL.md Format

### Complete Frontmatter Template

```yaml
---
name: skill-name
description: Clear description of what the skill does and when to use it. Include specific keywords to help agents identify relevant tasks.
license: MIT
compatibility: Requires NGC CLI installed and configured
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags:
  - ngc
  - nvcf
  - cli
  - cloud-infrastructure
  - skill
tools:
  - Shell
  - Read
  - Write
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0"
  tags:
    - ngc
    - nvcf
    - relevant-tag
  languages:
    - bash
  frameworks:
    - ngc-cli
  domain: cloud-infrastructure
---
```

### Required Fields

| Field | Type | Constraints |
|-------|------|-------------|
| `name` | string | 1-64 chars, lowercase, alphanumeric + hyphens, no leading/trailing/consecutive hyphens, must match directory name |
| `description` | string | 1-1024 chars, must describe what AND when to use, include keywords for discoverability |
| `author` | string | Author name or team |
| `version` | string | Semantic version string |
| `tags` | array | Categorization tags (recommend 1-5 items for top-level spec compatibility) |
| `tools` | array | Agent tools required by the skill (for example `Shell`, `Read`, `Write`) |

### Optional Fields

| Field | Type | Description |
|-------|------|-------------|
| `license` | string | License name or reference to bundled license file |
| `compatibility` | string | Max 500 chars. Environment requirements |
| `metadata` | object | NVCARPS metadata fields (see below) |

### NVCARPS Metadata Fields (Required)

These fields must be placed inside the `metadata` object:

| Field | Type | Description | Example |
|-------|------|-------------|---------|
| `internal` | boolean | Must be `false` for public skills in this subtree | `false` |
| `author` | string | Author name and NVIDIA email | `"Your Name <your-email@nvidia.com>"` |
| `version` | string | Skill version | `"1.0"` |
| `tags` | array | List of searchable tags | `["ngc", "nvcf", "cli"]` |
| `languages` | array | Programming languages this skill applies to | `["bash", "python"]` |
| `frameworks` | array | Frameworks or libraries this skill covers | `["ngc-cli"]` |
| `domain` | string | Domain or area of expertise | `"cloud-infrastructure"` |

### Fields NOT to Include

- `alwaysApply` - This is for rules, not skills
- `globs` - This is for rules, not skills

Skills are invoked on-demand, not automatically applied like rules.

## Name Validation Rules

- Lowercase letters, numbers, and hyphens only (`a-z`, `0-9`, `-`)
- Must not start or end with a hyphen
- Must not contain consecutive hyphens (`--`)
- Must match the parent directory name exactly

## Description Best Practices

Include:
1. What the skill does (actions it enables)
2. When to use it (trigger phrases, keywords)

Example:
```yaml
description: Manage NVCF clusters via NGC CLI. Register, list, and delete clusters for function and task deployments. Use when registering clusters, managing cluster configurations, or when the user mentions ngc cf cluster, NVCF clusters, or cluster registration.
```

## Body Content Guidelines

- Keep main `SKILL.md` under 500 lines
- Move detailed reference material to `references/` directory
- Include a concise `## Instructions` section near the top of the body
- Include step-by-step instructions
- Provide command examples
- Document common edge cases

### Standard Sections for CLI Skills

1. Before You Start - list required config, binaries, and access.
2. Core Commands - primary operations with examples.
3. Examples - real-world usage scenarios.
4. Additional Resources - links to detailed docs or reference files.

### File References

Use relative paths from the skill root:

```markdown
For detailed examples, see [examples.md](examples.md).
For reference material, see [references/REFERENCE.md](references/REFERENCE.md).
Run the helper script: `python scripts/helper.py`
```

## Validation Checklist

Before committing changes, verify:

- [ ] `name` field matches directory name
- [ ] `name` is lowercase with hyphens only
- [ ] `description` includes what AND when to use
- [ ] `compatibility` field is present
- [ ] Top-level `author` is present
- [ ] Top-level `version` is present and semantic
- [ ] Top-level `tags` is present
- [ ] Top-level `tools` is present
- [ ] `metadata.author` includes name and NVIDIA email
- [ ] `metadata.version` is present
- [ ] `metadata.tags` array is populated
- [ ] `metadata.languages` array is populated
- [ ] `metadata.frameworks` array is populated
- [ ] `metadata.domain` is set
- [ ] File is under 500 lines
- [ ] All relative links are valid
- [ ] No `alwaysApply` or `globs` fields present

## Existing Skills

| Skill | Purpose | Domain |
|-------|---------|--------|
| `bazel-go-gazelle` | Wire Go modules into Bazel with rules_go and Gazelle | build-systems |
| `bazel-java-maven` | Wire Java and Spring Boot services into Bazel with Maven artifacts | build-systems |
| `bazel-monorepo-bootstrap` | Bootstrap Bazel in an existing polyglot monorepo | build-systems |
| `bazel-oci-images` | Build multi-arch OCI images from Bazel binaries | build-systems |
| `bazel-rust-crate-universe` | Wire Rust services into Bazel with crate_universe | build-systems |
| `bazel-synthetic-import-strategy` | Plan Bazel rollout for NVCF synthetic imports | build-systems |
| `documentation-style` | NVCF documentation conventions for public repo prose | documentation |
| `nvcf-explore-stack` | Navigate self-hosted stack topology, helmfile deployment order, chart ownership, and dependencies | cloud-infrastructure |
| `official-docs-style` | External-facing NVCF user documentation voice and structure | documentation |
| `nvcf-self-managed-installation` | Install and deploy the nvcf-self-managed-stack helmfile bundle: installation, teardown, values overrides, pull secrets, debugging | cloud-infrastructure |
| `nvcf-self-managed-cli` | Standalone NVCF CLI for self-managed/self-hosted deployments: function lifecycle, deployment, invocation, API keys, and registry credentials | cloud-infrastructure |

## References

- [Agent Skills Specification](https://agentskills.io/specification)
- [Vercel Skills CLI](https://github.com/vercel-labs/skills)
- [NVCARPS Skill Contribution Guide](https://nvidia.atlassian.net/wiki/spaces/GAIT/pages/2992484731/HOW-To-Contribute-Skills)
