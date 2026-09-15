#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

mkdir -p "$test_dir/bin"

cat >"$test_dir/bin/date" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == "+%s" ]]
IFS= read -r now <"$TEST_CLOCK_STATE"
printf '%s\n' "$now"
EOF
chmod +x "$test_dir/bin/date"

cat >"$test_dir/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
max_time=""
while (($# > 0)); do
  case "$1" in
    --max-time)
      max_time="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
[[ -n "$max_time" ]]
printf '%s\n' "$max_time" >>"$TEST_CURL_MAX_TIME_LOG"

count=0
if [[ -f "$TEST_CURL_STATE" ]]; then
  count="$(<"$TEST_CURL_STATE")"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$TEST_CURL_STATE"

now="$(<"$TEST_CLOCK_STATE")"
duration="${TEST_CURL_DURATION_SECONDS:-0}"
if ((duration > max_time)); then
  duration="$max_time"
fi
printf '%s\n' "$((now + duration))" >"$TEST_CLOCK_STATE"

if ((count < TEST_CURL_SUCCEED_ON)); then
  exit 22
fi
EOF
chmod +x "$test_dir/bin/curl"

cat >"$test_dir/bin/sleep" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" >>"$TEST_SLEEP_LOG"
now="$(<"$TEST_CLOCK_STATE")"
printf '%s\n' "$((now + $1))" >"$TEST_CLOCK_STATE"
exit 0
EOF
chmod +x "$test_dir/bin/sleep"

export PATH="$test_dir/bin:$PATH"
export TEST_CLOCK_STATE="$test_dir/clock"
export TEST_CURL_STATE="$test_dir/curl-count"
export TEST_CURL_MAX_TIME_LOG="$test_dir/curl-max-time"
export TEST_SLEEP_LOG="$test_dir/sleep"
export TEST_CURL_SUCCEED_ON=3
export TEST_CURL_DURATION_SECONDS=0
printf '100\n' >"$TEST_CLOCK_STATE"

GATEWAY_ROUTE_TIMEOUT_SECONDS=5 \
GATEWAY_ROUTE_RETRY_INTERVAL_SECONDS=1 \
  "$repo_dir/scripts/validate-gateway-route.sh" http://nginx.localhost:8080/ \
  >"$test_dir/success.out"

[[ "$(<"$TEST_CURL_STATE")" == "3" ]]
grep -q "Gateway route not reachable yet" "$test_dir/success.out"

printf '100\n' >"$TEST_CLOCK_STATE"
printf '0\n' >"$TEST_CURL_STATE"
: >"$TEST_CURL_MAX_TIME_LOG"
: >"$TEST_SLEEP_LOG"
export TEST_CURL_SUCCEED_ON=99
export TEST_CURL_DURATION_SECONDS=2
if GATEWAY_ROUTE_TIMEOUT_SECONDS=5 \
  GATEWAY_ROUTE_RETRY_INTERVAL_SECONDS=2 \
  "$repo_dir/scripts/validate-gateway-route.sh" http://nginx.localhost:8080/ \
  >"$test_dir/failure.out" 2>"$test_dir/failure.err"; then
  echo "expected Gateway route validation to time out" >&2
  exit 1
fi

[[ "$(<"$TEST_CURL_STATE")" == "2" ]]
[[ "$(<"$TEST_CLOCK_STATE")" == "105" ]]
[[ "$(tr '\n' ' ' <"$TEST_CURL_MAX_TIME_LOG")" == "5 1 " ]]
grep -q "did not become reachable within 5 seconds" "$test_dir/failure.err"

printf '100\n' >"$TEST_CLOCK_STATE"
printf '0\n' >"$TEST_CURL_STATE"
: >"$TEST_CURL_MAX_TIME_LOG"
: >"$TEST_SLEEP_LOG"
if GATEWAY_ROUTE_TIMEOUT_SECONDS=5 \
  GATEWAY_ROUTE_RETRY_INTERVAL_SECONDS=4 \
  "$repo_dir/scripts/validate-gateway-route.sh" http://nginx.localhost:8080/ \
  >"$test_dir/capped-sleep.out" 2>"$test_dir/capped-sleep.err"; then
  echo "expected Gateway route validation to time out" >&2
  exit 1
fi

[[ "$(<"$TEST_CLOCK_STATE")" == "105" ]]
[[ "$(<"$TEST_SLEEP_LOG")" == "3" ]]

for name in GATEWAY_ROUTE_TIMEOUT_SECONDS GATEWAY_ROUTE_RETRY_INTERVAL_SECONDS; do
  if env "$name=0" "$repo_dir/scripts/validate-gateway-route.sh" http://nginx.localhost:8080/ \
    >"$test_dir/invalid.out" 2>"$test_dir/invalid.err"; then
    echo "expected $name=0 to fail validation" >&2
    exit 1
  fi
  grep -q "$name must be a positive integer" "$test_dir/invalid.err"
done

echo "Gateway route retry tests passed."
