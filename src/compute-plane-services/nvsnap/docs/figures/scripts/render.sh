#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Regenerate every nvsnap figure (or the ones named) and render its light and
# dark PNGs. Needs python3 and Node with Playwright on NODE_PATH (see
# docs/figures/README.md).
set -euo pipefail
cd "$(dirname "$0")/../../.."
names=("$@")
if [ ${#names[@]} -eq 0 ]; then
  for s in docs/figures/scripts/nvsnap-*.py; do names+=("$(basename "$s" .py)"); done
fi
for n in "${names[@]}"; do
  python3 "docs/figures/scripts/$n.py"
  node .agents/skills/nvaf-architecture-diagrams/scripts/render-and-check.cjs \
    "docs/figures/$n.svg" "$PWD/docs/figures/$n"
done
