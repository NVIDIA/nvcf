# NCP Environment Prerequisite Check

`check-ncp-env-prereqs.sh` checks whether the local workstation and selected
Kubernetes cluster have the tools and cluster-level components expected before
installing or validating a self-managed NVCF/NCP environment.

---

#### Testing Environment
- BCM + K8S cluster provision by NCP
  
----

Run it from the repository root:

```bash
./tools/scripts/check-ncp-env-prereqs/check-ncp-env-prereqs.sh
```

To check a specific Kubernetes context:

```bash
./tools/scripts/check-ncp-env-prereqs/check-ncp-env-prereqs.sh --context <context-name>
```

To check only local workstation tools:

```bash
./tools/scripts/check-ncp-env-prereqs/check-ncp-env-prereqs.sh --skip-cluster-checks
```

## What It Checks

Local tools:

- `kubectl`
- `helm`
- `helmfile`
- `helm-diff`
- `ngc`

Kubernetes cluster checks:

- Cluster connectivity through `kubectl`
- Container runtime on the first node
- CNI detection for common Calico installations
- MetalLB pods
- MetalLB `IPAddressPool` address ranges
- Common alternative load balancers
- NVIDIA Network Operator pods
- `NicClusterPolicy`
- NVIDIA GPU Operator pods
- GPU Operator `ClusterPolicy`
- GPU node capacity through `nvidia.com/gpu`

## Output Status

Each check prints one of three statuses:

- `PASS`: The required tool or cluster component was found.
- `WARN`: The item may be optional, environment-specific, or not fully
  configured.
- `FAIL`: A required prerequisite is missing or the cluster cannot be reached.

The script exits with:

- `0` when there are no failed checks.
- `1` when one or more checks fail.
- `2` for invalid command-line arguments.

## Notes

Calico and the NVIDIA Network Operator are checked separately. Calico confirms
that the cluster has a detected CNI. It does not replace NVIDIA Network Operator
configuration.

`NicClusterPolicy` is an NVIDIA Network Operator custom resource. If the
Network Operator is installed but `NicClusterPolicy` is missing, the operator may
not be configured to manage NVIDIA networking features such as RDMA, SR-IOV, or
NVIDIA NIC-related device plugins. This is reported as a warning because some
environments may not require those features.

MetalLB IP ranges are read from `IPAddressPool` resources in the
`metallb-system` namespace. The output shows only the configured address ranges,
for example:

```text
PASS  MetalLB IP ranges        7.247.223.200-7.247.223.220
```

## Example Passing Output

```text
============================================================
  NVCF prerequisite check
============================================================

  PASS  kubectl                  v1.32.11
  PASS  helm                     v3.18.3
  PASS  helmfile                 1.1.3
  PASS  helm-diff                3.15.8
  PASS  ngc-cli                  vunknown
  PASS  cluster connectivity     Kubernetes control plane is running at https://127.0.0.1:10443
  PASS  container runtime        containerd://1.7.27
  PASS  CNI                      Calico detected (calico-node pods)
  PASS  MetalLB                  12/12 pods Running
  PASS  MetalLB IP ranges        7.247.223.200-7.247.223.220
  PASS  other load balancers     none detected besides MetalLB
  PASS  network-operator         27/28 pods Running; chart network-operator-25.7.0
  WARN  NicClusterPolicy         not found
  PASS  gpu-operator             60/69 pods Running; chart gpu-operator-v25.3.3
  PASS  ClusterPolicy            cluster-policy
  PASS  GPU nodes                nvidia-h100-worker001(8gpu), nvidia-h100-worker002(8gpu), nvidia-h100-worker003(8gpu), nvidia-h100-worker004(8gpu), nvidia-h100-worker006(8gpu), nvidia-h100-worker007(8gpu), nvidia-h100-worker008(8gpu), nvidia-h100-worker009(8gpu), nvidia-h100-worker010(8gpu)

============================================================
  Summary: 15 passed, 1 warned, 0 failed
============================================================
  PASS  kubectl                  v1.32.11
  PASS  helm                     v3.18.3
  PASS  helmfile                 1.1.3
  PASS  helm-diff                3.15.8
  PASS  ngc-cli                  vunknown
  PASS  cluster connectivity     Kubernetes control plane is running at https://127.0.0.1:10443
  PASS  container runtime        containerd://1.7.27
  PASS  CNI                      Calico detected (calico-node pods)
  PASS  MetalLB                  12/12 pods Running
  PASS  MetalLB IP ranges        7.247.223.200-7.247.223.220
  PASS  other load balancers     none detected besides MetalLB
  PASS  network-operator         27/28 pods Running; chart network-operator-25.7.0
  WARN  NicClusterPolicy         not found
  PASS  gpu-operator             60/69 pods Running; chart gpu-operator-v25.3.3
  PASS  ClusterPolicy            cluster-policy
  PASS  GPU nodes                nvidia-h100-worker001(8gpu), nvidia-h100-worker002(8gpu), nvidia-h100-worker003(8gpu), nvidia-h100-worker004(8gpu), nvidia-h100-worker006(8gpu), nvidia-h100-worker007(8gpu), nvidia-h100-worker008(8gpu), nvidia-h100-worker009(8gpu), nvidia-h100-worker010(8gpu)
============================================================
```

## Common Failures

If cluster connectivity fails, verify `KUBECONFIG`, the selected context, VPN
access, and Kubernetes API reachability:

```bash
kubectl cluster-info
```

If MetalLB is missing, install or configure a supported load balancer before
deploying components that require `LoadBalancer` services:

```text
FAIL  MetalLB                  metallb-system namespace not found
```

If MetalLB is installed but no IP ranges are found, verify `IPAddressPool`
resources:

```bash
kubectl get ipaddresspools.metallb.io -n metallb-system
```

If GPU nodes are not detected, verify the GPU Operator installation and node
capacity:

```bash
kubectl get nodes -o custom-columns='NAME:.metadata.name,GPU:.status.capacity.nvidia\.com/gpu'
```
