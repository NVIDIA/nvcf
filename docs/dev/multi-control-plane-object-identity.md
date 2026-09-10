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

## ID Budget

User control-plane IDs are capped at 30 characters even though Kubernetes DNS
labels allow 63 characters. Cassandra keyspace names are the tighter downstream
consumer.

| Consumer | Constraint | Result |
|---|---|---|
| Kubernetes names | DNS-1123 label, up to 63 characters | IDs must be lowercase DNS labels. |
| Cassandra keyspaces and roles | Unquoted CQL identifiers allow letters, digits, and underscores, up to 48 characters | IDs are transformed from hyphenated DNS labels to underscore-safe Cassandra prefixes. The longest known legacy keyspace is `schema_migrations`, so `48 - 1 - len("schema_migrations") = 30`. |
| OpenBao mount paths and NATS accounts/subjects | Exact legacy layout still to confirm before those phases change behavior | These consumers must not loosen the 30-character limit unless this table and the control-plane identity tests are updated together. |

## Kubernetes Object Matrix

| Surface | Current Legacy Identity | Named-Plane Target | Owner | Phase 0 Evidence |
|---|---|---|---|---|
| Control namespaces | `nvcf`, `api-keys`, `ess`, `sis` | Derived from validated control-plane ID | Plane | Golden render and lifecycle inventory |
| Data namespaces | `cassandra-system`, `nats-system`, `vault-system` | Derived from validated control-plane ID | Plane | Golden render and lifecycle inventory |
| Ingress namespace | Environment-provided Gateway API namespace | Shared or externally managed; never deleted by plane teardown | Shared or external | Golden render and matrix review |
| cert-manager namespace | `cert-manager` | Shared prerequisite namespace | Shared | Golden render and prereq inventory |
| Service names | Chart defaults such as `api`, `ess-api`, `grpc-proxy` | Derived only where cross-plane collision exists | Plane | Golden render |
| ClusterIssuer | `nvcf-openbao-pki` | Deferred to Phase 4; derive from validated control-plane ID before named mode is enabled | Plane | Golden render and exact-name cleanup tests |
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

## Phase 2 Ownership Audit

Phase 2 adds owner labels and lifecycle guards without changing legacy object
names. Named-mode namespace derivation is a later phase, so any fixed legacy
singleton below is either guarded now or explicitly deferred here.

Legacy compatibility intentionally has one narrow exception to the "missing owner
means do not touch" rule: the default control plane may adopt known legacy
objects by adding `nvcf.nvidia.com/control-plane-owner=default`. This preserves
legacy upgrade and teardown behavior for clusters installed before ownership
labels existed. Named control planes do not get that exception. The CLI mixed-mode
gate blocks a named install until the legacy default plane has been upgraded and
those known objects are labelled.

| Surface | Phase 2 Status | Evidence |
|---|---|---|
| Workload namespace creation | Plane-scoped. NVCA writes the owner label when it creates or updates a request namespace. | `pkg/operator/reconcile/nvcaagent_reconcile.go` |
| Workload namespace cleanup | Plane-scoped. Cleanup requires both the workload label and the owner label. Legacy adoption can label old default-plane workload namespaces; named planes do not adopt legacy fixed-name objects. | `pkg/operator/cleanup/cleanup.go`, `pkg/operator/reconcile/control_plane_adoption.go` |
| Bare workload-label adoption scan | Default-plane adoption may list namespaces with only `nvca.nvcf.nvidia.io/workload-instance-type` to find old workload namespaces created before owner labels existed. That scan is not used by named planes and is not a deletion boundary; deletion still requires the owner label. | `pkg/operator/reconcile/control_plane_adoption.go`, `pkg/operator/cleanup/cleanup.go` |
| Admission webhook selectors | Plane-scoped. Webhook object names are derived from the plane identity, and namespace selectors require both workload type and owner. | `pkg/operator/reconcile/webhooks.go` |
| Mirrored image-pull secrets | Plane-scoped. Mirrored secrets carry the owner label, cleanup selects by owner, and foreign or unowned targets are skipped. | `pkg/operator/mirror/controller.go` |
| Self-managed and compute-plane Helm output | Plane or shared scoped. Post-renderers label rendered objects, and placement tests reject owner labels in selectors and pod-template labels. | `deploy/stacks/*/renderers/control-plane-owner-label.sh`, `deploy/stacks/*/tests/verify-control-plane-owner-label-placement.sh` |
| Model-cache init namespace | Deferred. It is still the fixed legacy namespace in Phase 2. Default-plane adoption can label existing legacy state; cleanup refuses missing, foreign, or shared owners. Per-plane or shared final semantics must be resolved with Phase 3 namespace derivation before named mode is enabled. | `pkg/storage/controller_modelcache.go`, `pkg/nvca/backendk8scache.go`, `pkg/nvca/backendk8scache_gxcache.go` |
| Lower NVCA internal workload-label filters | Deferred with model-cache namespace derivation. These filters are inside one agent's own cache/reconcile loop and are not used as a cross-plane deletion or admission boundary in Phase 2. | `pkg/nvca/backendk8scache.go`, `internal/miniservice/reconcile.go` |

