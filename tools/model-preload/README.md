# model-preload (ROX shared-volume prototype)

Prototype for cutting the model-download thundering herd on scale-up. Instead of
each worker pod downloading the same model from NGC (N downloads, N copies), one
Job downloads it once into a ReadWriteOnce (RWO) volume, then the same volume is
re-exposed ReadOnlyMany (ROX) and mounted read-only by every worker.

Result on scale-up: N downloads become 1 download, and N stored copies become 1.

## Why this works

`nvcf-sc` (NVMesh) uses `reclaimPolicy: Retain`, so the underlying CSI volume
survives PVC deletion. That lets a second PV point at the already-populated
volume handle in ReadOnlyMany mode. NVMesh supports read-only multi-node attach:
in production, one ROX nvcf-sc volume is already mounted by many pods across
several nodes at once.

## Usage

    export NGC_API_KEY=<nvapi-...>          # for the size lookup
    kubectl create secret generic ngc-key \
      --from-literal=NGC_API_KEY=$NGC_API_KEY -n <ns>

    python3 model-preload-rox.py \
      --model <org>/[<team>/]<model>:<version> \
      --namespace <ns> \
      --name <model-short-name> \
      --ngc-secret ngc-key \
      --image nvcr.io/nvidia/pytorch:24.10-py3

The script prints the ROX PVC name. Worker pods mount it read-only:

    volumes:
    - name: models
      persistentVolumeClaim:
        claimName: <name>-ro
        readOnly: true
    # container volumeMounts: [{name: models, mountPath: /models, readOnly: true}]

Flags: `--storage-class` (default `nvcf-sc`), `--multiplier` (default `1.1`),
`--floor-gi`, `--pull-secret`, `--csi-driver`, `--mount`, `--cli-version`.

## What the script does

1. Reads the model total size from NGC (`totalSizeInBytes`) and sizes the PVC at
   `size * multiplier`, floored.
2. Creates a RWO PVC on the storage class and a downloader Job that fetches the
   NGC CLI and runs `ngc registry model download-version` into it.
3. Waits for the Job to complete.
4. Reads the bound PV's CSI `volumeHandle`.
5. Deletes the Job and RWO PVC (Retain keeps the volume), then creates a ROX PV
   on the same handle and a ROX PVC bound to it.
6. Prints the ROX PVC name for workers to mount.

## Design considerations before productionizing

This is a prototype, not a controller. Known gaps:

- Read bandwidth. N readers share one volume's read path. Fine for load-once
  weight reads; measure before assuming it scales to large N.
- Immutability and new versions. The ROX PV is `Retain`. A new model version
  needs a new preload run and a new ROX PVC; old volumes must be garbage
  collected explicitly (Retain does not reclaim them).
- Orchestration and timing. Workers must not start until the ROX PVC is Bound.
  A real version would gate the worker rollout on preload completion (init
  container, Job dependency, or an operator), not manual ordering.
- Concurrency. No lock guards two preload runs for the same model racing on the
  same name. Run one at a time per model, or add a lease.
- Failure handling. A failed download leaves a RWO PVC behind; clean up before
  retry. The script does not roll back partial state.

## Cleanup

    kubectl -n <ns> delete pvc <name>-ro
    kubectl delete pv <name>-ro-pv        # then reclaim the NVMesh volume out of band
