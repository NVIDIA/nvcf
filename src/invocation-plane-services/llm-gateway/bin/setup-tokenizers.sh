#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
cd "$SCRIPT_DIR/.." || exit 1

echo "Setting up tokenizers..."

uname_s=$(uname)
uname_m=$(uname -m)
version="${TOKENIZERS_VERSION:-v1.24.0}"

curl -fsSL \
  "https://github.com/daulet/tokenizers/releases/download/${version}/libtokenizers.${uname_s}-${uname_m}.tar.gz" |
  tar xz

go env -w CGO_LDFLAGS="-O2 -g -L$(pwd)"
