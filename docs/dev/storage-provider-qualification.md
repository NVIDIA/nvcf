# Storage provider qualification

NVCA decides how to cache models from one file: the storage capability
catalog installed with the NVCA chart. This page records what the catalog
means, how a provider is qualified for it, and what has been measured so far.

## What the catalog records

The catalog names exact CSI provisioners. For each one it records the provider
id, the PVC access modes qualified end to end in an NVCF cache workflow, and
the mount options NVCA must apply to reader PVs it creates.

```yaml
drivers:
  nvmesh-csi.excelero.com:
    provider: nvmesh
    accessModes: [ReadWriteOnce, ReadOnlyMany]
    readerMountOptions: [ro, norecovery, nouuid]
```

Nothing about the cache flow is declared. NVCA derives it from the access
modes:

| Qualified modes | Shape |
|---|---|
| ReadWriteMany | one shared claim, readers mount it read-only |
| ReadWriteOnce and ReadOnlyMany | writer takes the claim, readers get ROX on it |
| neither | disabled |

Function model caching keeps its readers in the request namespace, so either
shape serves it. Helm model caching must reach other namespaces. A
ReadWriteMany claim does that natively. The ROX shape does not, except on
NVMesh, whose CSI volume handles encode the namespace so NVCA can derive a
reader PV for another namespace from the writer volume.

An empty `accessModes` list means nothing is qualified yet and both workflows
stay off. A provisioner absent from the file is unsupported. Enabling a
backend is an edit to `accessModes`, backed by a qualification run.

## What a qualification run must prove

Access modes advertised by a driver are not evidence. A claim that binds is
not evidence either. A run must show all of the following, on the exact
provisioner and storage class the cluster will use.

1. A writer can populate a claim, and the data is durable after the writer
   exits.
2. A reader in the same namespace sees the identical bytes. Compare a hash,
   not a directory listing.
3. A reader in a different namespace sees the identical bytes. This is the
   Helm case and it is the one that is usually assumed rather than measured.
4. Read-only is enforced by the filesystem, not merely requested. Probe a
   create, an append, a rename, a chmod, a truncate and a delete, and require
   EROFS. A `ro` flag in the mount options is not proof.
5. The reclaim policy is Retain, so a deleted claim does not destroy a warm
   cache.

Record the failure mode as well as the result. A cache that silently serves an
empty directory is worse than one that fails to bind, because nothing alerts.

## Measured results

### Weka, csi.weka.io

Cluster `amahmood3-dgxc-k8s-mst-blc-01`, storage class `nv-storage-file`
(Retain, Immediate). Measured 2026-08-31. Writer wrote 16 MiB and published a
manifest; readers verified the SHA-256.

| Reader | Volume | Sees cache | Mount | Writes |
|---|---|---|---|---|
| static PV, RWX claim, other namespace | writer volume | yes, hash matches | wekafs ro,relatime | blocked, EROFS |
| static PV, ROX claim, other namespace | writer volume | yes, hash matches | wekafs ro,relatime | blocked, EROFS |
| fresh dynamic PVC, same class | new volume | no, listing empty | wekafs rw | create succeeded |

Weka qualifies for `[ReadWriteMany, ReadOnlyMany]`. Cross-namespace sharing
works and read-only is enforced.

The mechanism is a static PV in the reader namespace that reuses the writer's
volume handle unchanged. Weka handles look like
`weka/v2/csivol-pvc-<id>-<suffix>` and carry no namespace, so there is nothing
to rewrite. NVMesh needs the same static PV plus a handle rewrite because its
handles are namespace scoped.

The third row is the important one. A reader claim that only names a storage
class gets a new empty volume, not the cache.

### OCI FSS, fss.csi.oraclecloud.com

Not qualified. Performance was measured in August. The cache workflow was
not.

The target cluster is `nvcf-dgxc-k8s-oci-jbp-ct4`, reached through the
production Teleport proxy `nv-prd-dgxc.teleport.sh`, not the staging proxy
that serves the Weka cluster. Log in to that proxy before `tsh kube login`.

The OCI dev clusters are not a substitute. `nvcf-dgxc-k8s-oci-iad-dev1` and
`nvcf-dgxc-k8s-oci-ord-dev1` register the FSS CSI driver but neither has an
FSS storage class.

### OCI Lustre, lustre.csi.oraclecloud.com

Not qualified. The driver is registered on the same OCI dev clusters. No PVC
access mode has been qualified in a cache workflow.

## The shared filesystem assumption

`doModelCacheSharedFS` creates its reader as a PVC that names only a storage
class, with no PV and no `volumeName`, and relies on the class to make every
claim resolve to the same data. Its comment states the assumption directly:
cross-namespace sharing is a property of the shared class.

The other two backends do the opposite. NVMesh creates a static secondary PV
pointing at the writer volume with the handle rewritten for the reader
namespace. Samba creates a static PV per reader at the same share root as the
writer, bound by `volumeName`. Both point the reader at the writer's data
explicitly.

The assumption holds only for a class where every dynamically provisioned
claim lands on the same directory. That is not what a CSI driver normally
does. EFS creates an access point per claim, CephFS a subvolume, Weka a new
filesystem, and OCI FSS a new file system or export. It holds for a class
pinned to one export with no per-volume subdirectory, which is how the NFS and
SMB CSI drivers behave when `subDir` or `source` is fixed. That configuration
has to be built deliberately.

So the path is usable only by a `nvcf-miniservice-sc` an operator
pre-provisioned that way. It is not usable by the drivers its own comment
lists. On Weka it was measured to serve an empty directory, and it fails
quietly: the claim binds, the pod starts, the mount succeeds, and the model is
missing.

The path arrived in `c79102d9`, "feat(helm-model): select model cache backend
by storage class". A shared filesystem data-sharing probe was filed as a
follow-up at the time and never run. This is that probe, with a negative
result.

## Adding a provider

1. Run the qualification above on the exact provisioner and class.
2. Add or edit the driver entry, setting `accessModes` to what the run proved
   and nothing more.
3. Set `readerMountOptions` when NVCA creates reader PVs for that driver. The
   ROX shape requires `ro`. Vendor specific options belong here, not in code:
   `norecovery` and `nouuid` are NVMesh XFS requirements and apply to no other
   driver.
4. Regenerate the vendored chart so both catalog copies match.
5. Cite the run in the commit message, including cluster, class and hashes.

No code change should be needed. If one is, the catalog is not carrying a fact
it should carry.
