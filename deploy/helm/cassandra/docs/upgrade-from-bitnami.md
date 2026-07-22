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

1. StatefulSet immutability. A direct `helm upgrade` from the Bitnami-subchart
   release to the in-house chart fails:
   `StatefulSet.apps "cassandra" is invalid: spec: Forbidden: updates to
   statefulset spec for fields other than 'replicas', 'ordinals', 'template',
   'updateStrategy', 'persistentVolumeClaimRetentionPolicy' and
   'minReadySeconds' are forbidden`. The volumeClaimTemplate and serviceName
   differ, and those fields are immutable. The StatefulSet must be recreated,
   not updated in place.

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

## StatefulSet immutability and the recreate step

This is the mechanical heart of an in-place migration, so it is worth spelling
out.

Kubernetes freezes almost the entire StatefulSet `spec` after creation. The only
fields you may change on an existing StatefulSet are `replicas`, `ordinals`,
`template`, `updateStrategy`, `persistentVolumeClaimRetentionPolicy`, and
`minReadySeconds`. Everything else is immutable, including `selector`,
`serviceName`, `podManagementPolicy`, and `volumeClaimTemplates`.

The old and new StatefulSets share the name `cassandra` but differ in several of
those frozen fields:
- `serviceName`: the Bitnami headless service name vs the in-house
  `cassandra-headless`.
- `selector.matchLabels`: the Bitnami labels vs the in-house
  `cassandra.selectorLabels`.
- `volumeClaimTemplates`: name, size, storageClass, and labels differ.

So `helm upgrade` applies the new chart onto the existing `cassandra`
StatefulSet, and the API server rejects it with the Forbidden error above. You
cannot reshape one StatefulSet into a structurally different one in place. This
is a Kubernetes constraint, not a chart defect.

The recreate step works because the data lives in a PersistentVolumeClaim
(`data-cassandra-0`) whose lifecycle is independent of the StatefulSet
controller object. Deleting the StatefulSet does not delete its PVCs;
StatefulSet-managed PVCs are retained by default (this is what
`persistentVolumeClaimRetentionPolicy` governs, and its default is Retain). The
runbook:

1. `kubectl delete statefulset cassandra --cascade=orphan` removes only the
   StatefulSet controller object. `--cascade=orphan` also leaves the running
   pods; the PVC is kept regardless of cascade mode.
2. Delete the old pod so the new controller starts a fresh one on the official
   image. The PVC stays.
3. `helm upgrade` creates a brand-new StatefulSet named `cassandra`. A create is
   not an update, so there is no immutability check.
4. Because the new StatefulSet has the same name and the same
   volumeClaimTemplate name (`data`), it re-adopts the existing
   `data-cassandra-0` PVC by name instead of provisioning a new empty one.
   StatefulSets bind to a matching existing PVC and never recreate one that is
   already present.

Net effect: the controller object is swapped, the data volume is untouched and
re-adopted, and the new pod comes up on the old data. In the Phase 4 test the
PVC stayed `Bound` throughout and the new UID-999 pod mounted and read it.

Two caveats:
- This is a one-time migration hop (Bitnami-shaped StatefulSet to
  in-house-shaped StatefulSet). Ordinary upgrades within the in-house chart
  later do not hit this, because the StatefulSet spec shape stays stable, unless
  a future change edits a `volumeClaimTemplate` field (which would trip the same
  rule).
- The orphan-delete is a manual, operator-error-prone step (a wrong flag or
  target can delete more than intended). That risk is one of the reasons Option
  C (backup and restore) is the safer path for production.

## Options

### Option A: in-place adopt (legacy layout + config compat)

Reuse the existing PVC in place. Mechanics:
- Recreate the StatefulSet: `kubectl delete statefulset cassandra
  --cascade=orphan` (keeps the pod and PVC), delete the old pod (the PVC
  persists), then `helm upgrade` so the new StatefulSet adopts
  `data-cassandra-0`.
- Align the layout: set `persistence.subPath: data` so the old nested
  `data/{data,commitlog,...}` surfaces at the official defaults
  `/var/lib/cassandra/*` with no cassandra.yaml directory edits. This is
  implemented in the chart today.
- Align config: the chart must also set the cassandra.yaml settings the old
  data requires, at least `uuid_sstable_identifiers_enabled: true`, via the
  existing conf initContainer patch. The full set of settings that must match
  is not yet enumerated.


The orphan-delete-and-recreate runbook is scripted with safety checks at
`upgrade/migrate-from-bitnami.sh` (dry-run by default). It is provisional until
the config-compat set and strategy below are settled.

Pros: no downtime beyond the pod restart, no data copy, keeps the existing PVC.
Cons: relies on the orphan-delete runbook (operator error prone), and on
enumerating every cassandra.yaml setting the old fleet used. Config drift risk:
if an old setting is missed, the node fails to start on real data. Leaves the
data physically in the Bitnami nesting.

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
  enumerated and encoded, and with the orphan-delete-and-recreate documented as
  a supported runbook.
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
- In-place upgrade: PVC adoption mechanically works; `subPath: data` surfaces
  the old data and the UID-999 image reads the UID-1001 files; blocked by the
  `uuid_sstable_identifiers_enabled` config mismatch and gated behind the
  StatefulSet recreate runbook.
