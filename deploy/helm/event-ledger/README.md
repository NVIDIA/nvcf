# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Event Ledger Helm Chart

Helm chart for deploying the event-ledger service in a self-managed NVCF stack.

## Prerequisites

- Helm 3.x
- OpenBao (Vault) with the `22_setup_event-ledger.sh` migration applied (`nvcf-openbao-migrations` >= `0.18.0`). This migration provisions the JWT auth role, the secret paths that the Vault Agent sidecar reads, and the ServiceAccount binding.
- Cassandra with the `event_ledger` keyspace created by the `nvcf-cassandra-migrations` image (`>= 0.16.0`).
- An api-keys-api policy evaluator reachable at the address configured in `eventLedger.config.auth.policy.policy-evaluator-addr`.

## Image settings

`eventLedger.image.registry` and `eventLedger.image.repository` are required and have no defaults. Supply them at install time:

```yaml
eventLedger:
  image:
    registry: <your-registry>
    repository: <your-org>
```

`appVersion` in `Chart.yaml` tracks the image tag used in `values.yaml`. Update both together when bumping the image.

## Namespace and service name

`eventLedger.fullnameOverride` defaults to `event-ledger`. This fixes the in-cluster DNS name to `event-ledger.<namespace>.svc.cluster.local:8080` regardless of the Helm release name, matching the service contract defined in the SADD (section 3.1). Do not change this unless the rest of the stack is updated to match.

The namespace defaults to the Helm release namespace. Set `eventLedger.namespace` to override.

## Override points

- `eventLedger.image.registry` and `eventLedger.image.repository`: required, no defaults
- `eventLedger.replicaCount`: defaults to 1
- `eventLedger.resources`: defaults are conservative; tune for your environment
- `eventLedger.config.database.cassandra.hosts`: defaults to `cassandra.cassandra-system.svc.cluster.local`
- `eventLedger.config.auth.policy.policy-evaluator-addr`: set to the api-keys-api address in your stack

## API Keys policy evaluator

In self-managed deployments, `EVENT_LEDGER_SELF_MANAGED=true` is set by default. This makes the service call the api-keys-api evaluation endpoint without a bearer token (the endpoint has no pre-authorize in self-managed mode). Set `eventLedger.config.auth.policy.policy-evaluator-addr` to the api-keys-api address.

## Metrics

Prometheus metrics are exposed on port 8081 at `/metrics`. The internal service port is always `8081`. To enable scraping, configure your Prometheus instance to scrape `event-ledger.<namespace>.svc.cluster.local:8081/metrics`.

## Install example

```bash
helm install event-ledger deploy/helm/event-ledger \
  --namespace nvcf \
  --values deploy/helm/event-ledger/values.yaml \
  --set eventLedger.image.registry=<your-registry> \
  --set eventLedger.image.repository=<your-org> \
  --wait
```
