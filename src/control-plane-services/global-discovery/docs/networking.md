<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# GDS networking

How clients in other VPCs and regions reach gds-server: one internal Network
Load Balancer (NLB) per GDS cluster, Route53 private names for each region
and for the whole deployment, and the client behaviour that makes failover
work. The design is section 4.7 of
`docs-gds/global-discovery-service-design.md`; the chart is
`deploy/helm/global-discovery`.

## Layout

```
 consumer VPCs (peered, or attached to a Transit Gateway)
   controllers, routers, pylons, Pylon Operator
        |  DNS: gds.<env>.internal (latency), gds.<region>.<env>.internal
        v
 GDS VPC, region A                         GDS VPC, region B
   internal NLB (TCP 9090, 8080)             internal NLB (TCP 9090, 8080)
     -> gds-server pod IPs x3                  -> gds-server pod IPs x3
        health check HTTP /readyz :9091           health check HTTP /readyz :9091
     -> DynamoDB replica A  <== Global Table ==>  DynamoDB replica B
```

Every replica in every region serves the same replicated data, so any NLB,
any replica and any region answers every call. Writes are accepted
everywhere (`docs/consistency-dynamodb.md`).

## Ports

| Port | Listener | Exposed by the NLB |
| :---- | :---- | :---- |
| 9090 | gRPC, h2c or TLS (`--tls-cert-file`) | yes, `externalService.grpcPort` |
| 8080 | REST gateway, HTTP or TLS | optional, `externalService.httpPort` (0 disables) |
| 9091 | admin: `/metrics`, `/healthz`, `/readyz`, `/debug/pprof` | no; target of the health check only |

## The regional NLB

Each GDS cluster exposes its replicas through one internal NLB, created by
the AWS Load Balancer Controller from the chart's LoadBalancer Service:

```yaml
externalService:
  enabled: true
  loadBalancerSourceRanges: [10.0.0.0/8]   # the consumer VPC CIDRs
```

The chart sets these annotations (`externalService.awsNlb`):

| Annotation (`service.beta.kubernetes.io/...`) | Value | Why |
| :---- | :---- | :---- |
| `aws-load-balancer-type` | `external` | Provisioned by the AWS Load Balancer Controller, not the in-tree controller. |
| `aws-load-balancer-nlb-target-type` | `ip` | Targets are pod IPs: no node hop, and the health check reaches the pod's admin port. |
| `aws-load-balancer-scheme` | `internal` | Private addresses only; reached over peering. |
| `aws-load-balancer-attributes` | `load_balancing.cross_zone.enabled=true` | Every NLB node uses every replica, whatever zone the client resolves to. |
| `aws-load-balancer-target-group-attributes` | `deregistration_delay.timeout_seconds=30` | A draining target keeps its connections for 30 s, within the 40 s grace period. |
| `aws-load-balancer-healthcheck-protocol`, `-port`, `-path` | `HTTP`, the admin port, `/readyz` | A replica that is not ready, or has begun its shutdown drain, leaves the target group. |
| `aws-load-balancer-healthcheck-interval`, `-timeout`, `-healthy-threshold`, `-unhealthy-threshold` | 10 s, 5 s, 2, 2 | A failed replica leaves in about 20 s; the Service endpoints drop it at once. |

Behaviour to keep in mind:

- `/readyz` needs a store ping, one refresh of every clusterset and a fresh
  view, so a replica that lost DynamoDB leaves the NLB.
- Shutdown: the replica turns not ready, waits `--shutdown-drain` (5 s),
  ends watches with `UNAVAILABLE SHUTTING_DOWN` and stops within
  `--shutdown-timeout` (25 s). Clients reconnect to another replica and get
  a fresh watch snapshot.
- The NLB closes idle TCP flows after 350 s. Watches carry a bookmark every
  5 s and the server pings idle gRPC connections after 60 s, so neither
  goes idle.
- `loadBalancerSourceRanges` becomes the NLB security group rules. The
  replicas' security group must also admit the NLB on 9090, 8080 and 9091;
  the controller adds those rules when it manages the backend security
  group.
- More than one GDS cluster in a region: each has its own NLB; give the
  regional record one weighted alias per NLB, all with
  `EvaluateTargetHealth`.

## Route53 records

