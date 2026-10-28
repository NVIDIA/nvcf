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

# GDS storage: the DynamoDB store

gds-server keeps every record in one DynamoDB table, replicated to each GDS
region as a Global Table (version 2019.11.21). Every replica of gds-server in
every region reads and writes the replica of its own region. The store is
`internal/store/dynamodb`; it implements the same `store.Store` contract as
the in-memory store and passes the same conformance suite. The design is
`docs-gds/global-discovery-service-design.md`, sections 4.8 and 7.

Related documents:

- `docs/iam.md`: IAM policies for the server and admin roles, IRSA.
- `docs/consistency-dynamodb.md`: what Global Table replication means for
  leases, generations and pool names.

Server flags: `--store=dynamodb`, `--dynamodb-table` (default `gds`) and
`--dynamodb-endpoint` (dynamodb-local only). The region comes from
`--region` or `AWS_REGION`; credentials come from the default AWS chain
(IRSA in EKS).

## Item catalogue

| Item | pk | sk | Body (`nvidia.gds.v1`) | Top-level attributes | TTL |
| :---- | :---- | :---- | :---- | :---- | :---- |
| Clusterset index | `CSX` | `CS#<clusterset>` | none | none | no |
| Cluster | `CS#<clusterset>` | `CL#<cluster>` | `Cluster` | `gen`, `lease_epoch`, `lease_renewed_at`, `updated_at`, `expires_at` | yes |
| ServiceExport | `CS#<clusterset>` | `EX#<cluster>#<namespace>#<name>` | `ServiceExportSpec` | `gen`, `updated_at` | no |
| Pool | `CS#<clusterset>` | `PL#<cluster>#<pool_id>` | `Pool` | `gen`, `lease_epoch` (leased pools), `lease_renewed_at`, `owner_id`, `updated_at`, `expires_at` | yes |
| Pool name reservation | `CS#<clusterset>` | `PN#<cluster>#<name>` | none | `pool_id` | no |
| Pool agent registration | `CS#<clusterset>` | `PA#<cluster>#<pool_id>#<role>#<agent_id>` | `PoolAgentRegistration` | `gen`, `lease_epoch`, `lease_renewed_at`, `updated_at`, `expires_at`, `pool_name` | yes |
| Endpoint partition marker | `CS#<clusterset>` | `EPX#<cluster>` | none | none | no |
| EndpointSet | `EP#<clusterset>#<cluster>` | `<namespace>#<name>` | `ClusterEndpoints` (`endpoints` only) | `gen`, `updated_at` | no |

Notes:

- The clusterset partition `CS#<clusterset>` holds the small,
  heartbeat-heavy records. Endpoint sets, which are large and change often,
  live in one partition per cluster. Deleting or loading a cluster needs
  prefix queries only; no global secondary index exists.
- The clusterset index is written once per gds-server process and
  clusterset, before the first record. `ListClusterSets` queries `pk = CSX`.
  The store never deletes it.
- A pool agent registration item is one process instance's registration
  of an agent role of a pool (`RegisterPoolAgent`), for example one per
  Pylon pod. `<role>` is the store spelling of the role: `OPENAI`,
  `KV_RELAY` or `PYLON`; `<agent_id>` is the process's id, escaped like
  every key component. Each registration has its own lease, so the
  instances of one role never fence each other. The body holds
  `attributes`, `lease.ttl` and `registered_at`; `pool_name`, the join key
  with labelled exports and pool records, is a top-level attribute. The
  registrations of a pool share the prefix `PA#<cluster>#<pool_id>#`,
  those of a cluster the prefix `PA#<cluster>#`. The server merges the
  registrations of one role into the pool's `agents[]` entry.
- The endpoint partition marker records that `EP#<clusterset>#<cluster>`
  exists, so that `LoadClusterSet` finds an endpoint set whose cluster has no
  registration, export or pool. A replica rewrites its marker at most every
  5 minutes; `DeleteCluster` removes it.
- Key components are escaped so they never contain `#`: `%` becomes `%25`
  and `#` becomes `%23`. API identifiers are DNS labels and are stored as
  they are. Every prefix query ends with `#`, so `EX#c1#` never matches
  cluster `c10`.
