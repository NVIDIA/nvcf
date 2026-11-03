#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# An empty result preserves the published legacy migration behavior. Versioned
# deployments mount the manifest copied from their actual server image.
jwt_plugin_version() {
    local version=${BAO_JWT_PLUGIN_VERSION:-}
    if [[ -z $version && -n ${BAO_JWT_PLUGIN_VERSIONS_FILE:-} ]]; then
        [[ -r $BAO_JWT_PLUGIN_VERSIONS_FILE ]] || {
            echo 'JWT plugin version manifest is missing' >&2
            return 1
        }
        version=$(
            source "$BAO_JWT_PLUGIN_VERSIONS_FILE"
            printf '%s' "${JWT_PLUGIN_TARGET_VERSION:-}"
        )
        [[ -n $version ]] || { echo 'JWT target version is missing' >&2; return 1; }
    fi
    if [[ -z $version && ${JWT_PLUGIN_LIFECYCLE_MODE:-legacy} == versioned ]]; then
        echo 'Versioned JWT mounts require the server image version manifest' >&2
        return 1
    fi
    if [[ -n $version && ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
        echo 'Invalid JWT plugin version' >&2
        return 1
    fi
    printf '%s' "$version"
}
