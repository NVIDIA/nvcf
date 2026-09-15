#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${SCRIPT_DIR}/utils.sh" ]; then
    echo "Error: utils.sh not found in ${SCRIPT_DIR}"
    exit 1
fi
source "$SCRIPT_DIR/utils.sh"

if ! check_kubernetes; then
    log_error "kubernetes check failed"
    exit 1
fi

configure_kube_proxy() {
    log_step "Configuring kube-proxy..."

    # Check if ConfigMap exists
    if ! kubectl get configmap kube-proxy -n kube-system >/dev/null 2>&1; then
        log_info "Creating kube-proxy ConfigMap..."
        cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: kube-proxy
  namespace: kube-system
data:
  config.conf: |-
    apiVersion: kubeproxy.config.k8s.io/v1alpha1
    kind: KubeProxyConfiguration
    mode: "ipvs"
    ipvs:
      strictARP: true
EOF
        if [ $? -ne 0 ]; then
            log_error "Failed to create kube-proxy ConfigMap"
            return 1
        fi
    else
        log_info "Updating existing kube-proxy ConfigMap..."
        if ! kubectl get configmap kube-proxy -n kube-system -o yaml > /tmp/kube-proxy.yaml; then
            log_error "Failed to get existing kube-proxy ConfigMap"
            return 1
        fi

        if ! sed -i.bak -e "s/strictARP: false/strictARP: true/" /tmp/kube-proxy.yaml; then
            log_error "Failed to modify kube-proxy configuration"
            return 1
        fi

        if ! kubectl apply -f /tmp/kube-proxy.yaml; then
            log_error "Failed to apply updated kube-proxy ConfigMap"
            return 1
        fi

        rm -f /tmp/kube-proxy.yaml /tmp/kube-proxy.yaml.bak
    fi

    log_success "kube-proxy configuration completed"
    return 0
}

setup_metallb() {
    local statefulset=$1
    local metallb_version="v0.14.9"
    local pool_start="192.168.5.240"
    local pool_end="192.168.5.250"
    local pool_name="openbao-server-pool"
    local l2_name="${pool_name}-l2"

    log_section "Setting up MetalLB ..."

    # Configure kube-proxy before MetalLB installation
    if ! configure_kube_proxy; then
        log_error "Failed to configure kube-proxy"
        return 1
    fi

    log_step "Installing MetalLB version ${metallb_version}..."
    if ! kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/${metallb_version}/config/manifests/metallb-native.yaml; then
        log_error "Failed to install MetalLB"
        return 1
    fi

    log_step "Waiting for MetalLB components to be ready..."
    if ! kubectl wait --namespace metallb-system \
        --for=condition=ready pod \
        --selector app=metallb \
        --timeout=90s; then
        log_error "MetalLB pods failed to become ready"
        return 1
    fi

    log_step "Configuring kube-proxy for strict ARP..."
    if ! kubectl get configmap kube-proxy -n kube-system -o yaml | \
        sed -e "s/strictARP: false/strictARP: true/" | \
        kubectl apply -f - -n kube-system; then
        log_error "Failed to configure kube-proxy"
        return 1
    fi

    log_step "Creating MetalLB IP address pool..."
    kubectl apply -f - <<EOF
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: ${pool_name}
  namespace: metallb-system
spec:
  addresses:
  - ${pool_start}-${pool_end}
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: ${l2_name}
  namespace: metallb-system
spec:
  ipAddressPools:
    - ${pool_name}
EOF

    if [ $? -ne 0 ]; then
        log_error "Failed to create MetalLB configuration"
        return 1
    fi

    log_step "Verifying MetalLB installation..."
    if ! kubectl get pods -n metallb-system | grep -q "Running"; then
        log_error "MetalLB pods are not running"
        return 1
    fi

    if ! kubectl get ipaddresspools -n metallb-system ${pool_name} >/dev/null 2>&1; then
        log_error "IP address pool not configured properly"
        return 1
    fi

    if ! kubectl get l2advertisements -n metallb-system ${l2_name} >/dev/null 2>&1; then
        log_error "L2 advertisement not configured properly"
        return 1
    fi

    log_step "Creating LoadBalancer service for ${statefulset}..."
    kubectl apply -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: ${statefulset}-lb
  namespace: vault-system
  labels:
    app.kubernetes.io/name: openbao
    app.kubernetes.io/instance: ${statefulset}
spec:
  type: LoadBalancer
  selector:
    app.kubernetes.io/name: openbao
    app.kubernetes.io/instance: ${statefulset}
    component: server
  ports:
  - name: http
    port: 8200
    targetPort: 8200
    protocol: TCP
  - name: https-internal
    port: 8201
    targetPort: 8201
    protocol: TCP
EOF

    if [ $? -ne 0 ]; then
        log_error "Failed to create LoadBalancer service"
        return 1
    fi

    log_success "MetalLB setup completed successfully"
    return 0
}

cleanup_metallb() {
    local statefulset=$1

    log_section "Cleaning up MetalLB for ${statefulset}..."

    # Remove LoadBalancer service
    log_info "Removing LoadBalancer service..."
    if ! kubectl delete svc ${statefulset}-lb -n vault-system 2>/dev/null; then
        log_info "LoadBalancer service not found or already removed"
    fi

    # Remove L2 advertisements
    log_info "Removing L2 advertisements..."
    if ! kubectl delete l2advertisements.metallb.io -n metallb-system --all; then
        log_error "Failed to remove L2 advertisements"
        return 1
    fi

    # Remove IP address pools
    log_info "Removing IP address pools..."
    if ! kubectl delete ipaddresspools.metallb.io -n metallb-system --all; then
        log_error "Failed to remove IP address pools"
        return 1
    fi

    # Remove MetalLB installation
    log_info "Removing MetalLB installation..."
    if ! kubectl delete -f https://raw.githubusercontent.com/metallb/metallb/v0.14.9/config/manifests/metallb-native.yaml; then
        log_error "Failed to remove MetalLB installation"
        return 1
    fi

    log_success "MetalLB cleanup completed successfully"
    return 0
}

# Usage
statefulset="openbao-server"

if [[ "$1" == "--cleanup" ]]; then
    if ! cleanup_metallb $statefulset; then
        log_error "MetalLB cleanup failed"
        exit 1
    fi
else
    if ! setup_metallb $statefulset; then
        log_error "MetalLB setup failed"
        exit 1
    fi
fi
