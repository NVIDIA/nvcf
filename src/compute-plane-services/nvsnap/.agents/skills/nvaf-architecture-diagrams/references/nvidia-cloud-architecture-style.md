# NVIDIA Cloud Architecture Diagrams Style Guide

Authoritative source: [Cloud Architecture Diagrams Style Guide v1.1, January 17, 2024](https://docs.google.com/presentation/d/1qdcZMpXhuvlmIZg7DffTkdKauqHUQWk6/edit)

This is a maintained working summary of the rules that affect NVAF repository figures. Consult the source deck when a design decision is not covered here.

## Scope

Cloud architecture diagrams document components and their relationships. They may range from highly technical to explanatory or executive-summary views. They are not workflow diagrams or software-stack diagrams.

Start with a high-level sketch of API interactions and the essential components. Choose the detail level for the audience, then simplify without losing the primary relationship.

## Paths

- Every diagram has a primary path.
- Use the predefined solid, secondary, optional dashed, and API-access path styles. Do not invent path styles.
- The guide's base path uses a 1 pt stroke, 4 pt arrowhead, and rounded corners.
- Path labels are optional. Use them only when they clarify meaning; over-labeling reduces readability.
- Keep path labels to roughly six words, in NVIDIA Sans Medium at 10 pt.
- Use title case for names and titles. Use sentence case without a period for action phrases.
- Place path icons or numbered callouts directly on the path.
- Snap path endpoints directly to their target. Group-level connections terminate at the group edge; component-specific connections pass through the group boundary and terminate at the component.

### Connector geometry for repository SVGs

- Route on an orthogonal grid and reserve open lanes between groups and component rows. Do not run a connector through a card, icon, or label.
- Prefer the fewest bends that preserve separation. Avoid backtracking, U-turns, stair-step routing, and overlapping path segments that make direction ambiguous.
- Give every arrow a straight final approach of at least 16 pt at the diagram's authored scale. Aim at the midpoint of the target edge when possible; never put a bend immediately before the arrowhead.
- The arrowhead tip must meet the target edge. It must not stop short, overshoot into the card, or sit underneath the card border.
- Put numbered callouts on straight segments with clear space on all sides. Keep them away from bends, arrowheads, group boundaries, and other callouts; parallel callouts need at least one callout diameter between their centerlines.
- Check connector geometry in the rendered PNG at normal reading size. A route that is technically connected but looks detached, crowded, or directionally unclear must be revised.

## Icons and labels

- Preserve supplied icons exactly: color, proportions, orientation, and format.
- Do not crop, recolor, flip, rotate, redraw, or create a substitute icon.
- Use a generic icon when the library lacks a dedicated component.
- Use one of the standard icon sizes consistently across the entire diagram; never mix sizes. Small icons are the default for technical diagrams. Large icons are for simple executive diagrams.
- The guide identifies 48 × 48 pt and 63 × 72 pt presets, with a 42 pt minimum.
- Keep icon labels short, normally about six words, in NVIDIA Sans Regular at 12 pt with approximately 10 pt clearance.
- Break approved long names across two lines when needed. A shortened form is acceptable only after the full name appears once.
- Do not remove “NVIDIA” from an NVIDIA product or invent a new abbreviation.
- Do not place icons everywhere; use them where they improve identification.

## Component categories and palette

Use color only for the category or ownership meaning assigned by the guide.

| Category | Color |
| --- | --- |
| NVIDIA service, microservice, or container | `#76b900` |
| Resource | `#8922b9` |
| Function | `#ffbc01` |
| Actor | `#2290c7` |
| Third-party service | `#c2262d` |
| Third-party application | `#d128a8` |
| Location | `#1dbfa4` |
| Data | `#d96421` |
| Primary paths and text | `#000000` |
| Optional path | `#999999` |
| Group boundary | `#cccccc` |
| Partner-service group | `#f4f7f7` |
| Diagram background and icon interior | `#ffffff` |

NVIDIA Green identifies NVIDIA-owned or NVIDIA-powered components. Do not use it decoratively. For a third-party component powered by NVIDIA, use the guide's green underline or chevron below the icon rather than recoloring the icon.

## Groups and layout

- A group contains more than one component.
- Use groups to organize relationships, but do not overuse, recolor, or restyle them.
- Supported semantics include generic groups, third-party service groups, NVIDIA service groups, NVIDIA-powered generic groups, and sidecar groups.
- Lay out the diagram on a grid. Align icons and paths whenever possible.
- Keep ample breathing room around icons, labels, paths, and group edges.

## Background and exports

Architecture diagrams are always presented on white. On a dark slide or application background, place the white diagram on a white panel. Do not create a dark/inverted architecture palette.

The guide's PowerPoint export settings are 200% zoom, zero border, page size, light appearance, no shadow, and no grid. It recommends a transparent PNG for placement on a white panel. For NVAF documentation that explicitly requires a white-background PNG, flatten the export onto solid white without changing the diagram.

The source deck assumes draw.io. This repository tracks manually maintained SVG figures, so preserve the guide's visual semantics while following the repository's SVG workflow and validation rules.
