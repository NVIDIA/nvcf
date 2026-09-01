# Global Stargate dev deployment plan

## Decision

Use Helm for every Kubernetes resource.

- Reuse the existing `llm-request-router` and `llm-api-gateway` charts.
- Deploy the upstream OpenTelemetry Collector chart directly in `deployment` mode.
- Keep the dev auth and MockDC charts local to this stack.
- Use Helmfile only to coordinate Helm releases, values, kube contexts, and install order.
- Do not add Kustomize, generated raw manifests, or an umbrella chart spanning clusters.

The auth fixture is test infrastructure. Keep its chart under this dev stack, build it as a separate dev-only image, and exclude it from production charts, images, release promotion, and deployment environments.

Separate Helm releases are preferable to one umbrella chart because a Helm release belongs to one Kubernetes cluster, while each region contains three clusters with different lifecycles.

## Goals

- Deploy the five confirmed regions.
- Deploy one Stargate cluster and two MockDC clusters in each region.
- Keep all regional configuration and image versions in Git.
- Make rendering, diffing, deployment, verification, and rollback repeatable.
- Export application metrics from every cluster to one global Grafana dashboard.
- Avoid committing credentials or relying on the current kubectl context.

## Non-goals for the first deployment

- Global request routing between regional gateways.
- Running Grafana or a metrics database inside a Stargate or MockDC cluster.
- Logs, traces, Kubernetes events, node metrics, or kubelet metrics.
- Production NVCF control-plane dependencies such as Cassandra and OpenBao.
- Production identity semantics, JWT or OAuth validation, credential issuance, runtime credential rotation, or auth configuration reload.
- High availability for the dev-only auth fixture.

## Topology

```text
                              Grafana Cloud
                                   ^
                                   | OTLP metrics
        +--------------------------+--------------------------+
        | repeat once for each confirmed region listed below |
        +--------------------------+--------------------------+
                                   |
   +---------------- Stargate EKS cluster --------------------+
   |                                                           |
   |  client -> gateway x2 -> Stargate StatefulSet x3          |
   |                  |             ^                          |
   |                  v             |                          |
   |            dev auth fixture x1 |                          |
   |                                |                          |
   |  regional NLB -> stargate-k8s-router x3 -----------------+
   |       TLS 50071 + UDP 50072
   |
   |  OTel Collector x1
   +-----------------------------------------------------------+
               ^                              ^
               |                              |
   +-----------+ MockDC 1 EKS -----+  +-------+ MockDC 2 EKS -----+
   | backend-0 pod                 |  | backend-0 pod             |
   |   mock-dynamo + Pylon         |  |   mock-dynamo + Pylon     |
   | backend-1 pod                 |  | backend-1 pod             |
   |   mock-dynamo + Pylon         |  |   mock-dynamo + Pylon     |
   | OTel Collector x1             |  | OTel Collector x1         |
   +-------------------------------+  +---------------------------+
```

## Confirmed cluster inventory

| Region | Stargate cluster | Stargate nodes | MockDC clusters | Nodes per MockDC |
|---|---|---|---|---|
| `us-west-2` | `stargate-usw2` | 3 x `c7i.xlarge` | `mockdc-usw2-a`, `mockdc-usw2-b` | 2 x `t3.medium` |
| `us-east-1` | `stargate-ue1` | 3 x `c7i.xlarge` | `mockdc-ue1-a`, `mockdc-ue1-b` | 2 x `t3.medium` |
| `eu-west-1` | `stargate-ew1` | 3 x `c7i.xlarge` | `mockdc-ew1-a`, `mockdc-ew1-b` | 2 x `t3.medium` |
| `ap-northeast-1` | `stargate-an1` | 3 x `c7i.xlarge` | `mockdc-an1-a`, `mockdc-an1-b` | 2 x `t3.medium` |
| `ap-southeast-2` | `stargate-as2` | 3 x `c7i.xlarge` | `mockdc-as2-a`, `mockdc-as2-b` | 2 x `t3.medium` |

All 15 clusters were handed off as active on Kubernetes 1.34, with CS-Admin authentication configured through both an EKS access entry and the cluster authentication ConfigMap. Deployment preflight checks must verify this live state instead of relying on the handoff snapshot.

Use each MockDC Kubernetes cluster name as its logical MockDC cluster ID. This keeps one identifier per cluster and already guarantees that the two IDs in each region differ.

## Deployment invariants

