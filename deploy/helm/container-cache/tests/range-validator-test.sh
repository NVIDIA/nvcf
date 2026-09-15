#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Runs the safe-range.lua unit tests. Prefers a local luajit/lua, otherwise
# borrows the one in the OpenResty image. Skips only if neither is available.
#
#   bash tests/range-validator-test.sh
set -euo pipefail
DIR="$(cd "$(dirname "$0")/.." && pwd)"
LUA_FILE="deploy/files/lua/safe-range.lua"
TEST_FILE="tests/range-validator-test.lua"
cd "$DIR"

for bin in luajit lua5.1 lua; do
  if command -v "$bin" >/dev/null 2>&1; then
    exec "$bin" "$TEST_FILE" "$LUA_FILE"
  fi
done

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  exec docker run --rm -v "$DIR:/w" -w /w "${OPENRESTY_IMAGE:-openresty/openresty:alpine}" \
    /usr/local/openresty/luajit/bin/luajit "$TEST_FILE" "$LUA_FILE"
fi

echo "SKIP: no lua interpreter and no docker available"
