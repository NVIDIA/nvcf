#!/usr/bin/env sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eu

# A private directory rather than fixed /tmp paths: these files carry a dev
# root token and server logs, and predictable names in a shared /tmp are both
# a disclosure risk and a collision between concurrent runs.
smoke_tmp=$(mktemp -d "${TMPDIR:-/tmp}/nvcf-openbao-smoke.XXXXXX")
chmod 700 "$smoke_tmp"
trap 'rm -rf "$smoke_tmp"' EXIT INT TERM

export BAO_ADDR="${BAO_ADDR:-http://127.0.0.1:8200}"
export BAO_TOKEN="${BAO_TOKEN:-root}"
PLUGIN_PATH="${PLUGIN_PATH:-/openbao/plugins/vault-plugin-secrets-jwt}"
EXPECTED_PLUGIN_SHA="${EXPECTED_PLUGIN_SHA:-}"
JWTVERIFY="${JWTVERIFY:-}"

decode_jwt_payload() {
  token="$1"
  payload="$(printf "%s" "${token}" | cut -d. -f2 | tr "_-" "/+")"
  case $((${#payload} % 4)) in
    2) payload="${payload}==" ;;
    3) payload="${payload}=" ;;
  esac
  printf "%s" "${payload}" | base64 -d
}

decode_or_verify_jwt() {
  token="$1"
  output="$2"

  if [ -n "${JWTVERIFY}" ] && [ -x "${JWTVERIFY}" ]; then
    "${JWTVERIFY}" "${token}" "${BAO_ADDR}/v1/jwt/jwks" > "${output}"
  else
    decode_jwt_payload "${token}" > "${output}"
  fi
}

printf "%s\n" "plugin_directory = \"/openbao/plugins\"" > $smoke_tmp/openbao-dev.hcl
bao server -dev -dev-root-token-id="${BAO_TOKEN}" -dev-listen-address=127.0.0.1:8200 -config=$smoke_tmp/openbao-dev.hcl >$smoke_tmp/openbao.log 2>&1 &
server_pid=$!
trap 'kill "${server_pid}" >/dev/null 2>&1 || true' EXIT

ready=0
for _ in $(seq 1 30); do
  if bao status >$smoke_tmp/bao-status.txt 2>&1; then
    ready=1
    break
  fi
  sleep 1
done

if [ "${ready}" != "1" ]; then
  cat $smoke_tmp/openbao.log
  cat $smoke_tmp/bao-status.txt 2>/dev/null || true
  exit 1
fi

actual_sha="$(sha256sum "${PLUGIN_PATH}" | awk "{print \$1}")"
if [ -n "${EXPECTED_PLUGIN_SHA}" ] && [ "${actual_sha}" != "${EXPECTED_PLUGIN_SHA}" ]; then
  echo "unexpected plugin sha: ${actual_sha}"
  exit 1
fi

bao plugin register -sha256="${actual_sha}" secret vault-plugin-secrets-jwt
bao secrets enable -path=jwt vault-plugin-secrets-jwt
bao write jwt/config key_ttl=3s jwt_ttl=30s "subject_pattern=^[A-Z][a-z]+ [A-Z][a-z]+$"

printf "%s\n" "{\"claims\":{\"sub\":\"Zapp Brannigan\"}}" > /tmp/claims.json
printf "%s\n" "{\"claims\":{\"sub\":\"This name should be invalid because it has more than one space\"}}" > /tmp/invalid-claims.json
printf "%s\n" "{\"claims\":{\"foo\":\"bar\"}}" > /tmp/foo-claims.json

if bao write -field=token jwt/sign/test @/tmp/claims.json >/tmp/missing-role.jwt 2>/tmp/missing-role.err; then
  echo "signing with missing role unexpectedly succeeded"
  exit 1
fi
grep -q "unknown role" /tmp/missing-role.err || { cat /tmp/missing-role.err; exit 1; }

bao write jwt/roles/test issuer=test.example.com
bao read -field=issuer jwt/roles/test | grep -qx "test.example.com"

bao write -field=token jwt/sign/test @/tmp/claims.json > /tmp/jwt1.txt
decode_or_verify_jwt "$(cat /tmp/jwt1.txt)" /tmp/decoded1.json
jq -e ".iss == \"test.example.com\" and .sub == \"Zapp Brannigan\" and (.exp | type == \"number\") and (.iat | type == \"number\") and (.nbf | type == \"number\") and (.jti | type == \"string\")" /tmp/decoded1.json >/dev/null

if bao write -field=token jwt/sign/test @/tmp/invalid-claims.json >/tmp/invalid.jwt 2>/tmp/invalid.err; then
  echo "signing invalid subject unexpectedly succeeded"
  exit 1
fi
grep -q "validation of .sub. claim failed" /tmp/invalid.err || { cat /tmp/invalid.err; exit 1; }

if bao write -field=token jwt/sign/test @/tmp/foo-claims.json >/tmp/disallowed.jwt 2>/tmp/disallowed.err; then
  echo "signing disallowed foo claim unexpectedly succeeded"
  exit 1
fi
grep -q "claim foo not permitted" /tmp/disallowed.err || { cat /tmp/disallowed.err; exit 1; }

bao write jwt/config allowed_claims=foo allowed_claims=aud
bao write -field=token jwt/sign/test @/tmp/foo-claims.json > /tmp/jwt2.txt
decode_or_verify_jwt "$(cat /tmp/jwt2.txt)" /tmp/decoded2.json
jq -e ".iss == \"test.example.com\" and .foo == \"bar\"" /tmp/decoded2.json >/dev/null

bao write jwt/config sig_alg=RS256 set_iat=false
sleep 3
bao write -field=token jwt/sign/test @/tmp/foo-claims.json > /tmp/jwt3.txt
decode_or_verify_jwt "$(cat /tmp/jwt3.txt)" /tmp/decoded3.json
jq -e "(.foo == \"bar\") and (has(\"iat\") | not)" /tmp/decoded3.json >/dev/null

jwks_count="$(wget -qO- "${BAO_ADDR}/v1/jwt/jwks" | jq ".keys | length")"
if [ "${jwks_count}" -lt 2 ]; then
  echo "expected at least two JWKS keys after RSA switch/rotation, got ${jwks_count}"
  exit 1
fi

printf "JWT plugin runtime smoke passed: plugin_sha=%s jwks_keys=%s\n" "${actual_sha}" "${jwks_count}"
