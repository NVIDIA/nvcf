#!/bin/bash

# Usage
namespace="${1:-vault-system}"
statefulset="${2:-openbao-server}"

# Valid install methods
readonly INSTALL_METHOD_SCRIPT="script"
readonly INSTALL_METHOD_HELM="helm"

# Default to local if not specified
install_method="${3:-$INSTALL_METHOD_SCRIPT}"

# Validate install method
case "$install_method" in
"$INSTALL_METHOD_SCRIPT" | "$INSTALL_METHOD_HELM") ;;
*)
    log_error "Invalid install method: $install_method. Must be one of: $INSTALL_METHOD_SCRIPT, $INSTALL_METHOD_HELM"
    exit 1
    ;;
esac

DEPLOY_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ ! -f "${DEPLOY_SCRIPT_DIR}/utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${DEPLOY_SCRIPT_DIR}/utils"
    exit 1
fi

source "$DEPLOY_SCRIPT_DIR/utils/utils.sh"

if ! check_kubernetes; then
    log_error "kubernetes check failed"
    exit 1
fi

if ! check_helm; then
    log_error "helm check failed"
    exit 1
fi

if [ "${install_method}" = "$INSTALL_METHOD_HELM" ]; then
    log_info "Checking jwker availability for helm deployment..."
    jwker_path="$(command -v jwker)"
    if [ -z "$jwker_path" ]; then
        log_error "jwker is not available in the init image. Use an nvcf-openbao-migrations image that includes jwker."
        exit 1
    fi
    log_success "jwker available at $jwker_path"
fi

# Helper function to get root token
get_root_token() {
    local namespace=$1
    local statefulset=$2
    kubectl get secret ${statefulset}-root-token -n ${namespace} -o jsonpath='{.data.root_token}' | base64 -d
}

# Step 0: Pre-checks
pre_checks() {
    local namespace=$1
    local statefulset=$2

    log_section "Pre-checks"

    # Check if pod exists
    if ! kubectl get pod ${statefulset}-0 -n ${namespace} >/dev/null 2>&1; then
        log_info "Pod '${statefulset}-0' not found. Checking for existing PVCs..."
        # If pod doesn't exist, check for leftover PVCs
        # Capture the output and check exit code
        local PVC_OUTPUT=$(kubectl get pvc -l app.kubernetes.io/name=openbao -n ${namespace} 2>/dev/null)
        if [ $? -ne 0 ]; then
            log_error "Found existing PVCs but no pods. Please run ./cleanup.sh script first."
            return 1
        fi
    fi

    # If pod exists, check initialization status
    local init_status=$(kubectl exec ${statefulset}-0 -n ${namespace} -- \
        bao status -format=json | jq -r '.initialized')
    if [ "$init_status" = "true" ]; then
        log_error "OpenBao is already initialized. Please run ./cleanup.sh script first."
        return 1
    fi
}

# Step 1: Create empty secret
create_empty_secret() {
    local namespace=$1
    local statefulset=$2

    log_section "Setting up kubernetes secrets in namespace '${namespace}' and statefulset '${statefulset}'"
    log_info "Creating empty unseal secret..."
    kubectl create secret generic ${statefulset}-unseal \
        --from-literal=unseal_key="" \
        -n ${namespace}
}

# Step 2: Install OpenBao with Helm
install_openbao() {
    local namespace=$1
    local statefulset=$2

    log_section "Setting up OpenBao nodes"
    log_info "Installing OpenBao via Helm..."
    helm repo add openbao https://openbao.github.io/openbao-helm
    helm repo update openbao
    helm install -n ${namespace} ${statefulset} openbao/openbao --values helm/values.yaml --debug --set='global.imagePullSecrets[0].name=nvcr-secret'
}

