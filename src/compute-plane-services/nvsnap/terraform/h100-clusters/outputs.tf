output "cluster_a_name" {
  value       = google_container_cluster.cluster_a.name
  description = "Name of cluster A."
}

output "cluster_b_name" {
  value       = google_container_cluster.cluster_b.name
  description = "Name of cluster B."
}

output "cluster_a_endpoint" {
  value       = google_container_cluster.cluster_a.endpoint
  description = "Cluster A API endpoint."
  sensitive   = true
}

output "cluster_b_endpoint" {
  value       = google_container_cluster.cluster_b.endpoint
  description = "Cluster B API endpoint."
  sensitive   = true
}

output "vpc_self_link" {
  value       = google_compute_network.vpc.self_link
  description = "Shared VPC self-link."
}

output "get_credentials_a" {
  value       = "gcloud container clusters get-credentials ${google_container_cluster.cluster_a.name} --zone ${var.zone} --project ${var.project_id}"
  description = "Run this to wire kubectl to cluster A."
}

output "get_credentials_b" {
  value       = "gcloud container clusters get-credentials ${google_container_cluster.cluster_b.name} --zone ${var.zone} --project ${var.project_id}"
  description = "Run this to wire kubectl to cluster B."
}

output "next_steps" {
  value       = <<-EOT

    ─────────────────────────────────────────────────────────────────
    Clusters provisioned. Post-apply checklist:

    1. Wire kubectl to both clusters:
         gcloud container clusters get-credentials ${google_container_cluster.cluster_a.name} --zone ${var.zone} --project ${var.project_id}
         gcloud container clusters get-credentials ${google_container_cluster.cluster_b.name} --zone ${var.zone} --project ${var.project_id}

       (kubeconfig contexts will be named:
          gke_${var.project_id}_${var.zone}_${var.cluster_a_name}
          gke_${var.project_id}_${var.zone}_${var.cluster_b_name})

    2. Verify GPU driver installation on each cluster:
         kubectl --context=... get nodes -o wide
         kubectl --context=... describe node <node> | grep nvidia.com/gpu
         (should show: nvidia.com/gpu: 8)

    3. Deploy nvsnap (from $HOME/personal/gpucr):
         ./scripts/build-agent.sh deploy --kubeconfig=...
         ./scripts/test-e2e.sh vllm-small

    4. Cross-cluster cascade bench (the #97 we've been waiting to run):
         ./scripts/bench-cross-node-restore.sh

    ─────────────────────────────────────────────────────────────────
  EOT
  description = "Steps after terraform apply."
}
