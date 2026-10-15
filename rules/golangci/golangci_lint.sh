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

usage() {
    cat <<'EOF'
Usage: golangci_lint.sh --dir <module-dir> --config <config-path> [options] [-- <golangci-lint args>]

Run golangci-lint for one Go module from a Bazel sh_test.

Options:
  --dir <module-dir>                 Go module directory, relative to the repository root.
  --config <config-path>             golangci-lint config path, relative to the repository root.
  --modules-download-mode <mode>     Optional golangci-lint modules download mode.
  --workspace <repo-root>            Optional repository root override.
  -h, --help                         Show this help.
EOF
}

die() {
    echo "golangci_lint.sh: $*" >&2
    exit 2
}

module_dir=""
config_path=""
modules_download_mode=""
workspace_override=""
lint_args=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dir)
            [[ $# -ge 2 ]] || die "--dir requires a value"
            module_dir="$2"
            shift 2
            ;;
        --config)
            [[ $# -ge 2 ]] || die "--config requires a value"
            config_path="$2"
            shift 2
            ;;
        --modules-download-mode)
            [[ $# -ge 2 ]] || die "--modules-download-mode requires a value"
            modules_download_mode="$2"
            shift 2
            ;;
        --workspace)
            [[ $# -ge 2 ]] || die "--workspace requires a value"
            workspace_override="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        --)
            shift
            lint_args+=("$@")
            break
            ;;
        *)
            lint_args+=("$1")
            shift
            ;;
    esac
done

[[ -n "${module_dir}" ]] || die "missing required --dir"
[[ -n "${config_path}" ]] || die "missing required --config"

resolve_script_dir() {
    local script="${BASH_SOURCE[0]:-$0}"
    if [[ "${script}" != /* ]]; then
        script="${PWD}/${script}"
    fi
    while [[ -L "${script}" ]]; do
        local script_dir target
        script_dir="$(cd -P "$(dirname "${script}")" >/dev/null && pwd)"
        target="$(readlink "${script}")"
        if [[ "${target}" != /* ]]; then
            target="${script_dir}/${target}"
        fi
        script="${target}"
    done
    cd -P "$(dirname "${script}")" >/dev/null && pwd
}

find_workspace_root() {
    local candidate
    for candidate in "${workspace_override:-}" "${NVCF_WORKSPACE_DIR:-}" "${BUILD_WORKSPACE_DIRECTORY:-}"; do
        if [[ -n "${candidate}" && -f "${candidate}/MODULE.bazel" && -f "${candidate}/${module_dir}/go.mod" ]]; then
            cd "${candidate}" >/dev/null && pwd -P
            return
        fi
    done

    local start search
    for start in "${PWD}" "$(resolve_script_dir)"; do
        search="$(cd "${start}" >/dev/null && pwd -P)"
        while [[ "${search}" != "/" ]]; do
            if [[ -f "${search}/MODULE.bazel" && -f "${search}/${module_dir}/go.mod" ]]; then
                echo "${search}"
                return
            fi
            search="$(dirname "${search}")"
        done
    done

    return 1
}

repo_root="$(find_workspace_root)" || die "could not locate repository root for ${module_dir}"
module_abs="${repo_root}/${module_dir}"
config_abs="${repo_root}/${config_path}"

[[ -f "${module_abs}/go.mod" ]] || die "missing go.mod under ${module_abs}"
[[ -f "${config_abs}" ]] || die "missing golangci-lint config ${config_abs}"

if ! command -v go >/dev/null 2>&1; then
    for go_bin in /usr/local/bin/go /usr/local/go/bin/go /opt/homebrew/bin/go /usr/bin/go; do
        if [[ -x "${go_bin}" ]]; then
            export PATH="$(dirname "${go_bin}"):${PATH}"
            break
        fi
    done
fi

if ! command -v go >/dev/null 2>&1; then
    echo "go is required to run golangci-lint; CI should set needs_host_go for this Bazel lane" >&2
    exit 127
fi

if [[ ${#lint_args[@]} -eq 0 ]]; then
    lint_args=("./...")
fi

export GOWORK=off
export CGO_ENABLED=1
export GOGC=1
export GOCACHE="${GOCACHE:-/tmp/gocache}"
export GOMODCACHE="${GOMODCACHE:-/tmp/gomodcache}"
export GOPATH="${GOPATH:-/tmp/gopath}"
mkdir -p "${GOCACHE}" "${GOMODCACHE}" "${GOPATH}"

golangci_args=(
    run
    -c "${config_abs}"
    --timeout 10m
)

if [[ -n "${modules_download_mode}" ]]; then
    golangci_args+=("--modules-download-mode=${modules_download_mode}")
fi

cd "${module_abs}"
exec go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.3.0 "${golangci_args[@]}" "${lint_args[@]}"