# Step 3: Initialize the cluster
initialize_cluster() {
    local namespace=$1
    local statefulset=$2

    log_section "Initializing OpenBao nodes"

    # Wait for pods to be ready with 2-minute timeout
    log_info "Waiting for OpenBao pods to be ready (timeout: 2 minutes)..."
    local end=$((SECONDS + 120)) # 120 seconds = 2 minutes

    for i in {0..2}; do
        while [[ $(kubectl get pod $statefulset-${i} -n ${namespace} -o jsonpath='{.status.containerStatuses[0].ready}') != "true" ]]; do
            if [ $SECONDS -gt $end ]; then
                log_error "Timeout waiting for pod ${statefulset}-${i} to be ready"
                return 1
            fi
            log_info "Waiting for pod ${statefulset}-${i} to be ready..."
            sleep 5
        done
    done
    log_info "All OpenBao pods are ready"

    # Idempotency guard. This script runs as a post-install Job whose pod is
    # retried by the Kubernetes Job controller (backoffLimit defaults to 6, so
    # up to 7 attempts within a single helm install). If an earlier attempt
    # already ran `bao operator init` (for example it initialized the cluster
    # and then failed later during the raft bootstrap), re-running it returns
    # HTTP 400 "Vault is already initialized" and the attempt bails out, so the
    # bootstrap can never finish. Treat an already-initialized cluster as
    # success and reuse the stored secrets, letting the retry pick up where the
    # previous attempt stopped.
    local init_status=$(kubectl exec ${statefulset}-0 -c openbao -n ${namespace} -- \
        bao status -format=json 2>/dev/null | jq -r '.initialized')
    if [ "${init_status}" = "true" ]; then
        log_info "OpenBao cluster already initialized; skipping 'bao operator init'"
        # "Secret exists" is not a sufficient check: the unseal secret is
        # pre-created empty and only patched with the real key later, so
        # present-but-empty is a reachable state. These helpers return "" both
        # when the secret is missing and when its value is empty. Without this
        # check we would fall through to `bao operator unseal ""` and report a
        # far less actionable error. An initialized cluster whose keys were
        # never persisted cannot be recovered.
        local stored_unseal_key=$(get_unseal_key "${namespace}" "${statefulset}")
        local stored_root_token=$(get_root_token "${namespace}" "${statefulset}")
        if [ -z "${stored_unseal_key}" ] || [ -z "${stored_root_token}" ]; then
            log_error "Cluster is initialized but its stored unseal key and/or root token are missing or empty."
            log_error "The generated keys are unrecoverable. Run ./cleanup.sh and reinstall."
            return 1
        fi
        log_success "Reusing unseal key and root token from '${statefulset}-unseal' and '${statefulset}-root-token'"
        return 0
    fi

    log_info "Initializing OpenBao cluster"
    local init_output=$(kubectl exec ${statefulset}-0 -c openbao -n ${namespace} -- \
        bao operator init \
        -key-shares=1 \
        -key-threshold=1 \
        -format=json)

    # Extract keys
    local unseal_key=$(echo ${init_output} | jq -r '.unseal_keys_b64[0]')
    local root_token=$(echo ${init_output} | jq -r '.root_token')

    # Check if unseal key is empty
    if [ -z "${unseal_key}" ]; then
        log_error "Failed to get unseal key from initialization output"
        return 1
    fi

    # Update the secret with the new unseal key
    kubectl patch secret ${statefulset}-unseal \
        --patch "data:
    unseal_key: $(echo -n "${unseal_key}" | base64)" \
        -n ${namespace}

    log_info "Updated Kubernetes secret '${statefulset}-unseal' with unseal key"

    # Store root token in a new secret
    log_info "Creating secret '${statefulset}-root-token' with root token..."
    kubectl create secret generic ${statefulset}-root-token \
        -n ${namespace} \
        --from-literal=root_token=${root_token}
}

get_unseal_key() {
    local namespace=$1
    local statefulset=$2
    kubectl get secret ${statefulset}-unseal -n ${namespace} -o jsonpath='{.data.unseal_key}' | base64 -d
}

