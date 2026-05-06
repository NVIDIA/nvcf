#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


# Kubernetes Cluster Validation Script
# Validates cluster health, GPU resources, and GPU Operator installation

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Icons
CHECK_MARK="✓"
CROSS_MARK="✗"
WARNING="⚠"
INFO="ℹ"

# Environment configuration (hidden: set NVCF_ENV=staging for staging validation)
NVCF_ENV="${NVCF_ENV:-production}"

# Validation state tracking
CONTROL_PLANE_HEALTHY=true
WEBHOOKS_SUPPORTED=false
NETWORK_POLICIES_SUPPORTED=false
SMB_CSI_DRIVER_OK=false
EGRESS_CONNECTIVITY_OK=false
HELM_CHART_ACCESS_VERIFIED=true
GPU_AVAILABLE=false
GPU_OPERATOR_INSTALLED=false
HAS_CLUSTER_ADMIN=false
HELM_AVAILABLE=false
RECOMMENDATIONS=()
WARNINGS=()

# Cluster information
CLUSTER_CONTEXT=""
K8S_VERSION=""
TOTAL_NODES=""

print_header() {
  echo -e "\n${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
  echo -e "${BLUE}  $1${NC}"
  echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
}

print_success() {
  echo -e "${GREEN}${CHECK_MARK} $1${NC}"
}

print_error() {
  echo -e "${RED}${CROSS_MARK} $1${NC}"
}

print_warning() {
  echo -e "${YELLOW}${WARNING} $1${NC}"
}

print_info() {
  echo -e "${BLUE}${INFO} $1${NC}"
}

