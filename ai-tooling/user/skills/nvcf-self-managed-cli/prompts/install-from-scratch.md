# Install self-hosted NVCF from scratch

User wants to bring up self-hosted NVCF on a fresh Kubernetes cluster (or k3d for local dev). This is the most common entrypoint.

## Steps

1. Confirm the topology. Ask the user:
   - "Single-cluster (control plane and compute plane on one cluster, simpler) or split (control plane on cluster A, compute plane on cluster B, production-shaped)?"
   - "What's the cluster name (the `--cluster-name` flag, becomes the ICMS row identifier)? Examples: `ncp-local` for local dev, `prod-us-east-1` for production."
   - For split: "Control-plane kubeconfig context name? Compute-plane context? Public ICMS URL?"

2. Prepare remote Gateway and CLI config if this is not local k3d. One-click
   applies the control plane, then immediately calls API, API Keys, invocation,
   and gRPC endpoints. For remote clusters, the Gateway must be programmed and
   the CLI config must point at the Gateway load balancer before `up` runs.

   If Gateway API ingress is not already present, create it first. These are the
   shared Gateway setup steps used by one-click, Helmfile, and standalone chart
   install paths:

   ```sh
   kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/experimental-install.yaml

   for namespace in envoy-gateway-system envoy-gateway api-keys ess sis nvcf; do
     kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
   done

   for namespace in envoy-gateway api-keys ess sis nvcf; do
     kubectl label namespace "$namespace" nvcf/platform=true --overwrite
   done

   helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm \
     --version v1.1.3 \
     -n envoy-gateway-system

   # Non-EKS-Auto-Mode example: use AWS LBC with instance targets.
   kubectl apply -f - <<EOF
   apiVersion: gateway.envoyproxy.io/v1alpha1
   kind: EnvoyProxy
   metadata:
     name: eg
     namespace: envoy-gateway-system
   spec:
     provider:
       type: Kubernetes
       kubernetes:
         envoyService:
           annotations:
             service.beta.kubernetes.io/aws-load-balancer-type: "external"
             service.beta.kubernetes.io/aws-load-balancer-nlb-target-type: "instance"
             service.beta.kubernetes.io/aws-load-balancer-scheme: "internet-facing"
   ---
   apiVersion: gateway.networking.k8s.io/v1
   kind: GatewayClass
   metadata:
     name: eg
   spec:
     controllerName: gateway.envoyproxy.io/gatewayclass-controller
     parametersRef:
       group: gateway.envoyproxy.io
       kind: EnvoyProxy
       name: eg
       namespace: envoy-gateway-system
   EOF

   kubectl apply -f - <<EOF
   apiVersion: gateway.networking.k8s.io/v1
   kind: Gateway
   metadata:
     name: nvcf-gateway
     namespace: envoy-gateway
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

   Service annotations must be under
   `EnvoyProxy.spec.provider.kubernetes.envoyService.annotations`. Envoy Gateway
   copies them to the generated Envoy Service. Do not put Service annotations
   on the `Gateway` resource.

   Before applying the example on EKS, determine whether the cluster uses
   [EKS Auto Mode](https://docs.aws.amazon.com/eks/latest/userguide/auto-configure-nlb.html).
   For EKS Auto Mode, replace the AWS Load Balancer Controller annotation map
   with:

   ```yaml
   envoyService:
     loadBalancerClass: eks.amazonaws.com/nlb
     annotations:
       service.beta.kubernetes.io/aws-load-balancer-scheme: "internet-facing"
   ```

   For a cluster that does not use EKS Auto Mode, the applied example uses the
   AWS Load Balancer Controller. If that cluster uses the legacy AWS cloud
   provider Service controller instead, use only
   `service.beta.kubernetes.io/aws-load-balancer-type: "nlb"` in the
   `envoyService.annotations` map. The legacy controller creates an
   internet-facing load balancer by default. Do not combine the legacy `nlb`
   selector with the AWS Load Balancer Controller target type and scheme
   annotations. The legacy controller supports an NLB Service whose ports use
   only TCP or only UDP, but rejects one Service that combines TCP and UDP
   ports. Use the AWS Load Balancer Controller when the generated Envoy Service
   combines protocols. Replace the annotations with the provider's Service
   annotations on non-AWS clusters.

   Capture the Gateway values and write the CLI config:

   ```sh
   export CLUSTER_NAME="nvcf-remote"
   export NCA_ID="nvcf-default"
   export REGION="us-west-1"
   export HTTP_GATEWAY_NAMESPACE="envoy-gateway"
   export HTTP_GATEWAY_NAME="nvcf-gateway"
   export GRPC_GATEWAY_NAMESPACE="envoy-gateway"
   export GRPC_GATEWAY_NAME="nvcf-gateway"

   kubectl -n "$HTTP_GATEWAY_NAMESPACE" wait "gateway/$HTTP_GATEWAY_NAME" \
     --for=condition=Programmed=True --timeout=10m
   kubectl -n "$GRPC_GATEWAY_NAMESPACE" wait "gateway/$GRPC_GATEWAY_NAME" \
     --for=condition=Programmed=True --timeout=10m

   export GATEWAY_ADDR="$(kubectl -n "$HTTP_GATEWAY_NAMESPACE" get "gateway/$HTTP_GATEWAY_NAME" \
     -o jsonpath='{.status.addresses[0].value}')"
   export GRPC_GATEWAY_ADDR="$(kubectl -n "$GRPC_GATEWAY_NAMESPACE" get "gateway/$GRPC_GATEWAY_NAME" \
     -o jsonpath='{.status.addresses[0].value}')"
   export STACK_DOMAIN="$GATEWAY_ADDR"
   ```

   Set `STACK_DOMAIN` to the hostname suffix used by the installed HTTPRoutes.
   For remote test flows without production DNS, use the Gateway load balancer
   address.

   ```sh
   cat > .nvcf-cli.yaml <<EOF
   base_http_url: "http://${GATEWAY_ADDR}"
   invoke_url: "http://${GATEWAY_ADDR}"
   base_grpc_url: "${GRPC_GATEWAY_ADDR}:10081"
   api_keys_service_url: "http://${GATEWAY_ADDR}"

   api_keys_host: "api-keys.${STACK_DOMAIN}"
   api_host: "api.${STACK_DOMAIN}"
   invoke_host: "invocation.${STACK_DOMAIN}"

   api_keys_service_id: "nvidia-cloud-functions-ncp-service-id-aketm"
   api_keys_issuer_service: "nvcf-api"
   api_keys_owner_id: "svc@nvcf-api.local"

   client_id: "${NCA_ID}"
   EOF
   ```

3. Run pre-flight. Choose the flavor matching the topology:

   ```sh
   # Single-cluster
   nvcf-cli self-hosted check --pre --json | jq -c .

   # Split
   nvcf-cli self-hosted check --pre \
     --control-plane-context=admin@cp \
     --compute-plane-context=admin@gpu1 \
     --json | jq -c .

   # Compute-only (operator with kubectl on compute plane only)
   nvcf-cli self-hosted check --pre \
     --compute-plane-context=admin@gpu1 \
     --icms-url=https://icms.nvcf.example.com \
     --json | jq -c .
   ```

   Parse the JSONL stream. If any check fails (`"passed":false`) with `severity: error`, surface `message` + `hintURL` to the user and stop. Don't proceed to install with broken prereqs.

4. Mint an admin token if needed. `nvcf-cli init` is idempotent. Call it if the
   user does not have a session yet. Init talks to API Keys through the public
   API gateway, so it also works in compute-only mode.

5. Run `up`. Use `--json` for the JSONL event stream:

   ```sh
   nvcf-cli self-hosted up \
     --cluster-name="${CLUSTER_NAME}" \
     --nca-id="${NCA_ID}" \
     --region="${REGION}" \
     --json
   ```

   For split-cluster installs, add both context flags:

   ```sh
   export CONTROL_PLANE_CONTEXT="admin@control-plane"
   export COMPUTE_PLANE_CONTEXT="admin@gpu-cluster"

   nvcf-cli self-hosted up \
     --control-plane-context="${CONTROL_PLANE_CONTEXT}" \
     --compute-plane-context="${COMPUTE_PLANE_CONTEXT}" \
     --cluster-name="${CLUSTER_NAME}" \
     --nca-id="${NCA_ID}" \
     --region="${REGION}" \
     --json
   ```

   Parse events:
   - `phase_started` / `phase_completed`: log progress
   - `phase_progress`: show progress for the long apply phases (resource counts)
   - `waiting`: surface to the user if it persists for more than 2 minutes
   - `phase_failed`: stop and surface `errMessage` plus each `remediation` line.
     Decide based on `retryClass`: `immediate` -> may re-run now with user
     approval; `backoff` -> wait `retryAfterSec` and then re-run with user
     approval; `after_remediation`, `none`, or `unknown` -> the operator must
     act first, so do not auto-retry.
   - `final`: done. Print clusterId and NVCFBackend health.

6. Verify with status. Run `nvcf-cli self-hosted status --json | jq` and expect
   `verdict: "healthy"`. Otherwise, use
   [diagnose-failed-install.md](diagnose-failed-install.md).

7. Optionally smoke test a function. Use
   [deploy-and-invoke.md](deploy-and-invoke.md) for the create -> deploy ->
   invoke flow.

## Notes

- Single-cluster `up` against a fresh cluster takes about 10-13 minutes,
  depending on chart pull speed and NATS stream-init latency.
- `up` is idempotent. It is safe to re-run after fixing a prerequisite.
- Never propose `--force` (no command takes one anyway).
- If the user has CI / `$CI` set, always use `--non-interactive --token=$JWT`; never propose `nvcf-cli init` interactively.
