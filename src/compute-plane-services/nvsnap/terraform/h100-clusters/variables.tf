// ─── What you fill in ───────────────────────────────────────────────────

variable "project_id" {
  description = <<-EOT
    GCP project where the GKE clusters and VPC will be created. NOT the
    reservation owner project. We provision INTO YOUR_GPU_PROJECT
    (display name SIS-GCP-STAGE) — access is granted via the
    SIS-GCP-STAGE group. The reservation we consume lives in a separate
    project (YOUR_RESERVATION_PROJECT); see reservation_project_id.
  EOT
  type        = string
}

variable "region" {
  description = "Region. Reservation is in asia-southeast1; cluster must match."
  type        = string
  default     = "asia-southeast1"
}

variable "zone" {
  description = <<-EOT
    Zone where node VMs run. MUST match the reservation's zone. The
    shared reservation gsc-a3-megagpu-8g-shared-res-3 lives in
    asia-southeast1-c.
  EOT
  type        = string
  default     = "asia-southeast1-c"
}

// ─── Reservation pointer (shared from owner project) ────────────────────

variable "reservation_project_id" {
  description = "Project that OWNS the shared H100 reservation."
  type        = string
  default     = "YOUR_RESERVATION_PROJECT"
}

variable "reservation_name" {
  description = "Name of the shared reservation we consume."
  type        = string
  default     = "gsc-a3-megagpu-8g-shared-res-3"
}

// ─── Cluster shape ──────────────────────────────────────────────────────

variable "cluster_a_name" {
  description = "Name of the first cluster (capture-side)."
  type        = string
  default     = "GCP-H100-a"
}

variable "cluster_b_name" {
  description = "Name of the second cluster (restore-side for cross-cluster tests)."
  type        = string
  default     = "GCP-H100-b"
}

variable "nodes_per_cluster" {
  description = "Number of a3-megagpu-8g nodes per cluster. 16 total / 2 clusters = 8 each."
  type        = number
  default     = 8
}

variable "machine_type" {
  description = "Compute machine type. MUST match the reservation."
  type        = string
  default     = "a3-megagpu-8g"
}

// ─── CPU pool (matches NVCF pattern: n2d-standard-64 × 4) ──────────────

variable "cpu_machine_type" {
  description = <<-EOT
    Machine type for the CPU node pool. n2d-standard-16 (16 vCPU, 64 GiB)
    is sized for our bench workload — GKE system + nvsnap-server +
    nvsnap-blobstore + optional Prometheus, with comfortable headroom.
    NVCF prod (usc1-prd3, azne1-prd7) uses n2d-standard-64 for
    multi-tenant / long-retention observability reasons that don't
    apply to a 6-week bench. Bump back up if the workload changes.
  EOT
  type        = string
  default     = "n2d-standard-16"
}

variable "cpu_nodes_per_cluster" {
  description = <<-EOT
    Number of CPU nodes per cluster. 2 provides HA for kube-system pods
    while keeping the cost reasonable for a bench cluster.
  EOT
  type        = number
  default     = 2
}

// ─── Networking ─────────────────────────────────────────────────────────

variable "vpc_name" {
  description = "Name of the shared VPC the two clusters share."
  type        = string
  default     = "nvsnap-h100-vpc"
}

variable "subnet_a_cidr" {
  description = "Primary subnet CIDR for cluster A's nodes."
  type        = string
  default     = "10.40.0.0/22"
}

variable "subnet_b_cidr" {
  description = "Primary subnet CIDR for cluster B's nodes."
  type        = string
  default     = "10.44.0.0/22"
}

variable "pods_a_cidr" {
  description = "Secondary CIDR for cluster A's pods."
  type        = string
  default     = "10.48.0.0/16"
}

variable "pods_b_cidr" {
  description = "Secondary CIDR for cluster B's pods."
  type        = string
  default     = "10.52.0.0/16"
}

variable "services_a_cidr" {
  description = "Secondary CIDR for cluster A's services."
  type        = string
  default     = "10.56.0.0/20"
}

variable "services_b_cidr" {
  description = "Secondary CIDR for cluster B's services."
  type        = string
  default     = "10.56.16.0/20"
}

// ─── GKE control plane ──────────────────────────────────────────────────
//
// kubernetes_version + release_channel are the shared defaults applied
// to both clusters. Each cluster has a *_a / *_b override variable
// below — when an override is non-empty it wins for that cluster,
// when empty the shared default is used. This lets cluster B run a
// different K8s minor (e.g. 1.35 / Ubuntu 24.04 / kernel 6.8) while
// cluster A stays on the 1.31 / Ubuntu 22.04 / kernel 5.15 baseline
// known to be safe for the NvSnap CRIU + GPU Operator R580.95.05
// contract (NvSnap issue #41).

variable "kubernetes_version" {
  description = <<-EOT
    Shared GKE control plane minor version. Leave blank to let GKE pick
    the release-channel default. Per-cluster override available via
    kubernetes_version_a / kubernetes_version_b.
  EOT
  type        = string
  default     = ""
}

variable "release_channel" {
  description = "Shared GKE release channel: RAPID / REGULAR / STABLE / EXTENDED. Override per cluster with release_channel_a / release_channel_b."
  type        = string
  default     = "REGULAR"
}

variable "kubernetes_version_a" {
  description = <<-EOT
    Cluster A's GKE control plane minor version. Empty inherits
    kubernetes_version. Cluster A's baseline is 1.31.14-gke.1942000
    (Ubuntu 22.04 / kernel 5.15) — do NOT bump past 1.31 without
    closing NvSnap issue #41 first.
  EOT
  type        = string
  default     = ""
}

variable "kubernetes_version_b" {
  description = <<-EOT
    Cluster B's GKE control plane minor version. Empty inherits
    kubernetes_version. Set to a 1.35+ patch (e.g. "1.35.3-gke.1389000")
    to opt this cluster into Ubuntu 24.04 / kernel 6.8. Set to the
    same value as cluster A to mirror it for cross-cluster cascade
    testing.
  EOT
  type        = string
  default     = ""
}

variable "release_channel_a" {
  description = "Cluster A's release channel. Empty inherits release_channel."
  type        = string
  default     = ""
}

variable "release_channel_b" {
  description = "Cluster B's release channel. Empty inherits release_channel. Use REGULAR for 1.35+ (EXTENDED tops out at 1.31)."
  type        = string
  default     = ""
}

// ─── Labels / tags ──────────────────────────────────────────────────────

variable "owner" {
  description = <<-EOT
    Owner identifier used in resource labels. GCP labels reject '@' and '.'
    so this is the local-part of the email, not the full address. (The full
    address is you@example.com.)
  EOT
  type        = string
  default     = "owner"
}

variable "purpose" {
  description = "Cluster purpose label."
  type        = string
  default     = "nvsnap-bench"
}