Use one private hosted zone, for example `prod.internal`, associated with the
GDS VPCs and every consumer VPC. VPC peering does not share DNS: associate
the zone with each consumer VPC (across accounts, with
`aws route53 create-vpc-association-authorization` in the zone's account and
`aws route53 associate-vpc-with-hosted-zone` in the VPC's account).

| Name | Type | Routing | Target |
| :---- | :---- | :---- | :---- |
| `gds.<region>.<env>.internal` | A, alias | simple (weighted with several GDS clusters) | the region's NLB, `EvaluateTargetHealth: true` |
| `gds.<env>.internal` | A, alias | latency, one record per region (`SetIdentifier`, `Region`) | the region's NLB, `EvaluateTargetHealth: true` |

The latency record of a region drops out of answers while every target of
its NLB is unhealthy, so queries then resolve to the next closest region.
The resolver of a VPC answers latency records for the VPC's own region.

Health checks:

- Route53 endpoint health checks run from the public internet and cannot
  reach an internal NLB. Use alias target health (`EvaluateTargetHealth`),
  which follows the NLB's `/readyz` target health.
- For an explicit signal, attach a CloudWatch alarm health check to each
  latency record: an alarm on the NLB target group metric
  `HealthyHostCount` below 1. A calculated health check can combine it with
  other alarms. Never attach an HTTP health check of the NLB's private IPs.

TTLs:

- Alias records use the target's TTL, 60 s for load balancers.
- Plain CNAME records to the NLB DNS name, where aliases are not possible,
  take a TTL of 30 s.
- Flip time after a region fails: the NLB marks targets unhealthy after two
  failed checks (about 20 s), Route53 picks the change up within tens of
  seconds, and clients see it when their cached answer expires (up to the
  TTL). Expect 30 to 90 s; the client fallbacks below shorten it.

Example change batch for the latency record of one region (the alias hosted
zone id is the NLB's `CanonicalHostedZoneId` from
`aws elbv2 describe-load-balancers`):

```json
{
  "Changes": [
    {
      "Action": "UPSERT",
      "ResourceRecordSet": {
        "Name": "gds.prod.internal",
        "Type": "A",
        "SetIdentifier": "us-east-1",
        "Region": "us-east-1",
        "AliasTarget": {
          "HostedZoneId": "<NLB canonical hosted zone id>",
          "DNSName": "<NLB DNS name>",
          "EvaluateTargetHealth": true
        }
      }
    }
  ]
}
```

```bash
aws route53 change-resource-record-sets --hosted-zone-id <private zone id> --change-batch file://gds-us-east-1.json
```

## Clients

Every consumer (the MCS controller, Stargate, Pylon, Pylon Operator, the
LLM API gateway) takes an ordered URL list. Configure:

1. `gds.<env>.internal`, the global latency name, first.
2. Optionally the per-region names of the closest regions,
   `gds.<region>.<env>.internal`.

and set the chart's `advertiseEndpoints` to every region's per-region name,
so `GetServerInfo` hands them to clients as further fallbacks.

The client libraries (`pkg/client`, the Rust `gds-client` crate):

- build a new connection and re-resolve DNS on every reconnect, so a
  Route53 change applies at the next reconnect;
- after the primary URL fails, try the next configured URL, then the
  advertised fallbacks, and probe the primary periodically to return to it;
- treat every replica and region as equivalent, and accept a fresh watch
  snapshot after a reconnect (the revision belongs to one replica).

Do not resolve the names once and pin IP addresses, and do not put a
resolver cache with a TTL floor in front of the clients.

URL scheme and port: one client uses one scheme for all its URLs.

| Server setup | Client URLs |
| :---- | :---- |
| No TLS (default, v1 has no authentication) | `http://gds.prod.internal:9090`, with `AllowInsecureHTTP` in `pkg/client` |
| `tls.existingSecret` on the server | `https://gds.prod.internal:9090`; the certificate covers the global and per-region names |
| TLS at the NLB (`externalService.grpcPort: 443`, an ACM certificate through `aws-load-balancer-ssl-cert` and `aws-load-balancer-ssl-ports`, and `aws-load-balancer-alpn-policy: HTTP2Preferred`) | `https://gds.prod.internal` |

Inside a GDS cluster, clients may use the ClusterIP Service directly:
`http://<release>-global-discovery.<namespace>.svc:9090`, or
`http://global-discovery.<namespace>.svc:9090` for a release named
`global-discovery`.

## Local clusters

`tests/e2e/global-discovery-k3d` installs the chart on k3d with the memory
store and reaches it through `kubectl port-forward`. No load balancer or DNS
is involved there.