### Deferred Phase 3 Risk

The model-cache init namespace is still a fixed legacy singleton in Phase 2.
Do not enable named control planes broadly until Phase 3 decides whether this
namespace becomes per-plane or shared and updates creation, cleanup, and webhook
selectors consistently.

## Phase 3 Namespace Derivation Contract

Phase 3 routes namespace and service DNS identities through one naming contract.
Go callers use the shared helper in
`src/libraries/go/lib/pkg/types/controlplane`. Helmfile templates cannot import
that Go package directly, so the stack templates currently mirror the same
legacy-compatible naming rule.

The helper contract is intentionally simple:

- The default identity keeps every legacy namespace name unchanged.
- A named identity prefixes each known stack namespace with the validated
  control-plane ID.
- In-cluster service DNS uses the derived namespace while keeping the legacy
  service name.
- All derived namespace names must remain valid Kubernetes DNS labels, including
  when the control-plane ID is at the maximum supported length.

Phase 3 wires Helmfile templates and service URLs to that naming rule while
preserving the legacy render output. The Helmfile namespace prefix is still an
empty literal in this phase; Phase 4 must plumb the validated control-plane ID
into those templates before named mode can install.

ClusterIssuer naming is intentionally still legacy-fixed in Phase 3. Phase 4
must derive the default issuer name and update the managed-issuer detection
together, because the current detection still keys on exact
`ClusterIssuer/nvcf-openbao-pki`.

Managed LLM router PKI DNS names are also intentionally legacy-explicit in
Phase 3. `environments/base.yaml` still lists the legacy service DNS names so
the existing fail-closed PKI guard keeps default installs working without
silently inventing certificate names. Phase 4 must add a named-mode derivation
path for those DNS names without weakening that guard. A named render must not
leave legacy service DNS such as `llm-request-router.nvcf.svc.cluster.local` or
`*.llm-request-router-headless.nvcf.svc.cluster.local` in generated certificate
configuration.

## Render Structure Guard

Both self-managed and compute-plane local render tests run a parser-based
structure guard before comparing against golden files.

The guard checks rendered Kubernetes manifests for:

- valid YAML documents;
- glued YAML separators from missing document breaks;
- `apiVersion`, `kind`, and `metadata.name` on every Kubernetes object;
- Kubernetes object names over 253 characters;
- namespace fields that are not valid Kubernetes DNS labels or are over 63
  characters;
- Kyverno `Policy` and `ClusterPolicy` rule names over 63 characters.

The compute-plane render output also contains `*-values.yaml` handoff files.
Those files are values input, not Kubernetes manifests, so the guard skips them.

This Phase 3 guard keeps the legacy render safe while names are being routed
through derivation helpers. The future two-plane named render check still belongs
to Phase 4, once the control-plane ID is user-settable through the stack values.

CI coverage is asymmetric by design in Phase 3. The self-managed CI job runs a
fresh local render and compares it with golden output. The compute-plane CI job
runs the static stack tests plus the structure guard against committed golden
fixtures, but it does not perform a fresh compute-plane render. A full
compute-plane render still belongs to local or credentialed test runs because
that stack resolves published chart dependencies from external repositories and
registries.

## Data And Auth Matrix

| Surface | Current Legacy Identity | Named-Plane Target | Notes |
|---|---|---|---|
| Cassandra keyspaces | `nvcf_api`, `api_keys_api`, `ess_api`, `sis_api`, `nvct_api`, `nvcf_autoscaler`, `event_ledger`, `schema_migrations`, `nvcf_api_keys` | `<id_>_<legacy-keyspace>` | Hyphen-to-underscore transform; keep final keyspace within Cassandra's 48-character unquoted identifier limit. `schema_migrations` leaves a 30-character ID budget. |
| Cassandra role | Shared application role; exact legacy role name still to confirm before Phase 5 | `<id_>_app` or equivalent derived role | Role and migrations must use the same helper. |
| OpenBao auth mounts | Legacy JWT/service mounts; exact path inventory still to confirm before Phase 5 | Paths under the plane identity prefix | No named plane may read or write another plane's mounts. |
| OpenBao PKI | `services/all/pki/...`; full legacy layout still to confirm before Phase 5 | Plane-prefixed issuer material | ClusterIssuer must point at the derived path. |
| NATS accounts | Shared account and subjects; exact account inventory still to confirm before Phase 5 | Account and subjects scoped by control-plane ID | Invocation, API, NVCA, and auth-callout must derive from one helper. |
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
| Gateway API | CustomResourceDefinition | External prerequisite in this repo snapshot; the self-managed stack renders Gateway routes but does not install a managed Gateway API CRD inventory here | `shared` |

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
