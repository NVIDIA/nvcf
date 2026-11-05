#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
state=/tmp/nvcf-raft-runtime
umask 077
active() {
    local i found=()
    for i in 0 1 2; do
        if curl -sf --max-time 2 "http://127.0.0.1:$((18200+i*10))/v1/sys/leader" | jq -e '.is_self == true' >/dev/null; then
            found+=("$i")
        fi
    done
    [[ ${#found[@]} == 1 ]] || return 1
    printf '%s\n' "${found[0]}"
}
wait_active() {
    local attempt
    for ((attempt=0; attempt<60; attempt++)); do
        active && return 0
        sleep 1
    done
    echo 'No unique active server within 60 seconds' >&2
    return 1
}
wait_cluster() {
    local attempt leader
    for ((attempt=0; attempt<90; attempt++)); do
        if leader=$(active) && /test/runtime-raft-bao.sh "$leader" bao read -format=json sys/storage/raft/autopilot/state |
            jq -e '.data | .healthy and (.voters | length == 3)' >/dev/null; then
            return 0
        fi
        sleep 1
    done
    echo 'Three healthy voters did not recover within 90 seconds' >&2
    return 1
}
start_node() {
    local node=$1 layout=$2 binary=bao attempt
    cat > "$state/node$node/server.hcl" <<EOF
api_addr = "http://127.0.0.1:$((18200+node*10))"
cluster_addr = "https://127.0.0.1:$((18201+node*10))"
listener "tcp" {
  address = "127.0.0.1:$((18200+node*10))"
  tls_disable = true
}
storage "raft" {
  path = "$state/node$node/data"
  node_id = "node$node"
}
EOF
    if [[ $layout == legacy ]]; then
        binary=/test/bao-legacy
        printf 'plugin_directory = "%s/legacy/plugins"\n' "$state" >> "$state/node$node/server.hcl"
    else
        sed "s|/openbao/plugins|$state/$layout/plugins|g" "$state/$layout/plugin-config/jwt.hcl" >> "$state/node$node/server.hcl"
    fi
    printf '%s\n' "$layout" > "$state/node$node/layout"
    "$binary" server -config="$state/node$node/server.hcl" > "$state/node$node/server-$layout.log" 2>&1 &
    echo $! > "$state/node$node/server.pid"
    for ((attempt=0; attempt<60; attempt++)); do
        curl -sf --max-time 2 "http://127.0.0.1:$((18200+node*10))/v1/sys/init" >/dev/null && return 0
        sleep 1
    done
    echo "Node $node did not start" >&2
    return 1
}
unseal_node() {
    jq -c '{key:.unseal_keys_b64[0]}' "$state/init.json" |
        curl -sf --max-time 10 --data-binary @- "http://127.0.0.1:$((18200+$1*10))/v1/sys/unseal" >/dev/null
}
replace_standby() {
    local node=$1 layout=$2 attempt stopped=false pid
    [[ $(wait_active) != "$node" ]] || { echo 'Refusing to replace active server' >&2; return 1; }
    pid=$(cat "$state/node$node/server.pid")
    kill "$pid"
    for ((attempt=0; attempt<60; attempt++)); do
        # The listener closes before Raft releases its database lock.
        if [[ ! -r /proc/$pid/stat ]] || [[ $(awk '{print $3}' "/proc/$pid/stat") == Z ]]; then
            stopped=true
            break
        fi
        sleep 1
    done
    [[ $stopped == true ]] || return 1
    start_node "$node" "$layout"
    unseal_node "$node"
    wait_cluster
}
[[ $# -ge 1 ]] || exit 1
if [[ $1 == active && $# == 1 ]]; then wait_active; exit; fi
[[ $# == 2 ]] || { echo 'Usage: runtime-raft.sh {init|restart} {legacy|bridge}' >&2; exit 1; }
action=$1
layout=$2
case $layout in legacy|bridge) ;; *) exit 1 ;; esac
case $action in
    init)
        mkdir "$state"
        # shellcheck disable=SC1091
        source /openbao/jwt-plugin-versions.env
        for package in legacy bridge; do
            JWT_PACKAGE_OUTPUT="$state/$package" JWT_LEGACY_BINARY=/openbao/plugins/vault-plugin-secrets-jwt \
                JWT_TARGET_BINARY="/openbao/plugins/vault-plugin-secrets-jwt-$JWT_PLUGIN_TARGET_VERSION" \
                JWT_INCLUDE_LEGACY=true JWT_VERSIONS_FILE=/openbao/jwt-plugin-versions.env \
                sh /test/package-jwt-plugins.sh
        done
        for node in 0 1 2; do mkdir -p "$state/node$node/data"; done
        start_node 0 "$layout"
        BAO_ADDR=http://127.0.0.1:18200 bao operator init -key-shares=1 -key-threshold=1 -format=json > "$state/init.json"
        unseal_node 0
        wait_active >/dev/null
        for node in 1 2; do
            start_node "$node" "$layout"
            BAO_ADDR="http://127.0.0.1:$((18200+node*10))" bao operator raft join http://127.0.0.1:18200 >/dev/null
            unseal_node "$node"
        done
        wait_cluster
        ;;
    restart)
        old=$(wait_active)
        for node in 0 1 2; do
            [[ $node == "$old" ]] || replace_standby "$node" "$layout"
        done
        /test/runtime-raft-bao.sh "$old" bao operator step-down
        moved=false
        for ((attempt=0; attempt<60; attempt++)); do
            if leader=$(active) && [[ $leader != "$old" ]]; then moved=true; break; fi
            sleep 1
        done
        [[ $moved == true ]] || { echo 'Leadership did not transfer; stop' >&2; exit 1; }
        replace_standby "$old" "$layout"
        ;;
    *) exit 1 ;;
esac
printf 'Raft fixture ready: %s\n' "$layout"
