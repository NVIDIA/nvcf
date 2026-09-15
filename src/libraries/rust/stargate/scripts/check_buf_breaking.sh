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

set -euo pipefail

against="${1:-.git#branch=main}"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

if ! command -v buf >/dev/null 2>&1; then
  echo "buf is required on PATH" >&2
  exit 127
fi

if buf breaking --against "$against" --error-format=json >"$tmp"; then
  exit 0
fi

python3 - "$tmp" <<'PY'
import collections
import json
import sys

# Calibration is now entirely local Pylon startup behavior. The wire protocol
# intentionally removes its RPC, messages, enum, registration flag, and ACK
# directives. Keep this allowance exact so future protobuf breaks still fail.
PROTO_PATH = "crates/proto/proto/stargate.proto"
expected_messages_by_rule = {
    "ENUM_NO_DELETE": [
        'Previously present enum "CalibrationState" was deleted from file.',
    ],
    "FIELD_NO_DELETE": [
        'Previously present field "2" with name "model_calibration_directives" on message "InferenceServerAck" was deleted.',
        'Previously present field "6" with name "coordinated_calibration" on message "InferenceServerRegistration" was deleted.',
    ],
    "MESSAGE_NO_DELETE": [
        'Previously present message "ModelCalibrationDirective" was deleted from file.',
        'Previously present message "SubmitClusterCalibrationRequest" was deleted from file.',
        'Previously present message "SubmitClusterCalibrationResponse" was deleted from file.',
    ],
    "RPC_NO_DELETE": [
        'Previously present RPC "SubmitClusterCalibration" on service "StargateControlPlane" was deleted.',
    ],
}
allowed = collections.Counter(
    (PROTO_PATH, rule, message)
    for rule, messages in expected_messages_by_rule.items()
    for message in messages
)

actual = collections.Counter()
path = sys.argv[1]
with open(path, encoding="utf-8") as handle:
    for line_number, line in enumerate(handle, start=1):
        line = line.strip()
        if not line:
            continue
        try:
            violation = json.loads(line)
        except json.JSONDecodeError as error:
            print(
                f"buf breaking returned non-JSON output on line {line_number}: {error}",
                file=sys.stderr,
            )
            sys.exit(1)
        actual[
            (
                violation.get("path"),
                violation.get("type"),
                violation.get("message"),
            )
        ] += 1

extra = actual - allowed
missing = allowed - actual
if extra or missing:
    if extra:
        print("unexpected protobuf breaking changes:", file=sys.stderr)
        for (path, rule, message), count in sorted(extra.items()):
            print(f"- {path} {rule} x{count}: {message}", file=sys.stderr)
    if missing:
        print("expected calibration-removal changes were not observed:", file=sys.stderr)
        for (path, rule, message), count in sorted(missing.items()):
            print(f"- {path} {rule} x{count}: {message}", file=sys.stderr)
    sys.exit(1)

print("buf breaking: only the expected calibration-removal changes were detected")
PY