- Every region has exactly one Stargate cluster and two MockDC clusters.
- The Stargate cluster has three Stargate replicas, three router replicas, two gateway replicas, and one auth replica.
- Each MockDC has two one-replica Deployments.
- Each MockDC Pod contains one MockDynamo container and one Pylon sidecar.
- Pylons in one MockDC have unique inference-server IDs and the same cluster ID.
- Both MockDCs serve the same regional test model so Stargate sees two logical clusters and four backends.
- The two MockDC cluster IDs in a region are their distinct Kubernetes cluster names.
- All four regional Pylon inference-server IDs are unique and derived as `{cluster_id}-backend-{0,1}`.
- Every image is pinned to an immutable digest.
- The auth fixture is deployed only by this dev stack and is never included in a production Stargate image or chart.
- Auth credentials are cryptographically random, generated once for a regional deployment, and unchanged for the lifetime of that deployment.
- The auth fixture reads an immutable Secret once at startup and has no mutation or reload API.
- No credential value is committed in chart defaults, environment files, or Helmfile state, or passed through a command-line argument.
- Every Helm release names its kube context explicitly.
- A region deploys and verifies its Stargate plane before either MockDC is deployed.
- One OTel Collector performs Prometheus scraping in each cluster to avoid duplicate samples.

## Repository layout

```text
deploy/stacks/stargate-dev/
  PLAN.md
  helmfile.yaml.gotmpl
  charts/
    stargate-dev-auth/                # new, dev-only
    stargate-dev-mockdc/              # new, stack-local
  environments/
    example.yaml
    us-west-2.yaml
    us-east-1.yaml
    eu-west-1.yaml
    ap-northeast-1.yaml
    ap-southeast-2.yaml
  values/
    versions.yaml                       # chart versions and image digests
  dashboard/
    global-dashboard.json
  scripts/
    deploy.py                           # credential init and guarded apply only
    provision_dashboard.py
    verify.py
  tests/
    test_invariants.py
```

Each region has one nested environment file. It owns the region's AWS account, three cluster names, ARNs and kube contexts, DNS and certificate inputs, routing values, and existing Secret names. The two MockDC cluster names are also their logical cluster IDs. `values/versions.yaml` is the single source for chart versions and immutable image digests. The Helmfile maps these two files directly into releases without intermediate common or per-release values files.

The example environment contains placeholders and documents the same shape. Real environment files never contain secret values.

## Helmfile design

Select one region with `--environment`. Each release supplies its own `kubeContext` and labels.

Release groups:

1. `phase=stargate`
   - Stargate-cluster OTel Collector Deployment
   - Dev auth fixture
   - LLM request router, which deploys Stargate and `stargate-k8s-router`
   - LLM API Gateway
2. `phase=mockdc`
   - MockDC 1 OTel Collector Deployment
   - MockDC 1 MockDynamo/Pylon workloads
   - MockDC 2 OTel Collector Deployment
   - MockDC 2 MockDynamo/Pylon workloads

Use Helmfile `needs` for dependencies within a cluster. Do not rely on a cross-cluster `needs` edge for NLB or DNS readiness. The deployment command verifies the Stargate endpoint before applying the MockDC phase.

## Existing chart changes

### LLM request router

Use the existing `deploy/helm/llm-request-router/llm-request-router` chart.

Configure:

- `replicaCount: 3`
- Backend router enabled with `replicaCount: 3`
- Stargate PDB with `minAvailable: 2`
- Router PDB with `minAvailable: 2`
- Metrics enabled
- Stargate Prometheus scrape annotations for `/metrics` on port 9090
- Backend-router Prometheus scrape annotations for `/metrics` on port 8080
- Reverse backend connectivity
- Regional advertised hostname template
- Regional load-balancer configuration
- TLS from an existing Secret
- `vault.noVaultAnnotations: true`
- Worker-auth caller credentials from an existing Secret
- Soft topology spread across nodes and availability zones

Add chart values where they are currently missing:

- Image digest support for the Stargate and backend-router images
- HTTP port exposure on the ready-only ClusterIP Service
- Backend-router Service type
- Backend-router Service annotations
- Backend-router `loadBalancerClass`
- Optional load balancer source ranges
- An existing worker-auth caller Secret mount with configurable Secret name, key, mount path, and JSON key path

