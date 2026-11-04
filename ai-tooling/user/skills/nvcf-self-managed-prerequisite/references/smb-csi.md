# SMB CSI driver — install detail

Reference for the SMB CSI step in `nvcf-self-managed-prerequisite/SKILL.md`.

## What the SMB CSI driver is

`csi-driver-smb` provisions Kubernetes PersistentVolumes backed by SMB (Samba) shares. The NVCA operator's `selfManaged.sharedStorage.imageRepository` runs Samba pods inside the cluster that export file shares; those shares are surfaced to NVCF function workload pods (in the `nvcf-backend` namespace) as PVCs via this CSI driver.

Without the driver installed, the NVCA shared-storage PVCs stay `Pending` and stage-14 function deploys fail with PV-binding errors.

## Version pin

The driver version tracks what NVCF validates against. At the time of writing, the pinned chart version is `v1.17.0`. csi-driver-smb is an upstream chart (not an NVCF image), so it is not in `manifest.yaml`; check the NVCF cluster-management docs for the current validated version before installing.

## Install — upstream helm chart (recommended for any cloud)

This is the cloud-neutral path. Same command on AKS, EKS, GKE, k3d, and MicroK8s.

```bash
helm repo add csi-driver-smb \
  https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts
helm repo update

helm install csi-driver-smb csi-driver-smb/csi-driver-smb \
  -n kube-system \
  --version v1.17.0 \
  --wait --timeout 5m
```

Verify the CSIDriver resource is registered:

```bash
kubectl get csidriver smb.csi.k8s.io
```

Verify the controller and node pods are Running:

```bash
kubectl get pods -n kube-system -l app.kubernetes.io/name=csi-driver-smb
# Expect: 1 controller pod + 1 node pod per cluster node, all Running.
```

## AKS-specific alternative — managed add-on

AKS offers a managed `csi-driver-smb` add-on bundled with the blob driver. Either path works; the upstream helm chart above is portable across clouds and avoids needing the Azure CLI.

To enable the managed add-on instead:

```bash
az aks update --resource-group "$RG" --name "$CLUSTER_NAME" --enable-blob-driver
# Note: as of Azure CLI 2.x the SMB driver flag is bundled with the blob driver.
```

After enabling, verify the same way:

```bash
kubectl get csidriver smb.csi.k8s.io
```

## Failure modes

| Symptom | Cause | Fix |
| ------- | ----- | --- |
| NVCA `sharedStorage` PVCs stuck `Pending` after NVCA install | SMB CSI driver not installed before NVCA | Install via the helm chart above, then `kubectl delete pvc -n nvca-operator <pending-pvc>` and let NVCA recreate it |
| `kubectl get csidriver smb.csi.k8s.io` returns NotFound after helm install | Helm install succeeded but CSIDriver CRD didn't apply | Re-run helm with `--wait --timeout 5m`; check `kubectl get pods -n kube-system -l app.kubernetes.io/name=csi-driver-smb` for errors |
| AKS managed add-on enable fails with "Operation not allowed in current cluster state" | Cluster is mid-upgrade or has an in-flight operation | Wait for the in-flight op to complete, then retry `az aks update --enable-blob-driver` |

## Uninstall

Upstream helm chart path:

```bash
helm uninstall csi-driver-smb -n kube-system
```

AKS managed add-on:

```bash
az aks update --resource-group "$RG" --name "$CLUSTER_NAME" --disable-blob-driver
```

## References

- [csi-driver-smb upstream](https://github.com/kubernetes-csi/csi-driver-smb)
- [Azure blob CSI driver docs](https://learn.microsoft.com/en-us/azure/aks/azure-blob-csi)
