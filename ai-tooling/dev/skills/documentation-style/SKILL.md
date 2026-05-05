---
name: documentation-style
description: >-
  NVCF docs style: no bold, no emojis, no em-dash or en-dash, ASCII-only prose.
  Use when editing markdown, READMEs, AGENTS.md, plans, or PR descriptions.
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

Keep NVCF documentation plain, consistent, and agent-friendly. Avoid typographic Unicode that breaks grep and diff tools, and avoid emphasis patterns that encourage noisy, marketing-flavored prose.

## When to use

- Any new or edited markdown in this repo (root docs, `dependencies-plan.md`, skills, comments in guides).
- User-facing explanations, commit messages, or PR bodies when the user wants repo style.
- Refactors of existing docs where you are already changing the file.

## Instructions

1. No markdown bold for emphasis. Do not use `**...**` or `__...__`, and do not reach for raw HTML `<b>` or `<strong>`. Use structure (headings, lists, tables) and `backticks` for paths, commands, env vars, and identifiers. Let the wording carry emphasis.

2. No emojis in documentation or user-facing prose.

3. No em-dash (Unicode U+2014) or en-dash (U+2013). Split the thought into two sentences, or use a comma, parenthesis, or colon when you need a break.

4. ASCII only in committed prose. Avoid smart quotes, ellipsis characters, non-breaking spaces, arrows, and other typographic Unicode. Prefer `->` over an arrow glyph and `...` over a single-character ellipsis. Code blocks and quoted fixtures are exempt.

5. Be succinct. Prefer short sentences and direct wording. Cut filler and repetition. Keep one idea per sentence when possible.

6. Be easy to understand. Define acronyms on first use if the audience is broad. Prefer concrete examples over abstract jargon.

## Examples

Inline bold label, replaced with a plain prefix:

```
Before: **Note:** restart after edit.
After:  Note: restart after edit.
```

Bold section header, replaced with a real heading:

```
Before: **Prerequisites**

        - Repository access to github.com/NVIDIA/nvcf.

After:  ## Prerequisites

        - Repository access to github.com/NVIDIA/nvcf.
```

Em-dash, replaced with a period:

```
Before: Services live upstream -- this repo mirrors them.
After:  Services live upstream. This repo mirrors them.
```

## Editing existing files

If a file already uses bold, emojis, em-dashes, en-dashes, or non-ASCII punctuation, normalize it when you touch that file for another reason, or when the user asks for a style pass. Do not expand scope into unrelated trees just to fix style.

## Limitations

- The rules target agent-edited prose in this repo. Upstream documentation in synthetic imports keeps its own style until merged natively.
- There is no automated linter today. Enforcement happens during review.

## Exceptions

- Content you are quoting verbatim (upstream LICENSE text, cited errors, user paste).
- Generated files (for example output from `go run ./tools/collect-dependencies`) where the generator controls formatting; fix style in the generator if needed.
- Auto-generated changelogs or release notes assembled from external commit messages; fix the source commits, not the rolled-up file.