The worker-auth caller Secret key contains the JSON file expected by Stargate, including `nvcfApiToken`. The chart mounts that file and passes its path to Stargate. It does not use Vault annotations or require an OpenBao injector.

The backend-router Service becomes an internet-facing AWS NLB exposing:

- TLS 50071 for Pylon registration, terminated by the NLB and forwarded as gRPC over plaintext TCP to the router
- UDP 50072 for reverse tunnels
- TCP health checks on the router health port

Configure `backendRouter.pylonGrpcDialAddress` with the NLB's `https://` address. Regional wildcard DNS resolves `{pod_name}.stargate.<region>.<dev-zone>` to this NLB and supplies the per-Stargate gRPC authority and QUIC SNI identity.

### LLM API Gateway

Use the existing `deploy/helm/llm-api-gateway/llm-api-gateway` chart.

Configure:

- `replicaCount: 2`
- PDB with `minAvailable: 1`
- Request-router URL pointing to the ready-only Stargate ClusterIP Service
- NVCF gRPC address pointing to the dev auth Service
- `nvcfGrpcInsecure: true` for the private, plaintext dev auth ClusterIP Service
- Metrics enabled
- Prometheus scrape annotations for `/metrics` on port 9464
- Olric disabled
- Rate limiting disabled
- `vault.noVaultAnnotations: true`
- Regional HTTPS ingress through an internal AWS NLB

Add chart values where they are currently missing:

- Image digest support
- An optional external LoadBalancer Service that exposes only HTTPS traffic to the gateway HTTP port
- External Service annotations, `loadBalancerClass`, and source ranges
- An existing Kubernetes Secret mount for the NVCF service token

Keep the existing gateway Service as ClusterIP for readiness and metrics. The separate external Service selects the same Pods but exposes only port 443, so port 9464 is never added to the NLB. Terminate TLS at the NLB with an ACM certificate and forward plaintext HTTP to port 8080 inside the cluster. Do not add an ALB or Kubernetes Ingress path.

## New dev workloads

### `stargate-dev-auth`

Deploy a stateless fixture for the two NVCF LLM gateway auth RPCs. This is not a reduced production NVCF API. Its only purpose is to authenticate fixed credentials for this dev deployment.

The service implements:

- `AuthLlmWorker` for Pylon registration
- `AuthLlmInvocation` for LLM API Gateway requests
- One service-token check for calls from Stargate and the LLM API Gateway
- One client bearer-token check for invocation requests
- Static worker-token-to-routing-key lookup
- Invocation authorization only for routing keys present in the worker mappings
- Health, readiness, and Prometheus metrics endpoints

Use one replica. Do not add a PDB; high availability for this dev fixture is not a current requirement.

Extend the existing worker-only fixture into a clearly named `stargate-dev-auth` binary instead of deploying the full NVCF API. Build a dedicated `stargate-dev-auth-runtime` image target. Do not copy the binary into the production Stargate runtime image or publish it through the production release path.

Keep the chart under `deploy/stacks/stargate-dev/charts`. It creates only the dev auth Deployment, ClusterIP Service, NetworkPolicy, ServiceAccount, and immutable Secret. Only Stargate and LLM API Gateway Pods may reach its gRPC port. Do not create a reusable production chart or expose the Service through an NLB or ingress.

The fixture reads one JSON document from the mounted Secret at startup:

```json
{
  "serviceToken": "generated service token",
  "clientToken": "generated client token",
  "workers": [
    {
      "token": "generated worker token",
      "routingKey": "regional routing key"
    }
  ]
}
```

Use one worker token per routing key. All Pylons intended to join the same backend pool use that token and therefore receive the same routing key. Derive the set of valid invocation routing keys from the worker mappings instead of configuring it twice.

Both RPCs require the configured service token in gRPC bearer metadata. `AuthLlmWorker` rejects an unknown worker token and otherwise returns its mapped routing key. `AuthLlmInvocation` rejects an unknown client token or routing key and otherwise returns:

- The requested routing key
- A fixed dev client subject
- A fixed nonempty `ncaId`, which the LLM API Gateway requires as its rate-limit identity
- No project, model specifications, URI restrictions, token rate limits, routing method, or priority

The absence of model specifications intentionally permits any mock model and disables per-model token limits. Do not add configuration for fields without a concrete dev scenario.

