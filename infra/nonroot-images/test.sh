#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Container scripts deliberately expand their variables inside the container.
# shellcheck disable=SC2016
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
registry="${REGISTRY:-local/nvcf}"
version="${VERSION:-dev}"
platform="${PLATFORM:?Set PLATFORM to linux/amd64 or linux/arm64}"
run=(docker run --rm --platform "$platform" --network none --read-only --cap-drop ALL
     --security-opt no-new-privileges --tmpfs "/tmp:rw,nosuid,nodev,mode=1777" --entrypoint /bin/sh)

for target in k8s-helper nats-box; do
    image="${registry}/nvcf-${target}-nonroot:${version}"
    upstream="$(awk '$1 == "FROM" { base = $2 } END { print base }' "Dockerfile.${target}")"
    upstream_digest="$(docker buildx imagetools inspect "$upstream" --raw | jq -r --arg arch "${platform#linux/}" ' .manifests[] | select(.platform.os == "linux" and .platform.architecture == $arch) | .digest')"
    [[ "$upstream_digest" =~ ^sha256:[a-f0-9]{64}$ ]]
    upstream="${upstream%@*}@${upstream_digest}"
    test "$(docker image inspect --format '{{.Config.User}}' "$image")" = 1000:1000
    "${run[@]}" "$image" -ec '
        test "$(id -u):$(id -g)" = 1000:1000
        test "$HOME" = /tmp
        mkdir -p "$XDG_CACHE_HOME"
        touch "$HOME/.helper-write-test" "$XDG_CACHE_HOME/.helper-write-test"
        apk info --installed "brotli-libs=1.2.1-r0"
    '
    if [[ "$target" == k8s-helper ]]; then
        "${run[@]}" "$image" -ec '
            sha256sum -c /usr/share/nvcf/kubectl.sha256
            grep -Eq "dep[[:space:]]+golang.org/x/net[[:space:]]+v0.60.0([[:space:]]|$)" /usr/share/nvcf/kubectl.buildinfo
        '
        tools=(helm curl jq bash)
        "${run[@]}" --env KUBECTL_VERSION="$(cat kubectl-version.txt)" "$image" -ec '
            kubectl version --client -o json | jq -e --arg version "v$KUBECTL_VERSION" ".clientVersion.gitVersion == \$version and .clientVersion.gitTreeState == \"dirty\" and .clientVersion.goVersion == \"go1.26.10\""
            kubectl create configmap receipt --from-literal=installed_stack_version=test --dry-run=client -o yaml >/tmp/receipt.yaml
            helm version --short
            curl --version
            bash -ec '\''test "$(printf test | base64 -w0 | base64 -d)" = test'\''
        '
    else
        tools=(nats nsc nk)
        "${run[@]}" "$image" -ec '
            nats --version
            nsc --version
            nk -v
            nats context save helper-test --server nats://127.0.0.1:4222
            nats context info helper-test >/dev/null
            nsc generate nkey --user >/dev/null
        '
    fi
    # Only kubectl is deliberately refreshed; other upstream tools stay intact.
    hash_tools='for tool do sha256sum "$(command -v "$tool")"; done'
    upstream_hashes="$("${run[@]}" "$upstream" -ec "$hash_tools" sh "${tools[@]}")"
    image_hashes="$("${run[@]}" "$image" -ec "$hash_tools" sh "${tools[@]}")"
    test "$upstream_hashes" = "$image_hashes"
    package_list='apk list --installed 2>/dev/null | sed "/^brotli-libs-/d" | sort'
    test "$("${run[@]}" "$upstream" -ec "$package_list")" = "$("${run[@]}" "$image" -ec "$package_list")"
    echo "$target: fixed Brotli, numeric identity, writable cache and upstream tool bytes verified on $platform"
done