- `body` is a serialized protobuf (deterministic marshalling). A value kept
  as a top-level attribute is never also set in the body: the key fields,
  `generation`, `updated_at`, `owner_id`, `lease.epoch` and
  `lease.renewed_at` are empty in the body, and `lease.ttl` and
  `registered_at` are only in the body. Store records use Kubernetes
  spellings for enumerations (`ClusterSetIP`, `TCP`); the codec maps them to
  the proto enums and rejects values it cannot represent.

### Attributes

| Attribute | Type | Meaning |
| :---- | :---- | :---- |
| `pk`, `sk` | S | Keys, see above |
| `body` | B | Serialized protobuf record body |
| `gen` | N | Record generation. Writer supplied for exports, endpoint sets, pools and pool agents; incremented by the store on every cluster registration. Always present, also when 0 |
| `lease_epoch` | N | Lease incarnation, minted by the store as `max(stored + 1, now in unix ms)`. Absent for a pool or pool agent without a lease |
| `lease_renewed_at` | N | Last registration or heartbeat, Unix nanoseconds, server time |
| `owner_id` | S | Pool writer identity |
| `updated_at` | N | Record `UpdatedAt`, Unix nanoseconds, server time. Sort key of the `by_updated_at` index. Heartbeats do not change it |
| `expires_at` | N | TTL attribute, Unix seconds: last write or heartbeat plus 7 days |
| `pool_id` | S | Owner of a pool name reservation |
| `pool_name` | S | Pool name of a pool agent registration |

## Writes

Every write is conditional, so any replica of gds-server can accept any
write and a lost race never overwrites newer data.

| Operation | Requests | Condition | On a failed condition |
| :---- | :---- | :---- | :---- |
| `PutCluster` (register) | `GetItem` (consistent), `PutItem` | `attribute_not_exists(pk) OR (lease_epoch < :new AND gen = :seen)` | Retry from the returned item (`ALL_OLD`); epochs stay unique |
| `RenewClusterLease` | `UpdateItem SET lease_renewed_at, expires_at` | `lease_epoch = :epoch` | No item: `ErrNotFound`; else `ErrLeaseEpochMismatch` |
| `PutExport`, `PutEndpointSet` | `PutItem` | `attribute_not_exists(pk) OR gen < :gen` | `ErrStaleGeneration{Current}` from the returned item |
| `DeleteExport` | `BatchWriteItem` (export and endpoint set) | none | |
| `DeleteEndpointSet` | `DeleteItem` | none | |
| `PutPool` (register, update) | `GetItem` (consistent), `TransactWriteItems` | pool: `attribute_not_exists(pk)`, or `gen = :seen AND body = :seen AND lease_epoch = :seen` (plus `lease_renewed_at = :seen` when the lease is kept); reservation: free or held by this pool; old reservation (rename): delete when held by this pool | Pool changed: retry, `ErrStaleGeneration` when the stored generation is not lower; name held by a live pool: `ErrPoolNameTaken` |
| `RenewPoolLease` | `UpdateItem`; with a patch that changes the body, `GetItem` first | `lease_epoch = :epoch`, plus `body = :seen` with a patch | As for clusters; a changed body is retried |
| `DeletePool` | `GetItem` (consistent), `TransactWriteItems`, then `Query` (consistent, keys only) of `PA#<cluster>#<pool_id>#` and `BatchWriteItem` | pool `gen = :seen AND body = :seen`; reservation free or held by this pool; agents: none | Retry; a reservation held by another pool is left alone. The agents are removed also when the pool item does not exist |
| `PutPoolAgent` (register) | `GetItem` (consistent), `PutItem` | `attribute_not_exists(pk)`, or `gen = :seen AND lease_epoch = :seen` (plus `lease_renewed_at = :seen` when the lease is kept) | Retry from the returned item; `ErrStaleGeneration` when the stored generation is not lower |
| `RenewPoolAgentLease` | `UpdateItem`; with attributes that change the body, `GetItem` first | `lease_epoch = :epoch`, plus `body = :seen` with attributes | As for clusters; a changed body is retried |
| `DeletePoolAgent` | `DeleteItem` | none | |
| `DeleteCluster` | `Query` (consistent, keys only) of `CL#`, `EX#<cluster>#`, `PL#<cluster>#`, `PN#<cluster>#`, `PA#<cluster>#` and `EP#<clusterset>#<cluster>`, then `BatchWriteItem` | none | Chunks of 25; unprocessed items are retried with backoff |

