# NVCA model cache figure assets

Unmodified 192 x 192 PNG components from the NVIDIA Cloud Architecture Diagrams
Style Guide v1.1, as carried in the nvaf repository's figure assets. The figure
`docs/dev/assets/nvca-model-cache-architecture.svg` embeds byte-identical
copies as data-URI symbols so the SVG renders without external file loading;
each symbol records its source path in a `data-source` attribute. Icons are
scaled uniformly and never recolored, cropped, flipped, or rotated.

| File | Figure use |
| --- | --- |
| `service-generic.png` | Cloud Functions API, NVCA Operator, NVCA Agent, Writer Job, function pods |
| `data-database.png` | Artifact registry, capability catalog ConfigMap, persisted selection |
| `function-admission.png` | Admission webhook |
| `resource-folder.png` | StorageClass, shared claim or writer volume, reader PV |
| `third-party-generic.png` | CSIDriver object and storage backends |
| `actor-generic-user.png`, `actor-machine-service.png` | Unused, kept for future revisions |

Render check: open the SVG in a browser at 1600 x 900, light and dark. The
diagram stays on a white panel in both modes, as the style guide requires.
