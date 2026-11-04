#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
[[ $# == 2 ]] || { echo 'Usage: runtime-server.sh {init|restart} {legacy|bridge|target-only}' >&2; exit 1; }
action=$1
layout=$2
case $layout in legacy|bridge|target-only) ;; *) exit 1 ;; esac
state=/tmp/nvcf-jwt-runtime
export BAO_ADDR=http://127.0.0.1:18200
source /openbao/jwt-plugin-versions.env
umask 077
case $action in
    init)
        # The caller supplies a new isolated container. Never reuse persisted state.
        mkdir "$state"
        mkdir "$state/data"
        for package in legacy bridge target-only; do
            JWT_PACKAGE_OUTPUT="$state/$package" \
                JWT_LEGACY_BINARY=/openbao/plugins/vault-plugin-secrets-jwt \
                JWT_TARGET_BINARY="/openbao/plugins/vault-plugin-secrets-jwt-${JWT_PLUGIN_TARGET_VERSION}" \
                JWT_INCLUDE_LEGACY=$([[ $package == target-only ]] && echo false || echo true) \
                JWT_VERSIONS_FILE=/openbao/jwt-plugin-versions.env \
                sh /test/package-jwt-plugins.sh
        done
        ;;
    restart)
        kill "$(cat "$state/server.pid")"
        stopped=false
        for ((attempt=0; attempt<60; attempt++)); do
            if ! curl -s --connect-timeout 1 "$BAO_ADDR/v1/sys/health" >/dev/null; then
                stopped=true
                break
            fi
            sleep 0.5
        done
        [[ $stopped == true ]] || { echo 'Server did not stop' >&2; exit 1; }
        ;;
    *) exit 1 ;;
esac
printf '%s\n' "$layout" > "$state/layout"
cat > "$state/server.hcl" <<EOF
disable_mlock = true
api_addr = "$BAO_ADDR"
storage "file" { path = "$state/data" }
listener "tcp" {
  address = "127.0.0.1:18200"
  tls_disable = true
}
EOF
if [[ $layout == legacy ]]; then
    # Start without declarative registration to seed the pre-bridge catalog.
    printf 'plugin_directory = "%s/legacy/plugins"\n' "$state" >> "$state/server.hcl"
    server_binary=/test/bao-legacy
else
    sed "s|/openbao/plugins|$state/$layout/plugins|g" \
        "$state/$layout/plugin-config/jwt.hcl" >> "$state/server.hcl"
    server_binary=bao
fi
"$server_binary" server -config="$state/server.hcl" > "$state/server-$layout.log" 2>&1 &
echo $! > "$state/server.pid"
ready=false
for ((attempt=0; attempt<60; attempt++)); do
    if curl -s --fail "$BAO_ADDR/v1/sys/init" >/dev/null; then
        ready=true
        break
    fi
    sleep 0.5
done
[[ $ready == true ]] || { cat "$state/server-$layout.log" >&2; exit 1; }
if [[ $action == init ]]; then
    bao operator init -key-shares=1 -key-threshold=1 -format=json > "$state/init.json"
fi
# Supply the unseal key on stdin so it never enters command logs or argv.
jq -c '{key: .unseal_keys_b64[0]}' "$state/init.json" | \
    curl --silent --show-error --fail --data-binary @- "$BAO_ADDR/v1/sys/unseal" > /dev/null
bao status > /dev/null
echo "Runtime server ready: $layout"
