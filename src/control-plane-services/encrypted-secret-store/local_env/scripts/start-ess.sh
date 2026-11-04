#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Build (unless a jar is supplied) and run the ESS service with the local
# profile against the local Cassandra instance.
#
# Environment overrides:
#   SERVER_PORT   HTTP port for the service (default 8085).
#   ESS_APP_JAR   Path to a prebuilt executable jar; skips the Bazel build.
#   SKIP_BUILD=1  Reuse the default Bazel output jar without rebuilding.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

if [ ! -f "${SECRETS_FILE}" ]; then
  echo "Secrets file not found: ${SECRETS_FILE}" >&2
  echo "Expected local_env/secrets/secrets.json to exist." >&2
  exit 1
fi

app_jar="${ESS_APP_JAR:-}"
if [ -z "${app_jar}" ]; then
  if [ "${SKIP_BUILD:-0}" != "1" ]; then
    echo "Building ${ESS_APP_TARGET} with Bazel ..."
    ( cd "${REPO_ROOT}" && bazel build "${ESS_APP_TARGET}" )
  fi
  app_jar="${ESS_APP_JAR_DEFAULT}"
fi

if [ ! -f "${app_jar}" ]; then
  echo "Executable jar not found: ${app_jar}" >&2
  echo "Build it with 'bazel build ${ESS_APP_TARGET}' or set ESS_APP_JAR." >&2
  exit 1
fi

echo "Starting ESS on port ${SERVER_PORT} with profile ${SPRING_PROFILE}"
echo "  jar:     ${app_jar}"
echo "  secrets: ${SECRETS_FILE}"

# Run from the ESS directory so any relative resource lookups resolve, and pass
# the secrets path as an absolute file: URL so it works regardless of cwd.
cd "${ESS_DIR}"
SPRING_PROFILES_ACTIVE="${SPRING_PROFILE}" exec java \
  -Dserver.port="${SERVER_PORT}" \
  -jar "${app_jar}" \
  --nv-boot.reloadable-properties.file="file:${SECRETS_FILE}"
