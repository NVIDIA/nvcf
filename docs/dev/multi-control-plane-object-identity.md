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
| ClusterIssuer | `nvcf-openbao-pki` | Phase 4H derives the stack-managed issuer as `<owner>-nvcf-openbao-pki` for named mode | Plane | Golden render and exact-name cleanup tests |
| OpenBao injector webhook | Chart-generated static name | Per-plane webhook object and service reference derived from the named OpenBao fullname | Plane | Phase 4G focused OpenBao render test |
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
preserving the legacy render output. Phase 4B then plumbs the validated
control-plane owner into the Helmfile namespace prefix so named renders use the
same rule that the Go helper already tests.

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

## Phase 4A Alpha Named-Mode Gate

Phase 4A adds an explicit alpha opt-in before any named control plane can be
requested. It also keeps named mode fail-closed while the remaining named-mode
identity work is wired.

Legacy/default mode does not need this opt-in and must keep rendering exactly as
before. A user-provided `--control-plane-id` requires
`--alpha-named-control-plane` in the CLI. Direct stack users must set
`NVCF_ALPHA_NAMED_CONTROL_PLANE=true` when they set `NVCF_CONTROL_PLANE_OWNER`
to any value other than `default`, and the stack must still fail if a named
owner would render before all required identity derivation is complete.

This guard is present in every stack file that declares the namespace prefix,
including self-managed `global.yaml.gotmpl` and the compute-plane dependencies
state. That keeps direct single-file Helmfile invocations from bypassing the
fail-closed rule.

This gate deliberately does not make named mode usable yet. It prevents both
accidental use and unsafe direct use while the remaining Phase 4 subphases make
object namespaces, service DNS names, ClusterIssuer names, gateway routes,
OpenBao injector objects, and auth/data identities safe.

## Phase 4B Named Namespace Derivation

Phase 4B wires the stack namespace prefix to the validated control-plane owner,
but keeps named mode fail-closed at the object-render layer.

The legacy `default` owner still uses an empty prefix, so default renders keep
their historical namespace names. A named owner such as `plane-a`, with
`NVCF_ALPHA_NAMED_CONTROL_PLANE=true`, computes the prefix `plane-a-`.

Examples:

- self-managed `nvcf` becomes `plane-a-nvcf`;
- self-managed `api-keys` becomes `plane-a-api-keys`;
- self-managed `cassandra-system` becomes `plane-a-cassandra-system`;
- compute-plane `nvca-operator` becomes `plane-a-nvca-operator`.

Shared prerequisite namespaces stay unprefixed. Examples include
`cert-manager`, `kai-scheduler`, `grove-system`, and `dynamo-system`.

The namespace-derivation fail-closed guard remains in every prefix-declaring
template as a tripwire. For a valid named owner it no longer fires because the
prefix is non-empty. If a later refactor accidentally clears that prefix while
named mode is requested, the render fails before it can write objects into
legacy namespaces.

Phase 4B also adds a second fail-closed guard for object-level identity
derivation. That guard blocks named renders until gateway routes,
ReferenceGrants, service DNS defaults, OpenBao injector cluster-scoped objects,
and ClusterIssuer naming are all isolated. This is required because a Helmfile
release namespace alone does not guarantee that every Kubernetes object rendered
by that release stays out of legacy namespaces.

The CLI validates `--control-plane-id` and the alpha opt-in, but still refuses
named IDs with the same object-level identity message until the later Phase 4
subphases remove this temporary block.

Named namespace cleanup remains conservative in this phase. The Makefile
teardown path still destroys Helm releases with the named owner selector, but it
does not delete named namespaces until the shared prerequisite lifecycle is wired
later in Phase 4.

## Phase 4C Named Render Isolation Checker

Phase 4C adds the object-level render checker that later Phase 4 work must pass
before the named-mode block is removed.

The checker parses rendered YAML instead of grepping raw text. It fails a named
render when it finds any of these unsafe identities:

- an object namespace, `Namespace`, gateway backend reference, RoleBinding
  subject, or other `namespace` field that still points at a legacy plane-owned
  namespace such as `nvcf`, `api-keys`, `sis`, or `vault-system`;
- an in-cluster service DNS name that still points at a legacy plane-owned
  namespace, such as `openbao-server.vault-system.svc.cluster.local`;
- a gateway route in any unprefixed namespace whose name is not derived from the
  control-plane owner, unless that exact route identity is explicitly allowed as
  shared;
- any cluster-scoped object that is not derived from the control-plane owner,
  unless that exact object identity is explicitly allowed as shared;
- the same Kubernetes object identity appearing in two named renders, unless
  that exact object identity is explicitly allowed as shared.