# Wait until the bootstrap node (pod 0) has won leader election and can serve
# raft challenges. After being unsealed the node must become the active leader
# before any peer joins; joining earlier returns HTTP 500 "failed to join raft
# cluster: failed to get raft challenge". This replaces a fixed `sleep 5` that
# raced leader election.
wait_for_active_leader() {
    local namespace=$1
    local statefulset=$2
    local timeout=${3:-120}
    local pod="${statefulset}-0"
    local end=$((SECONDS + timeout))

    log_info "Waiting for ${pod} to become the active Raft leader (timeout: ${timeout}s)..."
    while true; do
        local status_json=$(kubectl exec "${pod}" -c openbao -n "${namespace}" -- \
            bao status -format=json 2>/dev/null)
        # Read these with plain accessors, never `.field // empty`: jq's `//`
        # treats boolean false the same as null, so `.sealed // empty` yields
        # "" for an unsealed node and this loop would never see sealed=false.
        local initialized=$(echo "${status_json}" | jq -r '.initialized')
        local sealed=$(echo "${status_json}" | jq -r '.sealed')
        local ha_mode=$(echo "${status_json}" | jq -r '.ha_mode')
        if [ "${initialized}" = "true" ] && [ "${sealed}" = "false" ] && [ "${ha_mode}" = "active" ]; then
            log_success "${pod} is unsealed and active (ha_mode=active)"
            return 0
        fi
        if [ $SECONDS -gt $end ]; then
            log_error "Timeout waiting for ${pod} to become active leader (initialized=${initialized:-?}, sealed=${sealed:-?}, ha_mode=${ha_mode:-?})"
            return 1
        fi
        log_info "  ${pod} not ready yet (sealed=${sealed:-?}, ha_mode=${ha_mode:-?}); retrying in 3s..."
        sleep 3
    done
}

# True when the pod is already unsealed, and therefore already a working member
# of the raft cluster. Lets a retry skip peers a previous attempt finished.
is_pod_unsealed() {
    local namespace=$1
    local pod=$2
    local sealed=$(kubectl exec "${pod}" -c openbao -n "${namespace}" -- \
        bao status -format=json 2>/dev/null | jq -r '.sealed')
    [ "${sealed}" = "false" ]
}

# Join a peer to the raft cluster, retrying the transient "failed to get raft
# challenge" 500 that can still occur for a few seconds after the leader goes
# active. Treats an already-joined node as success so retries are idempotent.
raft_join_with_retry() {
    local namespace=$1
    local statefulset=$2
    local pod=$3
    local attempts=${4:-12}
    local i=1
    # Declare before assigning: with `local out=$(...)` the `local` builtin is
    # the executed command, so $? would capture the assignment status (always 0)
    # rather than the kubectl/bao exit code, masking real join failures.
    local out rc
    while [ $i -le $attempts ]; do
        out=$(kubectl exec "${pod}" -c openbao -n "${namespace}" -- \
            bao operator raft join "http://${statefulset}-0.${statefulset}-internal:8200" 2>&1)
        rc=$?
        echo "${out}"
        if [ $rc -eq 0 ]; then
            return 0
        fi
        if echo "${out}" | grep -qi "already"; then
            log_info "${pod} is already a member of the Raft cluster"
            return 0
        fi
        log_warn "raft join attempt ${i}/${attempts} for ${pod} failed; retrying in 5s..."
        sleep 5
        i=$((i + 1))
    done
    return 1
}

