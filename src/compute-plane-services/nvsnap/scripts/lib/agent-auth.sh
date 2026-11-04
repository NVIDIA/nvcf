# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Shared bearer-token helper for scripts that call the agent HTTP API.
#
# Once the agent runs with --auth-mode=required it gates every route except
# /health and /metrics (nvsnap#486). A caller that omits the header gets back a
# bare "unauthorized" with no indication of which credential is missing, so
# every script that talks to the API has to send the token (nvsnap#734).
#
# An absent Secret is the normal case, not an error: the chart only creates it
# when agent.auth.enabled is set, and these scripts must keep working against a
# cluster installed without auth. Callers therefore get an empty header list
# rather than a failure, and the same command works in both configurations.
#
# Sourced, not executed — no shebang, no set -e (that would leak into callers).
#
# Usage:
#     source "$(dirname "${BASH_SOURCE[0]}")/lib/agent-auth.sh"
#     nvsnap_agent_auth_args                 # populates NVSNAP_AUTH_ARGS
#     curl -s "${NVSNAP_AUTH_ARGS[@]}" "$url"
#
# For calls made *inside* the agent container via kubectl exec, prefer the
# NVSNAP_AGENT_TOKEN env var the chart already injects there over piping the
# token in from outside.

# Print the agent API token, or nothing when auth is not configured.
# Namespace: explicit argument, else $NAMESPACE, else nvsnap-system.
nvsnap_agent_token() {
    local ns="${1:-${NAMESPACE:-nvsnap-system}}"
    kubectl get secret nvsnap-agent-token -n "$ns" \
        -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null || true
}

# Populate NVSNAP_AUTH_ARGS with the curl flags carrying the token, or leave it
# empty when there is no token to send. Kept as an array so the header survives
# word-splitting intact.
nvsnap_agent_auth_args() {
    local token
    token=$(nvsnap_agent_token "$@")
    NVSNAP_AUTH_ARGS=()
    if [ -n "$token" ]; then
        NVSNAP_AUTH_ARGS=(-H "Authorization: Bearer $token")
    fi
}
