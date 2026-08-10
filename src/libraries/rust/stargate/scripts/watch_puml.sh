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

# Watch a directory for .puml changes and render with PlantUML.
# Uses plantuml, PLANTUML_JAR + java, or Docker (--docker or auto if no local runner).
# Needs inotifywait for watch mode.
# Run with -h for usage.

set -euo pipefail

usage() {
  cat <<'EOF'
Watch a directory for .puml files and render with PlantUML.

Usage: watch_puml.sh [options] [DIR]

DIR defaults to <git-root>/docs/diagrams when that directory exists.

Options:
  --format FMT   svg (default), png, or pdf
  --out NAME     output subdirectory under DIR (default: out)
  --once         render all diagrams once and exit
  --docker       force PlantUML in Docker (otherwise Docker is used only if no local runner)
  -h, --help     show this help
EOF
}

repo_root() {
  git rev-parse --show-toplevel 2>/dev/null || true
}

format="svg"
out_subdir="out"
once=false
use_docker=false
watch_dir=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  --format)
    format="${2:-}"
    shift 2
    ;;
  --out)
    out_subdir="${2:-}"
    shift 2
    ;;
  --once)
    once=true
    shift
    ;;
  --docker)
    use_docker=true
    shift
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  -*)
    echo "unknown option: $1" >&2
    usage >&2
    exit 2
    ;;
  *)
    if [[ -n "$watch_dir" ]]; then
      echo "extra argument: $1" >&2
      exit 2
    fi
    watch_dir="$1"
    shift
    ;;
  esac
done

root="$(repo_root)"
if [[ -z "$watch_dir" ]]; then
  if [[ -n "$root" && -d "$root/docs/diagrams" ]]; then
    watch_dir="$root/docs/diagrams"
  else
    echo "DIR not given and $root/docs/diagrams missing; pass a directory." >&2
    exit 2
  fi
fi

if [[ ! -d "$watch_dir" ]]; then
  echo "not a directory: $watch_dir" >&2
  exit 2
fi

watch_dir="$(cd "$watch_dir" && pwd)"
out_dir="${watch_dir}/${out_subdir}"
mkdir -p "$out_dir"

case "$format" in
svg | png | pdf) ;;
*)
  echo "unsupported --format: $format (use svg, png, or pdf)" >&2
  exit 2
  ;;
esac

collect_puml() {
  find "$watch_dir" -type f -name '*.puml' ! -path '*/.*' | LC_ALL=C sort
}

mapfile -t all_puml < <(collect_puml || true)
if [[ ${#all_puml[@]} -eq 0 && "$once" == true ]]; then
  echo "No .puml files under $watch_dir"
  exit 0
fi

plantuml_local=()

ensure_docker_plantuml() {
  if ! command -v docker >/dev/null 2>&1; then
    echo "docker not found in PATH" >&2
    exit 1
  fi
  if [[ -z "$root" ]]; then
    echo "Docker mode requires a git repository root to mount the watch directory." >&2
    exit 1
  fi
  root="$(cd "$root" && pwd)"
  case "$watch_dir" in
  "$root" | "$root"/*) ;;
  *)
    echo "Docker mode: watch directory must be under git root: $root" >&2
    exit 1
    ;;
  esac
}

if [[ "$use_docker" == true ]]; then
  ensure_docker_plantuml
elif [[ -n "${PLANTUML_JAR:-}" ]] && command -v java >/dev/null 2>&1; then
  plantuml_local=(java -jar "$PLANTUML_JAR")
elif command -v plantuml >/dev/null 2>&1; then
  plantuml_local=(plantuml)
else
  echo "No local PlantUML; using Docker ..." >&2
  use_docker=true
  ensure_docker_plantuml
fi

run_plantuml() {
  local -a files=("$@")
  if [[ ${#files[@]} -eq 0 ]]; then
    return 0
  fi
  if [[ "$use_docker" == true ]]; then
    docker run --rm \
      -v "${root}:${root}" \
      plantuml/plantuml:latest \
      "-t${format}" -o "$out_dir" "${files[@]}"
  else
    "${plantuml_local[@]}" "-t${format}" -o "$out_dir" "${files[@]}"
  fi
}

if [[ ${#all_puml[@]} -eq 0 ]]; then
  echo "No .puml files under $watch_dir (watching for new files) ..."
else
  echo "Rendering ${#all_puml[@]} diagram(s) to $out_dir ..."
  run_plantuml "${all_puml[@]}"
fi

if [[ "$once" == true ]]; then
  exit 0
fi

if ! command -v inotifywait >/dev/null 2>&1; then
  echo "inotifywait not found (install inotify-tools). Use --once for a single render." >&2
  exit 1
fi

echo "Watching $watch_dir for .puml changes (Ctrl+C to stop) ..."

inotifywait -m -r -e close_write,create,moved_to --format '%w|%f' "$watch_dir" |
  while IFS='|' read -r watched name; do
    [[ -n "${name:-}" ]] || continue
    case "$name" in
    *.puml) ;;
    *) continue ;;
    esac
    full="${watched%/}/${name}"
    if [[ ! -f "$full" ]]; then
      continue
    fi
    echo "Re-rendering: $full"
    run_plantuml "$full"
  done