check_prerequisites() {
  print_header "Checking Prerequisites"

  # Check kubectl
  if ! command -v kubectl &> /dev/null; then
    print_error "kubectl is not installed or not in PATH"
    exit 1
  fi
  print_success "kubectl is available"

  # Check helm v3+
  if ! command -v helm &> /dev/null; then
    print_error "helm is not installed or not in PATH"
    print_info "helm v3+ is required to install nvca-operator"
    HELM_AVAILABLE=false
    RECOMMENDATIONS+=("Install helm v3+: https://helm.sh/docs/intro/install/")
  else
    local helm_version
    helm_version=$(helm version --short 2>/dev/null | grep -oE 'v[0-9]+' | head -1 | tr -d 'v' || echo "0")
    if [[ -n "$helm_version" ]] && [[ "$helm_version" -ge 3 ]]; then
      print_success "helm v3+ is available ($(helm version --short 2>/dev/null))"
      HELM_AVAILABLE=true
    else
      print_error "helm version is below v3 (found: v${helm_version})"
      print_info "helm v3+ is required to install nvca-operator"
      HELM_AVAILABLE=false
      RECOMMENDATIONS+=("Upgrade to helm v3+: https://helm.sh/docs/intro/install/")
    fi
  fi

  # Check cluster connectivity
  if ! kubectl cluster-info &> /dev/null; then
    print_error "Cannot connect to Kubernetes cluster. Please check your kubeconfig."
    echo ""
    echo -e "${RED}╔═══════════════════════════════════════════════════════════╗${NC}"
    echo -e "${RED}║                                                           ║${NC}"
    echo -e "${RED}║              ${CROSS_MARK}  Cluster is NVCF-Not-Ready  ${CROSS_MARK}              ║${NC}"
    echo -e "${RED}║                                                           ║${NC}"
    echo -e "${RED}╚═══════════════════════════════════════════════════════════╝${NC}"
    echo ""
    exit 1
  fi
  print_success "Connected to Kubernetes cluster"
  
  # Collect and display cluster information
  echo -e "\nCluster Information:"
  CLUSTER_CONTEXT=$(kubectl config current-context 2>/dev/null || echo "unknown")
  print_info "  Current context: ${CLUSTER_CONTEXT}"
  
  K8S_VERSION=$(kubectl version 2>/dev/null | grep -i "server version" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' || echo "unknown")
  if [[ -z "$K8S_VERSION" ]] || [[ "$K8S_VERSION" == "unknown" ]]; then
    K8S_VERSION=$(kubectl version -o json 2>/dev/null | jq -r '.serverVersion.gitVersion' 2>/dev/null || echo "unknown")
  fi
  print_info "  Kubernetes version: ${K8S_VERSION}"
  
  TOTAL_NODES=$(kubectl get nodes --no-headers 2>/dev/null | wc -l || echo "0")
  print_info "  Total nodes: ${TOTAL_NODES}"

  # Check cluster-admin capability
  echo -e "\nPermissions Check:"
  if kubectl auth can-i '*' '*' --all-namespaces &> /dev/null; then
    print_success "Current user has cluster-admin privileges"
    HAS_CLUSTER_ADMIN=true
  else
    print_warning "Current user does NOT have cluster-admin privileges"
    print_info "cluster-admin is required to install nvca-operator"
    HAS_CLUSTER_ADMIN=false
    RECOMMENDATIONS+=("Obtain cluster-admin privileges before installing nvca-operator")
  fi
}

check_control_plane_health() {
  print_header "Kubernetes Control Plane Health"

  local all_healthy=true

  # Check API server connectivity
  if kubectl get --raw='/readyz' &> /dev/null; then
    print_success "API Server is ready"
  else
    print_error "API Server is not ready"
    all_healthy=false
  fi

  # Check component statuses (deprecated but still useful)
  echo -e "\nComponent Status:"
  while read -r component status message; do
    if [[ "$status" == "Healthy" ]] || [[ "$status" == "True" ]]; then
      print_success "  $component: $status"
    else
      print_error "  $component: $status - $message"
      all_healthy=false
    fi
  done < <(kubectl get componentstatuses -o custom-columns=NAME:.metadata.name,STATUS:.conditions[0].type,MESSAGE:.conditions[0].message --no-headers 2>/dev/null || echo "")

  # Check kube-system pods health
  echo -e "\nControl Plane Pods (kube-system):"
  local critical_pods=("kube-apiserver" "kube-controller-manager" "kube-scheduler" "etcd" "coredns" "kube-proxy")

  for pod_prefix in "${critical_pods[@]}"; do
    local pod_status
    pod_status=$(kubectl get pods -n kube-system -l component="$pod_prefix" -o jsonpath='{.items[*].status.phase}' 2>/dev/null || \
                 kubectl get pods -n kube-system --field-selector=status.phase=Running 2>/dev/null | grep -c "$pod_prefix" || echo "0")

    if [[ -n "$pod_status" ]] && [[ "$pod_status" != "0" ]]; then
      local running_count
      running_count=$(kubectl get pods -n kube-system 2>/dev/null | grep "$pod_prefix" | grep -c "Running" || echo "0")
      if [[ "$running_count" -gt 0 ]]; then
        print_success "  $pod_prefix: $running_count instance(s) running"
      else
        print_warning "  $pod_prefix: Not found or not running"
      fi
    fi
  done

  # Check nodes status
  echo -e "\nNode Status:"
  local ready_nodes not_ready_nodes
  ready_nodes=$(kubectl get nodes --no-headers 2>/dev/null | grep -c " Ready" || echo "0")
  not_ready_nodes=$(kubectl get nodes --no-headers 2>/dev/null | grep -c "NotReady" || echo "0")

  print_info "  Ready nodes: $ready_nodes"
  if [[ "$not_ready_nodes" -gt 0 ]]; then
    print_warning "  NotReady nodes: $not_ready_nodes"
    all_healthy=false
  fi

  echo ""
  if $all_healthy; then
    print_success "Control plane is healthy"
  else
    print_warning "Some control plane components may need attention"
    CONTROL_PLANE_HEALTHY=false
    RECOMMENDATIONS+=("Fix control plane issues: Check node status and kube-system pods")
  fi
}

check_webhook_support() {
  print_header "Webhook Support"

  local webhooks_supported=true

  # Check if admissionregistration.k8s.io API is available
  echo "Admission Registration API:"
  if kubectl api-resources --api-group=admissionregistration.k8s.io 2>/dev/null | grep -q "mutatingwebhookconfigurations"; then
    print_success "MutatingWebhookConfiguration API is available"
  else
    print_error "MutatingWebhookConfiguration API is not available"
    webhooks_supported=false
  fi

  if kubectl api-resources --api-group=admissionregistration.k8s.io 2>/dev/null | grep -q "validatingwebhookconfigurations"; then
    print_success "ValidatingWebhookConfiguration API is available"
  else
    print_error "ValidatingWebhookConfiguration API is not available"
    webhooks_supported=false
  fi

  # Check if we can create/manage webhook configurations
  echo -e "\nWebhook Permissions:"
  if kubectl auth can-i create mutatingwebhookconfigurations.admissionregistration.k8s.io &> /dev/null; then
    print_success "Can create MutatingWebhookConfigurations"
  else
    print_warning "Cannot create MutatingWebhookConfigurations"
    webhooks_supported=false
  fi

  if kubectl auth can-i create validatingwebhookconfigurations.admissionregistration.k8s.io &> /dev/null; then
    print_success "Can create ValidatingWebhookConfigurations"
  else
    print_warning "Cannot create ValidatingWebhookConfigurations"
    webhooks_supported=false
  fi

  # Check for existing webhooks as evidence of support
  echo -e "\nExisting Webhooks:"
  local mutating_count validating_count
  mutating_count=$(kubectl get mutatingwebhookconfigurations --no-headers 2>/dev/null | wc -l || echo "0")
  validating_count=$(kubectl get validatingwebhookconfigurations --no-headers 2>/dev/null | wc -l || echo "0")
  print_info "MutatingWebhookConfigurations: $mutating_count"
  print_info "ValidatingWebhookConfigurations: $validating_count"

  echo ""
  if $webhooks_supported; then
    print_success "Cluster supports admission webhooks"
    WEBHOOKS_SUPPORTED=true
  else
    print_error "Cluster does not fully support admission webhooks"
    WEBHOOKS_SUPPORTED=false
    RECOMMENDATIONS+=("Enable admission webhooks in your cluster (MutatingAdmissionWebhook, ValidatingAdmissionWebhook)")
  fi
}

check_network_policies() {
  print_header "Network Policy Support"

  local supports_netpol=false

  # Check if NetworkPolicy API is available
  if kubectl api-resources --api-group=networking.k8s.io 2>/dev/null | grep -q "networkpolicies"; then
    print_success "NetworkPolicy API is available"
  else
    print_error "NetworkPolicy API is not available"
    RECOMMENDATIONS+=("Ensure Kubernetes cluster supports networking.k8s.io API group")
    return
  fi

  # Check for CNI plugins that support network policies
  echo -e "\nCNI Plugin Detection:"

  # Check for Calico
  if kubectl get pods -n kube-system -l k8s-app=calico-node --no-headers 2>/dev/null | grep -q "Running"; then
    print_success "Calico CNI detected (supports network policies)"
    supports_netpol=true
  # Check for Cilium
  elif kubectl get pods -n kube-system -l k8s-app=cilium --no-headers 2>/dev/null | grep -q "Running"; then
    print_success "Cilium CNI detected (supports network policies)"
    supports_netpol=true
  # Check for Weave
  elif kubectl get pods -n kube-system -l name=weave-net --no-headers 2>/dev/null | grep -q "Running"; then
    print_success "Weave Net CNI detected (supports network policies)"
    supports_netpol=true
  # Check for Antrea
  elif kubectl get pods -n kube-system -l app=antrea --no-headers 2>/dev/null | grep -q "Running"; then
    print_success "Antrea CNI detected (supports network policies)"
    supports_netpol=true
  # Check for Canal (Calico + Flannel)
  elif kubectl get pods -n kube-system -l k8s-app=canal --no-headers 2>/dev/null | grep -q "Running"; then
    print_success "Canal CNI detected (supports network policies)"
    supports_netpol=true
  # Check for any existing network policies as evidence of support
  elif [[ $(kubectl get networkpolicies --all-namespaces --no-headers 2>/dev/null | wc -l) -gt 0 ]]; then
    print_info "Existing NetworkPolicies found in cluster"
    supports_netpol=true
  else
    print_warning "Could not detect a known CNI plugin with network policy support"
    print_info "Common CNI plugins checked: Calico, Cilium, Weave, Antrea, Canal"
  fi

  echo ""
  if $supports_netpol; then
    print_success "Cluster supports network policies"
    NETWORK_POLICIES_SUPPORTED=true
  else
    print_warning "Network policy support could not be confirmed"
    print_info "Network policies may still work if your CNI plugin supports them"
    print_info "Flannel and some cloud CNIs do NOT enforce network policies"
    NETWORK_POLICIES_SUPPORTED=false
    WARNINGS+=("Network Policies: Could not confirm support - verify your CNI plugin supports them")
    RECOMMENDATIONS+=("Verify your CNI plugin supports network policies (Calico, Cilium, etc.)")
  fi
}

check_smb_csi_driver() {
  print_header "SMB CSI Driver"

  local smb_version=""
  local required_version="1.16.0"

  # Check if SMB CSI Driver is installed by looking for the CSIDriver resource
  if kubectl get csidriver smb.csi.k8s.io &> /dev/null; then
    print_success "SMB CSI Driver is installed"

    # Try to get the version from the controller deployment
    echo -e "\nVersion Check:"
    
    # Check common namespaces for SMB CSI driver
    local smb_namespaces=("kube-system" "smb-csi" "csi-smb")
    local found_ns=""
    
    for ns in "${smb_namespaces[@]}"; do
      if kubectl get deployment -n "$ns" -l app=csi-smb-controller &> /dev/null 2>&1 || \
         kubectl get deployment -n "$ns" csi-smb-controller &> /dev/null 2>&1; then
        found_ns="$ns"
        break
      fi
    done

    if [[ -n "$found_ns" ]]; then
      # Try to extract version from the image tag
      smb_version=$(kubectl get deployment -n "$found_ns" csi-smb-controller -o jsonpath='{.spec.template.spec.containers[?(@.name=="smb")].image}' 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | tr -d 'v' || echo "")
      
      if [[ -z "$smb_version" ]]; then
        # Try alternative container name
        smb_version=$(kubectl get deployment -n "$found_ns" -l app=csi-smb-controller -o jsonpath='{.items[0].spec.template.spec.containers[0].image}' 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | tr -d 'v' || echo "")
      fi
    fi

    if [[ -n "$smb_version" ]]; then
      print_info "  Detected version: v${smb_version}"
      
      # Compare versions
      if version_gte "$smb_version" "$required_version"; then
        print_success "  Version v${smb_version} meets minimum requirement (v${required_version}+)"
        SMB_CSI_DRIVER_OK=true
      else
        print_error "  Version v${smb_version} is below minimum requirement (v${required_version}+)"
        SMB_CSI_DRIVER_OK=false
        RECOMMENDATIONS+=("Upgrade SMB CSI Driver to v${required_version} or higher")
      fi
    else
      print_warning "  Could not determine SMB CSI Driver version"
      print_info "  Please verify manually that version is v${required_version} or higher"
      # Mark as OK but add recommendation to verify
      SMB_CSI_DRIVER_OK=true
      RECOMMENDATIONS+=("Verify SMB CSI Driver version is v${required_version} or higher")
    fi
  else
    print_error "SMB CSI Driver is NOT installed"
    print_info "SMB CSI Driver v${required_version}+ is required for persistent storage"
    SMB_CSI_DRIVER_OK=false
    echo ""
    print_info "To install SMB CSI Driver:"
    echo ""
    echo -e "${YELLOW}# Using Helm:${NC}"
    echo "helm repo add csi-driver-smb https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts"
    echo "helm install csi-driver-smb csi-driver-smb/csi-driver-smb \\"
    echo "  --namespace kube-system \\"
    echo "  --version v1.16.0"
    echo ""
    print_info "For more information: https://github.com/kubernetes-csi/csi-driver-smb"
    RECOMMENDATIONS+=("Install SMB CSI Driver v${required_version} or higher")
  fi
}

# Helper function to compare semantic versions (returns 0 if v1 >= v2)
version_gte() {
  local v1="$1"
  local v2="$2"
  
  # Split versions into arrays
  local IFS='.'
  read -ra v1_parts <<< "$v1"
  read -ra v2_parts <<< "$v2"
  
  # Compare each part
  for i in 0 1 2; do
    local p1="${v1_parts[$i]:-0}"
    local p2="${v2_parts[$i]:-0}"
    
    if [[ "$p1" -gt "$p2" ]]; then
      return 0
    elif [[ "$p1" -lt "$p2" ]]; then
      return 1
    fi
  done
  
  return 0  # Equal
}

check_egress_connectivity() {
  print_header "Egress Connectivity"

  local nvcr_ok=false

  # Set endpoints based on environment
  local nvcr_host helm_host env_label
  if [[ "$NVCF_ENV" == "staging" ]]; then
    nvcr_host="stg.nvcr.io"
    helm_host="helm.stg.ngc.nvidia.com"
    env_label="Staging"
  else
    nvcr_host="nvcr.io"
    helm_host="helm.ngc.nvidia.com"
    env_label="Production"
  fi

  local nvcr_image="${nvcr_host}/nvidia/nvcf-byoc/nvca-operator"
  local helm_repo="${helm_host}/nvidia/nvcf-byoc"

  # Generate unique pod name using timestamp and random suffix to avoid collisions
  local unique_id
  unique_id="$(date +%s)-${RANDOM}"
  local test_pod="nvcf-egress-test-${unique_id}"
  local test_ns="default"

  print_info "Testing egress connectivity for ${env_label} environment..."
  print_info "Container Registry: ${nvcr_host}"
  print_info "Helm Repository: ${helm_host}"
  echo ""

  # Helper function to cleanup test pod with timeout
  cleanup_test_pod() {
    local pod_name="$1"
    local namespace="$2"
    local timeout_secs="${3:-30}"

    kubectl delete pod "$pod_name" -n "$namespace" --ignore-not-found=true --wait=false &> /dev/null || true
    
    # Wait for deletion with timeout
    local elapsed=0
    while [[ $elapsed -lt $timeout_secs ]]; do
      if ! kubectl get pod "$pod_name" -n "$namespace" &> /dev/null; then
        return 0
      fi
      sleep 2
      elapsed=$((elapsed + 2))
    done
    
    # Force delete if still exists
    kubectl delete pod "$pod_name" -n "$namespace" --ignore-not-found=true --force --grace-period=0 &> /dev/null || true
  }

  # Helper function to test URL accessibility
  # Returns 0 if accessible (any HTTP response including 401/403), 1 if not reachable
  test_url_accessible() {
    local url="$1"
    local pod="$2"
    local ns="$3"
    
    # Try wget - returns 0 for 2xx, non-zero for errors
    # We check output for HTTP response codes to determine if server is reachable
    local output
    output=$(kubectl exec "$pod" -n "$ns" -- wget --spider --timeout=10 -S "$url" 2>&1) && return 0
    
    # Check if we got an HTTP response (server is reachable even if auth required)
    if echo "$output" | grep -qE "HTTP/[0-9.]+ (200|301|302|401|403)"; then
      return 0
    fi
    
    return 1
  }

  # Check if we can create test pods
  if ! kubectl auth can-i create pods -n "$test_ns" &> /dev/null; then
    print_warning "Cannot create test pods - skipping in-cluster connectivity test"
    print_info "Manual verification required for egress connectivity"
    EGRESS_CONNECTIVITY_OK=true  # Assume OK, add recommendation
    RECOMMENDATIONS+=("Manually verify egress connectivity to ${nvcr_host} and ${helm_host}")
    return 0
  fi

  # Cleanup any stale test pods from previous runs (matching pattern)
  kubectl delete pods -n "$test_ns" -l app=nvcf-egress-test --ignore-not-found=true &> /dev/null || true

  # Create test pod with busybox
  print_info "Creating temporary test pod..."
  if ! kubectl run "$test_pod" -n "$test_ns" \
    --image=busybox:stable \
    --restart=Never \
    --labels="app=nvcf-egress-test" \
    --command -- sh -c "sleep 180" &> /dev/null; then
    print_warning "Could not create test pod"
    EGRESS_CONNECTIVITY_OK=true
    RECOMMENDATIONS+=("Manually verify egress connectivity to ${nvcr_host} and ${helm_host}")
    return 0
  fi

  # Set up trap to ensure cleanup on exit/interrupt
  # shellcheck disable=SC2064 # We intentionally expand variables now to capture current values
  trap "cleanup_test_pod '$test_pod' '$test_ns' 10" RETURN

  # Wait for pod to be ready with timeout
  print_info "Waiting for test pod to be ready (max 60s)..."
  local retries=0
  local max_retries=30
  local pod_ready=false
  while [[ $retries -lt $max_retries ]]; do
    local phase
    phase=$(kubectl get pod "$test_pod" -n "$test_ns" -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
    case "$phase" in
      Running)
        pod_ready=true
        break
        ;;
      Failed|Error|ErrImagePull|ImagePullBackOff)
        print_warning "Test pod failed to start (phase: $phase)"
        break
        ;;
    esac
    sleep 2
    retries=$((retries + 1))
  done

  if ! $pod_ready; then
    print_warning "Test pod did not become ready in time - skipping connectivity test"
    EGRESS_CONNECTIVITY_OK=true
    RECOMMENDATIONS+=("Manually verify egress connectivity to ${nvcr_host} and ${helm_host}")
    return 0
  fi

  echo ""
  echo "Testing Container Registry Access:"

  # Test container registry access
  if test_url_accessible "https://${nvcr_host}/v2/" "$test_pod" "$test_ns"; then
    print_success "  ${nvcr_image}: Accessible"
    nvcr_ok=true
  else
    print_error "  ${nvcr_image}: Not Reachable"
  fi

  echo ""
  echo "Testing Helm Chart Repository Access:"

  # Test helm repo access for nvca-operator chart
  local helm_charts_url="https://${helm_host}/nvidia/nvcf-byoc/charts"
  if test_url_accessible "$helm_charts_url" "$test_pod" "$test_ns"; then
    print_success "  ${helm_repo}/charts (nvca-operator): Accessible"
    HELM_CHART_ACCESS_VERIFIED=true
  else
    print_warning "  ${helm_repo}/charts (nvca-operator): Could not verify access"
    print_info "  This may require authentication. Please verify manually."
    echo ""
    print_info "To verify Helm chart access, use your CLUSTER_KEY:"
    echo ""
    echo -e "${YELLOW}# Verify Helm chart access with your CLUSTER_KEY:${NC}"
    echo "helm fetch https://${helm_host}/nvidia/nvcf-byoc/charts/nvca-operator-<VERSION>.tgz \\"
    echo "  --username='\$oauthtoken' --password='<YOUR_CLUSTER_KEY>'"
    echo ""
    if [[ "$NVCF_ENV" == "staging" ]]; then
      print_info "If staging access is required but not working, please file a support ticket"
      print_info "for NGC Allowlisting to enable Staging NGC Access."
    fi
    # Don't fail readiness, just add warning
    HELM_CHART_ACCESS_VERIFIED=false
    WARNINGS+=("Helm Chart Access: Could not verify - please verify manually using helm fetch command")
  fi

  echo ""

  # Evaluate access - only fail if container registry is not reachable
  if $nvcr_ok; then
    print_success "${env_label} environment access: OK"
    EGRESS_CONNECTIVITY_OK=true
  else
    print_error "${env_label} environment access: FAILED"
    print_error "Container registry (${nvcr_host}) is not reachable"
    RECOMMENDATIONS+=("Configure egress access to ${nvcr_host} for pulling nvca-operator container images")
    EGRESS_CONNECTIVITY_OK=false
  fi
}

