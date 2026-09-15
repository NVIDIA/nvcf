// ─── Shared infrastructure ─────────────────────────────────────────────
//
// VPC + subnets + firewall + per-cluster service accounts. Everything
// in this file is shared between cluster A and cluster B. The clusters
// themselves and their node pools live in cluster_a.tf / cluster_b.tf
// so each can evolve independently (different K8s versions, OS,
// release channels) without touching the other.
//
// One VPC, two subnets (one per cluster). Same-VPC keeps cross-cluster
// traffic on Google's internal fabric — no public-IP path, no NAT,
// minimal latency. Each cluster gets its own /22 primary CIDR for
// nodes plus two secondary ranges (pods, services) for VPC-native
// (alias-IP) addressing.

resource "google_compute_network" "vpc" {
  name                    = var.vpc_name
  auto_create_subnetworks = false
  routing_mode            = "REGIONAL"

  description = "NvSnap H100 bench: shared VPC for cluster A and B (cross-cluster cascade testing)."
}

resource "google_compute_subnetwork" "subnet_a" {
  name          = "${var.cluster_a_name}-nodes"
  region        = var.region
  network       = google_compute_network.vpc.self_link
  ip_cidr_range = var.subnet_a_cidr

  private_ip_google_access = true

  secondary_ip_range {
    range_name    = "${var.cluster_a_name}-pods"
    ip_cidr_range = var.pods_a_cidr
  }
  secondary_ip_range {
    range_name    = "${var.cluster_a_name}-services"
    ip_cidr_range = var.services_a_cidr
  }
}

resource "google_compute_subnetwork" "subnet_b" {
  name          = "${var.cluster_b_name}-nodes"
  region        = var.region
  network       = google_compute_network.vpc.self_link
  ip_cidr_range = var.subnet_b_cidr

  private_ip_google_access = true

  secondary_ip_range {
    range_name    = "${var.cluster_b_name}-pods"
    ip_cidr_range = var.pods_b_cidr
  }
  secondary_ip_range {
    range_name    = "${var.cluster_b_name}-services"
    ip_cidr_range = var.services_b_cidr
  }
}

// Allow cluster-to-cluster traffic on nvsnap's ports (agent peer 8081,
// nvsnap-server 8080, blobstore 9000). Without these, the cross-cluster
// cascade can't work even though the nodes are in the same VPC.
resource "google_compute_firewall" "nvsnap_intra_vpc" {
  name    = "${var.vpc_name}-nvsnap-intra"
  network = google_compute_network.vpc.self_link

  description = "NvSnap agent peer + server + blobstore endpoints (intra-VPC)."

  source_ranges = [
    var.subnet_a_cidr,
    var.subnet_b_cidr,
    var.pods_a_cidr,
    var.pods_b_cidr,
  ]

  allow {
    protocol = "tcp"
    ports    = ["8080", "8081", "9000"]
  }
}

// Allow node-to-node TCP for kubelet, NodePort services, NCCL etc.
// Conservative — restrict to within-VPC.
resource "google_compute_firewall" "k8s_intra_vpc" {
  name    = "${var.vpc_name}-k8s-intra"
  network = google_compute_network.vpc.self_link

  description = "K8s + NCCL intra-VPC traffic."

  source_ranges = [
    var.subnet_a_cidr,
    var.subnet_b_cidr,
    var.pods_a_cidr,
    var.pods_b_cidr,
  ]

  allow {
    protocol = "tcp"
    ports    = ["0-65535"]
  }
  allow {
    protocol = "udp"
    ports    = ["0-65535"]
  }
  allow {
    protocol = "icmp"
  }
}

// ─── Per-cluster GKE service accounts ──────────────────────────────────
//
// Each cluster's nodes run under a dedicated SA scoped to just what
// the node needs (image pull, logging, metrics). Avoid the default
// Compute SA which has broad project-editor scopes.

resource "google_service_account" "node_sa_a" {
  account_id   = "${var.cluster_a_name}-node"
  display_name = "GKE node SA for ${var.cluster_a_name}"
}

resource "google_service_account" "node_sa_b" {
  account_id   = "${var.cluster_b_name}-node"
  display_name = "GKE node SA for ${var.cluster_b_name}"
}

locals {
  node_sa_roles = [
    "roles/logging.logWriter",
    "roles/monitoring.metricWriter",
    "roles/monitoring.viewer",
    "roles/stackdriver.resourceMetadata.writer",
    "roles/artifactregistry.reader",
  ]
}

resource "google_project_iam_member" "node_sa_a_roles" {
  for_each = toset(local.node_sa_roles)
  project  = var.project_id
  role     = each.key
  member   = "serviceAccount:${google_service_account.node_sa_a.email}"
}

resource "google_project_iam_member" "node_sa_b_roles" {
  for_each = toset(local.node_sa_roles)
  project  = var.project_id
  role     = each.key
  member   = "serviceAccount:${google_service_account.node_sa_b.email}"
}
