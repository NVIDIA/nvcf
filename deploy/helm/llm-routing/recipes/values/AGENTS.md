# Public model values

Keep one values file for each deployable catalog profile. Use public placeholder nodes and documentation IP addresses. Keep cluster identities, credentials and private overrides outside this directory.

Automatic files select recipe metadata bundled in the charts. Flash-Next files use the phased workflow and duplicate exact pins and profiles from `../catalog.json`. Update these copies together when the catalog changes. Keep candidate prerequisites and validation status explicit.

Run from `recipes` after edits:

```bash
python3 -m unittest discover -s tests -p test_committed_values.py -v
```

The tests check profile coverage, source pin consistency and source/archive Helm rendering. Rendering does not establish hardware readiness or inference.
