#!/usr/bin/env bash
#
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
#
# update-collector-version.sh - Update OpenTelemetry Collector version across the repo.
#
# Usage: ./scripts/update-collector-version.sh <new_version>
#
# Example: ./scripts/update-collector-version.sh v0.147.0
#          ./scripts/update-collector-version.sh 0.147.0
#
# Updates: otel-collector-build.yaml, AGENTS.md, README.md, Dockerfile,
#          Dockerfile.nvcf-otel-collector, .gitlab-ci.yml
# Run from repository root.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

usage() {
	echo "Usage: $0 <new_version>" >&2
	echo "" >&2
	echo "  new_version  OpenTelemetry Collector version, with or without 'v' (e.g. v0.147.0 or 0.147.0)" >&2
	exit 1
}

if [ $# -ne 1 ]; then
	usage
fi

NEW_ARG="$1"
if [[ "$NEW_ARG" =~ ^v?(0\.[0-9]+\.[0-9]+)$ ]]; then
	if [[ "$NEW_ARG" == v* ]]; then
		NEW_V="${NEW_ARG}"
		NEW_PLAIN="${NEW_ARG#v}"
	else
		NEW_PLAIN="${NEW_ARG}"
		NEW_V="v${NEW_ARG}"
	fi
else
	echo "Error: version must match 0.x.y or v0.x.y (e.g. v0.147.0)" >&2
	usage
fi

# Detect current version from otel-collector-build.yaml (first v0.x.y on a gomod line)
CURRENT_V=$(grep -oE 'v0\.[0-9]+\.[0-9]+' otel-collector-build.yaml | head -1 || true)
if [ -z "$CURRENT_V" ]; then
	echo "Error: could not detect current collector version in otel-collector-build.yaml" >&2
	exit 1
fi
CURRENT_PLAIN="${CURRENT_V#v}"

if [ "$CURRENT_V" = "$NEW_V" ]; then
	echo "Already at version $NEW_V, nothing to do."
	exit 0
fi

echo "Updating OpenTelemetry Collector version: $CURRENT_V -> $NEW_V"
echo "  (plain form: $CURRENT_PLAIN -> $NEW_PLAIN)"
echo ""

# Replace vX.Y.Z (builder/gomod form)
for f in otel-collector-build.yaml AGENTS.md README.md Dockerfile Dockerfile.nvcf-otel-collector .gitlab-ci.yml; do
	if [ -f "$f" ]; then
		sed -i "s/${CURRENT_V}/${NEW_V}/g" "$f"
		echo "  Updated $f (v-form)"
	fi
done

# Replace X.Y.Z (image tag / LABEL form) in files that use plain version
for f in .gitlab-ci.yml Dockerfile.nvcf-otel-collector; do
	if [ -f "$f" ]; then
		sed -i "s/${CURRENT_PLAIN}/${NEW_PLAIN}/g" "$f"
		echo "  Updated $f (plain form)"
	fi
done

echo ""
echo "Done. Verify with: git diff"
