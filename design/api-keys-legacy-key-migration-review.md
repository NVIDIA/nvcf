# API Keys legacy key migration: review summary

Status: for team review. The implementation in this change follows Option B.

Related issue: [#2054 Async Migration Task](https://github.com/NVIDIA/nvcf/issues/2054),
part of [#2048 Multi-tenancy support in API Keys service](https://github.com/NVIDIA/nvcf/issues/2048).

## Problem

Keys created before multi-tenancy live in the legacy `keys` and
`keys_by_owner_and_service` tables, with no `nca_id`. `KeysV2Service` serves
them for the configured `apikeys.nca-id` by reading `keys_v2` first and falling
back to the legacy tables. The fallback stays until every legacy key has a copy
in `keys_v2` and `keys_by_owner_and_account_and_service`.

This design adds a background task that makes those copies.

## Goals and non-goals

Goals:

- Copy every legacy key into both v2 tables, in v2 form.
- Run on one replica at a time, only when turned on in config.
- Bound Cassandra load with small batches and a cursor.
- Be safe to rerun and to stop at any point.
- Never lose a key update or bring back a deleted key while traffic continues.

Non-goals:

- Changing or deleting legacy rows. Cleanup is a later step.
- Migrating owner status. For the legacy account, it stays in the legacy
  tables.

## Prerequisites

- v1 key management is cut off before the task is turned on. After that, the
  legacy tables change only through `KeysV2Service`, and no new legacy keys
  appear.
- Introspection reads through `KeysV2Service.getKeyByHash`, so a revoke or
  suspend on a migrated key takes effect.
- One datacenter. Lock and lease writes use `LOCAL_SERIAL`, which coordinates
  within one datacenter only.

## The core problem: concurrent writes

While the task copies a key, `KeysV2Service` can update or delete the same key
in the legacy tables. Without coordination:

| Race | Effect | Severity |
|------|--------|----------|
| Update reads legacy before the copy and writes legacy after it | v2 copy keeps the old state; a revoke is lost | High |
| Delete runs while the key is being copied | Deleted key comes back in `keys_v2` | High |
| Delete removes v2, then the task copies legacy before the legacy delete | Deleted key comes back in `keys_v2` | High |
| Task copies again after a newer v2 write | Newer v2 update is overwritten | Medium |
| Two pods with skewed clocks write the same key | Cassandra keeps the higher timestamp, which may be the older write | Medium |

Two options handle this.

## Option A: Per-key lock

The task and `KeysV2Service` serialize on a lock per key in `row_update_lock`,
record `("keys_v2", "migrate-for-key-hash-<hash>")`.

- Task, per key: skip if both v2 rows exist. Otherwise take the lock, re-read
  the legacy row and both v2 rows, copy only if no v2 copy exists, then
  release.
- `KeysV2Service`, for legacy-account keys that still have a legacy row:
  status update, authorizations update, and delete take the same lock. They
  reload the key under the lock and write to whichever table now holds it.
  Delete removes legacy rows before v2 rows. A held lock returns 429.
- Residual risk: a holder that stalls past the lock TTL right before its
  write can overlap another holder. The task skips the write if more than half
  the TTL has passed.

## Option B: Maintenance window

While a run is active, block the writes that race with the task. The runner
lease row `("legacy_key_migration", "leader")` is the maintenance flag, shared
by all replicas through Cassandra.

- Blocked, for the legacy account only: `updateKeyStatus`,
  `updateKeyAuthorizations`, `deleteKeyById`. They read the lease row without
  caching and return 503 with `Retry-After` while it exists.
- Open: `createKey`, owner status updates, reads, listing, introspection, and
  every other account.
- Run flow:
  1. Take the lease. Blocked calls start returning 503.
  2. Wait `maintenance-grace-period` (`PT15S`, longer than the Cassandra
     request timeout) so in-flight writes finish.
  3. Copy in batches. Renew the lease before each batch and stop if renewal
     fails.
  4. Release the lease. Writes resume. If the task crashes, the lease TTL
     expires and writes resume on their own.
- Residual risk: if the lease is lost mid-batch, writes reopen while the task
  finishes that batch. Renewal per batch limits this to one batch.

## Comparison

| | Option A: Per-key lock | Option B: Maintenance window |
|--|--|--|
| Key management during a run | Available; 429 only for the key being copied | 503 for legacy-account updates and deletes |
| Races removed | Narrowed by the lock | Removed: no writer touches legacy rows during a run |
| Cost per migrated key | Two lightweight transactions | None |
| `KeysV2Service` changes | Lock and reload in three methods | One maintenance check |
| Residual risk | Holder stalls past key lock TTL | Lease lost mid-batch |

## Proposal

Option B, with Option A as the fallback.

- It is simpler and removes the races instead of narrowing them.
- Its cost is short. A run takes minutes for about 100,000 keys, and only
  legacy-account updates and deletes are blocked.
- Choose Option A instead if revoking a legacy-account key must stay available
  during a run.

## Shared design

These apply to both options:

- One runner at a time. The lease is renewed before every batch.
- The cursor is the Cassandra paging state, kept in memory for one run. A
  restart starts over; copies are idempotent, and a key already copied costs
  two point reads.
- Keys are decrypted with the raw converter, so stored `ACTIVE` status is not
  rewritten as `EXPIRED`. Expired keys are copied as they are.
- Keys with an issuer service missing from `apikeys.service-id-map` are skipped
  with a warning.
- Both v2 rows are written in one logged batch with `KeysV2Dao.save`.
- The task stops scheduling on a replica after a clean run: `completed`, with
  no `migrated`, `failed`, or `skipped` keys.
- The authorization throttle always uses the `keys_v2` lock name, so a migrated
  key keeps its update interval.

## Configuration

```yaml
apikeys:
  migration:
    legacy-keys:
      enabled: false
      initial-delay: PT1M
      fixed-delay: PT10M
      page-size: 500
      batch-delay: PT0.2S
      page-fetch-attempts: 3
      page-fetch-backoff: PT1S
      lease-duration: PT2M
      maintenance-grace-period: PT15S   # Option B
      # key-lock-duration: PT30S        # Option A
```

## Observability

- Logs: run start and end with counts, each skipped or failed key, lost lease.
  Logs never include the key hash.
- Span `apikeys.legacy-key-migration.run` with `nvcf.org.id`.
- Metrics, pre-initialized to zero:
  - `nvcf_api_keys_legacy_key_migration_keys_total{result}`
  - `nvcf_api_keys_legacy_key_migration_runs_total{outcome}`
  - `nvcf_api_keys_legacy_key_migration_run_duration_seconds`
  - `nvcf_api_keys_legacy_key_migration_completed` gauge

## Rollout

1. Deploy with `enabled=false`. The write check (503 or per-key lock) runs on
   every replica whether or not the task is enabled, so every pod must run
   this release before any run starts.
2. Turn the task on in one environment. Watch run summaries and metrics.
3. Confirm the `completed` gauge on every replica.
4. Set `enabled=false` at the next deploy.
5. Remove the task and the write check in one release, tracked in a public
   issue.
6. Removing the legacy fallback and tables is follow-up work.

## Questions for review

1. Confirm Option B over Option A.
2. Is blocking legacy-account revoke and delete for a few minutes acceptable?
3. Is `PT15S` enough for `maintenance-grace-period`?
4. Which release cuts off v1 key management, and does it also move
   introspection to `KeysV2Service`?
