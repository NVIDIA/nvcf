# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
@openbao-raft-leadership
Feature: Verify every Raft member through JWT upgrade and rollback
  # A fresh, explicitly supplied test container hosts three persistent Raft voters.
  # The fixture only starts, unseals and replaces servers. The operator commands
  # and all transition, membership and signing assertions remain below.

  Scenario: Target every voter without changing membership or signing keys
    Given environment variable "OPENBAO_RAFT_TEST_CONTAINER" is set
    When I successfully run command "docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash /test/runtime-raft.sh init legacy"
    And I successfully run command:
      """
      docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash /test/runtime-raft-bao.sh 0 bash -euo pipefail -c '
        sha=$(sha256sum "$JWT_PLUGIN_DIR/vault-plugin-secrets-jwt" | awk "{print \$1}")
        bao plugin register -sha256="$sha" -command=vault-plugin-secrets-jwt secret vault-plugin-secrets-jwt
        bao secrets enable -path=jwt vault-plugin-secrets-jwt
        bao write jwt/config key_ttl=24h jwt_ttl=30s
        bao write jwt/roles/test issuer=raft-lifecycle.example.com
        token=$(bao write -force -field=token jwt/sign/test)
        /test/jwtverify "$token" "$BAO_ADDR/v1/jwt/jwks" | jq -e ".iss == \"raft-lifecycle.example.com\""
        curl -sf "$BAO_ADDR/v1/jwt/jwks" | jq -S . > /tmp/nvcf-raft-runtime/original-jwks.json
        bao read -format=json sys/storage/raft/configuration | jq -S ".data.config.servers | map({node_id,address,voter}) | sort_by(.node_id)" > /tmp/nvcf-raft-runtime/original-peers.json
        jwt-plugin-lifecycle bridge-preflight
      '
      """
    And I successfully run command "docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash /test/runtime-raft.sh restart bridge"
    # old pin
    And I successfully run command:
      """
      docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash -euo pipefail -c '
        leader=$(/test/runtime-raft.sh active)
        /test/runtime-raft-bao.sh "$leader" jwt-plugin-lifecycle pin-old
      '
      """
    And I successfully run command "docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash /test/runtime-raft.sh restart bridge"
    And I successfully run command:
      """
      docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash -euo pipefail -c '
        for target in 0 1 2; do
          leader=$(/test/runtime-raft.sh active)
          if [ "$leader" != "$target" ]; then
            /test/runtime-raft-bao.sh "$leader" bao write sys/storage/raft/transfer-leadership server_id="node$target"
          fi
          active=false
          for attempt in $(seq 1 60); do
            if curl -sf --max-time 2 "http://127.0.0.1:$((18200+target*10))/v1/sys/leader" | jq -e ".is_self == true" >/dev/null; then active=true; break; fi
            sleep 1
          done
          test "$active" = true
          /test/runtime-raft-bao.sh "$target" bash -euo pipefail -c '\''
            source /openbao/jwt-plugin-versions.env
            expected=$JWT_PLUGIN_OLD_VERSION
            sha=$(sha256sum "$JWT_PLUGIN_DIR/vault-plugin-secrets-jwt-$expected" | awk "{print \$1}")
            jwt-plugin-lifecycle status | jq -e --arg expected "$expected" --arg sha "$sha" ". | length > 0 and all(.[]; .value.plugin_version == \$expected and .value.running_plugin_version == \$expected and .value.running_sha256 == \$sha)"
            token=$(bao write -force -field=token jwt/sign/test)
            /test/jwtverify "$token" "$BAO_ADDR/v1/jwt/jwks" | jq -e ".iss == \"raft-lifecycle.example.com\""
            curl -sf "$BAO_ADDR/v1/jwt/jwks" | jq -S . | cmp - /tmp/nvcf-raft-runtime/original-jwks.json
            healthy=false
            for attempt in $(seq 1 60); do
              if bao read -format=json sys/storage/raft/autopilot/state | jq -e ".data.healthy" >/dev/null; then healthy=true; break; fi
              sleep 1
            done
            test "$healthy" = true
            bao read -format=json sys/storage/raft/configuration | jq -S ".data.config.servers | map({node_id,address,voter}) | sort_by(.node_id)" | cmp - /tmp/nvcf-raft-runtime/original-peers.json
          '\''
          echo "Verified old pin on node$target"
        done
      '
      """
    # target
    And I successfully run command:
      """
      docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash -euo pipefail -c '
        leader=$(/test/runtime-raft.sh active)
        /test/runtime-raft-bao.sh "$leader" jwt-plugin-lifecycle upgrade
      '
      """
    And I successfully run command "docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash /test/runtime-raft.sh restart bridge"
    And I successfully run command:
      """
      docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash -euo pipefail -c '
        for target in 0 1 2; do
          leader=$(/test/runtime-raft.sh active)
          if [ "$leader" != "$target" ]; then
            /test/runtime-raft-bao.sh "$leader" bao write sys/storage/raft/transfer-leadership server_id="node$target"
          fi
          active=false
          for attempt in $(seq 1 60); do
            if curl -sf --max-time 2 "http://127.0.0.1:$((18200+target*10))/v1/sys/leader" | jq -e ".is_self == true" >/dev/null; then active=true; break; fi
            sleep 1
          done
          test "$active" = true
          /test/runtime-raft-bao.sh "$target" bash -euo pipefail -c '\''
            source /openbao/jwt-plugin-versions.env
            expected=$JWT_PLUGIN_TARGET_VERSION
            sha=$(sha256sum "$JWT_PLUGIN_DIR/vault-plugin-secrets-jwt-$expected" | awk "{print \$1}")
            jwt-plugin-lifecycle status | jq -e --arg expected "$expected" --arg sha "$sha" ". | length > 0 and all(.[]; .value.plugin_version == \$expected and .value.running_plugin_version == \$expected and .value.running_sha256 == \$sha)"
            token=$(bao write -force -field=token jwt/sign/test)
            /test/jwtverify "$token" "$BAO_ADDR/v1/jwt/jwks" | jq -e ".iss == \"raft-lifecycle.example.com\""
            curl -sf "$BAO_ADDR/v1/jwt/jwks" | jq -S . | cmp - /tmp/nvcf-raft-runtime/original-jwks.json
            healthy=false
            for attempt in $(seq 1 60); do
              if bao read -format=json sys/storage/raft/autopilot/state | jq -e ".data.healthy" >/dev/null; then healthy=true; break; fi
              sleep 1
            done
            test "$healthy" = true
            bao read -format=json sys/storage/raft/configuration | jq -S ".data.config.servers | map({node_id,address,voter}) | sort_by(.node_id)" | cmp - /tmp/nvcf-raft-runtime/original-peers.json
          '\''
          echo "Verified target on node$target"
        done
      '
      """
    # rollback
    And I successfully run command:
      """
      docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash -euo pipefail -c '
        leader=$(/test/runtime-raft.sh active)
        /test/runtime-raft-bao.sh "$leader" jwt-plugin-lifecycle rollback
      '
      """
    And I successfully run command "docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash /test/runtime-raft.sh restart bridge"
    And I successfully run command:
      """
      docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash -euo pipefail -c '
        for target in 0 1 2; do
          leader=$(/test/runtime-raft.sh active)
          if [ "$leader" != "$target" ]; then
            /test/runtime-raft-bao.sh "$leader" bao write sys/storage/raft/transfer-leadership server_id="node$target"
          fi
          active=false
          for attempt in $(seq 1 60); do
            if curl -sf --max-time 2 "http://127.0.0.1:$((18200+target*10))/v1/sys/leader" | jq -e ".is_self == true" >/dev/null; then active=true; break; fi
            sleep 1
          done
          test "$active" = true
          /test/runtime-raft-bao.sh "$target" bash -euo pipefail -c '\''
            source /openbao/jwt-plugin-versions.env
            expected=$JWT_PLUGIN_OLD_VERSION
            sha=$(sha256sum "$JWT_PLUGIN_DIR/vault-plugin-secrets-jwt-$expected" | awk "{print \$1}")
            jwt-plugin-lifecycle status | jq -e --arg expected "$expected" --arg sha "$sha" ". | length > 0 and all(.[]; .value.plugin_version == \$expected and .value.running_plugin_version == \$expected and .value.running_sha256 == \$sha)"
            token=$(bao write -force -field=token jwt/sign/test)
            /test/jwtverify "$token" "$BAO_ADDR/v1/jwt/jwks" | jq -e ".iss == \"raft-lifecycle.example.com\""
            curl -sf "$BAO_ADDR/v1/jwt/jwks" | jq -S . | cmp - /tmp/nvcf-raft-runtime/original-jwks.json
            healthy=false
            for attempt in $(seq 1 60); do
              if bao read -format=json sys/storage/raft/autopilot/state | jq -e ".data.healthy" >/dev/null; then healthy=true; break; fi
              sleep 1
            done
            test "$healthy" = true
            bao read -format=json sys/storage/raft/configuration | jq -S ".data.config.servers | map({node_id,address,voter}) | sort_by(.node_id)" | cmp - /tmp/nvcf-raft-runtime/original-peers.json
          '\''
          echo "Verified rollback on node$target"
        done
      '
      """
    # re-upgrade
    And I successfully run command:
      """
      docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash -euo pipefail -c '
        leader=$(/test/runtime-raft.sh active)
        /test/runtime-raft-bao.sh "$leader" jwt-plugin-lifecycle upgrade
      '
      """
    And I successfully run command "docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash /test/runtime-raft.sh restart bridge"
    And I successfully run command:
      """
      docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash -euo pipefail -c '
        for target in 0 1 2; do
          leader=$(/test/runtime-raft.sh active)
          if [ "$leader" != "$target" ]; then
            /test/runtime-raft-bao.sh "$leader" bao write sys/storage/raft/transfer-leadership server_id="node$target"
          fi
          active=false
          for attempt in $(seq 1 60); do
            if curl -sf --max-time 2 "http://127.0.0.1:$((18200+target*10))/v1/sys/leader" | jq -e ".is_self == true" >/dev/null; then active=true; break; fi
            sleep 1
          done
          test "$active" = true
          /test/runtime-raft-bao.sh "$target" bash -euo pipefail -c '\''
            source /openbao/jwt-plugin-versions.env
            expected=$JWT_PLUGIN_TARGET_VERSION
            sha=$(sha256sum "$JWT_PLUGIN_DIR/vault-plugin-secrets-jwt-$expected" | awk "{print \$1}")
            jwt-plugin-lifecycle status | jq -e --arg expected "$expected" --arg sha "$sha" ". | length > 0 and all(.[]; .value.plugin_version == \$expected and .value.running_plugin_version == \$expected and .value.running_sha256 == \$sha)"
            token=$(bao write -force -field=token jwt/sign/test)
            /test/jwtverify "$token" "$BAO_ADDR/v1/jwt/jwks" | jq -e ".iss == \"raft-lifecycle.example.com\""
            curl -sf "$BAO_ADDR/v1/jwt/jwks" | jq -S . | cmp - /tmp/nvcf-raft-runtime/original-jwks.json
            healthy=false
            for attempt in $(seq 1 60); do
              if bao read -format=json sys/storage/raft/autopilot/state | jq -e ".data.healthy" >/dev/null; then healthy=true; break; fi
              sleep 1
            done
            test "$healthy" = true
            bao read -format=json sys/storage/raft/configuration | jq -S ".data.config.servers | map({node_id,address,voter}) | sort_by(.node_id)" | cmp - /tmp/nvcf-raft-runtime/original-peers.json
          '\''
          echo "Verified re-upgrade on node$target"
        done
      '
      """
    And I successfully run command:
      """
      docker exec ${OPENBAO_RAFT_TEST_CONTAINER} bash -euo pipefail -c '
        leader=$(/test/runtime-raft.sh active)
        export SELF_ID="node$leader"
        /test/runtime-raft-bao.sh "$leader" bash -euo pipefail -c '\''
          path=sys/storage/raft/transfer-leadership
          for target in "" unknown "$SELF_ID"; do
            if bao write "$path" server_id="$target" > /tmp/nvcf-raft-runtime/rejected.out 2>&1; then echo "Invalid transfer accepted" >&2; exit 1; fi
            grep -q "Code: 400" /tmp/nvcf-raft-runtime/rejected.out
          done
          printf "path \"%s\" { capabilities = [\"update\"] }\n" "$path" | bao policy write transfer-no-sudo -
          limited=$(bao token create -field=token -policy=transfer-no-sudo -no-default-policy -ttl=5m)
          if BAO_TOKEN="$limited" bao write "$path" server_id=node0 > /tmp/nvcf-raft-runtime/rejected.out 2>&1; then echo "Transfer without sudo accepted" >&2; exit 1; fi
          grep -q "Code: 403" /tmp/nvcf-raft-runtime/rejected.out
          if BAO_TOKEN= bao write "$path" server_id=node0 > /tmp/nvcf-raft-runtime/rejected.out 2>&1; then echo "Unauthenticated transfer accepted" >&2; exit 1; fi
          grep -q "Code: 403" /tmp/nvcf-raft-runtime/rejected.out
          bao read -format=json sys/leader | jq -e ".data.is_self == true"
        '\''
      '
      """
