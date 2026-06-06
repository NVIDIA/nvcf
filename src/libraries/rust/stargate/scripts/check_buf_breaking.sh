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

# Greenfield coordinated calibration removes client-supplied completion from
# registration and changes the value-bearing completion directive field into
# an opaque RUN assignment token. Only the assigned pylon submits capacity
# through SubmitClusterCalibration. Keep this allowance exact so future
# protobuf breaks still fail after this PR merges.
PROTO_PATH = "crates/proto/proto/stargate.proto"
expected_messages_by_rule = {
    "FIELD_NO_DELETE": [
        'Previously present field "3" with name "calibration_state" on message "InferenceServerModelRegistration" was deleted.',
    ],
    "FIELD_SAME_JSON_NAME": [
        'Field "3" with name "assignment_token" on message "ModelCalibrationDirective" changed option "json_name" from "lastMeanInputTps" to "assignmentToken".',
    ],
    "FIELD_SAME_NAME": [
        'Field "3" on message "ModelCalibrationDirective" changed name from "last_mean_input_tps" to "assignment_token".',
    ],
    "FIELD_SAME_TYPE": [
        'Field "3" with name "assignment_token" on message "ModelCalibrationDirective" changed type from "double" to "string".',
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
        print("expected owner-only coordinated-calibration changes were not observed:", file=sys.stderr)
        for (path, rule, message), count in sorted(missing.items()):
            print(f"- {path} {rule} x{count}: {message}", file=sys.stderr)
    sys.exit(1)

print("buf breaking: only the expected owner-only coordinated-calibration changes were detected")
PY
