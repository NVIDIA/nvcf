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
# Usage: ./scripts/update-collector-version.sh <new_version> [new_provider_version] [app_release_version]
#
# Example: ./scripts/update-collector-version.sh v0.153.0
#          ./scripts/update-collector-version.sh 0.153.0 v1.59.0
#          ./scripts/update-collector-version.sh 0.153.0 v1.59.0 0.153.2
#
# Updates: otel-collector-build.yaml, AGENTS.md, README.md, Makefile, Dockerfile,
#          Dockerfile.nvcf-otel-collector, .gitlab-ci.yml,
#          scripts/regenerate-otelcol.sh, VERSION
# Run from the BYOO collector root.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

usage() {
	echo "Usage: $0 <new_version> [new_provider_version] [app_release_version]" >&2
	echo "" >&2
	echo "  new_version           OpenTelemetry Collector version, with or without 'v' (e.g. v0.153.0 or 0.153.0)" >&2
	echo "  new_provider_version  Optional confmap provider version, with or without 'v' (e.g. v1.59.0 or 1.59.0)" >&2
	echo "  app_release_version   Optional app release version for VERSION; defaults to new_version without 'v'" >&2
	exit 1
}

if [ $# -lt 1 ] || [ $# -gt 3 ]; then
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
	echo "Error: version must match 0.x.y or v0.x.y (e.g. v0.153.0)" >&2
	usage
fi

NEW_PROVIDER_V=""
NEW_APP_PLAIN="$NEW_PLAIN"
if [ $# -ge 2 ]; then
	SECOND_ARG="$2"
	if [[ "$SECOND_ARG" =~ ^v?(0\.[0-9]+\.[0-9]+)$ ]]; then
		NEW_APP_PLAIN="${BASH_REMATCH[1]}"
	elif [[ "$SECOND_ARG" =~ ^v?(1\.[0-9]+\.[0-9]+)$ ]]; then
		if [[ "$SECOND_ARG" == v* ]]; then
			NEW_PROVIDER_V="${SECOND_ARG}"
		else
			NEW_PROVIDER_V="v${SECOND_ARG}"
		fi
	else
		echo "Error: provider version must match 1.x.y or v1.x.y, or app release version must match 0.x.y or v0.x.y" >&2
		usage
	fi
fi

if [ $# -eq 3 ]; then
	if [ -z "$NEW_PROVIDER_V" ]; then
		echo "Error: third argument is only valid when the second argument is the provider version" >&2
		usage
	fi
	NEW_APP_ARG="$3"
	if [[ "$NEW_APP_ARG" =~ ^v?(0\.[0-9]+\.[0-9]+)$ ]]; then
		NEW_APP_PLAIN="${BASH_REMATCH[1]}"
	else
		echo "Error: app release version must match 0.x.y or v0.x.y (e.g. 0.153.2)" >&2
		usage
	fi
fi

collector_major_minor="${NEW_PLAIN%.*}"
app_major_minor="${NEW_APP_PLAIN%.*}"
if [ "$app_major_minor" != "$collector_major_minor" ]; then
	echo "Error: app release major/minor must match collector major/minor (${collector_major_minor}.x), got ${NEW_APP_PLAIN}" >&2
	exit 1
fi

if [ -n "$NEW_PROVIDER_V" ]; then
	NEW_PROVIDER_ARG="${NEW_PROVIDER_V}"
	if [[ "$NEW_PROVIDER_ARG" =~ ^v?(1\.[0-9]+\.[0-9]+)$ ]]; then
		if [[ "$NEW_PROVIDER_ARG" == v* ]]; then
			NEW_PROVIDER_V="${NEW_PROVIDER_ARG}"
		else
			NEW_PROVIDER_V="v${NEW_PROVIDER_ARG}"
		fi
	else
		echo "Error: provider version must match 1.x.y or v1.x.y (e.g. v1.59.0)" >&2
		usage
	fi
fi

# Detect current version from otel-collector-build.yaml (first v0.x.y on a gomod line)
CURRENT_V=$(grep -oE 'v0\.[0-9]+\.[0-9]+' otel-collector-build.yaml | head -1 || true)
if [ -z "$CURRENT_V" ]; then
	echo "Error: could not detect current collector version in otel-collector-build.yaml" >&2
	exit 1
fi
CURRENT_PLAIN="${CURRENT_V#v}"

CURRENT_PROVIDER_V=""
if [ -n "$NEW_PROVIDER_V" ]; then
	CURRENT_PROVIDER_V=$(grep -oE 'v1\.[0-9]+\.[0-9]+' otel-collector-build.yaml | head -1 || true)
	if [ -z "$CURRENT_PROVIDER_V" ]; then
		echo "Error: could not detect current confmap provider version in otel-collector-build.yaml" >&2
		exit 1
	fi
fi

echo "Updating OpenTelemetry Collector version: $CURRENT_V -> $NEW_V"
echo "  (plain form: $CURRENT_PLAIN -> $NEW_PLAIN)"
if [ -n "$NEW_PROVIDER_V" ]; then
	echo "Updating confmap provider version: $CURRENT_PROVIDER_V -> $NEW_PROVIDER_V"
fi
if [ -f VERSION ]; then
	CURRENT_APP_PLAIN="$(tr -d '[:space:]' < VERSION)"
	echo "Updating app release version: ${CURRENT_APP_PLAIN:-<empty>} -> $NEW_APP_PLAIN"
fi
echo ""

replace_all() {
	local current="$1"
	local replacement="$2"
	local file="$3"
	CURRENT="$current" REPLACEMENT="$replacement" perl -0pi -e 's/\Q$ENV{CURRENT}\E/$ENV{REPLACEMENT}/g' "$file"
}

V_FORM_FILES=(
	otel-collector-build.yaml
	AGENTS.md
	README.md
	Makefile
	Dockerfile
	Dockerfile.nvcf-otel-collector
	.gitlab-ci.yml
	scripts/regenerate-otelcol.sh
)

PLAIN_FORM_FILES=(
	AGENTS.md
	README.md
	.gitlab-ci.yml
	Dockerfile.nvcf-otel-collector
)

# Replace vX.Y.Z (builder/gomod form)
for f in "${V_FORM_FILES[@]}"; do
	if [ -f "$f" ]; then
		replace_all "$CURRENT_V" "$NEW_V" "$f"
		echo "  Updated $f (v-form)"
	fi
done

# Replace X.Y.Z (image tag / LABEL form) in files that use plain version
for f in "${PLAIN_FORM_FILES[@]}"; do
	if [ -f "$f" ]; then
		replace_all "$CURRENT_PLAIN" "$NEW_PLAIN" "$f"
		echo "  Updated $f (plain form)"
	fi
done

if [ -n "$NEW_PROVIDER_V" ]; then
	for f in "${V_FORM_FILES[@]}"; do
		if [ -f "$f" ]; then
			replace_all "$CURRENT_PROVIDER_V" "$NEW_PROVIDER_V" "$f"
			echo "  Updated $f (provider v-form)"
		fi
	done
fi

if [ -f VERSION ]; then
	printf "%s\n" "$NEW_APP_PLAIN" > VERSION
	echo "  Updated VERSION"
fi

if [ -f Dockerfile.nvcf-otel-collector ]; then
	NEW_PLAIN="$NEW_PLAIN" perl -0pi -e 's/LABEL version="[0-9]+\.[0-9]+\.[0-9]+"/LABEL version="$ENV{NEW_PLAIN}"/' Dockerfile.nvcf-otel-collector
	echo "  Updated Dockerfile.nvcf-otel-collector (LABEL version)"
fi

echo ""
echo "Done. Verify with: git diff"
