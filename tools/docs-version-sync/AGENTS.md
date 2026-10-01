# Documentation version tooling

Run from the repository root:

```bash
go test -C tools/docs-version-sync ./...
go vet -C tools/docs-version-sync ./...
bash tools/scripts/test/test-cut-docs-version
./tools/ci/check-doc-version-sync
./tools/ci/check-docs
```

Use `gofmt` and the existing Go package structure. Add focused tests for
inventory selection, publication identity, and output protection changes.

Normal sync writes only current product trees. Historical exports must use
an explicit inventory and separate staging directory. Never let current
release selection, version overrides, or supplemental artifacts change a
historical snapshot. Preserve candidate provenance and qualification status.

Update `README.md` and `tools/scripts/cut-docs-version.sh` together when the
freeze command contract changes. Generated blocks must remain reproducible
from their matching catalog and immutable release inputs.