check_gpu_resources() {
  print_header "GPU Resources"

  # Check for nodes with GPU capacity
  local gpu_nodes gpu_capacity gpu_allocatable gpu_used

  # Get nodes with nvidia.com/gpu resource
  gpu_nodes=$(kubectl get nodes -o json | \
    jq '[.items[] | select(.status.capacity."nvidia.com/gpu" != null and (.status.capacity."nvidia.com/gpu" | tonumber) > 0)] | length' 2>/dev/null || echo "0")

  gpu_capacity=$(kubectl get nodes -o json | \
    jq '[.items[] | select(.status.capacity."nvidia.com/gpu" != null) | .status.capacity."nvidia.com/gpu" | tonumber] | add // 0' 2>/dev/null || echo "0")

  gpu_allocatable=$(kubectl get nodes -o json | \
    jq '[.items[] | select(.status.allocatable."nvidia.com/gpu" != null) | .status.allocatable."nvidia.com/gpu" | tonumber] | add // 0' 2>/dev/null || echo "0")

  echo "GPU Node Summary:"
  print_info "  Nodes with GPUs: $gpu_nodes"
  print_info "  Total GPU capacity: $gpu_capacity"
  print_info "  Total GPU allocatable: $gpu_allocatable"

  # Calculate used GPUs
  if [[ "$gpu_capacity" -gt 0 ]]; then
    gpu_used=$((gpu_capacity - gpu_allocatable))
    print_info "  GPUs in use: $gpu_used"
  fi

  # List GPU nodes with details
  if [[ "$gpu_nodes" -gt 0 ]]; then
    echo -e "\nGPU Node Details:"
    kubectl get nodes -o json | \
      jq -r '.items[] | select(.status.capacity."nvidia.com/gpu" != null and (.status.capacity."nvidia.com/gpu" | tonumber) > 0) | 
        "  \(.metadata.name): \(.status.capacity."nvidia.com/gpu") GPU(s) (allocatable: \(.status.allocatable."nvidia.com/gpu" // "0"))"' 2>/dev/null || true
  fi

  echo ""
  if [[ "$gpu_capacity" -eq 0 ]]; then
    print_warning "WARNING: No GPUs detected in the cluster!"
    print_info "This could mean:"
    print_info "  - No GPU nodes are present in the cluster"
    print_info "  - GPU Operator is not installed or not functioning"
    print_info "  - GPU drivers are not properly configured"
    GPU_AVAILABLE=false
    RECOMMENDATIONS+=("Add GPU nodes to the cluster or verify GPU Operator is functioning")
  else
    print_success "GPU resources detected in cluster"
    GPU_AVAILABLE=true
  fi
}

