#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
verifier="$script_dir/../scripts/verify-go-security.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
cat > "$work/go" <<'MOCK'
#!/bin/sh
cat "$METADATA"
MOCK
chmod +x "$work/go"
check() {
  expected=$1
  version=$2
  module=$3
  printf 'binary: %s\n' "$version" > "$work/metadata"
  if [ -n "$module" ]; then
    printf '\tdep\tgolang.org/x/net\t%s\th1:test\n' "$module" >> "$work/metadata"
  fi
  if GO="$work/go" METADATA="$work/metadata" "$verifier" binary >/dev/null 2>&1; then
    actual=pass
  else
    actual=fail
  fi
  if [ "$actual" != "$expected" ]; then
    echo "expected $expected for $version / $module, got $actual" >&2
    exit 1
  fi
}
check pass go1.26.9 v0.60.0
check pass go1.27.2 v0.60.0
check pass go1.26.10 v0.61.0
check pass go1.27.3 ''
check fail go1.26.8 v0.60.0
check fail go1.27.0 v0.60.0
check fail go1.27.1 v0.60.0
check fail go1.25.12 v0.60.0
check fail go1.27.2 v0.58.0
check fail go1.27.2 v0.60.0-rc.1
check fail go1.27rc1 v0.60.0
check fail unknown v0.60.0
check pass go1.27.2 v0.60.0
printf '\t=>\tgolang.org/x/net\tv0.58.0\th1:test\n' >> "$work/metadata"
if GO="$work/go" METADATA="$work/metadata" "$verifier" binary >/dev/null 2>&1; then
  echo "accepted an unverified x/net replacement" >&2
  exit 1
fi
echo "Go binary security baseline checks passed"
