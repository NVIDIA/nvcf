#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

capture_mode="${1:-}"
raw_file="${2:-}"
base_values_file="${3:-}"
environment_values_file="${4:-}"

emit_error() {
  printf 'error %s\n' "$1"
  exit 0
}

case "${capture_mode}" in
  file)
    [ -n "${raw_file}" ] || emit_error capture
    trap 'rm -f "${raw_file}"' EXIT HUP INT TERM
    [ -f "${raw_file}" ] || emit_error capture
    [ "$(wc -l < "${raw_file}" | tr -d '[:space:]')" = "1" ] || emit_error format
    IFS= read -r requested_control_plane_id < "${raw_file}" || requested_control_plane_id=""
    ;;
  environment)
    requested_control_plane_id="${NVCF_RAW_CONTROL_PLANE_ID:-}"
    ;;
  *)
    emit_error capture
    ;;
esac

requested_control_plane_id="$(
  printf '%s' "${requested_control_plane_id}" |
    sed 's/^[[:space:]]*//; s/[[:space:]]*$//'
)"

if [ -n "${requested_control_plane_id}" ]; then
  resolved_control_plane_id="${requested_control_plane_id}"
else
  command -v yq >/dev/null 2>&1 || emit_error yq
  yq --version 2>/dev/null | grep -Eq 'version v4\.' || emit_error yq
  [ -f "${base_values_file}" ] || emit_error configuration

  if [ -f "${environment_values_file}" ]; then
    resolved_control_plane_id="$(
      # The dollar-prefixed identifier belongs to yq, not the shell.
      # shellcheck disable=SC2016
      yq eval-all -r '. as $item ireduce ({}; . * $item) | .global.controlPlane.id // ""' \
        "${base_values_file}" "${environment_values_file}" 2>/dev/null
    )" || emit_error configuration
  else
    resolved_control_plane_id="$(
      yq -r '.global.controlPlane.id // ""' "${base_values_file}" 2>/dev/null
    )" || emit_error configuration
  fi
fi

resolved_control_plane_id="$(
  printf '%s' "${resolved_control_plane_id}" |
    sed 's/^[[:space:]]*//; s/[[:space:]]*$//'
)"

if [ -z "${resolved_control_plane_id}" ]; then
  printf 'ok\n'
  exit 0
fi

[ "${resolved_control_plane_id}" != "default" ] || emit_error reserved
case "${resolved_control_plane_id}" in
  -* | *- | *[!a-z0-9-]*) emit_error format ;;
esac
[ "${#resolved_control_plane_id}" -le 20 ] || emit_error length

# This is the only path that emits caller-controlled data. The validation above
# restricts it to one short DNS-label token, safe for subsequent Make expansion.
printf 'ok %s\n' "${resolved_control_plane_id}"
