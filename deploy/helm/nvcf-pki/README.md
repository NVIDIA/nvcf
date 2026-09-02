# NVCF PKI Helm Chart

This chart creates the cert-manager `ClusterIssuer` used for NVIDIA Cloud
Functions (NVCF) service TLS in a self-managed deployment. The issuer uses an
OpenBao PKI signing path and authenticates with a projected Kubernetes
ServiceAccount token.

The chart is opt-in. It renders no Kubernetes resources unless
`clusterIssuer.enabled=true`.

## Scope

The chart owns one cluster-scoped `ClusterIssuer`. It does not install or
configure:

- cert-manager or its custom resource definitions
- OpenBao
- the OpenBao PKI mount, signing role, authentication mount, or policy
- application `Certificate` resources

Provision those dependencies before enabling the issuer. The self-managed
OpenBao migrations can provision the NVCF service-issuing PKI backend. See the
[LLM OpenBao addon](../../../migrations/openbao/addons/llm/README.md) for its
default paths and role bindings.

## Prerequisites

- Kubernetes cluster
- Helm 3
- `kubectl` configured for the target cluster
- cert-manager with the `ClusterIssuer` custom resource definition installed
- OpenBao with a PKI signing path and JWT authentication role
- cluster-wide permission to create a `ClusterIssuer`

The cert-manager controller must have permission to request a token for the
ServiceAccount named by `clusterIssuer.auth.serviceAccount.name`. That
ServiceAccount must exist in cert-manager's cluster resource namespace,
typically `cert-manager`.

The OpenBao JWT role must bind the same ServiceAccount name and namespace. Its
bound audience must match
`clusterIssuer.auth.serviceAccount.audience`.

## Configuration

Chart defaults are defined in [values.yaml](./values.yaml).

| Value | Default | Description |
| --- | --- | --- |
| `clusterIssuer.enabled` | `false` | Creates the `ClusterIssuer` when set to `true`. |
| `clusterIssuer.name` | `nvcf-openbao-pki` | Name of the cluster-scoped issuer. |
| `clusterIssuer.controlPlaneID` | empty | Optional control-plane owner label used by named self-managed deployments. |
| `clusterIssuer.server` | empty | OpenBao server URL reachable from cert-manager. |
| `clusterIssuer.path` | empty | OpenBao PKI signing path, excluding the `/v1/` prefix. |
| `clusterIssuer.auth.mountPath` | `/v1/auth/jwt` | OpenBao authentication mount used for login. |
| `clusterIssuer.auth.role` | `cert-manager` | OpenBao JWT authentication role. |
| `clusterIssuer.auth.serviceAccount.name` | `cert-manager` | ServiceAccount used to request a projected token. |
| `clusterIssuer.auth.serviceAccount.audience` | empty | Token audience accepted by the OpenBao JWT role. |

The chart fails to render when it is enabled and any required value is empty.
When `clusterIssuer.controlPlaneID` is non-empty, the chart adds the
`nvcf.nvidia.com/control-plane-id` label. This label lets the self-managed
lifecycle remove retained issuers belonging to one named control plane without
removing another plane's or an external issuer.

Example values:

```yaml
clusterIssuer:
  enabled: true
  name: nvcf-openbao-pki
  server: https://openbao.example.com:8200
  path: services/all/pki/nvcf-service-issuing/sign/nvcf-service-server
  auth:
    mountPath: /v1/auth/jwt
    role: cert-manager
    serviceAccount:
      name: cert-manager
      audience: https://openbao.example.com:8200
```

## Installation

Install from the repository root with an environment-specific values file:

```bash
helm upgrade --install nvcf-pki deploy/helm/nvcf-pki \
  --namespace cert-manager \
  --create-namespace \
  --values path/to/nvcf-pki-values.yaml
```

The Helm release namespace does not set the namespace of the
`ClusterIssuer`, which is cluster-scoped. Use the cert-manager namespace for
the release unless your deployment has a different convention.

## Verification

Check the issuer status:

```bash
kubectl get clusterissuer nvcf-openbao-pki
kubectl describe clusterissuer nvcf-openbao-pki
kubectl wait \
  --for=condition=Ready \
  clusterissuer/nvcf-openbao-pki \
  --timeout=2m
```

If the issuer does not become ready, verify:

- cert-manager can reach `clusterIssuer.server`
- the signing path and authentication mount exist in OpenBao
- the JWT role binds the configured ServiceAccount name and namespace
- the projected token audience matches the OpenBao role's bound audience
- cert-manager can create a token for the referenced ServiceAccount

## Retained issuer lifecycle

The chart sets `helm.sh/resource-policy: keep` on the `ClusterIssuer`. Helm
retains the issuer when an upgrade, rollback, or uninstall would otherwise
delete it. The retained issuer remains active but is no longer managed by the
release.

These operations retain existing issuers:

- Setting `clusterIssuer.enabled=false`
- Uninstalling the release
- Changing `clusterIssuer.name`
- Rolling back to a revision that does not contain the issuer

Before disabling or uninstalling the chart, move or remove every
`Certificate` that references the issuer. When changing the issuer name:

1. Upgrade the release with the new name.
2. Wait for the new issuer to report `Ready=True`.
3. Move certificate consumers to the new issuer.
4. Delete the old issuer only after no certificate references it.

List certificate references before deleting a retained issuer:

```bash
kubectl get certificates --all-namespaces \
  -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,ISSUER-KIND:.spec.issuerRef.kind,ISSUER-NAME:.spec.issuerRef.name
```

Delete the retained issuer only after confirming it has no consumers:

```bash
kubectl delete clusterissuer nvcf-openbao-pki
```

## Local validation

Run from the repository root:

```bash
helm lint deploy/helm/nvcf-pki
helm template nvcf-pki deploy/helm/nvcf-pki >/dev/null
deploy/helm/nvcf-pki/scripts/check-render.sh
```
