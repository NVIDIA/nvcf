// ─── Cluster B ─────────────────────────────────────────────────────────
//
// The "newer-kernel" bench cluster. Default config provisions GKE 1.35
// on UBUNTU_CONTAINERD → Ubuntu 24.04 / kernel 6.8, which is what we
// need to exercise NvSnap's CRIU / cuda-checkpoint path on a modern
// kernel (still tracking NvSnap issue #41 — io_uring MAP_FIXED, iou-sqp
// zombie, cudaEvent_t segfault on 6.x).
//
// Two modes via tfvars:
//
//   (1) Ubuntu 24.04 / kernel 6.8 (default — exercise the newer kernel)
//         kubernetes_version_b = "1.35.3-gke.1389000"
//         release_channel_b    = "REGULAR"
//
//   (2) Mirror cluster A (Ubuntu 22.04 / kernel 5.15 — cross-cluster
//       cascade testing where both nodes run the known-good baseline)
//         kubernetes_version_b = ""   # inherit kubernetes_version
//         release_channel_b    = ""   # inherit release_channel
//
// GPU DRIVER = NVIDIA GPU Operator (NOT GKE auto-install). The node pool
// below sets gpu_driver_version="INSTALLATION_DISABLED" (see its
// gpu_driver_installation_config), handing driver lifecycle to the
// Operator. GKE's own nvidia-gpu-device-plugin DaemonSet then stays stuck
// at Init forever — EXPECTED and harmless; the Operator's driver +
// device-plugin are what actually expose nvidia.com/gpu.
//
// The Operator is NOT managed by Terraform (no helm provider here). It's
// a standalone helm release, installed out-of-band, identical on both
// clusters. EXACT recipe (verified running on cluster A + B, 2026-06-12):
//
//   helm repo add nvidia https://helm.ngc.nvidia.com/nvidia
//   helm install gpu-operator nvidia/gpu-operator \
//     --version v24.9.0 -n gpu-operator --create-namespace \
//     --set driver.enabled=true \
//     --set driver.version=580.95.05 \
//     --set toolkit.enabled=true \
//     --set toolkit.version=v1.17.8-ubuntu20.04   # cluster B ONLY — see below
//
//   # CLUSTER B DIVERGENCE FROM A: cluster B runs containerd 2.1.5, which
//   # uses config version 3. The toolkit shipped with operator v24.9.0
//   # (v1.17.0) can't parse it and crashloops with
//   # "unable to load containerd config: unsupported config version: 3",
//   # so the `nvidia` runtime is never registered and the validator can't
//   # create a sandbox ("no runtime for nvidia is configured"). Pinning
//   # toolkit >= v1.17.8 fixes it. Cluster A (older containerd, config v2)
//   # uses the operator default toolkit and does NOT need this override.
//
//   # REQUIRED on GKE — without this every Operator pod is rejected with
//   # "insufficient quota to match these scopes: [PriorityClass In
//   # [system-node-critical system-cluster-critical]]" and no GPUs ever
//   # appear. GKE only admits critical-priority pods in namespaces that
//   # have a ResourceQuota granting those scopes.
//   kubectl apply -n gpu-operator -f gpu-operator-allow-system-priorities.yaml
//   kubectl -n gpu-operator rollout restart deploy ds  # if pods were already rejected
//
// driver.version=580.95.05 is REQUIRED for cuda-checkpoint. The Operator
// auto-resolves the OS-matched driver image from the node OS, so the SAME
// command works on both — no per-cluster tag pinning:
//   cluster A (Ubuntu 22.04/k5.15) -> nvcr.io/nvidia/driver:580.95.05-ubuntu22.04
//   cluster B (Ubuntu 24.04/k6.8)  -> nvcr.io/nvidia/driver:580.95.05-ubuntu24.04
// (scripts/bringup-cluster.sh also installs it as a subchart of the
// `nvsnap` chart — equivalent values; the standalone command above is the
// minimal driver-only form.)

locals {
  k8s_version_b     = var.kubernetes_version_b != "" ? var.kubernetes_version_b : var.kubernetes_version
  release_channel_b = var.release_channel_b != "" ? var.release_channel_b : var.release_channel
}

