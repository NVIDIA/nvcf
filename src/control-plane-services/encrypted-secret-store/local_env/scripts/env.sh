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
# Shared configuration for the ESS local-instance scripts. Source this file;
# do not run it directly.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCAL_ENV_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
ESS_DIR="$(cd "${LOCAL_ENV_DIR}/.." && pwd)"
REPO_ROOT="$(git -C "${ESS_DIR}" rev-parse --show-toplevel 2>/dev/null || cd "${ESS_DIR}/../../.." && pwd)"

COMPOSE_FILE="${LOCAL_ENV_DIR}/docker-compose.yaml"
SECRETS_FILE="${LOCAL_ENV_DIR}/secrets/secrets.json"

# ESS service HTTP port. Override by exporting SERVER_PORT before running.
SERVER_PORT="${SERVER_PORT:-8085}"

# Spring profile for the local instance.
SPRING_PROFILE="${SPRING_PROFILE:-local}"

# Bazel target and default output location for the executable jar. Override the
# jar by exporting ESS_APP_JAR (for example when you copied it out of a
# container build).
ESS_APP_TARGET="//src/control-plane-services/ess/ess-service:app"
ESS_APP_JAR_DEFAULT="${REPO_ROOT}/bazel-bin/src/control-plane-services/ess/ess-service/app.jar"