# Step 4: Unseal the cluster
unseal_cluster() {
    local namespace=$1
    local statefulset=$2
    local unseal_key=$(get_unseal_key "${namespace}" "${statefulset}")

    log_section "Unsealing OpenBao cluster"

    # First unseal the primary node (pod 0). Unsealing an already-unsealed node
    # is a no-op that exits 0, so this is safe to re-run.
    log_info "Unsealing primary pod ${statefulset}-0"
    if ! kubectl exec ${statefulset}-0 -c openbao -n ${namespace} -- \
        bao operator unseal ${unseal_key}; then
        log_error "Failed to unseal primary pod ${statefulset}-0"
        return 1
    fi

    # Wait for the primary to win leader election and start serving raft
    # challenges before any peer joins.
    if ! wait_for_active_leader "${namespace}" "${statefulset}"; then
        return 1
    fi

    # Join and unseal remaining pods
    for i in {1..2}; do
        local pod="${statefulset}-${i}"
        if is_pod_unsealed "${namespace}" "${pod}"; then
            log_info "Pod ${pod} already unsealed and joined; skipping"
            continue
        fi

        log_info "Joining pod ${pod} to Raft cluster"
        if ! raft_join_with_retry "${namespace}" "${statefulset}" "${pod}"; then
            log_error "Failed to join pod ${pod} to Raft cluster"
            return 1
        fi

        log_info "Unsealing pod ${pod}"
        if ! kubectl exec ${pod} -c openbao -n ${namespace} -- \
            bao operator unseal ${unseal_key}; then
            log_error "Failed to unseal pod ${pod}"
            return 1
        fi
    done
    log_info "All pods joined Raft cluster and unsealed successfully"
}

get_and_save_jwt_signing_key() {
    local namespace=$1
    local statefulset=$2
    local jwt_pem_secret_name="cluster-jwt"

    log_section "Getting and saving Kubernetes JWT Signing key"

    log_info "Fetching JWT signing key from Kubernetes API"

    # Get the kubernetes service host ip from primary pod
    local kub_api_ip=$(kubectl exec ${statefulset}-0 -c openbao -n ${namespace} -- \
        printenv KUBERNETES_SERVICE_HOST)

    # get svc account token to access kubernetes api
    local svc_token=$(kubectl exec openbao-server-0 -c openbao -n ${namespace} -- \
        cat /var/run/secrets/kubernetes.io/serviceaccount/token)

    # call kubernetes api to get the jwt signing key and decode it to a pem
    local jwt_pem=$(kubectl exec ${statefulset}-0 -c openbao -n ${namespace} -- \
        curl -s --cacert /var/run/secrets/kubernetes.io/serviceaccount/ca.crt --header "Authorization: Bearer ${svc_token}" \
        "https://${kub_api_ip}/openid/v1/jwks" | jq ".keys[0]" > /tmp/cluster.jwks && jwker /tmp/cluster.jwks | base64)

    # Check if jwt_pem is empty
    if [ -z "${jwt_pem}" ]; then
        log_error "Failed to get JWT signing key from Kubernetes API, or there was a problem with the jwker tool"
        return 1
    fi

    # write to file to avoid string errors when creating secret
    echo "${jwt_pem}" > /tmp/jwt.pem
    trap 'rm -f /tmp/jwt.pem /tmp/cluster.jwks' EXIT

    log_info "Creating/Updating JWT signing key in kubernetes secrets"

    # Check if secret exists first
    if ! kubectl get secret "${jwt_pem_secret_name}" -n ${namespace} >/dev/null 2>&1; then
        log_info "Saving JWT signing key to secret '${jwt_pem_secret_name}'"
        if ! kubectl create secret generic ${jwt_pem_secret_name} \
            -n ${namespace} \
            --from-file=pem=/tmp/jwt.pem; then
            log_error "Failed to create secret '${jwt_pem_secret_name}'"
            return 1
        fi
        log_success "JWT signing key saved to secret '${jwt_pem_secret_name}'"
        return 0
    else
        log_info "Kubernetes JWT Signing key already exists in secret '${jwt_pem_secret_name}', patching..."
        if ! kubectl patch secret "${jwt_pem_secret_name}" \
            -n ${namespace} \
            --patch "{\"data\":{\"pem\":\"${jwt_pem}\"}}"; then
            log_error "Failed to patch secret at '${jwt_pem_secret_name}'"
            return 1
        fi
        log_success "JWT signing key updated at '${jwt_pem_secret_name}'"
        return 0
    fi
}

