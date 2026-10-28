#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

component=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd -- "$component/../../.." && pwd)
mode=${1:-all}
case "$mode" in
  all) packages=(./worker/httpstream ./worker ./service) ;;
  callback) packages=(./worker/httpstream) ;;
  *) echo "Usage: $0 [all|callback]" >&2; exit 2 ;;
esac

# Use sibling modules from this checkout without editing a global or root
# workspace. Never start the built utility or contact a deployed NVCF service.
test_tmp=$(mktemp -d "${TMPDIR:-/tmp}/nvcf-callback-test.XXXXXX")
trap 'rm -rf -- "$test_tmp"' EXIT
cat > "$test_tmp/go.work" <<EOF
go 1.26.0
use (
  "$component"
  "$repo/src/libraries/go/lib"
  "$repo/src/libraries/go/worker"
)
EOF
export GOWORK="$test_tmp/go.work"
export GOTOOLCHAIN=local
go_bin=${NVCF_TEST_GO:-go}
cd -- "$component"
"$go_bin" version
"$go_bin" test -count=1 -p 1 -timeout 5m "${packages[@]}"
"$go_bin" test -race -count=3 -short -timeout 3m ./worker/httpstream
"$go_bin" vet "${packages[@]}"
# This is a disposable compile check, not a release artifact. Read-only source
# may be mounted under another UID, which Git rejects for VCS stamping.
"$go_bin" build -buildvcs=false -o "$test_tmp/worker-utils" ./cmd
