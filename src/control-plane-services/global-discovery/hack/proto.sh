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
#
# Lints the proto contract under api/proto and generates the checked-in Go,
# grpc-gateway and OpenAPI code under api/gen with buf.
#
# Usage:
#   hack/proto.sh          lint, then regenerate api/gen in place
#   hack/proto.sh --check  lint, generate into a temporary directory and fail
#                          when the checked-in files differ
#
# buf and the plugins are local binaries found on PATH. The checked-in code was
# generated with the versions below; other versions change the version lines
# in the generated headers and fail the check:
#
#   go install github.com/bufbuild/buf/cmd/buf@v1.75.0
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
#   go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.28.0
#   go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@v2.28.0
set -euo pipefail

module_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${module_dir}"

: "${BUF:=buf}"

readonly gen_dir="api/gen"
readonly plugins=(protoc-gen-go protoc-gen-go-grpc protoc-gen-grpc-gateway protoc-gen-openapiv2)

for tool in "${BUF}" "${plugins[@]}"; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        echo "error: ${tool} not found on PATH; see the go install lines in $0" >&2
        exit 1
    fi
done

# protoc-gen-grpc-gateway does not copy the proto file header, so the license
# header is added to its output here. protoc-gen-go and protoc-gen-go-grpc copy
# the header from the .proto sources.
add_go_headers() {
    local f
    while IFS= read -r f; do
        if ! head -n 5 "${f}" | grep -q 'SPDX-License-Identifier'; then
            {
                cat <<'HEADER'
// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

HEADER
                cat "${f}"
            } > "${f}.tmp"
            mv "${f}.tmp" "${f}"
        fi
    done < <(find "$1" -type f -name '*.pb.gw.go')
}

# generate BASE_DIR: write the generated tree to BASE_DIR/api/gen.
generate() {
    "${BUF}" generate --template buf.gen.yaml -o "$1"
    add_go_headers "$1/${gen_dir}"
}

remove_generated() {
    find "${gen_dir}" -type f \( -name '*.pb.go' -o -name '*.pb.gw.go' -o -name '*.swagger.json' \) -delete
}

"${BUF}" lint

case "${1:-}" in
    "")
        remove_generated
        generate .
        echo "proto: regenerated ${gen_dir}"
        ;;
    --check)
        tmp="$(mktemp -d)"
        trap 'rm -rf "${tmp}"' EXIT
        generate "${tmp}"
        if ! diff -ru -x BUILD.bazel "${gen_dir}" "${tmp}/${gen_dir}"; then
            echo "error: generated files are stale; run 'make proto' and commit the result" >&2
            exit 1
        fi
        echo "proto: generated files are up to date"
        ;;
    *)
        echo "usage: $0 [--check]" >&2
        exit 2
        ;;
esac
