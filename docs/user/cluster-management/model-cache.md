# Helm Model Caching

The NVIDIA Cloud Functions Agent (NVCA) can cache model and resource artifacts
for Helm-based functions and tasks. The cache works like shared storage for
large model and resource files. The first workload downloads an artifact set,
and later workloads can mount the populated cache instead of downloading
another copy.

This page describes the Helm model cache behavior in NVCA 3.2.5.

## Before You Begin

Model caching requires both of these conditions:

- The function or task includes model or resource artifacts, and the control
  plane requests caching for them.
- The `CachingSupport` cluster feature is enabled. See
  [Caching Support](./configuration.md#caching-support).

Durable reuse across workloads also requires a shared storage backend. If no
shared backend is available, NVCA uses a pod-local `emptyDir` volume. See
[Backend Selection](#backend-selection).

For the NVCA-managed Samba backend, the cluster must also have:

- The `nvcf-sc` StorageClass.
- The SMB CSI driver.
- The `HelmSharedStorage` feature flag enabled. See
  [Enable Helm Shared Storage](./configuration.md#enable-helm-shared-storage).

## How the Cache Works

For a requested cache, the control plane sends NVCA a
`CacheLaunchSpecification` with `cacheArtifacts=true`, a positive `cacheSize`,
and a `cacheHandle`. The handle identifies the complete artifact set. Functions
or tasks that request the same artifacts should receive the same handle.

For a durable backend, the flow is:

```text
Function A ----> writer Job ----> populated cache for one cacheHandle
                                         |
Function A <---- read-only attachment ----+
Function B <---- read-only attachment ----+
```

1. NVCA selects the first available storage backend.
1. NVCA creates a model-cache `StorageRequest` in the workload namespace.
1. A Kubernetes Lease selects one request as the writer for the cache handle.
1. NVCA creates one writer Job in the `nvca-modelcache-init` namespace.
1. The writer downloads the artifacts to writable storage and exits.
1. NVCA records durable cluster state that marks the cache as populated.
1. NVCA creates a namespace-local, read-only attachment for the workload.
1. A later request with the same cache handle reuses the populated cache and
   skips the writer Job.

The durable object and its populated marker are the source of truth for reuse.
Metrics report cache activity, but they do not determine whether a cache is
ready.

## Backend Selection

NVCA uses the first matching backend in this order:

| Priority | Cluster condition | Backend | Reuse behavior |
| --- | --- | --- | --- |
| 1 | `nvcf-sc-30` exists | NVMesh | Durable reuse across namespaces |
| 2 | `nvcf-miniservice-sc` exists | Operator-provided shared filesystem | Durable reuse across namespaces |
| 3 | `HelmSharedStorage` is enabled | NVCA-managed Samba | Durable reuse across namespaces |
| 4 | No shared backend is available | `emptyDir` | Pod-local caching only |

NVCA does not create `nvcf-miniservice-sc`. If you provide this StorageClass,
separate claims must expose the same underlying cached data. A provisioner that
creates an isolated directory, access point, or subvolume for every claim does
not provide cross-namespace reuse through this backend.

The Samba backend creates a separate Samba server and `nvcf-sc` backing volume
for each cache handle. Readers mount the same SMB share with read-only
credentials.

## Workload Mounts

NVCA adds one `model-data` volume to the Helm workload and mounts it at:

- `/config/models`
- `/config/resources`

Each workload namespace receives its own read-only claim or attachment. The
attachment mechanism depends on the backend, but the paths inside the workload
do not change:

- NVMesh readers use namespace-specific secondary volume handles derived from
  the same primary volume.
- Shared-filesystem readers use separate claims that expose the same cached
  data.
- Samba readers mount the same SMB share.

## Pod-Local Fallback

When no durable shared backend is available, each pod receives:

- A writable `emptyDir` volume.
- A `model-cache-init` init container that downloads the artifacts.
- The same model and resource mounts used by durable backends.

This fallback makes the artifacts available during that pod's startup. It does
not reuse downloaded data across pods or namespaces.

The `nvcf-miniservice-metadata` ConfigMap carries the init-container environment
to the NVCA webhook. The ConfigMap is transport metadata. Its presence does not
enable caching for a workload.

## Workloads Without Cached Artifacts

Enabling `CachingSupport` does not make every Helm workload use a model cache.
When `cacheArtifacts=false`, NVCA does not add:

- A model-cache `StorageRequest`.
- A `model-data` volume or mount.
- A model-cache init container.
- Model-cache init metadata.

An unused cache handle or a rounded cache size in the launch data does not, by
itself, mean that the workload requested caching.

## Failure Behavior

Model caching is best-effort. If the writer Job or a storage operation fails,
NVCA marks the model-cache `StorageRequest` as failed and reports the
`CacheSuccessful=False` MiniService condition with the `CachingFailed` reason.
NVCA then stops waiting for the cache and installs the Helm workload without the
cache attachment.

A cache failure can increase startup time or leave cached artifacts unavailable,
but it does not block installation of the Helm workload.

## Verify Model Cache Resources

List model-cache requests across workload namespaces:

```bash
kubectl get storagerequests.nvca.nvcf.nvidia.io --all-namespaces
```

Inspect the writer, coordination, and durable storage resources:

```bash
kubectl get jobs,leases,pvc -n nvca-modelcache-init
```

For durable reuse, verify that later workloads with the same cache handle do not
create another writer Job and that each workload receives a read-only
attachment.
