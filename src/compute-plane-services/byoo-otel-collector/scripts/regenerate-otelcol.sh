#!/usr/bin/env bash
#
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Regenerate the otelcol/ tree from otel-collector-build.yaml using the
# OpenTelemetry Collector Builder (ocb).
#
# When to run:
#   - After bumping OpenTelemetry module versions in
#     otel-collector-build.yaml.
#   - After adding/removing receivers/processors/exporters/extensions in
#     otel-collector-build.yaml.
#
# What it does:
#   1. Installs ocb at the version pinned below into a per-run tempdir
#      (so this works without polluting GOBIN).
#   2. Runs ocb with --skip-compilation (we want sources, Bazel builds
#      the binary).
#   3. Moves the 6 emitted files into otelcol/.
#
# The drift detector at tools/ci/check-otelcol-generated re-runs this
# script into a tmp dir and diffs vs the checked-in otelcol/ to fail
# CI when the YAML has been edited without regenerating.

set -euo pipefail

# Pinned to the OpenTelemetry release line that all the receivers/
# processors/etc. in otel-collector-build.yaml are tagged for. Bump in
# lockstep with the gomod versions in that YAML.
OTEL_BUILDER_VERSION="${OTEL_BUILDER_VERSION:-v0.153.0}"

# OCB writes a temporary module under output/; keep parent go.work from
# overriding that generated module's replacements.
export GOWORK=off

PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUILD_YAML="${PROJECT_ROOT}/otel-collector-build.yaml"
OUT_DIR="${PROJECT_ROOT}/otelcol"

if [ ! -f "${BUILD_YAML}" ]; then
  echo "regenerate-otelcol: cannot find ${BUILD_YAML}" >&2
  exit 1
fi

TMPDIR="$(mktemp -d -t byoo-otel-builder.XXXXXX)"
trap 'rm -rf "${TMPDIR}"' EXIT

echo "regenerate-otelcol: installing ocb ${OTEL_BUILDER_VERSION} into ${TMPDIR}"
GOBIN="${TMPDIR}" go install "go.opentelemetry.io/collector/cmd/builder@${OTEL_BUILDER_VERSION}"

echo "regenerate-otelcol: running ocb (output -> ${PROJECT_ROOT}/output)"
(
  cd "${PROJECT_ROOT}"
  "${TMPDIR}/builder" --config="${BUILD_YAML}" --skip-compilation
)

if [ ! -d "${PROJECT_ROOT}/output" ]; then
  echo "regenerate-otelcol: ocb did not produce ./output -- aborting" >&2
  exit 1
fi

echo "regenerate-otelcol: moving generated files into ${OUT_DIR}"
mkdir -p "${OUT_DIR}"
# Replace, not append. Deletes any stale generated files (e.g. when
# components are dropped from the YAML).
find "${OUT_DIR}" -mindepth 1 -maxdepth 1 \
  ! -name "BUILD.bazel" \
  -exec rm -rf {} +
mv "${PROJECT_ROOT}"/output/* "${OUT_DIR}/"
rmdir "${PROJECT_ROOT}/output"

echo "regenerate-otelcol: done. Stage and commit ${OUT_DIR}/ if anything changed."
