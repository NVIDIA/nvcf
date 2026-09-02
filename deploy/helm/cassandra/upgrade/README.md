# Cassandra migration tooling

Tooling for migrating an existing Bitnami-subchart Cassandra release to the
in-house StatefulSet chart. Read `../docs/upgrade-from-bitnami.md` first for the
full context, the three migration options, and the open questions still under
review.

## Normal upgrade

Current charts preserve the immutable StatefulSet/PVC-template identity from
published 0.15.5. Use a normal Helm upgrade with
`cassandra.persistence.subPath: data` and the documented config-compat values.
Verify the StatefulSet, PVC, and PV UIDs and application data before and after.

## Legacy migrate-from-bitnami.sh fallback

This provisional script predates the immutable-identity fix. It recreates the
StatefulSet and adopts the existing data PVC, with safety checks. It is not the
normal upgrade path; retain it only as a recovery tool for an early source
chart that omitted the 0.15.5 PVC-template identity. It is dry-run by default
and refuses managed/cloud contexts.

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
