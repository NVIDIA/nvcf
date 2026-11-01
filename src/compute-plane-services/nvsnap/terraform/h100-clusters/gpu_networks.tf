// ─── GPUDirect-TCPXO networks ──────────────────────────────────────────
//
// a3-megagpu-8g has 8 dedicated GPU NICs (plus 1 default control NIC).
// To enable GPUDirect-TCPXO (the RDMA-equivalent fabric that gets us
// 1.6 Tbps GPU-to-GPU), each of those 8 GPU NICs must be attached to
// its own VPC with jumbo frames (MTU 8244).
//
// The 8 VPCs are shared across both clusters — nodes in cluster A and
// cluster B both attach to the same set of 8 GPU networks. This keeps
// the topology simple and avoids duplicate VPC quota.
//
// Each VPC needs:
//   - MTU 8244 (jumbo frames; required for TCPXO line rate)
//   - One /24 subnet in the cluster region
//   - One firewall rule allowing intra-VPC TCP/UDP/ICMP
//
// CIDR layout: 192.168.{0-7}.0/24. Each GPU VPC is isolated, so the
// CIDRs don't collide with the main cluster VPC (10.40.0.0/14-ish).

resource "google_compute_network" "gpu" {
  count = 8

  name                    = "nvsnap-h100-gpu-net-${count.index}"
  auto_create_subnetworks = false
  mtu                     = 8244
  routing_mode            = "REGIONAL"

  description = "GPU NIC ${count.index} network for GPUDirect-TCPXO (nvsnap H100 bench)."
}

resource "google_compute_subnetwork" "gpu" {
  count = 8

  name          = "nvsnap-h100-gpu-sub-${count.index}"
  network       = google_compute_network.gpu[count.index].self_link
  region        = var.region
  ip_cidr_range = "192.168.${count.index}.0/24"

  private_ip_google_access = true
}

resource "google_compute_firewall" "gpu_internal" {
  count = 8

  name    = "nvsnap-h100-gpu-net-${count.index}-internal"
  network = google_compute_network.gpu[count.index].self_link

  description = "Intra-VPC traffic on GPU NIC ${count.index} fabric."

  source_ranges = ["192.168.${count.index}.0/24"]

  allow {
    protocol = "tcp"
  }
  allow {
    protocol = "udp"
  }
  allow {
    protocol = "icmp"
  }
}

// ─── Compact placement: inherited from the shared reservation ──────────
//
// We initially declared our own google_compute_resource_policy here
// and attached it to the node pools. GCP rejected the create with:
//
//   "To consume reservation with placement policy, the instance has to
//    specify the same resource policy as the reservation."
//
// The shared reservation gsc-a3-megagpu-8g-shared-res-3 owned by
// YOUR_RESERVATION_PROJECT already has its own placement policy.
// Instances launched from that reservation inherit it; we cannot
// attach a different one. So we drop the explicit placement_policy
// block from the node pools and let the reservation's policy apply.
