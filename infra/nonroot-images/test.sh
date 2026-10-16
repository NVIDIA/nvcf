#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

registry="${REGISTRY:-local/nvcf}"
version="${VERSION:-dev}"
for name in nvcf-nats-server-nonroot nvcf-nats-config-reloader-nonroot nvcf-k8s-helper-nonroot; do
    image="${registry}/${name}:${version}"
    actual_user="$(docker image inspect --format '{{.Config.User}}' "$image")"
    if [[ "$actual_user" != "1000:1000" ]]; then
        echo "${name}: expected numeric OCI user 1000:1000, got ${actual_user}" >&2
        exit 1
    fi
done

docker run --rm --network none --read-only --cap-drop ALL \
    "${registry}/nvcf-nats-server-nonroot:${version}" --version

docker run --rm --network none --read-only --cap-drop ALL \
    "${registry}/nvcf-nats-config-reloader-nonroot:${version}" --help >/dev/null

docker run --rm --network none --read-only --cap-drop ALL \
    --tmpfs /tmp:rw,nosuid,nodev,mode=1777 \
    "${registry}/nvcf-k8s-helper-nonroot:${version}" sh -ec '
        test "$(id -u)" = 1000
        test "$(id -g)" = 1000
        test "$HOME" = /tmp
        mkdir -p "$XDG_CACHE_HOME"
        touch "$HOME/.helper-write-test"
        kubectl version --client >/dev/null
        helm version --short >/dev/null
        curl --version >/dev/null
        jq --version >/dev/null
    '
