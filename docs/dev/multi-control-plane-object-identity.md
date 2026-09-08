# Multi-Control-Plane Object Identity

This matrix is the implementation ledger for isolated self-managed NVCF control
planes in one Kubernetes cluster. It records which object identities stay shared,
which stay legacy-compatible, and which must become control-plane-derived before
named control planes are enabled.

Issue: https://github.com/NVIDIA/nvcf/issues/1481

## Identity Rules

| Concept | Rule |
|---|---|
| Legacy control plane | Existing names remain unchanged when no user control-plane ID is configured. |
| User control-plane ID | Lowercase DNS label, validated before deriving any Kubernetes, auth, data, or filesystem identity. |
| Reserved IDs | `default` and `shared` are internal-only and are rejected for user input. |
| Ownership label | `nvcf.nvidia.com/control-plane-owner` labels ownership-capable Kubernetes objects. |
| Shared owner | `shared` is reserved for cluster prerequisites managed outside a single plane. |
| Missing owner | Treated as pre-adoption or foreign; plane teardown must not delete it by guesswork. |
| Selector safety | The ownership label must not be added to workload selectors or pod-template labels unless the controller explicitly owns that selector contract. |

## Shared Code Contract

Go callers should use the shared identity helpers in
`src/libraries/go/lib/pkg/types/controlplane`.

Do not add separate control-plane ID validators in the CLI, NVCA, or individual
control-plane services. Those callers should all use the same validation and
naming package so the control-plane ID has one meaning across install, runtime,
and teardown paths.

## Kubernetes Object Matrix

| Surface | Current Legacy Identity | Named-Plane Target | Owner | Phase 0 Evidence |
|---|---|---|---|---|
| Control namespaces | `nvcf`, `api-keys`, `ess`, `sis` | Derived from validated control-plane ID | Plane | Golden render and lifecycle inventory |
| Data namespaces | `cassandra-system`, `nats-system`, `vault-system` | Derived from validated control-plane ID | Plane | Golden render and lifecycle inventory |
| Ingress namespace | Environment-provided Gateway API namespace | Shared or externally managed; never deleted by plane teardown | Shared or external | Golden render and matrix review |
| cert-manager namespace | `cert-manager` | Shared prerequisite namespace | Shared | Golden render and prereq inventory |
| Service names | Chart defaults such as `api`, `ess-api`, `grpc-proxy` | Derived only where cross-plane collision exists | Plane | Golden render |
| ClusterIssuer | `nvcf-openbao-pki` | Derived from validated control-plane ID | Plane | Golden render and exact-name cleanup tests |
| OpenBao injector webhook | Chart-generated static name | Per-plane webhook object and service reference | Plane | Webhook section and future Phase 2 render test |
| Admission webhook namespaceSelector | Broad or environment-provided selector | Match workload namespaces plus control-plane owner | Plane | Webhook section and future Phase 2 render test |
| Helm hook Jobs | Release/chart names | Release names or hook names derived per plane | Plane | Golden render |
| Hook RBAC | Cluster-scoped, release-derived | Derived per plane; never shared between planes | Plane | Golden render |
| Gateway routes | Static route names in shared Gateway namespace | Derived where multiple planes attach to one Gateway | Plane | Golden render |
| ReferenceGrants | Static names in backend namespaces | Derived when granting cross-namespace access for a named plane | Plane | Golden render |
| Mirrored workload secrets | Worker namespace secret names | Adopt or label only names expected by the current identity | Plane | Future Phase 2 startup-convergence tests |
| Workload namespaces | Created by NVCA | Label/adopt expected current-plane namespaces before informer processing | Plane | Future Phase 2 fake-client tests |
| NVCA operator CRDs | `nvcfbackends.nvcf.nvidia.com` and NVCA API CRDs | Shared CRDs, never plane-owned | Shared | Prerequisite inventory |
| Model cache namespace | Existing configured namespace | Per-plane by default, or shared and never deleted | Plane or shared | Matrix review |

## Data And Auth Matrix

| Surface | Current Legacy Identity | Named-Plane Target | Notes |
|---|---|---|---|
| Cassandra keyspaces | Existing unprefixed keyspaces | `<id_>_<legacy-keyspace>` | Hyphen-to-underscore transform; keep final keyspace within Cassandra's 48-character unquoted identifier limit. |
| Cassandra role | Shared application role | `<id_>_app` or equivalent derived role | Role and migrations must use the same helper. |
| OpenBao auth mounts | Legacy JWT/service mounts | Paths under the plane identity prefix | No named plane may read or write another plane's mounts. |
| OpenBao PKI | `services/all/pki/...` | Plane-prefixed issuer material | ClusterIssuer must point at the derived path. |
| NATS accounts | Shared account and subjects | Account and subjects scoped by control-plane ID | Invocation, API, NVCA, and auth-callout must derive from one helper. |
| CLI profile artifacts | Default profile paths | Profile paths include the control-plane ID | Legacy paths stay unchanged for default mode. |

## Shared Prerequisite Inventory

Plane teardown must never discover shared prerequisites by broad API group
matching. The prerequisite lifecycle should use Helm release manifests where Helm
tracks objects, plus an exact committed inventory for CRDs and other
cluster-scoped objects Helm does not manage after install.

| Prerequisite | Kind | Name | Owner |
|---|---|---|---|
| cert-manager | CustomResourceDefinition | `certificaterequests.cert-manager.io` | `shared` |
| cert-manager | CustomResourceDefinition | `certificates.cert-manager.io` | `shared` |
| cert-manager | CustomResourceDefinition | `challenges.acme.cert-manager.io` | `shared` |
| cert-manager | CustomResourceDefinition | `clusterissuers.cert-manager.io` | `shared` |
| cert-manager | CustomResourceDefinition | `issuers.cert-manager.io` | `shared` |
| cert-manager | CustomResourceDefinition | `orders.acme.cert-manager.io` | `shared` |
| NVCA operator | CustomResourceDefinition | `nvcfbackends.nvcf.nvidia.com` | `shared` |
| Gateway API | CustomResourceDefinition | To be filled from the managed Gateway API prerequisite source | `shared` |

## Phase 0 Verification

The self-managed stack now has a committed golden-render path:

```sh
cd deploy/stacks/self-managed
make generate-golden
make test-local
```

`make test-local` renders the legacy self-managed stack through a temporary
localhost Helm repository built from repo-local charts, checks the rendered YAML
with a parser, and compares file hashes against `testdata/golden/local`.
