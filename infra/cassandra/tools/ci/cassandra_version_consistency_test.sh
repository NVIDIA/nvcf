#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Enforce one Cassandra version contract across the two files that reference the
# base image: the transitional Dockerfile's `FROM cassandra:<tag>` and the
# `# cassandra-version:` marker recorded next to the pinned oci.pull digest in
# MODULE.bazel. The digest is authoritative for the built image; this test
# guards against bumping one reference and forgetting the other, which would
# otherwise ship divergent Cassandra versions from the Bazel and Dockerfile
# paths. It does not resolve the digest (that needs network); it asserts the two
# human-maintained version strings agree.
set -euo pipefail

dockerfile=${1:?usage: cassandra_version_consistency_test.sh <Dockerfile> <MODULE.bazel>}
module_bazel=${2:?usage: cassandra_version_consistency_test.sh <Dockerfile> <MODULE.bazel>}

from_tag="$(grep -oE '^FROM cassandra:[^ ]+' "${dockerfile}" | head -1 | sed 's/^FROM cassandra://')"
marker="$(grep -oE '^#[[:space:]]*cassandra-version:[[:space:]]*[^[:space:]]+' "${module_bazel}" | head -1 | sed -E 's/^#[[:space:]]*cassandra-version:[[:space:]]*//')"

if [[ -z "${from_tag}" ]]; then
  echo "FAIL: no 'FROM cassandra:<tag>' line in ${dockerfile}" >&2
  exit 1
fi
if [[ -z "${marker}" ]]; then
  echo "FAIL: no '# cassandra-version: <tag>' marker in ${module_bazel}" >&2
  exit 1
fi
if [[ "${from_tag}" != "${marker}" ]]; then
  echo "FAIL: Cassandra version drift: Dockerfile 'FROM cassandra:${from_tag}' != MODULE.bazel '# cassandra-version: ${marker}'." >&2
  echo "      Bump both together (and re-resolve the oci.pull digest); see infra/cassandra/AGENTS.md." >&2
  exit 1
fi

echo "OK: Cassandra version consistent across Dockerfile and MODULE.bazel (${from_tag})"