This phase intentionally keeps named mode fail-closed. The checker has synthetic
positive and negative tests now; later Phase 4 subphases must run it against real
named renders after each object-level leak class is fixed.

The cluster-scoped, unprefixed gateway-route, and duplicate-identity rules are
intentionally allowlist-based. A `shared` owner label is not enough by itself,
because a chart could accidentally mark a colliding object as shared. Truly
shared identities, such as shared prerequisite CRDs, webhook RBAC, or a shared
gateway route, must be passed with `--allow-shared-identity` or listed in a
committed `--allow-shared-identity-file` when the real named-render gate is
enabled. Duplicate identities across two named renders use the same rule, even
when both objects are labelled `shared`.

## Phase 4D Gateway Route Identity

Phase 4D wires the self-managed stack to pass owner-derived gateway route
identities when a named control plane is rendered.

For the legacy `default` owner, the stack still leaves the gateway-routes chart
defaults in control where possible, so the default render stays byte-identical.
For a named owner such as `plane-a`, the stack passes values like these into the
gateway-routes chart:

- route names such as `plane-a-nvcf-api`, `plane-a-api-keys`, and
  `plane-a-grpc`;
- route backend namespaces such as `plane-a-nvcf`, `plane-a-api-keys`,
  `plane-a-sis`, `plane-a-nats-system`, and `plane-a-nvcf-ui`;
- optional route names for gRPC, worker, NATS, LLM, vanity-gateway, ESS, UI,
  and event-ledger routes.

The gateway-routes chart also derives ReferenceGrant names and target namespaces
from those route values. For example, a named API keys route renders
`ReferenceGrant/plane-a-api-keys/allow-httproute-to-plane-a-api-keys` instead of
writing `ReferenceGrant/api-keys/allow-httproute-to-api-keys`.

Named mode intentionally remains fail-closed after this phase. Gateway route
identity is one leak class; service DNS, OpenBao injector cluster-scoped
objects, and ClusterIssuer identity still need to pass the Phase 4C checker
before the block can be removed.

## Phase 4E Admin And NATS Identity References

Phase 4E removes two remaining object-level references that still pointed at
legacy identities after the gateway-routes chart was fixed.

The admin-token-issuer-proxy chart renders its own HTTPRoute outside the
gateway-routes chart. For a named owner, the self-managed stack now passes a
prefixed `fullnameOverride`, so that chart renders objects such as
`HTTPRoute/gateway/plane-a-admin-token-issuer-proxy` and a backend reference to
`Service/plane-a-api-keys/plane-a-admin-token-issuer-proxy`. The same derived
name also feeds the `nvcf-ui` control-plane health endpoint so the health check
does not point at the old service name inside the named namespace.

The NATS chart already had a value for the OpenBao migration ServiceAccount
namespace. For a named owner, the self-managed stack now passes the derived
vault namespace, so the `nkey-bao-access` RoleBinding subject points at
`plane-a-vault-system` instead of `vault-system`.

Named mode intentionally remains fail-closed after this phase. Service DNS,
OpenBao injector cluster-scoped objects, ClusterIssuer identity, and the final
shared-prerequisite allowlist still need to pass before the object-level block
can be removed.

## Phase 4F Service DNS Identity

Phase 4F removes the rendered service DNS references that still pointed at
legacy plane-owned namespaces in named mode.

The self-managed stack now derives the common in-cluster service addresses from
the owner-derived namespaces before passing values into the service charts. For
a named owner such as `plane-a`, rendered values now point at addresses such as:

- `plane-a-openbao.plane-a-vault-system.svc.cluster.local:8200`;
- `api.plane-a-nvcf.svc.cluster.local:9090`;
- `api-keys.plane-a-api-keys.svc.cluster.local:8080`;
- `nats.plane-a-nats-system.svc.cluster.local:4222`;
- `llm-request-router.plane-a-nvcf.svc.cluster.local:8000`.

Several charts now expose the OpenBao token audience or bootstrap OpenBao
address as values instead of hardcoding the legacy namespace. The legacy chart
defaults remain the same, and the stack only overrides them for named owners, so
the default golden render stays byte-identical.

The LLM request-router certificate DNS defaults are also translated for named
owners when the operator has not supplied custom DNS names. This keeps the
fail-closed PKI check from Phase 3 while preventing the default
`llm-request-router.nvcf.svc.cluster.local` DNS names from leaking into a named
render.

A focused `service-dns-named-isolation.sh` test temporarily unblocks named mode
in a copied stack, renders the affected charts, checks the important derived
values directly, and then runs the Phase 4C object-level isolation checker over
the rendered manifests. After this phase, the service-DNS class passes that
checker in the full named render.