check_gpu_operator() {
  print_header "GPU Operator Status"

  local gpu_operator_ns="gpu-operator"
  local gpu_operator_installed=false

  # Check for GPU Operator namespace
  if kubectl get namespace "$gpu_operator_ns" &> /dev/null; then
    print_success "GPU Operator namespace exists: $gpu_operator_ns"

    # Check for GPU Operator deployment/pods
    local operator_pods
    operator_pods=$(kubectl get pods -n "$gpu_operator_ns" --no-headers 2>/dev/null | wc -l || echo "0")

    if [[ "$operator_pods" -gt 0 ]]; then
      gpu_operator_installed=true
      print_success "GPU Operator pods found: $operator_pods"

      echo -e "\nGPU Operator Components:"
      kubectl get pods -n "$gpu_operator_ns" --no-headers 2>/dev/null | while read -r line; do
        local pod_name status
        pod_name=$(echo "$line" | awk '{print $1}')
        status=$(echo "$line" | awk '{print $3}')
        if [[ "$status" == "Running" ]] || [[ "$status" == "Completed" ]]; then
          print_success "  $pod_name: $status"
        else
          print_warning "  $pod_name: $status"
        fi
      done

      # Check ClusterPolicy
      echo -e "\nClusterPolicy Status:"
      if kubectl get clusterpolicy cluster-policy &> /dev/null; then
        local policy_state
        policy_state=$(kubectl get clusterpolicy cluster-policy -o jsonpath='{.status.state}' 2>/dev/null || echo "unknown")
        if [[ "$policy_state" == "ready" ]]; then
          print_success "ClusterPolicy state: $policy_state"
        else
          print_warning "ClusterPolicy state: $policy_state"
        fi
      else
        print_warning "ClusterPolicy 'cluster-policy' not found"
      fi
    fi
  fi

  # Also check for gpu-operator in other namespaces (some installations use different namespaces)
  if ! $gpu_operator_installed; then
    local alt_namespaces
    alt_namespaces=$(kubectl get pods --all-namespaces -l app=gpu-operator --no-headers 2>/dev/null | awk '{print $1}' | sort -u || echo "")
    if [[ -n "$alt_namespaces" ]]; then
      print_info "GPU Operator found in namespace(s): $alt_namespaces"
      gpu_operator_installed=true
    fi
  fi

  echo ""
  if ! $gpu_operator_installed; then
    print_error "GPU Operator is NOT installed"
    GPU_OPERATOR_INSTALLED=false
    echo ""
    print_info "To install GPU Operator with default configuration:"
    echo ""
    echo -e "${YELLOW}# Add the NVIDIA Helm repository${NC}"
    echo "helm repo add nvidia https://helm.ngc.nvidia.com/nvidia"
    echo "helm repo update"
    echo ""
    echo -e "${YELLOW}# Install GPU Operator with default driver (auto-detect) and MIG disabled${NC}"
    echo "helm install gpu-operator nvidia/gpu-operator \\"
    echo "  --namespace gpu-operator \\"
    echo "  --create-namespace \\"
    echo "  --set mig.strategy=none \\"
    echo "  --set driver.enabled=true"
    echo ""
    print_info "For more information, see: https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html"
    RECOMMENDATIONS+=("Install GPU Operator using the command above")
  else
    print_success "GPU Operator is installed"
    GPU_OPERATOR_INSTALLED=true
  fi
}