Generate the service, client, and worker tokens from 32 random bytes during the first regional deployment. The deployment wrapper writes the generated credential bundle only to an operator-selected, permission-restricted file and sets `STARGATE_DEV_CREDENTIALS_FILE` while invoking Helmfile. Helmfile reads that file directly and passes each release only the fields it needs. It never prints credential values or places them in an argument. Helm creates immutable Secrets in the Stargate and MockDC clusters containing only the credentials needed by each workload. The credentials also exist in Helm release metadata, so access to Helm release Secrets must be restricted with Kubernetes RBAC.

The dev auth chart owns the Stargate-cluster Secret. It stores the fixture configuration and the service-token JSON consumed by Stargate and the LLM API Gateway. Each MockDC chart owns an immutable Secret containing only the worker token needed by its Pylon Pods. The existing charts consume these chart-owned Secrets through their existing-Secret interfaces; they do not own or duplicate the credentials.

The fixture reads its Secret once and does not watch for changes. Repeated applies must reuse the same credential bundle. The deployment wrapper refuses to replace an existing credential Secret with different data. Rotation requires an explicit new Secret name and workload rollout and is outside the initial deployment workflow.

### `stargate-dev-mockdc`

Keep this chart under `deploy/stacks/stargate-dev/charts`. It serves this stack only; move it to shared chart ownership only after another concrete deployment needs the same workload contract.

Deploy exactly two backend Deployments. Each Pod contains:

- MockDynamo listening on localhost port 8090
- Pylon using `http://127.0.0.1:8090` as its upstream

The chart configures:

- MockDC cluster ID taken directly from the Kubernetes cluster name
- Pylon inference-server IDs derived as `{cluster_id}-backend-0` and `{cluster_id}-backend-1`
- Shared test model
- Regional Stargate router address
- Reverse connectivity
- Chart-owned immutable worker-token Secret populated from the regional deployment credential bundle
- MockDynamo timing and capacity settings
- Pylon metrics on port 9089
- Readiness, liveness, resource, and security settings
- ClusterIP Services for MockDynamo test-control access
- Prometheus scrape annotations

Omit both Pylon calibration and initial input TPS by default. Add explicit values for either mode only when a test needs them.

The chart must reject:

- An empty cluster ID or model
- Calibration and initial TPS enabled together
- A missing or empty worker token in the regional credential bundle

Do not expose a separate cluster-ID, backend-count, or inference-server-ID value. The chart always uses the Kubernetes cluster name as its cluster ID, renders two backends, and derives their IDs. The Helmfile fails rendering or apply when the two regional cluster names are equal or the four derived inference-server IDs are not distinct.

### OpenTelemetry Collector

Install the upstream `open-telemetry/opentelemetry-collector` Helm chart directly in each cluster. Pin the chart version in `values/versions.yaml`, set `mode: deployment`, and run one replica. The chart already supports an image digest, existing-Secret environment variables, and additional ClusterRole rules. Do not install the OpenTelemetry Operator, its CRDs, or a Target Allocator for this fixed collector.

Create the chart's ServiceAccount, ClusterRole, and ClusterRoleBinding. Grant only `get`, `list`, and `watch` on Pods for direct Prometheus Pod discovery.

Collector configuration:

- Prometheus receiver with Kubernetes pod discovery
- Scraping controlled by `prometheus.io/scrape`, `prometheus.io/path`, and `prometheus.io/port` Pod annotations
- `memory_limiter` and `batch` processors
- Static region, cluster, role, provider, and environment resource attributes
- OTLP/HTTP metrics exporter to Grafana Cloud
- Export credentials from an existing Secret
- Collector self-metrics enabled

Scrape targets:

- Stargate on port 9090
- `stargate-k8s-router` on port 8080
- LLM API Gateway on port 9464
- Pylon on port 9089
- Dev auth on its metrics port

Keep this collector configuration metrics-only. Add traces, logs, events, kubelet metrics, or target allocation only when a concrete requirement appears.

The invariant test must assert the cross-component discovery contract: Stargate, backend-router, gateway, auth, and Pylon Pods have the annotations and ports selected by the collector configuration. Do not install or render Prometheus Operator resources.

## Image delivery

Publish the following multi-architecture images through the existing CI and registry path:

- Stargate image containing Stargate and `stargate-k8s-router`
- Pylon
- MockDynamo
- LLM API Gateway
- Dev-only auth fixture image built from the dedicated `stargate-dev-auth-runtime` target

Requirements:

