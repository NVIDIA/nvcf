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
  1. If the completion marker exists, end the run without taking the lease.
     See Completion marker.
  2. Take the lease. Blocked calls start returning 503.
  3. Wait `maintenance-grace-period` (`PT15S`, longer than the Cassandra
     request timeout) so in-flight writes finish.
  4. Copy in batches. Renew the lease before each batch and stop if renewal
     fails.
  5. If the run was clean, write the completion marker.
  6. Release the lease. Writes resume. If the task crashes, the lease TTL
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
  with a warning. They stay served by the legacy fallback and do not block
  completion, because no later run can copy them.
- Both v2 rows are written in one logged batch with `KeysV2Dao.save`.
- A clean run is `completed` with no `migrated` or `failed` keys. The task
  stops scheduling on a replica after a clean run, or after a run that finds
  the completion marker (outcome `already_completed`).

## Completion marker

Without a marker, every replica needs its own clean run before it stops, and
every restart schedules again. Each of those runs takes the lease and opens a
new 503 window, even when nothing is left to copy.

- Record: `("legacy_key_migration", "completed")` in `row_update_lock`, next to
  the lease row. No schema change.
- Written with the existing `lock` query and TTL `0`, so it never expires.
- Written only after a clean run, while the lease is still held, so no replica
  starts another pass between the end of the run and the write.
- Every run reads it before taking the lease. If it exists, the run ends with
  outcome `already_completed`, takes no lease, and opens no 503 window.
- It does not block writes. Only the lease row does.
- If the write fails, the run still ends clean on that replica, and another
  replica records the marker on its next clean run.
- The check and the lease are not atomic. Two replicas that check at the same
  moment can run one extra pass. That costs one extra window, not data.
- To run the migration again, delete the row:

  ```sql
  DELETE FROM row_update_lock
  WHERE table_name = 'legacy_key_migration' AND record_key = 'completed';
  ```
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
  - `nvcf_api_keys_legacy_key_migration_runs_total{outcome}`, including
    `already_completed`
  - `nvcf_api_keys_legacy_key_migration_run_duration_seconds`
  - `nvcf_api_keys_legacy_key_migration_completed` gauge

## Rollout

1. Deploy with `enabled=false`. The write check (503 or per-key lock) runs on
   every replica whether or not the task is enabled, so every pod must run
   this release before any run starts.
2. Turn the task on in one environment. Watch run summaries and metrics.
3. Confirm the completion marker row exists and the `completed` gauge is `1`
   on every replica. Check the `skipped` count and the skip warnings for keys
   whose issuer service needs a mapping.
4. Set `enabled=false` at the next deploy.
5. Remove the task and the write check in one release, tracked in a public
   issue. The marker row can be deleted then; leaving it is harmless.
6. Removing the legacy fallback and tables is follow-up work.

## Questions for review

1. Confirm Option B over Option A.
2. Is blocking legacy-account revoke and delete for a few minutes acceptable?
3. Is `PT15S` enough for `maintenance-grace-period`?
4. Which release cuts off v1 key management, and does it also move
   introspection to `KeysV2Service`?
