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

usage() {
    cat <<'EOF'
Usage: scripts/profile_stargate_bench.sh [--output-dir DIR] [--profile-name NAME] <stargate-bench args...>

Builds stargate-bench with release debuginfo and profiles the requested benchmark
command. The wrapper prefers cargo-flamegraph/flamegraph when available, then
falls back to Linux perf artifacts.

Examples:
  scripts/profile_stargate_bench.sh transport-bench --requests 20000 --concurrency 256
  scripts/profile_stargate_bench.sh --profile-name lb-pulsar lb-microbench --scenario pulsar

Environment:
  STARGATE_BENCH_PROFILE_OUTPUT_DIR  Output root, default .bench-out/profiles
  STARGATE_BENCH_PROFILE_DRY_RUN     If set to 1, write metadata but do not build/profile
  STARGATE_BENCH_PROFILE_NOW         Override timestamp for deterministic tests
  PERF_FREQUENCY                     perf sampling frequency, default 99
EOF
}

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_root="${STARGATE_BENCH_PROFILE_OUTPUT_DIR:-.bench-out/profiles}"
profile_name=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --output-dir)
            if [[ $# -lt 2 ]]; then
                echo "--output-dir requires a value" >&2
                exit 2
            fi
            output_root="$2"
            shift 2
            ;;
        --profile-name)
            if [[ $# -lt 2 ]]; then
                echo "--profile-name requires a value" >&2
                exit 2
            fi
            profile_name="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        --)
            shift
            break
            ;;
        *)
            break
            ;;
    esac
done

if [[ $# -eq 0 ]]; then
    usage >&2
    exit 2
fi

bench_args=("$@")
if [[ -z "$profile_name" ]]; then
    profile_name="${bench_args[0]}"
fi

timestamp="${STARGATE_BENCH_PROFILE_NOW:-$(date -u +%Y%m%dT%H%M%SZ)}"
profile_slug="$(
    printf '%s' "$profile_name" |
        tr -c 'A-Za-z0-9_.=-' '-' |
        tr -s '-' |
        sed 's/^-//;s/-$//'
)"
if [[ -z "$profile_slug" ]]; then
    profile_slug="stargate-bench"
fi

mkdir -p "$output_root"
output_root="$(cd "$output_root" && pwd -P)"
run_dir="$output_root/$timestamp-$profile_slug"
mkdir -p "$run_dir"

{
    printf 'target/release/stargate-bench'
    printf ' %q' "${bench_args[@]}"
    printf '\n'
} > "$run_dir/command.txt"

{
    printf 'timestamp=%s\n' "$timestamp"
    printf 'repo_root=%s\n' "$repo_root"
    printf 'profile_name=%s\n' "$profile_name"
    printf 'profile_slug=%s\n' "$profile_slug"
    printf 'perf_frequency=%s\n' "${PERF_FREQUENCY:-99}"
    if command -v rustc >/dev/null 2>&1; then
        rustc --version
    fi
    if command -v uname >/dev/null 2>&1; then
        uname -a
    fi
    if command -v git >/dev/null 2>&1; then
        git -C "$repo_root" rev-parse HEAD 2>/dev/null || true
        git -C "$repo_root" branch --show-current 2>/dev/null || true
    fi
} > "$run_dir/environment.txt"

echo "profile output: $run_dir"
if [[ "${STARGATE_BENCH_PROFILE_DRY_RUN:-}" == "1" ]]; then
    echo "dry-run: wrote profile metadata only"
    exit 0
fi

cd "$repo_root"
export CARGO_PROFILE_RELEASE_DEBUG="${CARGO_PROFILE_RELEASE_DEBUG:-1}"
cargo build --release -p stargate-bench

binary="$repo_root/target/release/stargate-bench"
svg="$run_dir/flamegraph.svg"
perf_data="$run_dir/perf.data"

if command -v cargo-flamegraph >/dev/null 2>&1; then
    cargo flamegraph \
        --output "$svg" \
        --package stargate-bench \
        --bin stargate-bench \
        -- "${bench_args[@]}"
    echo "wrote $svg"
elif command -v flamegraph >/dev/null 2>&1; then
    flamegraph --output "$svg" -- "$binary" "${bench_args[@]}"
    echo "wrote $svg"
elif command -v perf >/dev/null 2>&1; then
    perf record \
        --call-graph dwarf \
        -F "${PERF_FREQUENCY:-99}" \
        -o "$perf_data" \
        -- "$binary" "${bench_args[@]}"
    perf report --stdio --no-children -i "$perf_data" > "$run_dir/perf-report.txt" || true
    if command -v inferno-collapse-perf >/dev/null 2>&1 && command -v inferno-flamegraph >/dev/null 2>&1; then
        perf script -i "$perf_data" | inferno-collapse-perf > "$run_dir/perf.folded"
        inferno-flamegraph "$run_dir/perf.folded" > "$svg"
        echo "wrote $svg"
    elif command -v stackcollapse-perf.pl >/dev/null 2>&1 && command -v flamegraph.pl >/dev/null 2>&1; then
        perf script -i "$perf_data" | stackcollapse-perf.pl > "$run_dir/perf.folded"
        flamegraph.pl "$run_dir/perf.folded" > "$svg"
        echo "wrote $svg"
    else
        echo "wrote $perf_data and $run_dir/perf-report.txt"
        echo "install cargo-flamegraph or inferno to render SVG flamegraphs automatically"
    fi
else
    echo "no profiler found: install cargo-flamegraph, flamegraph, or Linux perf" >&2
    exit 127
fi