- Pin every deployment to a digest.
- Pin the collector image to a digest and its upstream Helm chart to an exact version.
- Build once per commit and use the same digest in every region.
- Ensure every EKS cluster can pull the images.
- Use ECR cross-region replication if the registry setup requires regional repositories.
- Never use `latest` or a mutable environment tag in a values file.
- Publish the auth fixture only to the development image repository and never promote it with production Stargate artifacts.

## Networking and TLS

The cluster handoff must provide:

- Stable public NAT Gateway egress IPs for every MockDC subnet
- AWS Load Balancer Controller
- Route53 public hosted-zone ownership
- Security-group rules for TCP 50071 and UDP 50072
- An ACM certificate ARN for the gateway HTTPS listener and Pylon registration listener
- A regional Stargate TLS Secret and Pylon trust bundle, or cert-manager support that creates the Secret
- Outbound HTTPS access to the Grafana OTLP endpoint

Keep the gateway NLB internal. Make the backend-router NLB internet-facing so MockDC clusters do not require cross-VPC routing. It exposes TLS 50071 and UDP 50072 only, and accepts traffic only from the configured public MockDC NAT Gateway addresses.

Use the existing-secret TLS mode. Pylon trusts the regional root CA. Do not make insecure QUIC the checked-in default.

## Metrics and Grafana

Use Grafana Cloud for the first deployment so the metrics backend is independent of every workload region. If external SaaS is not allowed, use Amazon Managed Prometheus with Amazon Managed Grafana in a separate observability account or region.

The checked-in dashboard contains variables for:

- Region
- Kubernetes cluster
- Cluster role
- Service
- Model
- MockDC cluster ID

Initial panels:

- Request rate, errors, and latency by region
- Active Stargates and registered inference servers
- Routing selections by MockDC cluster
- Pylon requests, retries, queue time, and TTFT
- Router connections and rejection outcomes
- Gateway request and auth latency
- Collector scrape health and exporter failures

Provision the dashboard after the first region is verified by running `scripts/provision_dashboard.py` directly from the deployment workstation or CI. The script reads the checked-in JSON and calls the Grafana HTTP API. It is not a Helm release because the dashboard is external state and has no Kubernetes lifecycle.

The script:

- Uses a stable dashboard UID and configured Grafana folder UID
- Reads the Grafana URL, folder UID, and service-account token from environment variables or a protected token file
- Never accepts the token as an argument or prints it
- Uses the Grafana HTTP API to create or overwrite the dashboard idempotently
- Reads the dashboard back by UID and verifies its title, folder, and version

The stable UID and overwrite operation make rerunning the script the recovery path. The deployment workflow never deletes the external dashboard.

## Deployment command

Use Helmfile directly for lint, template, diff, and status. Keep `scripts/deploy.py` limited to the two operations that need additional safety logic:

- `init` generates one immutable regional credential bundle and refuses to overwrite a file.
- `apply` verifies the AWS account, cluster ARNs, kube contexts, and credential Secret immutability before invoking Helmfile for one phase. Before `phase=mockdc`, it also verifies the Stargate endpoint.

Helmfile reads the credential bundle path from `STARGATE_DEV_CREDENTIALS_FILE`. Direct diff commands must use `--suppress-secrets`. Direct template commands write to a permission-restricted output directory rather than stdout; static CI rendering uses disposable credentials, not a deployed regional bundle.

Example:

```bash
python3 deploy/stacks/stargate-dev/scripts/deploy.py init --region us-east-1 --credentials /secure/path/us-east-1.json
export STARGATE_DEV_CREDENTIALS_FILE=/secure/path/us-east-1.json
helmfile --environment us-east-1 diff --suppress-secrets --selector phase=stargate
python3 deploy/stacks/stargate-dev/scripts/deploy.py apply --region us-east-1 --phase stargate --credentials /secure/path/us-east-1.json
python3 deploy/stacks/stargate-dev/scripts/verify.py --region us-east-1 --phase stargate
helmfile --environment us-east-1 status --selector phase=stargate
helmfile --environment us-east-1 diff --suppress-secrets --selector phase=mockdc
python3 deploy/stacks/stargate-dev/scripts/deploy.py apply --region us-east-1 --phase mockdc --credentials /secure/path/us-east-1.json
python3 deploy/stacks/stargate-dev/scripts/verify.py --region us-east-1 --phase regional
GRAFANA_URL=https://example.grafana.net \
GRAFANA_FOLDER_UID=stargate-dev \
GRAFANA_TOKEN_FILE=/secure/path/grafana-token \
python3 deploy/stacks/stargate-dev/scripts/provision_dashboard.py
```

