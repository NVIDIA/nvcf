# Phase 3: Gateway and Ingress

This phase installs the ingress layer that exposes NVCF services externally. It consists of
two parts: the Envoy Gateway infrastructure and the NVCF Gateway Routes chart that creates
HTTPRoutes and TCPRoutes for each service.

<Info>
All core services from [standalone-core-services](./standalone-core-services) must be running before proceeding.
The Gateway Routes chart depends on the Notary Service and API Keys being available.

</Info>

## Install Kubernetes Gateway CRDs

Install the Kubernetes Gateway API CRDs if not already present on your cluster:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/experimental-install.yaml
```

<Note>
If replacing the version (v1.2.0), ensure compatibility with the GatewayClass and Gateway
resources created below.

</Note>

## Install Envoy Gateway

Install Envoy Gateway as the Gateway API controller:

```bash
helm install eg oci://docker.io/envoyproxy/gateway-helm \
  --version v1.1.3 \
  -n envoy-gateway-system
```

Verify the Envoy Gateway pods are running:

```bash
kubectl get pods -n envoy-gateway-system

# Expected: envoy-gateway pod Running
```

## Create GatewayClass

Create the GatewayClass resource that binds to the Envoy Gateway controller:

```bash
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: eg
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
EOF
```

## Create Gateway

Create the Gateway resource that provisions the external load balancer.

<Note>
The `annotations` section is **cloud-provider specific** and controls how the external
load balancer is provisioned:

- **AWS (EKS)**: Creates an internet-facing Network Load Balancer
- **GCP (GKE)**: Creates an external HTTP(S) load balancer
- **Azure (AKS)**: Creates a public load balancer
- **On-prem**: Requires a load balancer solution like MetalLB, or use NodePort. Consult your infrastructure documentation.

</Note>

```bash
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: nvcf-gateway
  namespace: envoy-gateway
  annotations:
    # --- AWS (EKS) ---
    service.beta.kubernetes.io/aws-load-balancer-type: "nlb"
    service.beta.kubernetes.io/aws-load-balancer-scheme: "internet-facing"
    # --- GCP (GKE) - use these instead for GCP ---
    # cloud.google.com/load-balancer-type: "External"
    # --- Azure (AKS) - use these instead for Azure ---
    # service.beta.kubernetes.io/azure-load-balancer-internal: "false"
spec:
  gatewayClassName: eg
  listeners:
  - name: http
    protocol: HTTP
    port: 80
    allowedRoutes:
      namespaces:
        from: Selector
        selector:
          matchLabels:
            nvcf/platform: "true"
  - name: tcp
    protocol: TCP
    port: 10081
    allowedRoutes:
      namespaces:
        from: Selector
        selector:
          matchLabels:
            nvcf/platform: "true"
EOF
```

Verify the Gateway is ready and obtain the load balancer address:

```bash
kubectl wait --for=condition=Programmed gateway/nvcf-gateway -n envoy-gateway --timeout=300s

GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
echo "Gateway Address: $GATEWAY_ADDR"
```

<Info>
Save the `GATEWAY_ADDR` value. You will need it for the Gateway Routes configuration
and for verifying API connectivity.

</Info>

## Gateway Routes

The Gateway Routes chart creates HTTPRoutes and TCPRoutes that connect external traffic
to NVCF services through the Gateway.

| **Chart** | `nvcf-gateway-routes` |
| --- | --- |
| **Version** | `1.5.0` |
| **Namespace** | `envoy-gateway-system` |
| **Depends on** | Notary Service, API Keys (must be running), Gateway (must be programmed) |

### Configuration

Create `gateway-routes-values.yaml` ([download template](samples/configs/standalone/gateway-routes-values.yaml)):

<Accordion title="gateway-routes-values.yaml">
```yaml title="gateway-routes-values.yaml"
# Gateway Routes values for standalone installation
# Replace <DOMAIN> with your gateway address (load balancer domain).

nvcfGatewayRoutes:
  domain: "<DOMAIN>"  # e.g. abc123-4567890.us-west-2.elb.amazonaws.com
  gateways:
    shared:
      name: "nvcf-gateway"
      namespace: "envoy-gateway"
    grpc:
      name: "nvcf-gateway"
      namespace: "envoy-gateway"
  routes:
    nvcfApi:
      routeAnnotations: {}
    apiKeys:
      routeAnnotations: {}
    invocation:
      routeAnnotations: {}
    grpc:
      routeAnnotations: {}
```
</Accordion>

Replace `<DOMAIN>` with the `GATEWAY_ADDR` value obtained above.

### Install

```bash
helm upgrade --install ingress \
  oci://${REGISTRY}/${REPOSITORY}/nvcf-gateway-routes \
  --version 1.5.0 \
  --namespace envoy-gateway-system \
  --wait --timeout 10m \
  -f gateway-routes-values.yaml
```

### Verify

```bash
kubectl get httproutes -A

# Expected: HTTPRoutes for nvcf-api, api-keys, invocation, etc.

kubectl get tcproutes -A