resource "google_container_cluster" "cluster_b" {
  provider = google-beta

  name     = var.cluster_b_name
  location = var.zone

  remove_default_node_pool = true
  initial_node_count       = 1

  network    = google_compute_network.vpc.self_link
  subnetwork = google_compute_subnetwork.subnet_b.self_link

  enable_multi_networking = true
  datapath_provider       = "ADVANCED_DATAPATH"

  ip_allocation_policy {
    cluster_secondary_range_name  = "${var.cluster_b_name}-pods"
    services_secondary_range_name = "${var.cluster_b_name}-services"
  }

  release_channel {
    channel = local.release_channel_b
  }

  min_master_version = local.k8s_version_b != "" ? local.k8s_version_b : null

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
    dns_cache_config {
      enabled = true
    }
  }

  default_max_pods_per_node = 110
  networking_mode           = "VPC_NATIVE"

  resource_labels = {
    owner   = var.owner
    purpose = var.purpose
    cluster = "b"
  }

  lifecycle {
    prevent_destroy = false
  }
}

// ─── H100 GPU node pool ────────────────────────────────────────────────
//
// Identical hardware shape to pool_a (a3-megagpu-8g, NVMe RAID, gvNIC,
// shared reservation). The OS / kernel difference is driven entirely
// by the cluster's control-plane version above.

resource "google_container_node_pool" "pool_b" {
  provider = google-beta

  name     = "${var.cluster_b_name}-h100-pool"
  cluster  = google_container_cluster.cluster_b.name
  location = var.zone

  node_count = var.nodes_per_cluster

  management {
    auto_repair  = true
    auto_upgrade = true
  }

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
      # Operator, which installs R580.95.05 (required for cuda-checkpoint).
      # With LATEST, GKE pre-installs an older driver and the Operator's
      # upgrade path crashloops because it can't unload the in-use modules.
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

    host_maintenance_policy {
      maintenance_interval = "PERIODIC"
    }

    gvnic {
      enabled = true
    }

    # Same rationale as pool_a — ephemeral_storage_local_ssd_config
    # RAID0s the 16 NVMes and mounts them at /var/lib/containerd so
    # checkpoint hostPath lives on local NVMe, not pd-balanced.
    ephemeral_storage_local_ssd_config {
      local_ssd_count = 16
    }

    # Image streaming (gcfs) is COS-only — we use Ubuntu here so the
    # GPU Operator's NVCR driver image resolves. Cost: cold-start pulls
    # fully (no streaming). Tradeoff worth it.
    gcfs_config {
      enabled = false
    }

    # UBUNTU_CONTAINERD: GKE picks the underlying Ubuntu release based
    # on the cluster's control-plane version (1.31 → 22.04 / kernel 5.15,
    # 1.35+ → 24.04 / kernel 6.8). The GPU Operator helm values must
    # match — see the comment block at the top of this file.
    image_type   = "UBUNTU_CONTAINERD"
    disk_size_gb = 200
    disk_type    = "pd-balanced"

    service_account = google_service_account.node_sa_b.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]

    workload_metadata_config {
      mode = "GKE_METADATA"
    }

    labels = {
      owner        = var.owner
      purpose      = var.purpose
      cluster      = "b"
      gke-nodepool = "gpu"
    }
  }
}

// ─── CPU node pool ─────────────────────────────────────────────────────

resource "google_container_node_pool" "cpu_pool_b" {
  provider = google-beta

  name     = "${var.cluster_b_name}-cpu-pool"
  cluster  = google_container_cluster.cluster_b.name
  location = var.zone

  node_count = var.cpu_nodes_per_cluster

  management {
    auto_repair  = true
    auto_upgrade = true
  }

  node_config {
    machine_type = var.cpu_machine_type

    # Same shape as cpu_pool_a — pd-ssd / 1 TiB boot, UBUNTU_CONTAINERD
    # for consistency with the H100 pool.
    image_type   = "UBUNTU_CONTAINERD"
    disk_size_gb = 1024
    disk_type    = "pd-ssd"

    service_account = google_service_account.node_sa_b.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]

    workload_metadata_config {
      mode = "GKE_METADATA"
    }

    gcfs_config {
      enabled = true
    }

    labels = {
      owner        = var.owner
      purpose      = var.purpose
      cluster      = "b"
      gke-nodepool = "cpu"
    }
  }
}
