# NvSnap H100 bench clusters — Terraform

Two GKE clusters (8 × a3-megagpu-8g each, single zone, shared VPC)
that consume from the cross-project shared H100 reservation
`gsc-a3-megagpu-8g-shared-res-3` in `YOUR_RESERVATION_PROJECT`.

Used for the 16-node cascade fan-out bench (#97) and cross-cluster
checkpoint movement tests.

## Prerequisites

1. **Project ID where you have IAM**. The reservation is shared but
   you provision INTO your own project. Confirm with whoever sent
   you the reservation reference — your `you@example.com`
   account needs at minimum:
     - `roles/compute.admin` (VPC, subnets, firewalls, SAs)
     - `roles/container.admin` (GKE)
     - `roles/iam.serviceAccountAdmin` (create node SAs)
     - `roles/resourcemanager.projectIamAdmin` (bind roles)
2. **gcloud auth**:
   ```
   gcloud auth login
   gcloud auth application-default login
   gcloud config set project <your-project-id>
   ```
3. **Terraform** ≥ 1.5.0.

## Setup

```bash
cd terraform/h100-clusters
cp terraform.tfvars.example terraform.tfvars
$EDITOR terraform.tfvars      # fill in project_id

terraform init
terraform plan                # review what will be created
terraform apply               # ~15-25 min total (control plane + node pool warm-up)
```

## What gets created

| Resource | Count | Notes |
|---|---|---|
| VPC | 1 | `nvsnap-h100-vpc`, regional routing, custom subnets |
| Subnets | 2 | `/22` per cluster; non-overlapping with existing test cluster CIDRs |
| Firewall rules | 2 | Intra-VPC k8s + nvsnap endpoints (8080/8081/9000) |
| GKE clusters | 2 | Zonal, VPC-native, Workload Identity on, all relevant CSI drivers enabled |
| Node pools | 2 | 8 × a3-megagpu-8g each, gvNIC, COS_CONTAINERD, consume shared reservation, NVIDIA driver auto-installed |
| Service accounts | 2 | Per-cluster node SAs (logging/metrics/AR-reader; NOT default Compute SA) |

## Cost note

a3-megagpu-8g on-demand is ~$30/hr/VM in asia-southeast1 (last we
checked GCP pricing — verify current rates). 16 nodes × $30 ≈ **$480
per hour** if you pay on-demand. Because we're consuming a
RESERVATION, the per-VM cost is whatever the reservation contract
charges (typically a fraction of on-demand for the committed term).

The reservation is already paid for either way; running 0 VMs from
it wastes the same money as running 16. So spin up and bench.

## Post-apply

Terraform's `next_steps` output shows the `gcloud container clusters
get-credentials` commands for both clusters. After that:

1. **Verify GPU driver installed** on each cluster (GKE handles this
   via the `gpu_driver_installation_config` block; first node-ready
   is delayed ~3 min as the install runs):
   ```
   kubectl describe node <node> | grep -E 'nvidia\.com/gpu|gpu-driver-version'
   ```

2. **Deploy nvsnap** to each cluster (project root has the scripts):
   ```
   ./scripts/build-agent.sh deploy --kubeconfig=<context-A>
   ./scripts/build-agent.sh deploy --kubeconfig=<context-B>
   ```

3. **Smoke test**:
   ```
   ./scripts/test-e2e.sh vllm-small
   ```

4. **Cross-cluster cascade bench**: see `scripts/bench-cross-node-restore.sh`
   and `docs/archive/CACHING-MEETING-BRIEF.md` for what we're measuring.

## Teardown

```bash
terraform destroy
```

Reservation continues to bill regardless of whether VMs are running.
Destroying just frees the VMs back to the reservation pool.

## Known gotchas

- **CIDR conflicts**: defaults don't overlap with our existing test
  cluster (10.0.0.0/16 nodes, 192.168.0.0/16 pods). If your org has
  a wider IP-management policy, override the CIDR variables.
- **Driver install timing**: first nodes take ~3-5 min extra to
  become Ready because the NVIDIA driver DaemonSet runs before the
  node is marked schedulable. This is normal.
- **Quota even with reservation**: GCP still requires the project to
  have `NVIDIA_H100_GPUS` and `CPUS` quota at the project level even
  if the actual capacity comes from a shared reservation. If
  `terraform apply` fails on quota, request quota in the
  consumer project (usually fast for reservation-backed asks).
- **`gpcs-fuse-csi-driver-config`**: GCSFuse CSI lets us mount GCS
  buckets as PVCs. Enabled here for future cross-cluster testing;
  no operational cost if unused.
