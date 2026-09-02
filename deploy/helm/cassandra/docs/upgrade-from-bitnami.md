# Migrating existing Bitnami Cassandra deployments to the in-house chart

Status: pre-1.0.0 design note for review (consult before finalizing).
Context: Cassandra migration from the Bitnami base to the official Apache base.

## What this is

The Cassandra runtime moved off the archived `bitnamilegacy/cassandra` base
onto the official `cassandra:5.0.x` image, and the Helm chart moved off the
Bitnami subchart to an in-house StatefulSet chart. Fresh installs of the new
stack are validated end to end (single and multi node: ring formation, auth,
init CQL, migrations, exporter). This note is only about the remaining
question: how an existing Bitnami-based cluster with real data becomes a
new-stack cluster without losing data.

## Why it is not a simple `helm upgrade`

Phase 4 tested an in-place over-the-top upgrade on k3d (old Bitnami stack with a
known dataset, then upgrade to the new chart/image). Three concrete obstacles
surfaced, each with evidence:

1. StatefulSet immutability. Early versions of the in-house chart removed the
   `apiVersion`, `kind`, and the existing `app.kubernetes.io/name` and
   `app.kubernetes.io/instance` labels from
   `volumeClaimTemplates.metadata`. Kubernetes treats the entire volume claim
   template as immutable, so the API server rejected a normal Helm upgrade.
   The chart now preserves the exact identity emitted by published chart
   0.15.5, and CI guards that contract. Do not remove or rename those fields.

2. Data-layout nesting. Bitnami stored data nested under the mount:
   `<pvc>/data/{data,commitlog,hints,saved_caches}` with
   `data_file_directories: /bitnami/cassandra/data/data`. The official image
   uses the single-nested defaults `/var/lib/cassandra/{data,commitlog,...}`.
   A plain re-mount does not line up.

3. cassandra.yaml config compatibility. Even after the data is surfaced at the
   right paths, the node refuses to start when its config does not match the
   settings the old cluster used to write the data. Observed:
   `UUID sstable identifiers are disabled but some sstables have been created
   with UUID identifiers`. The Bitnami deployment ran with
   `uuid_sstable_identifiers_enabled: true`; the official image defaults it to
   false. Other settings the old fleet relied on may need the same treatment.

The PVC name (`data-cassandra-0`) and pod name (`cassandra-0`) do match between
the old and new charts, so PVC adoption is mechanically possible. The UID
difference (old 1001, new 999) is handled by the pod `fsGroup: 999`; in testing
the new image read the old files without a permission error.

## StatefulSet immutability and the data-safe upgrade

This is the mechanical heart of an in-place migration, so it is worth spelling
out.

Kubernetes freezes almost the entire StatefulSet `spec` after creation. The only
fields you may change on an existing StatefulSet are `replicas`, `ordinals`,
`template`, `updateStrategy`, `persistentVolumeClaimRetentionPolicy`, and
`minReadySeconds`. Everything else is immutable, including `selector`,
`serviceName`, `podManagementPolicy`, and `volumeClaimTemplates`.

The old and new StatefulSets share the name, selector, service name,
`podManagementPolicy`, and the `volumeClaimTemplates` identity and spec fields.
The in-house chart must keep that immutable subset byte-for-byte equivalent to
the Kubernetes-normalized 0.15.5 object. The regression test at
`helm/scripts/test-bitnami-upgrade-identity.sh` enforces the fields that caused
the original rejection; live upgrade validation must additionally confirm that
the StatefulSet UID and PVC UID remain unchanged.

With that immutable identity preserved, a normal `helm upgrade` updates the
existing StatefulSet in place. Set `cassandra.persistence.subPath: data` for
this one-time transition so the new UID-999 container sees the Bitnami layout,
and retain the compatibility keys in `cassandra.config`. Do not delete or
recreate the StatefulSet or PVC as part of the normal path.

The published-0.15.5-to-source validation kept the StatefulSet, PVC, and PV
UIDs unchanged, retained a pre-upgrade sentinel row, and reached Ready on the
official image. The pod was replaced, as expected for an image and pod-template
change; the controller and storage objects were not.

