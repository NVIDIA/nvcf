#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Boots the mock JWKS (:8888), test registries (:8282, :8383), and the reval
# server (:8080), then renders the bundled test chart with the signed JWT.
#
# Usage: bash test/test_server.sh
# Set CI=true if the dependencies are already running (skips make targets).

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." &>/dev/null && pwd)"
TOKEN_FILE="${TOKEN_FILE:-/tmp/reval-test-token}"

TEST_EXIT_CODE=1
trap 'exit $TEST_EXIT_CODE' SIGINT SIGTERM
trap 'kill 0' EXIT

if [ "${CI:-}" != "true" ]; then
    cd "$ROOT_DIR"

    # Mock JWKS: serves /jwks.json on :8888 and writes a signed JWT to TOKEN_FILE.
    rm -f "$TOKEN_FILE"
    go run ./test/mockjwks --port 8888 --token-file "$TOKEN_FILE" &

    make run-test-regs &
    make run &
fi

# Wait for the JWT to be issued.
RETRY=0
until [ -s "$TOKEN_FILE" ]; do
    if [ $RETRY -gt 60 ]; then
        echo "Max retries reached — token file $TOKEN_FILE was not produced"
        exit 1
    fi
    echo "Waiting for mock JWKS to issue token..."
    sleep 1
    RETRY=$((RETRY + 1))
done
TOKEN="$(cat "$TOKEN_FILE")"

# Wait for the server to become healthy.
RETRY=0
until [ "$(curl -sSL -o /dev/null -w "%{http_code}" localhost:8082/healthz)" = "200" ]; do
    if [ $RETRY -gt 60 ]; then
        echo "Max retries reached — server did not become healthy"
        exit 1
    fi
    echo "Waiting for server to be healthy..."
    sleep 1
    RETRY=$((RETRY + 1))
done

echo "Server healthy — running tests"

# ── Test: local test chart renders as valid ───────────────────────────────────
VALID=$(curl -sSL http://localhost:8080/v1/render \
    --header "Authorization: Bearer ${TOKEN}" \
    --header 'Accept: application/json' \
    --header 'Content-Type: application/json' \
    -d '{
    "helmChart": "http://localhost:8282/multi-node-secrets-test-0.3.4.tgz",
    "helmChartServiceName": "mini-service-server",
    "helmChartServicePort": 8000,
    "helmRegistryAuthConfig": {
        "k8sSecrets": [
            {
                "auths": {
                    "localhost:8282": {
                        "auth": "JG9hdXRodG9rZW46Zm9vYmFy"
                    }
                }
            }
        ]
    },
    "configuration": {
        "image": {
            "repository": "localhost:8383/foo/bar",
            "tag": "latest"
        },
        "annotations": {
            "dra.nvcf.nvidia.io/required-nvlink-domain-index": "0"
        }
    }
}' | python3 -c "import sys,json; d=json.load(sys.stdin); print(0 if d.get('valid') else 1)")

if [ "$VALID" != "0" ]; then
    echo "FAIL: chart expected to be valid, is not"
    TEST_EXIT_CODE=1
else
    echo "PASS: chart is valid"
    TEST_EXIT_CODE=0
fi
