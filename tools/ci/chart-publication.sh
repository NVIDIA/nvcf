#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Shared chart publication verification. Source from Bash with pipefail enabled.

fetch_chart_tgz() {
  local repo_name="$1" chart_name="$2" chart_version="$3" out_dir="$4"
  local max_attempts="${5:-1}"
  local retry_delay="${HELM_CHART_FETCH_RETRY_DELAY_SECONDS:-10}"
  local attempt

  for ((attempt = 1; attempt <= max_attempts; attempt++)); do
    rm -rf "${out_dir}"
    mkdir -p "${out_dir}"
    echo "Updating Helm repo ${repo_name}" >&2
    helm repo update "${repo_name}" >&2 || return 1
    echo "Downloading ${repo_name}/${chart_name}:${chart_version} to ${out_dir}" >&2
    if helm fetch -d "${out_dir}" --version "${chart_version}" "${repo_name}/${chart_name}" >&2; then
      local packages=("${out_dir}"/*.tgz)
      if [[ ${#packages[@]} -eq 1 && -f "${packages[0]}" ]]; then
        printf '%s\n' "${packages[0]}"
        return 0
      fi
    fi
    if [[ "${attempt}" -lt "${max_attempts}" ]]; then
      echo "Chart ${repo_name}/${chart_name}:${chart_version} is not fetchable yet; retrying in ${retry_delay}s" >&2
      sleep "${retry_delay}"
    fi
  done

  echo "ERROR: could not find downloaded chart under ${out_dir}" >&2
  return 1
}

verify_chart_metadata() {
  "${CHART_ARTIFACT_TOOL:-chart-artifact}" validate --name "$2" --version "$3" "$1"
}

verify_src_matches_dest() {
  "${CHART_ARTIFACT_TOOL:-chart-artifact}" compare --name "$3" --version "$4" "$1" "$2"
}

is_conflict_already_exists() {
  grep -qiE '(^|[[:space:]])409([[:space:]:]|$)|helm chart already exists|conflict' <<<"$1"
}

# Read-only preflight: 0 means matching existing content, 2 means absent,
# and 1 means mismatch or a failed/inconclusive read. Caller configures Helm auth.
check_oci_chart_archive() (
  set -euo pipefail
  local tgz="$1" chart_name="$2" chart_version="$3" destination="$4"
  local work output reference="${destination}/${chart_name}"
  work="$(mktemp -d)"
  trap 'rm -rf "${work}"' EXIT
  verify_chart_metadata "${tgz}" "${chart_name}" "${chart_version}" || exit 1
  if output="$(helm pull "${reference}" --version "${chart_version}" --destination "${work}" 2>&1)"; then
    verify_src_matches_dest "${tgz}" "${work}/${chart_name}-${chart_version}.tgz" "${chart_name}" "${chart_version}" || exit 1
    echo "Existing OCI chart matches; leaving ${reference}:${chart_version} unchanged"
    exit 0
  fi
  if grep -qiE 'unauthorized|forbidden|denied|authenticat|credential|certificate|x509|(^|[[:space:]])(401|403)([[:space:]:]|$)|timed out|timeout|TLS handshake|connection|no such host|unexpected EOF' <<<"${output}" ||
     ! grep -qiE ': not found|manifest[_ ]unknown|name[_ ]unknown|(^|[[:space:]])404([[:space:]:]|$)' <<<"${output}"; then
    printf '%s\n' "${output}" >&2
    echo "ERROR: cannot establish whether ${reference}:${chart_version} exists" >&2
    exit 1
  fi
  exit 2
)

# Used only by already-authorized publishers. Preflight cannot atomically exclude
# another writer; registry immutability is still required. Always read back writes.
publish_oci_chart_archive() (
  set -euo pipefail
  local tgz="$1" chart_name="$2" chart_version="$3" destination="$4"
  local work output rc reference="${destination}/${chart_name}"
  if check_oci_chart_archive "$@"; then
    exit 0
  else
    rc=$?
    [[ ${rc} -eq 2 ]] || exit "${rc}"
  fi
  work="$(mktemp -d)"
  trap 'rm -rf "${work}"' EXIT
  rc=0
  output="$(helm push "${tgz}" "${destination}" 2>&1)" || rc=$?
  printf '%s\n' "${output}"
  if [[ ${rc} -ne 0 ]] && ! is_conflict_already_exists "${output}"; then
    exit "${rc}"
  fi
  helm pull "${reference}" --version "${chart_version}" --destination "${work}" || exit 1
  verify_src_matches_dest "${tgz}" "${work}/${chart_name}-${chart_version}.tgz" "${chart_name}" "${chart_version}"
)

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  if [[ $# -ne 5 ]]; then
    echo "usage: chart-publication.sh check-oci|publish-oci ARCHIVE NAME VERSION OCI_DESTINATION" >&2
    exit 1
  fi
  mode="$1"; shift
  case "${mode}" in
    check-oci) check_oci_chart_archive "$@" ;;
    publish-oci) publish_oci_chart_archive "$@" ;;
    *) echo "unknown mode: ${mode}" >&2; exit 1 ;;
  esac
fi
