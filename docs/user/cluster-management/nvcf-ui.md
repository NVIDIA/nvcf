(enabling-nvcf-ui)=

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
- Gateway API ingress must be configured. See [Gateway Routing](../gateway-routing.md).

## Enable the gateway route

In your Helmfile environment values file (for example
`environments/<environment-name>.yaml`), set:

```yaml
ingress:
  gatewayApi:
    routes:
      nvcfUi:
        enabled: true
```

Then sync the ingress release to apply:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress sync
```

The UI is available at `http://nvcf-ui.<domain>` after the HTTPRoute is ready.

## Verify

```bash
kubectl get httproute nvcf-ui -n envoy-gateway
```

The route should show `Accepted` status and the hostname `nvcf-ui.<domain>`.
