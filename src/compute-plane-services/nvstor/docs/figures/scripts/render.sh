#!/usr/bin/env bash
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

# Regenerate every nvstor figure (or the ones named) and render its light and dark PNGs.
# Needs python3 and Node with Playwright (Chromium) on NODE_PATH (see docs/figures/README.md).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
figures="$(dirname "$here")"
names=("$@")
if [ ${#names[@]} -eq 0 ]; then
  for s in "$here"/nvstor-*.py; do names+=("$(basename "$s" .py)"); done
fi
for n in "${names[@]}"; do
  python3 "$here/$n.py"
  node "$here/render.cjs" "$figures/$n.svg" "$figures/$n"
done
