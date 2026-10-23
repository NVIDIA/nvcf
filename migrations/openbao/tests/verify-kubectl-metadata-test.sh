#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
verifier="$script_dir/../scripts/verify-kubectl.sh"
go_version=$(sed -n 's/^ARG KUBECTL_GO_VERSION=//p' "$script_dir/../Dockerfile")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
cat >"$work/go" <<'MOCK'
#!/bin/sh
cat "$METADATA"
MOCK
chmod +x "$work/go"

metadata() {
  version=$1
  printf 'kubectl: %s\n' "$go_version" >"$work/metadata"
  printf '\tpath\tk8s.io/kubernetes/cmd/kubectl\n' >>"$work/metadata"
  for module in '' /trace; do
    printf '\tdep\tgo.opentelemetry.io/otel%s\t%s\th1:test\n' "$module" "$version" >>"$work/metadata"
  done
  printf '\tdep\tgolang.org/x/net\tv0.60.0\th1:test\n' >>"$work/metadata"
  printf '\tbuild\tGOOS=linux\n\tbuild\tGOARCH=arm64\n\tbuild\tCGO_ENABLED=0\n' >>"$work/metadata"
}

check() {
  expected=$1
  if GO="$work/go" METADATA="$work/metadata" KUBECTL_GO_VERSION="$go_version" \
    TARGETARCH=arm64 "$verifier" >/dev/null 2>&1; then
    actual=pass
  else
    actual=fail
  fi
  if [ "$actual" != "$expected" ]; then
    echo "expected $expected, got $actual for:" >&2
    cat "$work/metadata" >&2
    exit 1
  fi
}

for version in v1.44.0 v1.44.1 v1.45.0; do
  metadata "$version"
  check pass
done
metadata v1.44.0
printf '\tdep\tgo.opentelemetry.io/otel/metric/x\tv0.66.0\th1:test\n' >>"$work/metadata"
check pass
metadata v1.44.0
printf '\tdep\tgo.opentelemetry.io/otel/metric\tv1.44.0\th1:test\n' >>"$work/metadata"
check pass
for version in v1.41.0 v1.42.0 v1.43.0 v1.44.0-rc.1 unknown; do
  metadata "$version"
  check fail
done
metadata v1.44.0
printf '\tdep\tgo.opentelemetry.io/otel/exporters/otlp/otlptrace\tv1.43.0\th1:test\n' >>"$work/metadata"
check fail
metadata v1.44.0
printf '\tdep\tgo.opentelemetry.io/otel/sdk/metric\tv1.45.0\th1:test\n' >>"$work/metadata"
check fail
metadata v1.44.0
printf '\tdep\tgo.opentelemetry.io/otel/sdk/metric\tv1.44.0\th1:test\n\t=>\t./local-sdk\t(devel)\n' >>"$work/metadata"
check fail
for missing in '' /trace; do
  metadata v1.44.0
  awk -v module="go.opentelemetry.io/otel$missing" '$2 != module' "$work/metadata" >"$work/filtered"
  mv "$work/filtered" "$work/metadata"
  check fail
done
metadata v1.44.0
sed 's/GOARCH=arm64/GOARCH=amd64/' "$work/metadata" >"$work/filtered"
mv "$work/filtered" "$work/metadata"
check fail

echo "kubectl OpenTelemetry and target metadata checks passed"
