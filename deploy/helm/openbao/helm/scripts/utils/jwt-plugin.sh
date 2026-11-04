#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

register_and_enable_jwt_plugin() {
    local namespace=$1 statefulset=$2 root_token
    local mode=${JWT_PLUGIN_LIFECYCLE_MODE:-legacy}
    local plugin_name=vault-plugin-secrets-jwt
    local plugin_dir=/openbao/plugins
    local pod_name="${statefulset}-0"

    case $mode in
        bridge|versioned)
            root_token=$(get_root_token "$namespace" "$statefulset") || return 1
            kubectl exec -n "$namespace" "$pod_name" -c openbao -- \
                env BAO_TOKEN="$root_token" jwt-plugin-lifecycle wait-ready || return 1
            log_success 'Declarative JWT plugin registration is ready.'
            return 0
            ;;
        legacy) ;;
        *) log_error 'JWT lifecycle mode must be legacy, bridge, or versioned'; return 1 ;;
    esac

    if ! kubectl exec -n "$namespace" "$pod_name" -c openbao -- \
        sh -ec 'test ! -r /openbao/jwt-plugin-versions.env'; then
        log_error 'Versioned OpenBao images require bridge or versioned lifecycle mode'
        return 1
    fi
    root_token=$(get_root_token "$namespace" "$statefulset") || return 1
    if ! kubectl exec -n "$namespace" "$pod_name" -c openbao -- \
        test -x "$plugin_dir/$plugin_name"; then
        log_error "Plugin binary not found at $plugin_dir/$plugin_name"
        return 1
    fi
    local plugin_sha256 catalog
    plugin_sha256=$(kubectl exec -n "$namespace" "$pod_name" -c openbao -- \
        sha256sum "$plugin_dir/$plugin_name" | awk '{print $1}') || return 1
    [[ $plugin_sha256 =~ ^[0-9a-f]{64}$ ]] || {
        log_error 'Failed to calculate the legacy JWT plugin SHA256'
        return 1
    }
    # A versioned entry with the same name cannot satisfy legacy registration.
    if catalog=$(kubectl exec -n "$namespace" "$pod_name" -c openbao -- \
        env BAO_TOKEN="$root_token" bao plugin info -format=json secret "$plugin_name") && \
        jq -e --arg sha "$plugin_sha256" --arg command "$plugin_name" \
            '.version == "" and .command == $command and .sha256 == $sha' <<< "$catalog" >/dev/null; then
        log_success "Unversioned plugin '$plugin_name' already registered"
        return 0
    fi
    if ! kubectl exec -n "$namespace" "$pod_name" -c openbao -- \
        env BAO_TOKEN="$root_token" bao plugin register -sha256="$plugin_sha256" \
        -command="$plugin_name" secret "$plugin_name"; then
        log_error "Failed to register plugin '$plugin_name'"
        return 1
    fi
    log_success "Plugin '$plugin_name' registered successfully."
}