Named mode intentionally remains fail-closed after this phase. OpenBao injector
cluster-scoped objects, ClusterIssuer identity, and the final shared-prerequisite
allowlist still need to pass before the object-level block can be removed.

## Phase 4G OpenBao Injector Identity

Phase 4G removes the OpenBao injector cluster-scoped object collisions for named
mode.

The upstream OpenBao chart derives the injector names from the same
`fullnameOverride` used by the server. For the legacy `default` owner, the stack
continues to rely on the chart default `openbao-server`, preserving objects such
as `MutatingWebhookConfiguration/openbao-server-agent-injector-cfg`.

For a named owner, the stack now passes a shorter named OpenBao fullname:
`<owner>-openbao`. The shorter base name is deliberate. A 30-character control
plane ID plus the upstream `-agent-injector-svc` suffix would exceed the
63-character Service name limit if the base name stayed `openbao-server`.

With owner `plane-a`, the rendered OpenBao dependency now uses identities such
as:

- `Service/plane-a-vault-system/plane-a-openbao-agent-injector-svc`;
- `Deployment/plane-a-vault-system/plane-a-openbao-agent-injector`;
- `MutatingWebhookConfiguration/plane-a-openbao-agent-injector-cfg`;
- `ClusterRole/plane-a-openbao-agent-injector-clusterrole`;
- `ClusterRoleBinding/plane-a-openbao-agent-injector-binding`;
- `StatefulSet/plane-a-vault-system/plane-a-openbao`.

Because the server fullname changes too, the stack also derives the named-mode
OpenBao service DNS, unseal secret volume, auto-unseal sidecar mount, UI health
inventory, migration job service address, and HA raft peer addresses from the
same fullname. This keeps the server, injector, hooks, and migration job pointed
at the same named OpenBao installation.

Phase 4G also follows that renamed OpenBao identity through the downstream
consumers that reference OpenBao-managed objects:

- the NATS nkey RoleBinding subject now points at the named OpenBao initialize
  ServiceAccount;
- the LLM request-router PKI migration hook now uses the named OpenBao service,
  initialize ServiceAccount, and root-token Secret;
- the LLS/SIS HMAC migration and rotation hooks now use the named OpenBao
  service, initialize ServiceAccount, root-token Secret, and vault namespace;
- the managed PKI release now points at the named OpenBao service URL for its
  backend `server` and service-account-token `audience` values.

The Phase 4C isolation checker now also rejects known stale legacy object
references, such as `openbao-server-root-token` or
`admin-token-issuer-proxy.<named-namespace>.svc`, inside named renders. This
catches same-namespace stale references that are not visible from namespace-only
or duplicate-identity checks.

A focused `openbao-injector-named-isolation.sh` test temporarily unblocks named
mode in a copied stack, renders the real OpenBao dependency chart, checks the
values and rendered manifests, and runs the Phase 4C isolation checker on that
OpenBao render. The test covers both `plane-a` and a 30-character owner to
protect the Kubernetes name-length edge.

Named mode intentionally remains fail-closed after this phase. The managed
ClusterIssuer object's own identity and the final shared-prerequisite allowlist
still need to pass before the object-level block can be removed.

## Phase 4H Managed ClusterIssuer Identity

Phase 4H removes the managed PKI `ClusterIssuer` name collision for named mode.

The default control plane still uses the legacy cluster-wide issuer name:
`nvcf-openbao-pki`. For a named owner, the stack-managed default issuer is
derived from the owner instead. With owner `plane-a`, the managed issuer becomes
`plane-a-nvcf-openbao-pki`. The legacy default is normalized this way even when
it appears explicitly in inherited named-plane values.

Both sides of the contract use the same effective name:

- the `nvcf-pki` dependency release creates
  `ClusterIssuer/plane-a-nvcf-openbao-pki`;
- the LLM request-router Certificate references
  `ClusterIssuer/plane-a-nvcf-openbao-pki`;
- the LLM worker gRPC TLS values default to the same issuer name when no
  explicit issuer override is provided.

If an operator sets a custom external issuer name, the stack keeps that custom
name. If the stack itself is asked to manage a custom ClusterIssuer for a named
plane, the name must use the named control-plane prefix so the cluster-scoped
object cannot collide with another plane. That validation is specifically for
custom managed issuers; the legacy default issuer name is normalized to the
derived managed name before validation runs.

The named service-DNS isolation test now also renders the real `nvcf-pki` chart in a
temporarily unblocked named stack and verifies that the rendered ClusterIssuer
name, OpenBao backend URL, and request-router issuer references all agree.

Named mode intentionally remains fail-closed after this phase. The remaining
blocker is the shared-prerequisite allowlist, which must explicitly review
objects that are supposed to be shared instead of per-plane.

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