`PutCluster`, `PutPool` and `PutPoolAgent` derive fields from the stored
item (the next epoch, `RegisteredAt`, the old name to release), so their
condition also pins the generation (and, for pools and pool agents, the
lease, and for pools the body) they read. That is stricter than the
designed `lease_epoch < :new` and `gen < :gen` and implies them. A pool
agent registration has no name reservation: agents join their pool by
`(cluster, pool_name)` in the server's view, and the API checks an explicit
`pool_id` against the view. A stale pool name reservation, whose pool no longer exists (for
example after a TTL deletion) or now carries another name, is taken over
inside the same transaction, with a condition check that the holder is
still absent or unchanged.

## Reads

- `LoadClusterSet`: one paginated query of `CS#<clusterset>`, then one
  paginated query per endpoint partition, at most `QueryConcurrency`
  (default 8) in parallel. Eventually consistent; each record is whole.
- `ListClusterSets`: paginated query of `pk = CSX`.
- `GetCluster` and the reads before conditional writes: strongly consistent
  `GetItem` on the local replica.
- `Ping`: `GetItem` of `CSX`/`PING`, an item that never exists. It needs only
  `dynamodb:GetItem` and fails when the table is missing or the credentials
  are rejected.

## Errors

| DynamoDB error | Store error |
| :---- | :---- |
| `ConditionalCheckFailedException`, `TransactionCanceledException` with `ConditionalCheckFailed` | Handled per operation (table above) |
| Item above 400 KB (`ValidationException`), `ItemCollectionSizeLimitExceededException` | `ErrTooLarge` |
| Other `ValidationException`, `SerializationException` | Plain error (a bug) |
| Throttling, capacity, internal errors, missing table, denied access, transport and DNS errors | `ErrUnavailable` (`STORE_UNAVAILABLE`) |
| Context cancellation and deadlines | Returned as they are |

The store also returns `ErrTooLarge` itself for more than 1000 endpoints or
an item above 400 KB, before calling DynamoDB. The AWS SDK retries
throttling and transient errors (standard retryer, 3 attempts) before the
store sees them.

## Table definition

`gds-admin create-table` creates the table, waits until it is `ACTIVE`, then
enables TTL and point-in-time recovery. On an existing table it checks the
key schema and enables only what is missing.

```bash
gds-admin create-table --table gds --region us-east-1
# dynamodb-local has no point-in-time recovery:
gds-admin create-table --table gds --region us-east-1 \
  --endpoint http://localhost:8000 --pitr=false --deletion-protection=false
```

The same with the AWS CLI. `create-table.json` is
`internal/store/dynamodb/testdata/create-table.json`; a unit test keeps it
equal to the request `gds-admin` sends:

```json
{
  "TableName": "gds",
  "AttributeDefinitions": [
    {"AttributeName": "pk", "AttributeType": "S"},
    {"AttributeName": "sk", "AttributeType": "S"},
    {"AttributeName": "updated_at", "AttributeType": "N"}
  ],
  "KeySchema": [
    {"AttributeName": "pk", "KeyType": "HASH"},
    {"AttributeName": "sk", "KeyType": "RANGE"}
  ],
  "LocalSecondaryIndexes": [
    {
      "IndexName": "by_updated_at",
      "KeySchema": [
        {"AttributeName": "pk", "KeyType": "HASH"},
        {"AttributeName": "updated_at", "KeyType": "RANGE"}
      ],
      "Projection": {"ProjectionType": "KEYS_ONLY"}
    }
  ],
  "BillingMode": "PAY_PER_REQUEST",
  "DeletionProtectionEnabled": true
}
```

```bash
aws dynamodb create-table --region us-east-1 --cli-input-json file://create-table.json
aws dynamodb wait table-exists --region us-east-1 --table-name gds
aws dynamodb update-time-to-live --region us-east-1 --table-name gds \
  --time-to-live-specification Enabled=true,AttributeName=expires_at
aws dynamodb update-continuous-backups --region us-east-1 --table-name gds \
  --point-in-time-recovery-specification PointInTimeRecoveryEnabled=true
```

The `by_updated_at` index is unused in v1. A local secondary index can only
be created with the table, so it exists now for a later incremental refresh
that reads only the keys changed since a time. Such a refresh will also need
lease renewals, which do not change `updated_at` today.

### CloudFormation

