#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


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

# The chart has already installed OpenBao before this hook starts.
if [ "${install_method}" = "$INSTALL_METHOD_SCRIPT" ] && ! check_helm; then
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

# Resolve the seal mode to use for init and unseal, into the global
# RESOLVED_SEAL_MODE ("auto" or "shamir"). Returns non-zero (fail closed) on an
# unreadable status or a mismatch, rather than guessing a path that could
# discard the only copy of a key.
#
# AUTO_UNSEAL is the chart's declared intent (server.autoUnseal.enabled), which
# also gates whether the unseal Secret exists. `bao status` reports the server's
# actual seal: recovery_seal=true for an auto-unseal (KMS or HSM) seal such as
# awskms, azurekeyvault, gcpckms, transit, or pkcs11; false for Shamir. These
# must agree: a flag set without the matching seal stanza (or the reverse) would
# otherwise take a path whose Secret does not exist.
#
# `bao status` exits 0 when unsealed and 2 when sealed; both return valid JSON.
# Any other exit (1) is a real error. jq -r is used, not jq -e, because a valid
# `false` makes jq -e exit non-zero.
RESOLVED_SEAL_MODE=""
resolve_seal_mode() {
    local namespace=$1
    local statefulset=$2
    local declared="${AUTO_UNSEAL:-false}"

    local out rc
    out=$(kubectl exec ${statefulset}-0 -c openbao -n ${namespace} -- \
        bao status -format=json 2>/dev/null)
    rc=$?
    if [ "${rc}" != "0" ] && [ "${rc}" != "2" ]; then
        log_error "Could not read seal status from ${statefulset}-0 (bao status exit ${rc})"
        return 1
    fi

    # Parse recovery_seal, failing closed on unparseable or non-boolean status.
    # A missing field stays on the Shamir path (matches the pre-existing default
    # for an uninitialized server); only an explicit boolean true selects auto.
    # jq -r (not -e) is used so a valid false does not look like a jq failure.
    local recovery_seal
    recovery_seal=$(printf '%s' "${out}" | jq -rs '
        if (length != 1) or ((.[0] | type) != "object") then error("invalid status")
        elif (.[0] | has("recovery_seal") | not) then false
        elif (.[0].recovery_seal | type) != "boolean" then error("recovery_seal not boolean")
        else .[0].recovery_seal end' 2>/dev/null) || {
        log_error "Could not parse seal status from ${statefulset}-0"
        return 1
    }
    local server_mode="shamir"
    if [ "${recovery_seal}" = "true" ]; then
        server_mode="auto"
    fi

    if [ "${declared}" = "true" ] && [ "${server_mode}" != "auto" ]; then
        log_error "server.autoUnseal.enabled is set but the server reports no auto-unseal seal. Add the seal stanza to server.ha.raft.config (see values-autounseal.yaml.example)."
        return 1
    fi
    if [ "${declared}" != "true" ] && [ "${server_mode}" = "auto" ]; then
        log_error "The server reports an auto-unseal seal but server.autoUnseal.enabled is not set. Enable it so the unseal Secret and init path match the seal."
        return 1
    fi

    RESOLVED_SEAL_MODE="${server_mode}"
    return 0
}

