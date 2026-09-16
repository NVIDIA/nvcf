# Enabling NVCF UI

NVCF UI is an optional customer-facing NVCF admin-panel UI. It is disabled by
default and is available only in stack packages that include the NVCF UI addon.
If your extracted stack package does not contain a `nvcf-ui` release and
`nvcfUi` route values, skip this page until you use a stack package that
includes them.

<Warning>
The NVCF UI admin panel is currently unauthenticated. Do not expose it to the
public internet. Restrict access to a trusted network, VPN, or an
authenticating proxy in front of the `nvcf-ui` route.
</Warning>

The addon runs as a Service named `nvcf-ui` in the `nvcf-ui` namespace on port
8300. When it is enabled, the gateway-routes chart also creates an HTTPRoute and
a ReferenceGrant that forward requests from `nvcf-ui.<domain>` to that Service,
where `<domain>` is the `global.domain` value in your environment file.

## Prerequisites

- A stack package that includes the `nvcf-ui` release and `nvcfUi` route values.
- Gateway API ingress configured. See
  [Gateway Routing](./gateway-routing.md#nvcf-ui-optional).

## Enable the addon

Set the addon flag in your environment file
(`environments/<environment-name>.yaml`):

```yaml
addons:
  nvcfUi:
    enabled: true
```

This single flag deploys the `nvcf-ui` release and enables the `nvcfUi`
HTTPRoute and ReferenceGrant on the shared Gateway. No `helmfile.d` edit is
needed.

## Configure the image pull secret (conditional)

`nvcf-ui` runs in its own `nvcf-ui` namespace, separate from the namespaces
covered by the main install step. If your `image` registry is private and your
cluster nodes do not have built-in credential helpers, create a
`docker-registry` secret in that namespace. See
[Enabling NVCF UI](./helmfile-installation.md#enabling-nvcf-ui) in the Helmfile
Installation guide for the exact commands.

## Apply

Preview and apply the service, then sync the ingress release so the route is
created:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector name=nvcf-ui template
HELMFILE_ENV=<environment-name> helmfile --selector name=nvcf-ui sync
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress sync
```

The UI is available at `http://nvcf-ui.<domain>` once the HTTPRoute is accepted.

## Verify

```bash
kubectl get deploy,svc -n nvcf-ui

kubectl get httproute -A --field-selector=metadata.name=nvcf-ui \
  -o jsonpath='{range .items[*]}{.metadata.namespace}{"\t"}{.spec.hostnames[0]}{"\t"}{.status.parents[*].conditions[?(@.type=="Accepted")].status}{"\n"}{end}'

curl -i -H "Host: nvcf-ui.<domain>" "http://<gateway-address>/status"
```

The route lives in the Gateway namespace configured for your install, its
hostname is `nvcf-ui.<domain>`, and the `Accepted` condition reports `True`.