register_and_enable_jwt_plugin() {
    local namespace=$1
    local statefulset=$2
    local root_token=$(get_root_token "$namespace" "$statefulset")
    local plugin_dir="/openbao/plugins"
    local plugin_name="vault-plugin-secrets-jwt"
    local pod_name="${statefulset}-0"

    log_section "Enabling JWT Secret Engine"

    if kubectl exec -n $namespace $pod_name -c openbao -- \
        env BAO_TOKEN="$root_token" \
        bao plugin list | grep "$plugin_name"; then
        log_success "Plugin '$plugin_name' already registered"
        return 0
    fi

    # Step 1: Verify Plugin Binary Exists
    log_step "Verifying if plugin binary exists at $plugin_dir/$plugin_name..."
    if ! kubectl exec -n $namespace $pod_name -c openbao -- test -f "$plugin_dir/$plugin_name"; then
        log_error "Plugin binary not found at $plugin_dir/$plugin_name. Check your container image tag."
        return 1
    fi

    # Step 2: Calculate SHA256 Checksum of the Plugin Binary
    log_step "Calculating SHA256 checksum for $plugin_name..."
    local plugin_sha256=$(kubectl exec -n $namespace $pod_name -c openbao -- sha256sum "$plugin_dir/$plugin_name" | awk '{print $1}')
    if [[ -z "$plugin_sha256" ]]; then
        log_error "Failed to calculate SHA256 checksum for $plugin_name."
        return 1
    fi
    log_success "JWT Plugin SHA256 checksum: $plugin_sha256"

    # Step 3: Register the Plugin
    log_step "Registering plugin '$plugin_name'..."
    kubectl exec -n $namespace $pod_name -c openbao -- \
        env BAO_TOKEN="$root_token" \
        bao plugin register \
        -sha256="$plugin_sha256" \
        -command="$plugin_name" \
        secret "$plugin_name"
    if [[ $? -ne 0 ]]; then
        log_error "Failed to register plugin '$plugin_name'."
        return 1
    fi
    log_success "Plugin '$plugin_name' registered successfully."
}

log_section "Deploying OpenBao cluster in namespace '${namespace}' and statefulset '${statefulset}' using ${install_method} method"

if [ "${install_method}" = "script" ]; then
    if ! pre_checks ${namespace} ${statefulset}; then
        log_error "Failed pre-checks"
        exit 1
    else
        log_success "pre-checks completed"
    fi
else
    log_info "Skipping pre-checks as install_method is not 'script'"
fi

if [ "${install_method}" = "script" ]; then
    if ! create_empty_secret ${namespace} ${statefulset}; then
        exit 1
    else
        log_success "created empty unseal secret on k8s"
    fi
else
    log_info "Skipping local installation as install_method is not 'script'"
fi

if [ "${install_method}" = "script" ]; then
    if ! install_openbao ${namespace} ${statefulset}; then
        exit 1
    else
        log_success "Successfully deployed the cluster"
    fi
else
    log_info "Skipping local installation as install_method is not 'script'"
fi

if ! initialize_cluster ${namespace} ${statefulset}; then
    exit 1
else
    log_success "Successfully initialized the cluster"
fi

if ! unseal_cluster ${namespace} ${statefulset}; then
    exit 1
else
    log_success "Successfully unsealed the cluster"
fi

if ! get_and_save_jwt_signing_key ${namespace} ${statefulset}; then
    exit 1
else
    log_success "Successfully saved JWT signing key to kubernetes secrets"
fi

if ! register_and_enable_jwt_plugin ${namespace} ${statefulset}; then
    exit 1
else
    log_success "Successfully enabled JWT plugin"
fi

log_section "Install Successful"

log_success "Successfully deployed the OpenBao cluster to ${namespace} namespace"
