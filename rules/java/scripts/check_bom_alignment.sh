#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Asserts that every canonical Spring BOM coordinate (from platform.bzl, passed
# as a newline-delimited file) appears verbatim in the consuming module's
# MODULE.bazel. Keeps each Java module's inlined boms list aligned with the
# single source of truth, since load() cannot be used in MODULE.bazel.
set -euo pipefail

module_bazel="$1"
canonical="$2"

missing=0
while IFS= read -r coord; do
    [ -z "$coord" ] && continue
    if ! grep -qF -- "$coord" "$module_bazel"; then
        echo "BOM drift: canonical coordinate not found in MODULE.bazel:" >&2
        echo "  $coord" >&2
        missing=1
    fi
done < "$canonical"

if [ "$missing" -ne 0 ]; then
    echo >&2
    echo "Every coordinate in @nvcf_java_rules//:platform.bzl SPRING_BOOT_BOM must" >&2
    echo "be inlined in this module's maven.install boms list. Re-align and re-pin." >&2
    exit 1
fi

echo "BOM alignment OK: $(grep -c . "$canonical") canonical coordinate(s) present."
