# Local Development (k3d)

Run a full NVCF self-hosted stack on your laptop using
[k3d](https://k3d.io/) for development, testing, or demos. The canonical
local-k3d tooling lives at `tools/ncp-local-cluster/` in this repo.

<Info>
This setup is for **local development only**. It uses fake GPUs, a single
Cassandra replica, and ephemeral storage. Do not use this for production
workloads.
</Info>

## Pick a flow

Four canonical flows are documented, one per page.

| Topology | Install path | Page |
|---|---|---|
| Single-cluster | CLI (`nvcf-cli self-hosted install`) | [Single-cluster CLI](./local-development/single-cluster-cli.md) |
| Single-cluster | Helmfile (`make install HELMFILE_ENV=...`) | [Single-cluster Helmfile](./local-development/single-cluster-helmfile.md) |
| Multi-cluster | CLI (`nvcf-cli self-hosted install`) | [Multi-cluster CLI](./local-development/multi-cluster-cli.md) |
| Multi-cluster | Helmfile (`make install HELMFILE_ENV=...`) | [Multi-cluster Helmfile](./local-development/multi-cluster-helmfile.md) |

## Topologies

**Single-cluster** brings up one k3d cluster named `ncp-local`. Control
plane and compute plane share the cluster. The fastest path for
function-lifecycle testing and basic install validation.

**Multi-cluster** brings up `ncp-local-cp` plus `ncp-local-compute-N`
(N=1 by default). Control plane lives on the cp cluster; compute plane on
the compute cluster. Required when you need to exercise cross-cluster
registration, the OIDC/JWKS discovery flow, or the `.test` hostname
routing the cp Gateway exposes to compute workers.

The two topologies are **mutually exclusive**: both claim host ports
8080/8443/4222. Destroy one before bringing up the other:

```bash
# from single-cluster -> multi-cluster
make -C tools/ncp-local-cluster destroy

# from multi-cluster -> single-cluster
make -C tools/ncp-local-cluster destroy-multicluster
```

## Install paths

**CLI** drives the install through `nvcf-cli self-hosted install
--control-plane` (writes a control-plane profile YAML), `nvcf-cli init`
(mints the admin JWT against the live api-keys service), and
`nvcf-cli compute-plane register/install` (writes compute-plane values
and applies them). The CLI manages URL block selection (in-cluster vs
cross-cluster reachable) based on the kube contexts you pass.

**Helmfile** drives the install through Make targets in
`deploy/stacks/self-managed/Makefile`: `make template`, `make install`,
`make register-cluster`, and `make install-nvca-operator`. The operator
authors topology-correct URLs into an environment file (different fixture
per topology) instead of relying on a CLI-managed profile.

The two install paths intentionally diverge. See
`tests/bdd/AGENTS.md` (the "CLI vs Helmfile install paths" section) for
the rationale.

## Prerequisites (common to all flows)

- [Docker](https://www.docker.com/get-started) (running)
- [k3d](https://k3d.io/#installation) v5.x or later
- `kubectl`
- `helm` >= 3.12
- `helmfile` >= 1.1.0, < 1.2.0 (Helmfile flows only)
- `helm-diff` plugin (Helmfile flows only):
  `helm plugin install https://github.com/databus23/helm-diff`
- An **NGC API key** with access to the NVCF chart and image registry.
- `nvcf-cli` built from this repo:

  ```bash
  go build -o nvcf-cli ./src/clis/nvcf-cli
  ```

Each flow page restates exactly the prerequisites that flow needs.

## Cleanup

The BDD suite ships destructive cleanup helpers reused for hand-driven
local dev:

| Scope | Command |
|---|---|
| Stack-only (helm releases) on single-cluster | `tests/bdd/scripts/destroy-stack.sh single` |
| Stack-only on multi-cluster | `tests/bdd/scripts/destroy-stack.sh multi` |
| Whole single-cluster topology | `make -C tools/ncp-local-cluster destroy` |
| Whole multi-cluster topology | `make -C tools/ncp-local-cluster destroy-multicluster` |
| Every `ncp-local*` k3d cluster on the host | `make -C tools/ncp-local-cluster destroy-all-ncp-local` |
