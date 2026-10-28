# Umbrella 1.0.4 stack inventories

These review snapshots describe the exact independently versioned stacks selected
for umbrella 1.0.4. Each JSON inventory preserves its immutable source tag,
commit, chart/image versions, registry URLs, and required/optional ownership.
`index.json` lists the files, SHA-256 checksums and stack resource identities.

| Stack | Version | Images | Charts |
| --- | --- | --- | --- |
| Self-Managed | 1.0.2 | 41 | 25 |
| Compute Plane | 1.0.1 | 23 | 5 |
| Observability | 1.0.0 | 4 | 5 |

The Self-Managed and Compute Plane snapshots were generated from the tagged
inventory tooling and selected chart packages, then checked for images that
are created indirectly. The inventories include optional add-ons and runtime
fallbacks. `provenance.json` records the supplemental evidence:

- Self-Managed includes the API's optional Triton 23.04-py3 fallback.
- Compute Plane includes ten KAI operator-created images and both collector
  image references encoded in the NVCA operator configuration.
- Observability retains its released inventory and adds the operator's required
  Target Allocator 0.158.0 default to this review snapshot. The original release
  asset remains unchanged; its checksum is recorded in the provenance.

These inventories do not change runtime pins or replace immutable release
assets. They are publication inputs for human review and tracking, not evidence
that every image, chart, or stack bundle is available through its final catalog
route. Component publication status and remaining gates are tracked in
[#2843](https://github.com/NVIDIA/nvcf/issues/2843) and its stack subissues.
Upstream registry URLs remain the qualified runtime references. NGC image
publication covers the NVCF images; upstream mirrors are outside this request.
