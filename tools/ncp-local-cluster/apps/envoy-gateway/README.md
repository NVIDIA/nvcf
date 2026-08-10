# Gateway API Infrastructure

This directory contains configuration for enabling Gateway API support in the k3d cluster using Envoy Gateway.

## Overview

The Gateway API is a Kubernetes-native way to define ingress routing that is:
- **Vendor-neutral**: Works across different ingress controllers
- **Role-oriented**: Separates infrastructure (Gateway) from application routing (HTTPRoute)
- **Expressive**: Supports advanced routing like wildcard subdomains

## Architecture

```
┌─────────────────────────────────────────────────┐
│  Cluster Operator Role (Infrastructure)         │
│  - Gateway API CRDs                             │
│  - Envoy Gateway                                 │
│  - Gateway resource (entry point)               │
└─────────────────────────────────────────────────┘
                      ▲
                      │ parentRefs
                      │
┌─────────────────────────────────────────────────┐
│  Application Developer Role (Routing)           │
│  - HTTPRoute for services (e.g. nginx sample)   │
└─────────────────────────────────────────────────┘
```

## Setup

### Automated Setup (Recommended)

Run the Make target to configure Gateway API infrastructure:

```bash
make setup-gateway-api
```

This will:
1. Install Gateway API CRDs (v1.2.1)
2. Install/upgrade Envoy Gateway v1.5.4 from OCI registry (`oci://docker.io/envoyproxy/gateway-helm`) in the `envoy-gateway-system` namespace
3. Apply GatewayClass and Gateway resources
4. Verify GatewayClass is available

## Verification

After setup, verify the infrastructure is ready:

```bash
# Check CRDs
kubectl get crd gateways.gateway.networking.k8s.io
kubectl get crd httproutes.gateway.networking.k8s.io

# Check GatewayClass
kubectl get gatewayclass eg -o yaml

# Check Envoy Gateway is running
kubectl get pods -n envoy-gateway-system -l app.kubernetes.io/name=envoy-gateway

# Check Gateway resource
kubectl get gateway shared-gw -n envoy-gateway-system

# Check Gateway service (LoadBalancer)
kubectl get svc -n envoy-gateway-system | grep LoadBalancer
```

## Next Steps

Once Gateway API infrastructure is ready:

1. **Deploy and validate the nginx sample**:
   ```bash
   make validate-gateway
   ```
   This will deploy an nginx sample and an `HTTPRoute` to test the gateway. The validation waits for the Gateway to be ready and tests hostname-based routing.

2. **Verify Gateway is ready**:
   ```bash
   kubectl get gateway -n envoy-gateway-system
   kubectl describe gateway shared-gw -n envoy-gateway-system
   ```

3. **Test routing**:
   ```bash
   curl http://nginx.localhost:8080/
   # Should return "Welcome to nginx!"
   ```
   Note: Ensure `nginx.localhost` resolves to `127.0.0.1` (check `/etc/hosts`)

## Troubleshooting

### GatewayClass not found

If `kubectl get gatewayclass` returns no resources:
1. Check Envoy Gateway logs:
   ```bash
   kubectl logs -n envoy-gateway-system -l app.kubernetes.io/name=envoy-gateway
   ```
2. Check Envoy Gateway deployment status:
   ```bash
   kubectl get deployment -n envoy-gateway-system
   kubectl describe deployment eg -n envoy-gateway-system
   ```

### HTTPRoute not attaching

Check HTTPRoute status:
```bash
kubectl get httproute -A
kubectl describe httproute nginx-route -n sample
```

Common issues:
- Gateway doesn't exist yet
- Namespace mismatch in `parentRefs`
- Backend service doesn't exist
- Cross-namespace routing blocked by missing `ReferenceGrant`

### Gateway route validation fails

If `make validate-gateway` fails:
1. Check Gateway status:
   ```bash
   kubectl get gateway shared-gw -n envoy-gateway-system -o yaml
   ```
   Ensure `status.conditions` shows `Programmed: True`
2. Check HTTPRoute is attached:
   ```bash
   kubectl get gateway shared-gw -n envoy-gateway-system -o jsonpath='{.status.listeners[0].attachedRoutes}'
   ```
3. Verify hostname resolution:
   ```bash
   ping nginx.localhost
   # Should resolve to 127.0.0.1
   ```
   If not, add to `/etc/hosts`: `127.0.0.1 nginx.localhost`

## Configuration Files

- `gatewayclass.yaml`: Defines the Envoy Gateway GatewayClass (`eg`)
- `gateway.yaml`: Defines the default Gateway resource (`shared-gw`) in `envoy-gateway-system` namespace
- `kustomization.yaml`: Kustomize configuration for easy deployment

## Installation Details

- **Version**: Envoy Gateway v1.5.4 (pinned)
- **Installation Method**: Helm via OCI registry
- **Chart**: `oci://docker.io/envoyproxy/gateway-helm`
- **Namespace**: `envoy-gateway-system`
- **Release Name**: `eg`

## References

- [Kubernetes Gateway API](https://gateway-api.sigs.k8s.io/)
- [Envoy Gateway Documentation](https://gateway.envoyproxy.io/)
- [Envoy Gateway GitHub](https://github.com/envoyproxy/gateway)
