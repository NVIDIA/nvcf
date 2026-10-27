# AGENTS.md - API Keys Service

API Keys is a single-module OSS/self-hosted Java service in the root `nvcf`
Bazel module. Do not create a synthetic core module, nested Bazel module,
lockfile, repository configuration, or third-party dependency hub.

The monorepo copy is Bazel-only and contains no project POM. Bazel consumes
nv-boot through direct source labels and produces the executable application
jar. Keep any Maven build support in the independent source repository. Do not
restore project POMs or add Maven build instructions here.

## Build and test

Run commands from the monorepo root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/api-keys/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/api-keys/... \
  --cache_test_results=no \
  --test_output=errors
```

The test target starts Cassandra through Testcontainers and Docker Compose. It
is tagged `requires-docker` and runs in the GitHub `docker-host` lane.

## Cassandra schema

Local and Testcontainers CQL lives in `local_env/cassandra/schema/`. The
deployed keyspace is `api_keys_api`, applied from
`migrations/cassandra/keyspaces/api_keys_api/`. Keep those copies aligned.

`0001_initial_schema.cql` follows the clean-slate model. It holds the full
local schema, including the multi-tenant tables. Update it in place instead of
adding local delta files.

`03_init_tables.up.sql` stays the original single-tenant tables. Do not add
multi-tenant objects to `03`. `04_add_multi_tenant_schema.up.sql` is the
deployed delta for new and existing clusters. `keys` and
`keys_by_owner_and_service` stay as legacy tables until the migration completes.

`KeysDao`, `KeyModel`, and `KeyVo` serve the legacy tables. Do not add
multi-tenant behavior to them. Keys issued to an account use `KeysV2Dao`,
`KeyV2Model`, and `KeyV2Vo` against `keys_v2` and
`keys_by_owner_and_account_and_service`. Owner status and bulk operations use
`OwnerStatusByAccountDao` and `KeyOperationsDao`.

`keys_v2.nca_id` is a regular column. `KeysV2Dao` and `KeyV2Model` require it.

`KeysV2Service` serves the v2 API. New keys always go to `keys_v2`. Create
rejects a hash that already exists in `keys_v2` or the legacy `keys` table.
Create takes the `keys_v2` authorization lock before the write, matching v1, so
an authorization update cannot run until `minAuthzUpdateIntervalSeconds` passes.
For the configured `apikeys.nca-id`, reads check `keys_v2` first and fall back
to the legacy DAOs, and updates and deletes go to the table that holds the key.
`LegacyAccountTranslator` maps v2 service IDs (the keys of
`apikeys.service-id-map`) to the legacy IDs stored in the legacy tables. Keep
legacy routing in these two classes instead of the DAOs or controllers.
`EncryptedModelConverter` maps each model to exactly one `@ValueObject` and
maps every model column to a same-named value-object field, so a new column on
an encrypted model also needs that field on its `@ValueObject`.

`com.nvidia.apikeys.migration` copies legacy keys into `keys_v2` and
`keys_by_owner_and_account_and_service` when
`apikeys.migration.legacy-keys.enabled` is true. A run holds a lease row in
`row_update_lock` through `LegacyKeyMaintenance`. While the lease exists,
`KeysV2Service` returns 503 for legacy-account key updates and deletes on every
replica, whether or not the task is enabled there. Ship the 503 check with the
task disabled first, and enable it only after every replica runs that build.
The authorization throttle always uses the `keys_v2` lock name, so a migrated
key keeps its interval.

Integration tests bind each `.cql` file in `local_env/docker-compose.test.yml`
because Bazel runfiles are symlinks. Local Compose mounts the whole schema
directory.

## Dependencies

The root `MODULE.bazel` and `maven_install.json` own
`@nv_third_party_deps`. BUILD targets declare compile and runtime edges. Use
direct labels for co-located nv-boot targets.

## NOTICE

Generate and check the runtime-derived component NOTICE with:

```bash
bazel run //src/control-plane-services/api-keys:generate_notice -- \
  --update-metadata --write
bazel test //src/control-plane-services/api-keys:notice_check_test
bazel build //src/control-plane-services/api-keys:osrb_dependency_delta
```

Do not restore the standalone Maven NOTICE template or copy its repository
`LICENSE` or generated `NOTICE`.
