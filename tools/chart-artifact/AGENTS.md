# Chart artifact tooling

This Go module owns chart archive validation and content comparison.
Keep registry credentials and destination selection outside the verifier.

From the repository root:

```sh
go test -C tools/chart-artifact ./...
go vet -C tools/chart-artifact ./...
bash tools/ci/test-check-helm-charts
```

Cover changed files, metadata mismatches, unreadable archives, and identical
content with different archive timestamps. Registry tests must use stubs and
must not publish artifacts. Keep CLI usage in `README.md`.