Two caveats:
- This is a one-time data-layout compatibility setting. Keep `subPath: data`
  for that release after the transition; changing it later changes where the
  node looks for its files.
- Any future edit to a `volumeClaimTemplate` field will trip the same immutable
  StatefulSet rule and must be rejected by upgrade testing.

## Options

### Option A: in-place adopt (legacy layout + config compat)

Reuse the existing PVC in place. Mechanics:
- Use a normal `helm upgrade`; the chart preserves the published 0.15.5
  StatefulSet and volume-claim-template identities.
- Align the layout: set `persistence.subPath: data` so the old nested
  `data/{data,commitlog,...}` surfaces at the official defaults
  `/var/lib/cassandra/*` with no cassandra.yaml directory edits. This is
  implemented in the chart today.
- Align config: the chart must also set the cassandra.yaml settings the old
  data requires, at least `uuid_sstable_identifiers_enabled: true`, via the
  existing conf initContainer patch. The full set of settings that must match
  is not yet enumerated.


Pros: no downtime beyond the pod restart, no data copy, keeps the existing PVC.
Cons: relies on enumerating every cassandra.yaml setting the old fleet used.
Config drift risk: if an old setting is missed, the node fails to start on real
data. Leaves the data physically in the Bitnami nesting.

### Option B: data relocation on first upgrade

An initContainer reshapes `data/{data,commitlog,...}` into the official
`/var/lib/cassandra/{data,commitlog,...}` layout once, then the node runs with
default paths.

Pros: clean end state, no permanent Bitnami paths or subPath.
Cons: mutates data on first boot (must be perfectly idempotent and safe to
interrupt), still needs the config-compat settings and the StatefulSet
recreate, hardest to make safe. Not recommended without strong justification.

### Option C: backup and restore into a fresh deployment

Do not adopt the PVC. Snapshot the old cluster (`nodetool snapshot`), stand up a
fresh new-stack cluster on new PVCs, and restore the snapshot (sstableloader or
refresh). Decommission the old deployment after validation.

Pros: safest for data integrity; the new cluster runs on the clean official
layout and config from the start; no immutable-field or config-drift surprises;
well-understood Cassandra operational procedure.
Cons: requires a maintenance window and enough capacity to run both during the
cutover; more operator steps; larger data means longer restore.

## Recommendation for discussion

Given we are pre-1.0.0 and want a clean result:
- Ship Option A as the convenience path for environments that want in-place
  adoption, but only after the full cassandra.yaml config-compat set is
  enumerated and encoded, with the published-to-source UID and data-retention
  regression kept as a release gate.
- Document Option C (backup/restore) as the recommended, safest path,
  especially for production, and as the fallback when in-place adoption is not
  acceptable.

Open questions for Brad:
- Do we commit to supporting in-place adoption (A), or make backup/restore (C)
  the only supported migration and keep the chart clean of legacy-layout knobs?
- If A, what is the complete set of cassandra.yaml settings the current Bitnami
  fleet runs with that new nodes must match to read existing sstables?
- Is a maintenance window acceptable for the migration, which would favor C?

## Backup and restore procedure (Option C outline)

1. On the existing cluster, flush and snapshot every keyspace:
   `nodetool flush && nodetool snapshot`.
2. Copy snapshots off-node (or to object storage).
3. Deploy the new-stack cluster fresh (new PVCs, official layout) and let init
   CQL plus migrations create the schema.
4. Restore data per keyspace/table using `sstableloader` against the new
   cluster, or place sstables and run `nodetool refresh`.
5. Validate row counts and application connectivity, then decommission the old
   deployment.

## Phase 4 evidence

- Fresh install (single and multi node) on the new stack: validated.
- In-place upgrade from published 0.15.5: validated with unchanged StatefulSet,
  PVC, and PV UIDs and a retained sentinel row. `subPath: data` surfaces the old
  data and the chart's compatibility config lets the UID-999 image read the
  UID-1001 files.
