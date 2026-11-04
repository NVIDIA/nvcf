#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
openbao_dir=$(CDPATH='' cd -- "$script_dir/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
export KUBECTL_BINARY="$work/kubectl"
export KUBECTL_METADATA_FIXTURE="$work/metadata.json"
cat >"$KUBECTL_BINARY" <<'EOF'
#!/bin/sh
cat "$KUBECTL_METADATA_FIXTURE"
EOF
chmod +x "$KUBECTL_BINARY"

export KUBECTL_GO_VERSION
KUBECTL_GO_VERSION=$(sed -n 's/^ARG KUBECTL_GO_VERSION=//p' "$openbao_dir/Dockerfile")
export TARGETARCH=arm64
export KUBECTL_VERSION KUBECTL_SOURCE_COMMIT KUBECTL_BUILD_DATE
for prefix in KUBECTL KUBECTL_COMPAT; do
  KUBECTL_VERSION=$(sed -n "s/^ARG ${prefix}_VERSION=//p" "$openbao_dir/Dockerfile")
  KUBECTL_SOURCE_COMMIT=$(sed -n "s/^ARG ${prefix}_SOURCE_COMMIT=//p" "$openbao_dir/Dockerfile")
  KUBECTL_BUILD_DATE=$(sed -n "s/^ARG ${prefix}_BUILD_DATE=//p" "$openbao_dir/Dockerfile")
  version=${KUBECTL_VERSION#v}
  major=${version%%.*}
  minor_patch=${version#*.}
  minor=${minor_patch%%.*}
  cat >"$work/good.json" <<EOF
{
  "major": "$major",
  "minor": "$minor",
  "gitVersion": "$KUBECTL_VERSION",
  "gitCommit": "$KUBECTL_SOURCE_COMMIT",
  "gitTreeState": "dirty",
  "buildDate": "$KUBECTL_BUILD_DATE",
  "goVersion": "$KUBECTL_GO_VERSION",
  "platform": "linux/$TARGETARCH"
}
EOF
  cp "$work/good.json" "$KUBECTL_METADATA_FIXTURE"
  "$openbao_dir/scripts/verify-kubectl-runtime.sh" >/dev/null
  for field in major minor gitVersion gitCommit gitTreeState buildDate goVersion platform; do
    sed "/\"$field\":/d" "$work/good.json" >"$KUBECTL_METADATA_FIXTURE"
    if "$openbao_dir/scripts/verify-kubectl-runtime.sh" >/dev/null 2>&1; then
      echo "runtime verifier accepted missing $field for $prefix" >&2
      exit 1
    fi
  done
done
echo "both kubectl runtime identities and missing-field rejection verified"
