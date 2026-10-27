<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Global Discovery Service Helm Chart

This directory contains the Helm chart for `gds-server`, the Global Discovery
Service (GDS) of the Dynamo Global Router: the multi-region registry of
clusters, Multi-Cluster Services exports and imports, pools and routers. The
server source and its documentation are in
`src/control-plane-services/global-discovery`. The per-cluster MCS controller
has its own chart.

## Overview

GDS runs in every GDS region, in hub clusters of its own VPC. Each region
runs three stateless replicas in front of its replica of one DynamoDB Global
Table. Clients in peered VPCs reach a region through an internal Network Load
Balancer and the Route53 names described in
[Networking](#networking).

The chart installs:

- the `gds-server` Deployment: three replicas spread over zones, a rolling
  update that never removes a replica before its replacement is ready,
  readiness on `/readyz` and liveness on `/healthz` of the admin port, and a
  40 s termination grace period for the drain on shutdown
- a ClusterIP Service with the gRPC (`9090`), REST (`8080`) and admin
  (`9091`) ports
- a ServiceAccount, with the IRSA role annotation when
  `serviceAccount.irsaRoleArn` is set
- a PodDisruptionBudget that keeps at least one replica
- optionally, a LoadBalancer Service for the regional internal NLB, a
  HorizontalPodAutoscaler and a Prometheus Operator ServiceMonitor

## Prerequisites

- Kubernetes 1.27 or later and Helm 3.x.
- The `nvcf-global-discovery` image (Bazel target
  `//src/control-plane-services/global-discovery:image`) in a registry the
  cluster can pull from. The chart sets no image repository.
- With the DynamoDB store (the default):
  - the table, created once with `gds-admin create-table` and replicated to
    every GDS region (module `docs/storage.md`);
  - an IAM role with the server policy of module `docs/iam.md`, bound to the
    ServiceAccount through IRSA or EKS Pod Identity.
- With the internal NLB: the AWS Load Balancer Controller in the cluster.

## Install

A values file for one region:

```yaml
image:
  repository: <your-registry>/<your-org>/nvcf-global-discovery
region: us-east-1
serviceAccount:
  irsaRoleArn: arn:aws:iam::<account>:role/gds-server
externalService:
  enabled: true
  loadBalancerSourceRanges:
    - 10.0.0.0/8
advertiseEndpoints:
  - region: us-east-1
    grpcUrl: http://gds.us-east-1.prod.internal:9090
    httpUrl: http://gds.us-east-1.prod.internal:8080
  - region: us-west-2
    grpcUrl: http://gds.us-west-2.prod.internal:9090
    httpUrl: http://gds.us-west-2.prod.internal:8080
```

```bash
helm install global-discovery global-discovery \
  --namespace global-discovery \
  --create-namespace \
  --values path/to/us-east-1.yaml \
  --wait
```

Repeat in every GDS region with that region's `region` and role. The release
notes print the in-cluster addresses, the table creation command and the
load balancer address.

For development, `store.kind: memory` keeps everything in the pod. Every
replica would hold its own registry, so the chart allows the memory store
with `replicaCount: 1` only. `tests/e2e/global-discovery-k3d` installs the
chart that way on a local k3d cluster.

## Values

Rendering fails when a required value is empty. `values.schema.json` rejects
unknown keys and checks the formats of durations, ports, the region and the
role ARN.

| Key | Default | Description |
| --- | --- | --- |
| `image.repository` | `""` | Server image repository. Required. |
| `image.tag` | `""` | Image tag. Defaults to the chart `appVersion`. A `sha256:` value is used as a digest. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | `[]` | Pull Secrets, as `{name: <Secret>}` entries. |
| `region` | `""` | Region served, a DNS label such as `us-east-1`. Required. `GDS_REGION` (`--region`) and `AWS_REGION`. |
| `replicaCount` | `3` | Replicas, when `autoscaling.enabled` is false. |
| `store.kind` | `dynamodb` | `--store`: `dynamodb` or `memory`. `memory` needs one replica. |
| `store.dynamodb.table` | `gds` | `--dynamodb-table`. |
| `store.dynamodb.endpoint` | `""` | `--dynamodb-endpoint`, for dynamodb-local. |
| `refreshInterval` | `2s` | `--refresh-interval`. |
| `clustersetDiscoveryInterval` | `30s` | `--clusterset-discovery-interval`. |
| `lease.skew` | `2s` | `--lease-skew`. Writers choose their lease TTLs, clamped to 10 s to 5 min. |
| `lease.evalInterval` | `1s` | `--lease-eval-interval`. |
| `retention` | `24h` | `--retention`. |
| `compactionInterval` | `10m` | `--compaction-interval`. |
| `watch.bookmarkInterval` | `5s` | `--bookmark-interval`. Keep it well under the 350 s NLB idle timeout. |
| `watch.history` | `10000` | `--watch-history`. |
| `watch.queueSize` | `1024` | `--watch-queue-size`. |
| `watch.maxWatchers` | `5000` | `--max-watchers`. |
| `advertiseEndpoints` | `[]` | `--advertise-endpoints`: `{region, grpcUrl, httpUrl, primary}` entries that `GetServerInfo` returns as client fallbacks. |
| `tls.existingSecret` | `""` | `kubernetes.io/tls` Secret for TLS on the gRPC and REST listeners, mounted at `/etc/gds/tls`. The admin port stays plain HTTP. |
| `shutdown.drain` | `5s` | `--shutdown-drain`. |
| `shutdown.timeout` | `25s` | `--shutdown-timeout`. |
| `terminationGracePeriodSeconds` | `40` | Above drain plus timeout plus the trace flush. |
| `otel.enabled` | `false` | `--otel-enabled`. |
| `otel.endpoint` | `""` | `OTEL_EXPORTER_OTLP_ENDPOINT`. |
| `otel.resourceAttributes` | `{}` | Extra `OTEL_RESOURCE_ATTRIBUTES` entries; the pod, namespace and region are always set. |
| `logLevel` | `info` | `--log-level`: `debug`, `info`, `warn` or `error`. |
| `extraArgs` | `[]` | Extra server arguments, appended last. |
| `extraEnv` | `[]` | Extra container environment variables. |
| `ports.grpc`, `ports.http`, `ports.admin` | `9090`, `8080`, `9091` | Listener and Service ports. |
| `service.type` | `ClusterIP` | Type of the main Service. |
| `service.annotations` | `{}` | Annotations of the main Service. |
| `externalService.enabled` | `false` | Render the LoadBalancer Service `<fullname>-external`. |
| `externalService.grpcPort`, `externalService.httpPort` | `9090`, `8080` | Load balancer listener ports. `httpPort: 0` exposes gRPC only. |
| `externalService.loadBalancerSourceRanges` | `[]` | Allowed source CIDRs. |
| `externalService.loadBalancerClass` | `""` | `spec.loadBalancerClass`. |
| `externalService.awsNlb.*` | internal, cross-zone, 30 s deregistration delay, health check every 10 s | Generated AWS Load Balancer Controller annotations. `enabled: false` drops them. |
| `externalService.annotations` | `{}` | Merged over the generated annotations; a key set here wins. |
| `serviceAccount.create` | `true` | Create the ServiceAccount. |
| `serviceAccount.name` | `""` | ServiceAccount name. Defaults to the release full name. |
| `serviceAccount.irsaRoleArn` | `""` | IAM role ARN, rendered as `eks.amazonaws.com/role-arn`. |
| `serviceAccount.annotations` | `{}` | Other ServiceAccount annotations. |
| `podDisruptionBudget.enabled` | `true` | Render the PodDisruptionBudget. |
| `podDisruptionBudget.minAvailable` | `1` | Set exactly one of `minAvailable` and `maxUnavailable`. |
| `autoscaling.enabled` | `false` | Render an HPA; the Deployment then leaves `replicas` unset. |
| `autoscaling.minReplicas`, `maxReplicas` | `3`, `9` | HPA bounds. |
| `autoscaling.targetCPUUtilizationPercentage` | `70` | CPU target. |
| `autoscaling.targetMemoryUtilizationPercentage` | `""` | Memory target, when set. |
| `metrics.serviceMonitor.enabled` | `false` | Render a ServiceMonitor for `/metrics` on the admin port. |
| `metrics.serviceMonitor.interval`, `scrapeTimeout`, `labels` | `30s`, `10s`, `{}` | ServiceMonitor settings. |
| `probes.readiness`, `probes.liveness` | see `values.yaml` | Probe timing. |
| `resources` | 100m CPU, 128Mi request; 512Mi limit | Container resources. |
| `topologySpreadConstraints` | zone spread, `ScheduleAnyway` | The chart adds the pod selector to entries without a `labelSelector`. `[]` disables it. |
| `podAnnotations`, `podLabels`, `priorityClassName`, `nodeSelector`, `tolerations`, `affinity` | empty | Pod settings. |
| `podSecurityContext`, `securityContext` | non-root uid 1000, read-only root, drop `ALL`, `RuntimeDefault` seccomp | Security contexts. |

The pod mounts no Kubernetes API token and disables Service links, which
would otherwise inject `<SERVICE>_*` variables that, for a release named
`gds`, collide with the `GDS_` flag fallbacks.

## Networking

The module document `docs/networking.md` describes the whole layout; this is
the deployment checklist.

1. Enable the load balancer in every GDS region:

   ```yaml
   externalService:
     enabled: true
     loadBalancerSourceRanges: [<consumer VPC CIDRs>]
   ```

   The AWS Load Balancer Controller then creates an internal NLB with IP
   targets (pod IPs), cross-zone load balancing and a target group health
   check of `HTTP /readyz` on the admin port (`9091`). A replica that is not
   ready, or that has started its shutdown drain, leaves the target group,
   and the 30 s deregistration delay lets its streams end within the 40 s
   grace period. The NLB idle timeout is 350 s; watch bookmarks every 5 s
   keep idle streams open.

2. Create the Route53 records in a private hosted zone `<env>.internal`
   associated with every consumer VPC and the GDS VPCs:

   - `gds.<region>.<env>.internal`: an alias A record to that region's NLB,
     one per region.
   - `gds.<env>.internal`: latency records, one per region (set identifier
     and region), each an alias to that region's NLB with
     `EvaluateTargetHealth` on, so a region whose targets are all unhealthy
     drops out of the answer.

   Route53 endpoint health checks cannot reach private addresses. Use alias
   target health, or a CloudWatch alarm health check on the NLB
   `HealthyHostCount` metric, never an HTTP health check of the NLB's private
   IPs.

3. Configure clients (controllers, routers, pylons, Pylon Operator) with
   `gds.<env>.internal` first, optionally followed by the per-region names,
   and set `advertiseEndpoints` so that `GetServerInfo` also offers them.
   Clients re-resolve DNS on every reconnect, so Route53 failover applies
   (design section 4.7).

The listeners are h2c and plain HTTP unless `tls.existingSecret` is set. TLS
can also end at the NLB with a TLS listener and an ACM certificate
(`service.beta.kubernetes.io/aws-load-balancer-ssl-cert`,
`aws-load-balancer-ssl-ports` and `aws-load-balancer-alpn-policy:
HTTP2Preferred` in `externalService.annotations`).

## Upgrades and disruptions

- A rolling update adds one new replica, waits for it to be ready, then
  drains an old one (`maxUnavailable: 0`, `maxSurge: 1`).
- On SIGTERM a replica turns not ready, waits `shutdown.drain` so the NLB
  and the Service stop sending it new connections, ends watches with
  `UNAVAILABLE SHUTTING_DOWN` and stops within `shutdown.timeout`. Clients
  reconnect to another replica and receive a new watch snapshot.
- The PodDisruptionBudget keeps one replica during node drains. With
  `store.kind: memory` and one replica it blocks voluntary evictions; set
  `podDisruptionBudget.enabled: false` on clusters that must drain freely.

## Development

From the repository root:

```bash
make -C deploy/helm/global-discovery lint       # helm lint with the CI values
make -C deploy/helm/global-discovery template   # render to bin/manifest.yaml
make -C deploy/helm/global-discovery validate   # kubeconform, when installed
make -C deploy/helm/global-discovery test       # scripts/check-render.sh
tools/ci/check-helm-charts                      # every chart with CI values
```

The CI values are in `tools/ci/helm-validate-values/global-discovery.yaml`.
They use the memory store with one replica; `scripts/check-render.sh`
restores the production defaults for its DynamoDB, NLB, IRSA, PDB and HPA
cases.
