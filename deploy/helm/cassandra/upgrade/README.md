# Cassandra migration tooling

Tooling for migrating an existing Bitnami-subchart Cassandra release to the
in-house StatefulSet chart. Read `../docs/upgrade-from-bitnami.md` first for the
full context, the three migration options, and the open questions still under
review.

## migrate-from-bitnami.sh

Automates Option A (in-place adopt) from the migration doc: it recreates the
StatefulSet and adopts the existing data PVC, with safety checks. It is dry-run
by default and refuses managed/cloud contexts.

Status: provisional. It does not resolve the open items from the migration doc
(the full cassandra.yaml config-compat set, and whether in-place adopt or
backup/restore is the supported strategy). The values file you pass must set
`persistence.subPath: data` and the config-compat settings the old data
requires. Backup and restore (Option C) remains the safer path for production.

Dry run first (prints the plan, changes nothing):

```bash
./migrate-from-bitnami.sh \
  --context k3d-ncp-local \
  --namespace cassandra-system \
  --values /path/to/upgrade-values.yaml \
  --probe-keyspace nvcf_api --probe-table <table> --cql-pass "$PW"
```

Execute:

```bash
./migrate-from-bitnami.sh ... --confirm
```

Safety behavior:
- Refuses contexts that look managed/cloud/prod (arn:aws, eks, gke, aks, prod,
  qa) unless `--allow-unsafe-context` is set.
- Requires the release and namespace to exist and the data PVC to be `Bound`.
- Refuses to proceed if the StatefulSet's
  `persistentVolumeClaimRetentionPolicy.whenDeleted` is `Delete` (that would
  garbage-collect the PVC on orphan-delete).
- Takes a `nodetool snapshot` before making changes unless `--skip-snapshot`.
- Verifies the node reaches `UN` after the upgrade, and if `--probe-*` is given,
  that the row count survived. On any failure it stops and leaves the PVC
  intact.
