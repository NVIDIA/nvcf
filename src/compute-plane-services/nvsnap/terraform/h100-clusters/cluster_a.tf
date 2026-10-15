// ─── Cluster A ─────────────────────────────────────────────────────────
//
// The capture-side bench cluster. Pinned to GKE 1.31 / Ubuntu 22.04 /
// kernel 5.15 because that's the only OS where NvSnap's CRIU + GPU
// Operator R580.95.05 contract is currently known-good (NvSnap issue #41
// is still open on kernel 6.x). Cross-cluster cascade testing pairs
// this cluster with cluster B.
//
// To deviate from the global kubernetes_version / release_channel, set
// kubernetes_version_a + release_channel_a in tfvars. Empty values
// inherit the globals.

locals {
  // Per-cluster override → fall back to the global default when blank.
  k8s_version_a     = var.kubernetes_version_a != "" ? var.kubernetes_version_a : var.kubernetes_version
  release_channel_a = var.release_channel_a != "" ? var.release_channel_a : var.release_channel
}

resource "google_container_cluster" "cluster_a" {
  provider = google-beta

  name     = var.cluster_a_name
  location = var.zone

  // Use a custom node pool below; the default pool gets removed
  remove_default_node_pool = true
  initial_node_count       = 1

  network    = google_compute_network.vpc.self_link
  subnetwork = google_compute_subnetwork.subnet_a.self_link

  // Enable multi-networking so the node pool can attach the 8 GPU
  // NIC networks (TCPXO requirement). Dataplane V2 is required for
  // multi-networking and gives eBPF policy.
  enable_multi_networking = true
  datapath_provider       = "ADVANCED_DATAPATH"

  ip_allocation_policy {
    cluster_secondary_range_name  = "${var.cluster_a_name}-pods"
    services_secondary_range_name = "${var.cluster_a_name}-services"
  }

  release_channel {
    channel = local.release_channel_a
  }

  min_master_version = local.k8s_version_a != "" ? local.k8s_version_a : null

  workload_identity_config {
    workload_pool = "${var.project_id}.svc.id.goog"
  }

  addons_config {
    gce_persistent_disk_csi_driver_config {
      enabled = true
    }
    gcp_filestore_csi_driver_config {
      enabled = true
    }
    gcs_fuse_csi_driver_config {
      enabled = true
    }
    network_policy_config {
      // DPv2 provides eBPF-based NetworkPolicy natively; this Calico-based
      // addon must be disabled when datapath_provider = ADVANCED_DATAPATH.
      disabled = true
    }
    // DNS cache reduces CoreDNS load under fan-out workloads.
    dns_cache_config {
      enabled = true
    }
  }

  default_max_pods_per_node = 110
  networking_mode           = "VPC_NATIVE"

  resource_labels = {
    owner   = var.owner
    purpose = var.purpose
    cluster = "a"
  }

  lifecycle {
    prevent_destroy = false
  }
}

// ─── H100 GPU node pool ────────────────────────────────────────────────
//
// Critical bits (shared with pool_b):
//   - machine_type = a3-megagpu-8g (8× H100 80GB per VM, gvNIC stack)
//   - reservation_affinity points at the cross-project shared reservation
//   - gvnic enabled (matches our existing test cluster's interface)
//   - 16× local NVMe SSDs RAID0'd at /var/lib/containerd via
//     ephemeral_storage_local_ssd_config (~6 GB/s for nvsnap checkpoints)
//   - GPU driver lifecycle handed to the NVIDIA GPU Operator (Helm)

