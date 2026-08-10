# AGENTS.md - ncp-local-cluster

Local k3d cluster tooling for NVCF self-hosted development.

## Build And Test

Run Go checks from `tools/ncp-local-cluster/credential-provider-go`:

```bash
go test ./...
go build ./cmd/generic-credential-provider
```

Run Makefile-only validation from `tools/ncp-local-cluster`:

```bash
make validate-compute-clusters
make print-compute-clusters
make test-multicluster-make
```

Cluster lifecycle targets require local tools such as `k3d`, `kubectl`, `helm`, and Docker.
For detailed local k3d workflow and cleanup safety, see
`docs/dev/local-development.md` from the repo root.

## Ownership

This subtree is monorepo-native. Do not sync it from the old standalone repo.