print_summary() {
  print_header "Validation Summary"

  local is_nvcf_ready=true

  echo "Check Results:"
  if $CONTROL_PLANE_HEALTHY; then
    print_success "  Control Plane: Healthy"
  else
    print_error "  Control Plane: Unhealthy"
    is_nvcf_ready=false
  fi

  if $WEBHOOKS_SUPPORTED; then
    print_success "  Admission Webhooks: Mutating & Validating Supported"
  else
    print_error "  Admission Webhooks: Not Supported"
    is_nvcf_ready=false
  fi

  if $NETWORK_POLICIES_SUPPORTED; then
    print_success "  Network Policies: Supported"
  else
    print_warning "  Network Policies: Not Confirmed"
  fi

  if $SMB_CSI_DRIVER_OK; then
    print_success "  SMB CSI Driver: v1.16.0+ Installed"
  else
    print_error "  SMB CSI Driver: Not Installed or Below v1.16.0"
    is_nvcf_ready=false
  fi

  local egress_env_label="Production"
  local egress_hosts="nvcr.io & helm.ngc.nvidia.com"
  if [[ "$NVCF_ENV" == "staging" ]]; then
    egress_env_label="Staging"
    egress_hosts="stg.nvcr.io & helm.stg.ngc.nvidia.com"
  fi

  if $EGRESS_CONNECTIVITY_OK; then
    print_success "  Egress Connectivity: ${egress_env_label} (${egress_hosts})"
  else
    print_error "  Egress Connectivity: ${egress_env_label} Not Reachable"
    is_nvcf_ready=false
  fi

  if $HELM_CHART_ACCESS_VERIFIED; then
    print_success "  Helm Chart Access: Verified"
  else
    print_warning "  Helm Chart Access: Not Verified (manual check required)"
  fi

  if $GPU_AVAILABLE; then
    print_success "  GPU Resources: Available"
  else
    print_warning "  GPU Resources: Not Available"
    is_nvcf_ready=false
  fi

  if $GPU_OPERATOR_INSTALLED; then
    print_success "  GPU Operator: Installed"
  else
    print_error "  GPU Operator: Not Installed"
    is_nvcf_ready=false
  fi

  if $HAS_CLUSTER_ADMIN; then
    print_success "  Cluster Admin: Yes"
  else
    print_warning "  Cluster Admin: No"
    is_nvcf_ready=false
  fi

  if $HELM_AVAILABLE; then
    print_success "  Helm: v3+ Installed"
  else
    print_error "  Helm: v3+ Not Available"
    is_nvcf_ready=false
  fi

  echo ""
  echo -e "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

  if $is_nvcf_ready; then
    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║                                                           ║${NC}"
    echo -e "${GREEN}║                ${CHECK_MARK}  Cluster is NVCF-Ready  ${CHECK_MARK}                ║${NC}"
    echo -e "${GREEN}║                                                           ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════════╝${NC}"
    echo ""
    print_success "Your cluster meets all requirements for NVCF workloads"
    echo ""
    echo "Validated Cluster:"
    print_info "  Context: ${CLUSTER_CONTEXT}"
    print_info "  Kubernetes Version: ${K8S_VERSION}"
    print_info "  Total Nodes: ${TOTAL_NODES}"
  else
    echo ""
    echo -e "${RED}╔═══════════════════════════════════════════════════════════╗${NC}"
    echo -e "${RED}║                                                           ║${NC}"
    echo -e "${RED}║              ${CROSS_MARK}  Cluster is NVCF-Not-Ready  ${CROSS_MARK}              ║${NC}"
    echo -e "${RED}║                                                           ║${NC}"
    echo -e "${RED}╚═══════════════════════════════════════════════════════════╝${NC}"
    echo ""
    print_error "Your cluster does not meet all requirements for NVCF workloads"
  fi

  # Show warnings if any (for both ready and not-ready states)
  if [[ ${#WARNINGS[@]} -gt 0 ]]; then
    echo ""
    echo -e "${YELLOW}Warnings (manual verification required):${NC}"
    echo ""
    local idx=1
    for warn in "${WARNINGS[@]}"; do
      echo -e "  ${YELLOW}${idx}. ${WARNING} ${warn}${NC}"
      idx=$((idx + 1))
    done
  fi

  # Show recommendations if any
  if [[ ${#RECOMMENDATIONS[@]} -gt 0 ]]; then
    echo ""
    echo -e "${YELLOW}Recommendations:${NC}"
    echo ""
    local idx=1
    for rec in "${RECOMMENDATIONS[@]}"; do
      echo -e "  ${idx}. $rec"
      idx=$((idx + 1))
    done
  fi

  echo ""
  echo "Validation completed at $(date '+%Y-%m-%d %H:%M:%S')"
}

main() {
  echo -e "${BLUE}"
  echo "╔═══════════════════════════════════════════════════════════╗"
  echo "║     NVIDIA Cloud BYOC Cluster Readiness Check             ║"
  echo "║         Kubernetes Cluster Validation Script              ║"
  echo "╚═══════════════════════════════════════════════════════════╝"
  echo -e "${NC}"

  check_prerequisites
  check_control_plane_health
  check_webhook_support
  check_network_policies
  check_smb_csi_driver
  check_egress_connectivity
  check_gpu_resources
  check_gpu_operator
  print_summary
}

main "$@"

