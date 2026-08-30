#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

mkdir -p "$test_dir/bin"

cat >"$test_dir/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
count=0
if [[ -f "$TEST_CURL_STATE" ]]; then
  count="$(<"$TEST_CURL_STATE")"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$TEST_CURL_STATE"
if ((count < TEST_CURL_SUCCEED_ON)); then
  exit 22
fi
EOF
chmod +x "$test_dir/bin/curl"

cat >"$test_dir/bin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$test_dir/bin/sleep"

export PATH="$test_dir/bin:$PATH"
export TEST_CURL_STATE="$test_dir/curl-count"
export TEST_CURL_SUCCEED_ON=3

GATEWAY_ROUTE_TIMEOUT_SECONDS=5 \
GATEWAY_ROUTE_RETRY_INTERVAL_SECONDS=1 \
  "$repo_dir/scripts/validate-gateway-route.sh" http://nginx.localhost:8080/ \
  >"$test_dir/success.out"

[[ "$(<"$TEST_CURL_STATE")" == "3" ]]
grep -q "Gateway route not reachable yet" "$test_dir/success.out"

printf '0\n' >"$TEST_CURL_STATE"
export TEST_CURL_SUCCEED_ON=99
if GATEWAY_ROUTE_TIMEOUT_SECONDS=2 \
  GATEWAY_ROUTE_RETRY_INTERVAL_SECONDS=1 \
  "$repo_dir/scripts/validate-gateway-route.sh" http://nginx.localhost:8080/ \
  >"$test_dir/failure.out" 2>"$test_dir/failure.err"; then
  echo "expected Gateway route validation to time out" >&2
  exit 1
fi

[[ "$(<"$TEST_CURL_STATE")" == "2" ]]
grep -q "did not become reachable within 2 seconds" "$test_dir/failure.err"

for name in GATEWAY_ROUTE_TIMEOUT_SECONDS GATEWAY_ROUTE_RETRY_INTERVAL_SECONDS; do
  if env "$name=0" "$repo_dir/scripts/validate-gateway-route.sh" http://nginx.localhost:8080/ \
    >"$test_dir/invalid.out" 2>"$test_dir/invalid.err"; then
    echo "expected $name=0 to fail validation" >&2
    exit 1
  fi
  grep -q "$name must be a positive integer" "$test_dir/invalid.err"
done

echo "Gateway route retry tests passed."
