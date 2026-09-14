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
make test-cluster-lifecycle-make
make test-multicluster-make
make test-validate-gateway-route
make test-gateway-timeout-compatibility
```

Cluster lifecycle targets require local tools such as `k3d`, `kubectl`, `helm`, and Docker.
For detailed local k3d workflow and cleanup safety, see
`docs/dev/local-development.md` from the repo root.

## Split-cluster validation

Use `build-and-deploy-multicluster` for behavior that crosses control and
compute planes. Cluster creation, compute registration, and NVCA readiness do
not prove worker traffic. Probe worker-facing DNS and ports from a pod in the
compute cluster, then launch a real worker and invoke it through the control
plane.

Do not rely on a control-plane `*.svc.cluster.local` name crossing the cluster
boundary. A compute-cluster alias or external route must be part of the
topology under test and must be probed from the compute cluster. Record the
baseline and first broken hop before applying a workaround.

## Ownership

This subtree is monorepo-native. Do not sync it from the old standalone repo.