resource "google_container_node_pool" "pool_a" {
  provider = google-beta

  name     = "${var.cluster_a_name}-h100-pool"
  cluster  = google_container_cluster.cluster_a.name
  location = var.zone

  node_count = var.nodes_per_cluster

  management {
    auto_repair  = true
    auto_upgrade = true
  }

  // Compact placement is inherited from the shared reservation's own
  // resource policy — we cannot attach a different one. See the
  // comment block in gpu_networks.tf.

  // 8 additional GPU NIC networks attached per node (TCPXO).
  network_config {
    dynamic "additional_node_network_configs" {
      for_each = range(8)
      content {
        network    = google_compute_network.gpu[additional_node_network_configs.value].name
        subnetwork = google_compute_subnetwork.gpu[additional_node_network_configs.value].name
      }
    }
  }

  node_config {
    machine_type = var.machine_type

    guest_accelerator {
      type  = "nvidia-h100-mega-80gb"
      count = 8

      # INSTALLATION_DISABLED hands driver lifecycle to the NVIDIA GPU
      # Operator (R580.95.05, required for cuda-checkpoint). With
      # LATEST, GKE pre-installs an older driver and the Operator's
      # upgrade path crashloops because it can't unload the in-use
      # modules.
      gpu_driver_installation_config {
        gpu_driver_version = "INSTALLATION_DISABLED"
      }
    }

    reservation_affinity {
      consume_reservation_type = "SPECIFIC_RESERVATION"
      key                      = "compute.googleapis.com/reservation-name"
      values = [
        "projects/${var.reservation_project_id}/reservations/${var.reservation_name}",
      ]
    }

    // Maintenance window MUST match the reservation. The shared
    // reservation is set to PERIODIC (predictable maintenance windows);
    // instances default to UNSPECIFIED, which the reservation rejects.
    host_maintenance_policy {
      maintenance_interval = "PERIODIC"
    }

    gvnic {
      enabled = true
    }

    // 16× 375 GB local NVMe SSDs bundled into a3-megagpu-8g — 6 TB
    // raw. `ephemeral_storage_local_ssd_config` tells GKE to RAID0 +
    // ext4-format them at boot and mount the result at
    // /var/lib/kubelet/pods + /var/lib/containerd, so the nvsnap agent's
    // hostPath `/var/lib/containerd/nvsnap-checkpoints` lands on NVMe
    // automatically (~6 GB/s aggregate vs pd-balanced's ~140 MB/s).
    ephemeral_storage_local_ssd_config {
      local_ssd_count = 16
    }

    // Image streaming (gcfs) is COS-only — we use Ubuntu here so the
    // GPU Operator's NVCR driver image (-ubuntu22.04) resolves.
    gcfs_config {
      enabled = false
    }

    # UBUNTU_CONTAINERD on GKE 1.31 = Ubuntu 22.04 / kernel 5.15. The
    # GPU Operator pulls `nvcr.io/nvidia/driver:<ver>-ubuntu22.04`;
    # NVCR doesn't publish COS-targeted driver builds for R580.95.05.
    image_type   = "UBUNTU_CONTAINERD"
    disk_size_gb = 200
    disk_type    = "pd-balanced"

    service_account = google_service_account.node_sa_a.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]

    workload_metadata_config {
      mode = "GKE_METADATA"
    }

    labels = {
      owner        = var.owner
      purpose      = var.purpose
      cluster      = "a"
      gke-nodepool = "gpu" // matches NVCF convention
    }
  }
}

// ─── CPU node pool ─────────────────────────────────────────────────────
//
// Hosts kube-system, nvsnap-server, nvsnap-blobstore, observability stack,
// and bench drivers. GPU pool gets the automatic nvidia.com/gpu taint
// from GKE so general pods stay off the H100 nodes by default.

resource "google_container_node_pool" "cpu_pool_a" {
  provider = google-beta

  name     = "${var.cluster_a_name}-cpu-pool"
  cluster  = google_container_cluster.cluster_a.name
  location = var.zone

  node_count = var.cpu_nodes_per_cluster

  management {
    auto_repair  = true
    auto_upgrade = true
  }

  node_config {
    machine_type = var.cpu_machine_type

    # Best-disks-everywhere directive (2026-06-01): boot disk on
    # pd-ssd at 1 TiB, not the 200-GB pd-balanced default. Faster
    # image pulls (esp. the multi-GB observability stack), more
    # headroom for emptyDir scratch under nvsnap-server / blobstore.
    #
    # image_type = UBUNTU_CONTAINERD: kept consistent with the H100
    # pool (kernel 5.15 baseline for the NvSnap CRIU + GPU Operator
    # contract). COS would diverge between pool types and force two
    # different driver-install paths.
    image_type   = "UBUNTU_CONTAINERD"
    disk_size_gb = 1024
    disk_type    = "pd-ssd"

    service_account = google_service_account.node_sa_a.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]

    workload_metadata_config {
      mode = "GKE_METADATA"
    }

    // Image streaming for the CPU pool — speeds up nvsnap-server,
    // blobstore, and any observability image pulls.
    gcfs_config {
      enabled = true
    }

    labels = {
      owner        = var.owner
      purpose      = var.purpose
      cluster      = "a"
      gke-nodepool = "cpu"
    }
  }
}