# Runtime version-skew check: verify that the auto-unseal-sidecar container's
# image tag matches the openbao server container's image tag in the live
# StatefulSet spec. This complements the template-time guard in
# helm/templates/validate-sidecar-version.yaml: the template guard catches
# misconfigured values; this runtime check catches drift introduced by
# mutating webhooks, manual kubectl edits, or partial upgrades.
check_sidecar_version() {
    local namespace=$1
    local statefulset=$2

    log_section "Validating openbao server / auto-unseal-sidecar image version match"

    local sts_path='{.spec.template.spec.containers'
    local server_image
    server_image=$(kubectl get statefulset "${statefulset}" -n "${namespace}" \
        -o jsonpath="${sts_path}"'[?(@.name=="openbao")].image}' 2>/dev/null)
    local sidecar_image
    sidecar_image=$(kubectl get statefulset "${statefulset}" -n "${namespace}" \
        -o jsonpath="${sts_path}"'[?(@.name=="auto-unseal-sidecar")].image}' 2>/dev/null)

    if [ -z "${server_image}" ]; then
        log_error "could not read 'openbao' container image from StatefulSet '${statefulset}'"
        return 1
    fi
    if [ -z "${sidecar_image}" ]; then
        log_warn "no 'auto-unseal-sidecar' container in StatefulSet '${statefulset}'; skipping version check"
        return 0
    fi

    local server_tag="${server_image##*:}"
    local sidecar_tag="${sidecar_image##*:}"

    if [ "${server_tag}" != "${sidecar_tag}" ]; then
        log_error "openbao server / auto-unseal-sidecar image tag mismatch:"
        log_error "  server  image : ${server_image}"
        log_error "  sidecar image : ${sidecar_image}"
        log_error "  server  tag   : ${server_tag}"
        log_error "  sidecar tag   : ${sidecar_tag}"
        log_error ""
        log_error "A bao client/server version skew here silently corrupts bao state during"
        log_error "the post-install raft bootstrap. See helm/templates/validate-sidecar-version.yaml"
        log_error "for the upstream OpenBao change refs that explain why"
        log_error "(openbao/openbao#1986, #2331, #2574, #1518, #1433)."
        log_error ""
        log_error "Update the auto-unseal-sidecar image tag in your values overlay to match"
        log_error "the server tag '${server_tag}' and reinstall."
        return 1
    fi

    log_success "server and sidecar images both at tag '${server_tag}'"
    return 0
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

    log_info "Initializing OpenBao cluster"
    if ! resolve_seal_mode "${namespace}" "${statefulset}"; then
        return 1
    fi
    local init_output
    local root_token
    if [ "${RESOLVED_SEAL_MODE}" = "auto" ]; then
        # Auto-unseal seal: initialize with recovery keys, not an unseal key.
        # Recovery keys regenerate the root token and rekey; they never unseal
        # (the seal does that), so no unseal key or unseal Secret is needed.
        local recovery_shares="${RECOVERY_SHARES:-5}"
        local recovery_threshold="${RECOVERY_THRESHOLD:-3}"
        log_info "Auto-unseal seal detected; initializing with ${recovery_shares} recovery shares (threshold ${recovery_threshold})"
        if ! init_output=$(kubectl exec ${statefulset}-0 -c openbao -n ${namespace} -- \
            bao operator init \
            -recovery-shares=${recovery_shares} \
            -recovery-threshold=${recovery_threshold} \
            -format=json); then
            log_error "bao operator init failed. If the server is now initialized its keys were not captured; tear down with cleanup.sh and reinitialize."
            return 1
        fi
        root_token=$(echo ${init_output} | jq -r '.root_token')
        if [ -z "${root_token}" ] || [ "${root_token}" = "null" ]; then
            log_error "Failed to get root token from initialization output"
            return 1
        fi
        # Persist the recovery keys before anything else can discard them: once
        # the server is initialized, `operator init` cannot reproduce them, so a
        # failed write here would lose the only copy and make break-glass
        # recovery impossible. Retry, and fail the init if it cannot be stored
        # rather than reporting success without keys.
        local recovery_keys
        recovery_keys=$(echo ${init_output} | jq -c '.recovery_keys_b64')
        if [ -z "${recovery_keys}" ] || [ "${recovery_keys}" = "null" ]; then
            log_error "Initialization did not return recovery keys (.recovery_keys_b64 is null). Aborting."
            return 1
        fi
        local persisted=false
        for attempt in 1 2 3; do
            if kubectl create secret generic ${statefulset}-recovery-keys \
                -n ${namespace} \
                --from-literal=recovery_keys_b64="${recovery_keys}" \
                --dry-run=client -o yaml | kubectl apply -f -; then
                persisted=true
                break
            fi
            log_warn "Could not persist recovery keys (attempt ${attempt}/3); retrying..."
            sleep 3
        done
        if [ "${persisted}" != "true" ]; then
            log_error "Failed to store recovery keys in Secret '${statefulset}-recovery-keys'. The server is initialized but the keys are not saved, so break-glass recovery is impossible. Tear down with cleanup.sh and reinitialize."
            return 1
        fi
        log_warn "Recovery keys stored in Secret '${statefulset}-recovery-keys'. Export them to a break-glass store and delete this Secret."
    else
        # Shamir seal: a single unseal key, stored for the auto-unseal sidecar.
        if ! init_output=$(kubectl exec ${statefulset}-0 -c openbao -n ${namespace} -- \
            bao operator init \
            -key-shares=1 \
            -key-threshold=1 \
            -format=json); then
            log_error "bao operator init failed. If the server is now initialized its keys were not captured; tear down with cleanup.sh and reinitialize."
            return 1
        fi
        local unseal_key=$(echo ${init_output} | jq -r '.unseal_keys_b64[0]')
        root_token=$(echo ${init_output} | jq -r '.root_token')
        if [ -z "${unseal_key}" ] || [ "${unseal_key}" = "null" ]; then
            log_error "Failed to get unseal key from initialization output"
            return 1
        fi
        if ! kubectl patch secret ${statefulset}-unseal \
            --patch "data:
    unseal_key: $(echo -n "${unseal_key}" | base64)" \
            -n ${namespace}; then
            log_error "Failed to store unseal key in Secret '${statefulset}-unseal'. The server is initialized but the unseal key is not saved; tear down with cleanup.sh and reinitialize."
            return 1
        fi
        log_info "Updated Kubernetes secret '${statefulset}-unseal' with unseal key"
    fi

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

# Step 4: Unseal the cluster
unseal_cluster() {
    local namespace=$1
    local statefulset=$2

    log_section "Unsealing OpenBao cluster"

    if ! resolve_seal_mode "${namespace}" "${statefulset}"; then
        return 1
    fi
    if [ "${RESOLVED_SEAL_MODE}" = "auto" ]; then
        # The auto-unseal seal unseals each node on start, and retry_join in the
        # raft config joins the peers. No manual unseal or raft join is needed;
        # wait for every pod to report unsealed.
        log_info "Auto-unseal seal detected; waiting for all pods to unseal"
        local end=$((SECONDS + 120))
        for i in {0..2}; do
            while [ "$(kubectl exec ${statefulset}-${i} -c openbao -n ${namespace} -- bao status -format=json 2>/dev/null | jq -r '.sealed' 2>/dev/null)" != "false" ]; do
                if [ $SECONDS -gt $end ]; then
                    log_error "Timeout waiting for pod ${statefulset}-${i} to auto-unseal"
                    return 1
                fi
                log_info "Waiting for ${statefulset}-${i} to auto-unseal..."
                sleep 5
            done
        done
        log_info "All pods auto-unsealed"
        return 0
    fi

    local unseal_key=$(get_unseal_key "${namespace}" "${statefulset}")

    # First unseal the primary node (pod 0)
    log_info "Unsealing primary pod ${statefulset}-0"
    if ! kubectl exec ${statefulset}-0 -n ${namespace} -- \
        bao operator unseal ${unseal_key}; then
        log_error "Failed to unseal primary pod ${statefulset}-0"
        return 1
    fi

    # Wait a moment for the primary to be ready
    sleep 5

    # Join and unseal remaining pods
    for i in {1..2}; do
        log_info "Joining pod ${statefulset}-${i} to Raft cluster"
        if ! kubectl exec ${statefulset}-${i} -c openbao -n ${namespace} -- \
            bao operator raft join http://${statefulset}-0.${statefulset}-internal:8200; then
            log_error "Failed to join pod ${statefulset}-${i} to Raft cluster"
            return 1
        fi

        log_info "Unsealing pod ${statefulset}-${i}"
        if ! kubectl exec ${statefulset}-${i} -c openbao -n ${namespace} -- \
            bao operator unseal ${unseal_key}; then
            log_error "Failed to unseal pod ${statefulset}-${i}"
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
    local svc_token=$(kubectl exec ${statefulset}-0 -c openbao -n ${namespace} -- \
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

if ! check_sidecar_version ${namespace} ${statefulset}; then
    log_error "Aborting before any bao operations due to sidecar/server version skew"
    exit 1
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
