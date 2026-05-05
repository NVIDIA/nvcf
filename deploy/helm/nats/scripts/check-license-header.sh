#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eu

failed=0

for file in "$@"; do
  if sed -n '1p' "$file" | grep -q '^#!'; then
    copyright_line=$(sed -n '2p' "$file")
    license_line=$(sed -n '3p' "$file")
  else
    copyright_line=$(sed -n '1p' "$file")
    license_line=$(sed -n '2p' "$file")
  fi

  if [ "$copyright_line" != "# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved." ] || \
     [ "$license_line" != "# SPDX-License-Identifier: Apache-2.0" ]; then
    echo "[license-header-check] missing or malformed header: $file" >&2
    failed=1
  fi
done

exit "$failed"
