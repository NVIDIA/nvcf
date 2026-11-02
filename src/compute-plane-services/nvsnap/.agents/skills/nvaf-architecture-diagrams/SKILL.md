---
name: nvaf-architecture-diagrams
description: Create or revise NVAF cloud architecture diagrams and PNG exports in the nvcf/nvaf repository using NVIDIA Cloud Architecture Diagrams Style Guide v1.1. Use for architecture SVGs, component and group layouts, icon selection, paths, and presentation exports; do not use for workflow or software-stack diagrams.
---

# NVAF Architecture Diagrams

Apply the NVIDIA Cloud Architecture Diagrams Style Guide to NVAF figures while preserving the technical meaning requested by the user.

## Sources of truth

Use this precedence:

1. The user's explicit content and deliverable requirements.
2. [NVIDIA Cloud Architecture Diagrams Style Guide v1.1](references/nvidia-cloud-architecture-style.md).
3. Repository conventions in [docs/AGENTS.md](../../../docs/AGENTS.md).
4. Existing figures as implementation references.

Before editing, read the style-guide summary and the provenance file for the relevant assets. For the turn-orchestration figure, read [its asset map](../../../docs/figures/assets/nvaf-turn-orchestration/README.md). Treat [nvaf-turn-orchestration.svg](../../../docs/figures/nvaf-turn-orchestration.svg) as a layout and validation example, not authority when it conflicts with the style guide.

## Authoring contract

- Use this visual system only for architecture diagrams. Do not force workflow or software-stack content into it.
- Establish one clear primary path before adding secondary or optional paths.
- Use groups only when they contain multiple related components.
- Select icons by component semantics. Reuse the supplied generic icon when no specific icon exists; do not invent one.
- Keep source icons byte-identical. Do not recolor, crop, flip, rotate, or otherwise modify them.
- Pick one standard component-icon size for the diagram. Use the small size for technical diagrams and the large size only for simple executive diagrams.
- Keep labels short, normally six words or fewer. Break a long approved name onto two lines or shorten it after showing the full name once; never solve overflow by shrinking one label.
- Align components and orthogonal paths to a grid. Reserve open connector lanes between groups and rows instead of routing through cards.
- For a cross-group connector, use only a short, direct stub from the source component to its nearest group edge; put lateral routing in the open lane outside the group.
- Land each arrow on the midpoint of its target edge with a straight final segment at least 16 pt long. Avoid backtracking, U-turns, and a bend immediately before the arrowhead.
- Put a numbered callout on a straight, open part of its path—not on a bend, arrowhead, group boundary, or another connector. Keep parallel callouts at least one callout diameter apart.
- Use the guide's 1 pt stroke and 4 pt arrowhead proportions. Arrowheads must touch the target edge and point cleanly into it at normal reading size.
- Snap a group-level path to the group edge and a component-level path to the component itself.
- Use the guide's existing path styles and palette. Do not invent new path colors, icon treatments, or group styles.
- Keep the architecture canvas on a white canvas. For a dark presentation surface, place the unchanged white diagram on a white panel over the dark surround; do not invert or recolor the diagram.
- Embed local PNG icons as data-URI symbols in tracked SVGs so GitLab renders them without external file loading. Keep a `data-source` attribute pointing to the repository asset.

## Renderer safety

Treat every SVG as untrusted input, including tracked repository figures. Keep the renderer's safety layers intact:

- reject scripts, event-handler attributes, `foreignObject`, document types and entities, script URLs, CSS imports, and external asset references before opening Chromium;
- run Chromium with page JavaScript disabled and service workers blocked;
- enforce a restrictive content security policy and abort every network request;
- require exactly one SVG root in the render panel.

Do not weaken these controls to make an SVG render. Convert required assets to embedded local PNG data URIs instead. When changing the renderer, verify that the normal figure still renders, an SVG containing script or event-handler content is rejected, and an SVG with an external image reference is rejected without making the request.

For text constrained by a card, give the card an ID and annotate each constrained text node:

```svg
<rect id="card-worker-api" ... />
<text data-fit="card-worker-api" data-fit-padding="4">Worker API</text>
```

## Render and verify

Validate XML and whitespace first:

```sh
xmllint --noout docs/figures/<figure>.svg
git diff --check
```

Then render both presentation contexts and run the fit checks:

```sh
node .agents/skills/nvaf-architecture-diagrams/scripts/render-and-check.cjs \
  docs/figures/<figure>.svg \
  /absolute/output/path/<figure>
```

The command writes `<figure>-light.png` and `<figure>-dark.png` at 2× resolution. The dark export follows the guide by showing the white diagram on a dark surround. If `playwright` is not on Node's module path, load the Codex workspace dependencies and set `NODE_PATH` to the returned Node package directory.

Inspect both PNGs at the size where readers will see them. Before finishing, confirm:

- no label exceeds its annotated card;
- no text falls outside the SVG view box;
- paths do not cross labels, icons, or unrelated cards;
- connector lanes are uncrowded, callouts sit on straight segments, and each arrow has a clean final approach;
- arrowheads touch target edges without overshooting, appearing detached, or hiding beneath a card;
- icons are aligned and use one component size;
- the primary path is obvious;
- group and category semantics match the style guide;
- the source SVG and requested PNGs are updated together.
