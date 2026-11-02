# NVCA upgrade lab fixtures

This directory contains a test identity provider, NGC route adapter, CPU-only
agent probe, and dedicated lab setup. Keep cluster state and version ownership
in real ICMS. Never synthesize the expected version in the route adapter.

Run from `tests/bdd/`:

```sh
go test -short ./fixtures/nvca-upgrade
go test -short ./...
golangci-lint run --config .golangci.yml ./...
```

Use `README.md` for live setup and the anchored `TestNVCAUpgrade` command. Use
a fresh dedicated lab for each application image. The development schema
resets the lab's `test` keyspace; setup must reject an existing lab namespace.

Keep the probe's mounted config format aligned with the operator's
`cluster.id` and `agent.icmsurl` fields. GPU descriptions are synthetic and
have zero capacity. The fixture must not claim to validate real GPU execution.

JWT signing keys are generated in memory. Never commit tokens, kubeconfigs,
private keys, private bug references, or lab result dumps.
