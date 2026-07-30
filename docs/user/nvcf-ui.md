# Enabling NVCF UI

The NVCF UI is an optional web interface for managing NVCF deployments. It is
disabled by default. Enable it only when the `nvcf-ui` addon is installed in
your cluster.

The NVCF UI addon runs as a Service named `nvcf-ui` in the `nvcf-ui` namespace
on port 8300. When enabled, the gateway-routes chart creates an HTTPRoute and a
ReferenceGrant that forward requests from `nvcf-ui.<domain>` to that Service.

## Prerequisites

- The `nvcf-ui` addon must be deployed in the `nvcf-ui` namespace before
  enabling the gateway route.
- Gateway API ingress must be configured. See [Gateway Routing](./gateway-routing.md).

## Enable the gateway route

The `nvcfUi` route is not wired through the standard environment file. Enable it
by adding a values override to the `ingress` release in
`helmfile.d/02-core.yaml.gotmpl`:

```yaml
- name: ingress
  chart: nvcf/nvcf-gateway-routes
  ...
  values:
    - ../global.yaml.gotmpl
    - nvcfGatewayRoutes:
        routes:
          nvcfUi:
            enabled: true
```

<Note>
When adding `values` to a release that already uses `../global.yaml.gotmpl`,
you must keep that entry in the list. YAML merge replaces lists entirely.
</Note>

Then sync the ingress release to apply:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress sync
```

The UI is available at `http://nvcf-ui.<domain>` after the HTTPRoute is ready.

## Verify

```bash
kubectl get httproute -A --field-selector=metadata.name=nvcf-ui
```

The route should show `Accepted` status and the hostname `nvcf-ui.<domain>`.