# Expected: TCPRoute for gRPC proxy
```

<Note>
For details on how routing works, verification commands, and production DNS/HTTPS setup,
see [gateway-routing](./gateway-routing).

</Note>

## Enable Admin Issuer Proxy Route

The Admin Token Issuer Proxy was installed in [standalone-core-services](./standalone-core-services) with
`gateway.enabled: false` because the Gateway CRDs did not yet exist. Now that the Gateway
is running, upgrade it to enable the admin endpoint HTTPRoute:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')

helm upgrade admin-issuer-proxy \
  oci://${REGISTRY}/${REPOSITORY}/helm-admin-token-issuer-proxy \
  --version 1.2.2 \
  --namespace api-keys \
  --wait --timeout 10m \
  --reuse-values \
  --set adminIssuerProxy.gateway.enabled=true \
  --set adminIssuerProxy.gateway.namespace=envoy-gateway \
  --set adminIssuerProxy.gateway.gatewayRef.name=nvcf-gateway \
  --set "adminIssuerProxy.gateway.hostname=api-keys.${GATEWAY_ADDR}" \
  --set adminIssuerProxy.gateway.path=/v1/admin/keys
```

Verify the admin route was created:

```bash
kubectl get httproutes -A | grep admin

# Expected: admin-token-issuer-proxy HTTPRoute in api-keys namespace
```

## Verify End-to-End Connectivity

With the gateway in place, verify the full stack is functional.

### Generate an Admin Token

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')

export NVCF_TOKEN=$(curl -s -X POST "http://${GATEWAY_ADDR}/v1/admin/keys" \
  -H "Host: api-keys.${GATEWAY_ADDR}" \
  | grep -o '"value":"[^"]*"' | cut -d'"' -f4)

echo "Token generated: ${NVCF_TOKEN:0:20}..."
```

### List Functions

```bash
curl -s -X GET "http://${GATEWAY_ADDR}/v2/nvcf/functions" \
  -H "Host: api.${GATEWAY_ADDR}" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" | jq .

# Expected: empty list of functions (initial state)
```

<Accordion title="(Optional) Create, Deploy, and Invoke a Test Function">
```bash
# Create a test function
curl -s -X POST "http://${GATEWAY_ADDR}/v2/nvcf/functions" \
  -H "Host: api.${GATEWAY_ADDR}" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" \
  -d '{
    "name": "my-echo-function",
    "inferenceUrl": "/echo",
    "healthUri": "/health",
    "inferencePort": 8000,
    "containerImage": "<YOUR_REGISTRY>/<YOUR_REPO>/load_tester_supreme:0.0.8"
  }' | jq .

# Extract function and version IDs from the response
export FUNCTION_ID=<function-id-from-response>
export FUNCTION_VERSION_ID=<version-id-from-response>

# Deploy the function (adjust instanceType and gpu for your cluster)
curl -s -X POST "http://${GATEWAY_ADDR}/v2/nvcf/deployments/functions/${FUNCTION_ID}/versions/${FUNCTION_VERSION_ID}" \
  -H "Host: api.${GATEWAY_ADDR}" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" \
  -d '{
    "deploymentSpecifications": [
      {
        "instanceType": "NCP.GPU.A10G_1x",
        "backend": "nvcf-default",
        "gpu": "A10G",
        "maxInstances": 1,
        "minInstances": 1
      }
    ]
  }' | jq .

# Generate an invocation API key
EXPIRES_AT=$(date -u -v+1d '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d '+1 day' '+%Y-%m-%dT%H:%M:%SZ')
SERVICE_ID="nvidia-cloud-functions-ncp-service-id-aketm"

export API_KEY=$(curl -s -X POST "http://${GATEWAY_ADDR}/v1/keys" \
  -H "Host: api-keys.${GATEWAY_ADDR}" \
  -H "Content-Type: application/json" \
  -H "Key-Issuer-Service: nvcf-api" \
  -H "Key-Issuer-Id: ${SERVICE_ID}" \
  -H "Key-Owner-Id: test@nvcf-api.local" \
  -d '{
    "description": "test invocation key",
    "expires_at": "'"${EXPIRES_AT}"'",
    "authorizations": {
      "policies": [{
        "aud": "'"${SERVICE_ID}"'",
        "auds": ["'"${SERVICE_ID}"'"],
        "product": "nv-cloud-functions",
        "resources": [
          {"id": "*", "type": "account-functions"},
          {"id": "*", "type": "authorized-functions"}
        ],
        "scopes": ["invoke_function", "list_functions", "queue_details", "list_functions_details"]
      }]
    },
    "audience_service_ids": ["'"${SERVICE_ID}"'"]
  }' | jq -r '.value')

echo "API Key: ${API_KEY:0:20}..."

# Wait for deployment to be ready, then invoke the function
curl -s -X POST "http://${GATEWAY_ADDR}/echo" \
  -H "Host: ${FUNCTION_ID}.invocation.${GATEWAY_ADDR}" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -d '{"message": "hello world", "repeats": 1}' | jq .
```

<Note>
The `backend` value should match the cluster group name registered by the NVCA operator.
The `instanceType` and `gpu` values depend on the GPU types available in your cluster.
For invocation, the Host header uses wildcard subdomain routing:
`<function-id>.invocation.<gateway-addr>`.

</Note>
</Accordion>

## Uninstalling

To remove all gateway components:

```bash
# Remove Gateway Routes
helm uninstall ingress -n envoy-gateway-system

# Remove Gateway and GatewayClass resources
kubectl delete gateway nvcf-gateway -n envoy-gateway --ignore-not-found
kubectl delete gatewayclass eg --ignore-not-found

# Uninstall Envoy Gateway
helm uninstall eg -n envoy-gateway-system

# Delete gateway namespaces
kubectl delete namespace envoy-gateway envoy-gateway-system --ignore-not-found

# (Optional) Remove Gateway API CRDs
kubectl delete -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/experimental-install.yaml
```

## Next Steps

Your NVCF control plane is now fully installed and accessible. Proceed to
[self-managed-clusters](./self-managed-clusters) to install the NVCA Operator and connect your GPU nodes
to the control plane.