Safety behavior:

- Never use the current kube context implicitly.
- Verify the configured cluster name, AWS account, AWS region, EKS cluster ARN, Kubernetes version, node shape, and CS-Admin access before an apply.
- Generate credentials only when explicitly initializing a new regional deployment, store the bundle with mode `0600`, and reuse it on every later apply.
- Refuse to overwrite a credential bundle or immutable in-cluster Secret during an ordinary apply.
- Run static validation before applying.
- Use Helm atomic upgrades with readiness waiting.
- Deploy one region at a time and stop on the first failure.
- Do not automate uninstall, namespace deletion, or cluster deletion.
- Roll back with the previous chart and values revision or `helm rollback`.
- Suppress Secrets in diffs. Keep template output containing Secret manifests in a mode `0700` temporary directory and delete it after validation.
- Redact Grafana API authorization headers and never log Secret contents.

## Validation

### Static validation

Use the standard tools for chart mechanics:

- `helmfile lint`
- `helmfile template` into a protected temporary directory with disposable credentials
- Values JSON schema validation
- `kubeconform` against Kubernetes 1.34

Keep `tests/test_invariants.py` limited to contracts that those tools cannot prove:

- Every release has an explicit kube context and expected cluster ARN.
- Each region renders one Stargate plane and two MockDCs with the required replica counts.
- The two MockDC cluster names used as cluster IDs and four derived inference-server IDs are distinct.
- Credential values appear only in Secret manifests, and chart-owned Secret names match every consumer reference.
- Vault injection is disabled, the gateway uses the ready-only Stargate Service, and its dev auth transport is private and explicitly insecure.
- Only the gateway and backend-router Services are LoadBalancers. Each exposes only its required ports; the gateway is internal and the backend router is internet-facing with restricted source ranges.
- Every expected metrics Pod annotation and port matches the collector's discovery configuration.
- Every deployed image is selected from the digest-pinned entries in `values/versions.yaml`.

Test the auth fixture itself at its trust boundary: valid credentials succeed, invalid or missing service, client, and worker tokens fail, and routing keys are limited to the worker mappings. Do not add render assertions for ordinary Helm output such as probes, selectors, PDB fields, and container literals.

### One-region acceptance

- Three Stargate, three router, two gateway, and one auth replica are ready.
- Both MockDC clusters have two healthy MockDynamo/Pylon Pods.
- Four Pylons register through the regional NLB.
- Stargate reports one model, two logical MockDC clusters, and four backends.
- Valid client and worker credentials succeed.
- Invalid client and worker credentials are rejected.
- Missing or invalid service tokens from Stargate and the API Gateway are rejected.
- Removing one Stargate, router, gateway, or MockDynamo Pod does not stop regional traffic.
- Grafana receives metrics from all three clusters with the correct region, cluster, and role attributes.
- The global dashboard is present at its stable UID and its queries return data from the region.

After one region soaks successfully, deploy the other four regions sequentially.

## Implementation sequence

1. Extend the request-router and gateway charts with digest, ready-only Service, NLB, scrape-annotation, Vault-disable, and existing-Secret support.
2. Extend the existing worker-auth fixture with invocation auth, build its dedicated dev-only image target, and add the stack-local `stargate-dev-auth` chart.
3. Add the stack-local `stargate-dev-mockdc` chart.
4. Add the two-phase Helmfile, one-file region environments, version pins, direct collector releases, and the init/apply wrapper.
5. Add focused invariant tests, regional verification, the dashboard JSON, and the direct dashboard provisioning script.
6. Deploy and soak one region, provision the dashboard, then deploy the other four regions sequentially and verify their data in the same dashboard.

## Required inputs before deployment

- Exact kube-context names
- Expected AWS account and EKS cluster ARNs
- The first region to use as the deployment canary
- Private networking topology
- Router and gateway DNS zone
- Stargate TLS Secret and trust-bundle ownership
- Gateway and registration NLB ACM certificate ARN
- Image registry and pull mechanism
- Grafana OTLP destination and existing collector Secret name
- Grafana dashboard URL and folder UID, plus a protected service-account token file for dashboard provisioning
- Whether AWS Load Balancer Controller and ExternalDNS are preinstalled
- A secure local or managed location for each region's generated, immutable dev credential bundle
