# CLI contributor guidance

Scope: this module and its command tests.

Use the root repository guidance for PRs, public issues, and release safety.
Native Go code uses the module's existing Cobra commands and internal packages.
Keep explicit local, Git, and OCI stack selection compatible when adding an
opt-in source. Do not change build-stamped component defaults implicitly.

From this directory:

```bash
go test ./internal/selfhosted/... ./cmd/...
go vet ./internal/selfhosted/... ./cmd/...
```

From the repository root:

```bash
bazel test //src/clis/nvcf-cli/... --test_output=streamed
git diff --check
```

Keep the adjacent BUILD.bazel source and test lists current. No real cluster
is required for unit tests. Live install tests use an isolated kubeconfig and
explicit context; follow the root environment inventory and cleanup rules.
Do not infer installed whole-product identity from a selected bundle: planes
can be applied independently, and upgrade receipts identify component versions.