An infrastructure repository that manages the table declaratively can use
`AWS::DynamoDB::GlobalTable`, which creates the table and its replicas in
one resource. Deploy the stack in the first region only:

```yaml
Resources:
  GdsTable:
    Type: AWS::DynamoDB::GlobalTable
    DeletionPolicy: Retain
    UpdateReplacePolicy: Retain
    Properties:
      TableName: gds
      BillingMode: PAY_PER_REQUEST
      AttributeDefinitions:
        - {AttributeName: pk, AttributeType: S}
        - {AttributeName: sk, AttributeType: S}
        - {AttributeName: updated_at, AttributeType: N}
      KeySchema:
        - {AttributeName: pk, KeyType: HASH}
        - {AttributeName: sk, KeyType: RANGE}
      LocalSecondaryIndexes:
        - IndexName: by_updated_at
          KeySchema:
            - {AttributeName: pk, KeyType: HASH}
            - {AttributeName: updated_at, KeyType: RANGE}
          Projection: {ProjectionType: KEYS_ONLY}
      TimeToLiveSpecification:
        AttributeName: expires_at
        Enabled: true
      StreamSpecification:
        StreamViewType: NEW_AND_OLD_IMAGES
      Replicas:
        - Region: us-east-1
          DeletionProtectionEnabled: true
          PointInTimeRecoverySpecification: {PointInTimeRecoveryEnabled: true}
        - Region: us-west-2
          DeletionProtectionEnabled: true
          PointInTimeRecoverySpecification: {PointInTimeRecoveryEnabled: true}
```

## Global Table replicas

Create the table in one region, then add a replica per GDS region. Version
2019.11.21 is what `update-table --replica-updates` creates:

```bash
aws dynamodb update-table --region us-east-1 --table-name gds \
  --replica-updates '[{"Create": {"RegionName": "us-west-2"}}]'
aws dynamodb describe-table --region us-east-1 --table-name gds \
  --query 'Table.Replicas[].{region: RegionName, status: ReplicaStatus}'
```

- Add one replica at a time and wait for `ACTIVE`. The replica receives the
  key schema, the local secondary index and the existing items.
- Adding the first replica turns on DynamoDB Streams with
  `NEW_AND_OLD_IMAGES`; replication depends on it, so leave it on.
- TTL and the capacity mode are synchronized to replicas. Point-in-time
  recovery and deletion protection are per replica: run
  `gds-admin create-table --region <replica region>` in each region after
  the replica is `ACTIVE` (it finds the table, enables point-in-time
  recovery) and enable deletion protection with
  `aws dynamodb update-table --region <replica region> --table-name gds --deletion-protection-enabled`.
- Check each region with `aws dynamodb describe-time-to-live` and
  `aws dynamodb describe-continuous-backups`.
- Watch the `ReplicationLatency` CloudWatch metric of each replica pair.
- gds-server in each region uses `AWS_REGION` of its own region and needs
  no other configuration.

To remove a region: scale its gds-server to zero, remove its Route53
records, then `--replica-updates '[{"Delete": {"RegionName": "us-west-2"}}]'`.

## TTL

`expires_at` is a garbage-collection safety net, not a lease. The server
evaluates leases from `lease_renewed_at` and the lease TTL at read time and
never relies on DynamoDB TTL timing.

- Cluster, pool and pool agent items get `expires_at` = last
  registration, update or heartbeat + 7 days (`Options.ExpiryAfter`;
  negative disables). A heartbeat extends it.
- Exports, endpoint sets, name reservations, markers and the clusterset
  index never expire. After a cluster item expires, its exports and endpoint
  sets remain until `DeregisterCluster` or server compaction removes them;
  without a registration they do not participate in imports.
- A reservation whose pool expired is taken over by the next pool that
  wants the name.
- DynamoDB deletes expired items in the background, usually within a few
  days. In a Global Table the deletion is replicated to every replica and
  consumes replicated write capacity there.

## Point-in-time recovery

Enable it on every replica. It keeps 35 days of continuous backups. A
restore creates a new, single-region table: restore into a new name, point
one region's gds-server at it with `--dynamodb-table`, add replicas to it,
then move the other regions. Records written after the restore point are
lost; controllers and pool operators re-register and resync within one
heartbeat or resync interval, because every write replaces the full state of
its record.

## Capacity

The table uses on-demand capacity. Request units per operation, for items
under 1 KB unless noted, in the region that accepts the write; every write
also costs one replicated write unit per KB in each other replica region:

| Operation | Read units | Write units |
| :---- | :---- | :---- |
| Cluster, pool or pool agent heartbeat (no patch) | 0 | 1 |
| Cluster registration | 1 | 1 for the item, 2 for the index entry (`updated_at` changes) |
| Export put | 0 | item KB rounded up, plus 2 for the index entry |
| Endpoint set replace | 0 | item KB rounded up (about 10 to 20 KB per 100 endpoints), plus 2 for the index entry |
| Pool register or update | 1 | twice the KB of each transaction item (pool, reservation, old reservation), plus the index entry |
| Pool agent register | 1 | 1 for the item, 2 for the index entry |
| `LoadClusterSet` | 0.5 per 4 KB read (eventually consistent) | 0 |

The refresh dominates reads: every gds-server replica loads every
clusterset every 2 seconds. With R replicas across all regions and S bytes
in a clusterset (metadata plus endpoint sets), reads are about
`R * S / 4 KB * 0.5 / 2` read units per second. Example: 6 replicas, 20
clusters with 10 exports of 50 endpoints each (about 1.2 MB) cost about 450
read units per second. Writes scale with heartbeats: 20 clusters at a
10-second interval cost 2 write units per second per region.

## Size and throughput ceilings

| Limit | Value | Effect on GDS |
| :---- | :---- | :---- |
| Item size | 400 KB | An endpoint set holds at most 1000 endpoints; with many attributes per endpoint it can exceed 400 KB and is rejected with `ErrTooLarge` |
| Item collection (one pk) with a local secondary index | 10 GB | Bounds one clusterset's metadata and one cluster's endpoint sets; far above expected sizes |
| Throughput of one partition key with a local secondary index | about 1,000 write and 3,000 read units per second | Items of one pk stay on one partition and are not split for heat. Bounds the heartbeat rate of one clusterset (all clusters, leased pools and pool agents) and the refresh rate of one partition |
| Query page | 1 MB | `LoadClusterSet` paginates |
| `BatchWriteItem` | 25 requests | `DeleteCluster` chunks |
| `TransactWriteItems` | 100 items, 4 MB | `PutPool` uses at most 4 items |
| Transactions in a Global Table | ACID in the accepting region only | See `docs/consistency-dynamodb.md` |

When a clusterset approaches the partition ceiling: lengthen heartbeat and
refresh intervals, move pools into per-cluster partitions (store-internal),
or switch the refresh to the `by_updated_at` index or DynamoDB Streams.

## The writer generation contract

- Exports, endpoint sets, pools and pool agents carry a writer-supplied
  `generation`.
  The store accepts a put only when it is greater than the stored one and
  otherwise returns the stored value (`STALE_GENERATION` with
  `current_generation`).
- Writers compute `generation = max(previous + 1, now in unix ms)`. The Go
  and Rust clients enforce it. A restarted writer, or one that failed over
  to another region, therefore starts above anything it wrote before
  without remembering it, and Global Table last-writer-wins agrees with
  generation order.
- A deleted record has no generation: a new export after `DeleteExport` may
  start lower.
- Cluster registrations do not carry a writer generation; the store
  increments `gen` itself. Lease epochs are minted by the store as
  `max(stored + 1, now in unix ms)`, so they increase across
  re-registrations even when the handling replica's clock is behind.
- Writers that do not follow the contract (hand-written REST clients) can
  only hurt their own records: a lower generation is rejected, a generation
  far in the future blocks their own later writes until it is deleted.

## Inspecting the table

Bodies are protobuf; the top-level attributes are readable directly:

```bash
aws dynamodb query --region us-east-1 --table-name gds \
  --key-condition-expression 'pk = :pk' \
  --expression-attribute-values '{":pk": {"S": "CS#prod"}}' \
  --projection-expression 'sk, gen, lease_epoch, lease_renewed_at, updated_at'
```

## Tests

- `make test`: unit tests, including the conformance suite against an
  in-memory fake of the DynamoDB API that enforces the same condition,
  placeholder and batch rules.
- `make test-integration`: the conformance suite, a two-replica test, a
  multi-page load and the table definition against `amazon/dynamodb-local`
  in Docker (testcontainers-go). Tests skip without Docker.
  `GDS_TEST_DYNAMODB_ENDPOINT` points them at a running endpoint instead.
