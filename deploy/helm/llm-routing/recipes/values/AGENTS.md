# Public model values

Keep one values file for each deployable catalog profile. Use public placeholder nodes and documentation IP addresses. Keep cluster identities, credentials and private overrides outside this directory.

Automatic files select recipe metadata bundled in the charts. Keep model pins and profiles in `../catalog.json`, without duplicating them in example values. Flash-Next examples include local-NVMe or fabric facts under `nodeCapabilities`, keyed by the selected nodes. Users must verify these facts for their nodes. Keep candidate prerequisites and validation status explicit.

Run from `recipes` after edits:

```bash
python3 -m unittest discover -s tests -p test_committed_values.py -v
```

The tests check profile coverage, node capability requirements and source/archive Helm rendering. Rendering does not establish hardware readiness or inference.
