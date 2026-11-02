# NvSnap H100 bench clusters — Terraform

Two GKE clusters (8 × a3-megagpu-8g each, single zone, shared VPC)
that consume from a cross-project shared H100 reservation
(`reservation_project_id` and `zone` in `variables.tf`).

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

   ```bash
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
terraform apply
```

## What gets created

| Resource | Count | Notes |
|---|---|---|
| VPC | 1 + 8 | `nvsnap-h100-vpc` (`main.tf`), plus eight GPU networks (`gpu_networks.tf`) |
| Subnets | 2 + 8 | `/22` per cluster (`subnet_a_cidr`, `subnet_b_cidr`), plus one `/24` per GPU network |
| Firewall rules | 2 + 8 | Intra-VPC Kubernetes and nvsnap endpoints, plus one per GPU network |
| GKE clusters | 2 | Zonal (`cluster_a.tf`, `cluster_b.tf`) |
| Node pools | 4 | Per cluster: 8 × a3-megagpu-8g on the shared reservation, gvNIC, `UBUNTU_CONTAINERD`, GKE driver install disabled; and a CPU pool |
| Service accounts | 2 | Per-cluster node SAs (`main.tf`) |

The NVIDIA driver is not installed by GKE or by Terraform: the GPU node pools
set `gpu_driver_version = "INSTALLATION_DISABLED"`, and the NVIDIA GPU
Operator, installed separately with Helm, provides the driver and the device
plugin (`cluster_b.tf`, header comment).

## Cost note

Check current GCP pricing for a3-megagpu-8g. The clusters consume a
reservation, so the VMs are billed under the reservation's terms whether or
not they run.

## Post-apply

Terraform's `next_steps` output shows the `gcloud container clusters
get-credentials` commands for both clusters. After that:

1. **Install the GPU Operator** on each cluster (the recipe is in the header
   comment of `cluster_b.tf`), then check that the nodes expose GPUs:

   ```bash
   kubectl describe node <node> | grep -E 'nvidia\.com/gpu'
   ```

2. **Install nvsnap** on each cluster with the Helm chart
   ([docs/operate/INSTALL.md](../../docs/operate/INSTALL.md)), with
   `--set gpu-operator.enabled=false` since the Operator is already there.

3. **Smoke test**:

   ```bash
   ./scripts/test-e2e.sh vllm-small
   ```

4. **Cross-cluster cascade bench**: see `scripts/bench-cross-node-restore.sh`.

## Teardown

```bash
terraform destroy
```

Reservation continues to bill regardless of whether VMs are running.
Destroying just frees the VMs back to the reservation pool.

## Known gotchas

- **CIDR conflicts**: check the defaults in `variables.tf` against the
  networks your project already uses, and override the CIDR variables
  if they overlap.
- **Quota even with reservation**: GCP still requires the project to
  have `NVIDIA_H100_GPUS` and `CPUS` quota at the project level even
  if the actual capacity comes from a shared reservation. If
  `terraform apply` fails on quota, request quota in the
  consumer project (usually fast for reservation-backed asks).
- **`gpcs-fuse-csi-driver-config`**: GCSFuse CSI lets us mount GCS
  buckets as PVCs. Enabled here for future cross-cluster testing;
  no operational cost if unused.
