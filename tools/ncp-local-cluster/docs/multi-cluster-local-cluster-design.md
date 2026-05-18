# Multi-Cluster Local Cluster Architecture

## Purpose

The default `ncp-local` workflow creates one k3d cluster that acts as both the
NVCF control plane and compute plane for local QA. Multi-cluster mode keeps
that workflow intact and adds a split topology for testing control-plane to
compute-plane behavior on one machine.

The operational quick start and command examples live in `README.md`. This
file captures the architecture decisions that should stay stable as the
workflow evolves.

## Topology

Multi-cluster mode creates one control-plane cluster and one or more
compute-plane clusters.

Default names:

```text
ncp-local-cp
ncp-local-compute-1
ncp-local-compute-2
```

The control-plane cluster owns host ports for HTTP, HTTPS, and NATS traffic.
The compute-plane clusters do not expose host ports, so multiple compute
clusters can run at the same time.

## Addon Placement

| Addon or validation | Single cluster | Control plane | Compute plane |
|---|---:|---:|---:|
| Generic credential provider | yes | yes | yes |
| Docker credential file mount | yes | yes | yes |
| Gateway API and Envoy Gateway | yes | yes | no |
| nginx route validation | yes | yes | no |
| Control-plane SIS/ReVal helper routes | no | yes | no |
| Control-plane NATS route | no | self-managed stack | no |
| Compute aliases for SIS/ReVal/NATS | no | no | yes |
| CSI SMB driver | yes | no | yes |
| fake GPU operator | yes | no | yes |
| private-image sample validation | yes | no | yes |

## Endpoint Model

Compute clusters use CoreDNS aliases under `CONTROL_PLANE_DOMAIN` for the
control-plane services that worker-side components need. The default domain is
`nvcf-control-plane.test`:

```text
sis.nvcf-control-plane.test
reval.nvcf-control-plane.test
nats.nvcf-control-plane.test
```

The aliases resolve to the Docker gateway IP for the compute cluster. Service
and endpoint objects in the compute cluster translate the expected in-cluster
ports to the control-plane host ports:

| Service | Compute service port | Control-plane host port |
|---|---:|---:|
| SIS | `8080` | `CONTROL_PLANE_HTTP_PORT` |
| ReVal | `8080` | `CONTROL_PLANE_HTTP_PORT` |
| NATS | `4222` | `CONTROL_PLANE_NATS_PORT` |

The local control-plane addon exposes helper Gateway routes for SIS and ReVal.
The NATS route is owned by the self-managed stack `nvcf-gateway-routes` chart.
ncp-local still provisions the Gateway TCP listener and the compute-cluster
alias that targets it.

## Configuration

The role-specific k3d configs are:

```text
k3d-config-control-plane.yaml
k3d-config-compute-plane.yaml
```

The main supported knobs are:

```make
CONTROL_PLANE_CLUSTER_NAME
CONTROL_PLANE_HTTP_PORT
CONTROL_PLANE_HTTPS_PORT
CONTROL_PLANE_NATS_PORT
CONTROL_PLANE_DOMAIN
COMPUTE_CLUSTER_COUNT
COMPUTE_CLUSTERS
COMPUTE_CLUSTER_PREFIX
```

`CONTROL_PLANE_DOMAIN` defaults to `nvcf-control-plane.test`. The same value is
used when rendering local SIS/ReVal Gateway hostnames and compute-plane CoreDNS
aliases, so a custom domain does not create DNS records that Envoy will reject
on host matching.

## Non-Goals

- Replace the existing single-cluster `ncp-local` workflow.
- Deploy the NVCF self-managed stack from this repository.
- Register NVCF clusters or install worker-layer components.
