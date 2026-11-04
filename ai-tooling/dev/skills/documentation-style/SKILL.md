---
name: documentation-style
description: >-
  NVCF public docs style: short, plain, ASCII Markdown with no bold, emojis,
  em-dash, or en-dash. Use when editing public docs, READMEs, AGENTS.md, agent
  skills, plans, GitHub issues, or GitHub Pull Request descriptions.
license: Apache-2.0
compatibility: Requires an NVCF documentation or repository checkout
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags:
  - nvcf
  - docs
  - style
  - markdown
tools:
  - Read
  - Edit
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0.0"
  tags:
    - nvcf
    - docs
    - style
    - markdown
  languages:
    - markdown
  frameworks: []
  domain: documentation
---

# Documentation and writing style

## Purpose

Keep NVCF documentation plain, consistent, and useful for external readers.
Write short docs an agent or contributor can act on without private context.
Avoid typographic Unicode that breaks grep and diff tools, and avoid emphasis
patterns that make docs noisy.

## When to use

- Any new or edited public Markdown in this repo.
- `AGENTS.md`, `CLAUDE.md`, `SKILL.md`, READMEs, plans, and guides.
- GitHub issue bodies, commit messages, and Pull Request descriptions.
- Existing docs you are changing for another reason.

## Instructions

1. No markdown bold for emphasis. Do not use `**...**`, `__...__`, raw HTML
`<b>`, or `<strong>`. Use headings, lists, and `backticks` for paths, commands,
env vars, and identifiers.

2. No emojis in documentation or user-facing prose.

3. No em-dash (Unicode U+2014) or en-dash (U+2013). Split the thought into two
sentences, or use a comma, semicolon, parenthesis, or colon.

4. ASCII only in committed prose. Avoid smart quotes, ellipsis characters,
non-breaking spaces, arrows, and other typographic Unicode. Prefer `->` over an
arrow glyph and `...` over a single-character ellipsis. Code blocks and quoted
fixtures are exempt.

5. Be succinct. Prefer short sentences and direct wording. Cut filler and
repetition. Keep one idea per sentence when possible.

6. Prefer lists for short, scan-friendly structure. Use bullets for unordered
items and numbered lists when sequence, priority, or stable rule references
matter. Use tables only when columns make comparison, status, option tradeoffs,
or matrix decisions easier. Do not turn a short list into a table.

7. Be easy to understand. Define acronyms on first use if the audience is broad.
Prefer concrete examples over abstract jargon.

8. Write for GitHub readers. Use public names, public paths, and public
behavior. Replace private evidence with the user-visible reason for the change.

9. Keep agent guidance actionable. Say when to use a skill, what to inspect
first, what command to run, and what done means. Do not add temporary branch
status, Pull Request queues, merge-order notes, or private routing details.

10. Avoid vague agent language. Remove filler such as "ensure", "leverage",
"robust", "comprehensive", "where applicable", and "as needed" unless the term
adds a concrete condition.

## Examples

Inline bold label, replaced with a plain prefix:

```text
Before: **Note:** restart after edit.
After:  Note: restart after edit.
```

Bold section header, replaced with a real heading:

```text
Before: **Prerequisites**

        - Repository access to github.com/NVIDIA/nvcf.

After:  ## Prerequisites

        - Repository access to github.com/NVIDIA/nvcf.
```

Double hyphen used as a dash, replaced with a period:

```text
Before: Services live upstream -- this repo mirrors them.
After:  Services live upstream. This repo mirrors them.
```

Long agent guidance, replaced with commands and durable criteria:

```text
Before: Ensure agents comprehensively verify symlink fanout where applicable.

After:  Run `python3 ai-tooling/dev/hooks/validate-skill-fanout.py` after
        changing root skills. Fix every reported path before finishing.
```

## Editing existing files

If a file already uses bold, emojis, em-dashes, en-dashes, or non-ASCII
punctuation, normalize it when you touch that file for another reason, or when
the user asks for a style pass. Do not expand scope into unrelated trees just to
fix style.

## Limitations

- The rules target agent-edited prose in this repo. Upstream documentation
  keeps its own style until merged natively.
- There is no automated linter for prose today. Review catches style issues.

## Exceptions

- Content you are quoting verbatim, such as upstream LICENSE text, cited errors,
  or user paste.
- Generated files where the generator controls formatting. Fix style in the
  generator if needed.
- Auto-generated changelogs or release notes assembled from external commit
  messages. Fix the source commits, not the rolled-up file.
