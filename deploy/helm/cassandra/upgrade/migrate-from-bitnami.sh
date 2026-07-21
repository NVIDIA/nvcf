#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Guarded in-place migration of an existing Bitnami-subchart Cassandra release
# to the in-house StatefulSet chart, adopting the existing data PVC.
#
# This automates Option A from docs/upgrade-from-bitnami.md. It is dry-run by
# default and refuses to run against anything that looks like a managed/cloud
# context. Read that doc before using this. Backup and restore (Option C) is
# the safer path for production.
#
# What it does (only with --confirm):
#   1. Pre-flight safety checks (context, release, PVC, retention policy).
#   2. nodetool snapshot on every pod (backup) unless --skip-snapshot.
#   3. Record the pre-migration probe row count (if --probe-* given).
#   4. Orphan-delete the StatefulSet and delete the old pods (PVC is retained).
#   5. helm upgrade to the in-house chart with the supplied values.
#   6. Wait for the node(s) to reach UN and verify the probe row count survived.

set -euo pipefail

CHART_DIR_DEFAULT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/helm"

CONTEXT=""
NAMESPACE=""
RELEASE="cassandra"
VALUES=""
CHART_DIR="${CHART_DIR_DEFAULT}"
PROBE_KEYSPACE=""
PROBE_TABLE=""
CQL_USER="cassandra"
CQL_PASS=""
CONFIRM="false"
SKIP_SNAPSHOT="false"
ALLOW_UNSAFE_CONTEXT="false"
TIMEOUT="15m"

log()  { printf '[migrate] %s\n' "$*"; }
warn() { printf '[migrate][warn] %s\n' "$*" >&2; }
die()  { printf '[migrate][error] %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
Usage: migrate-from-bitnami.sh --context CTX --namespace NS --values FILE [options]

Required:
  --context CTX        kube-context to operate on (must be explicit)
  --namespace NS       namespace of the existing release
  --values FILE        new-chart values (must set persistence.subPath=data and
                       any cassandra.yaml config-compat, e.g. the settings the
                       old fleet used to write its sstables)

Options:
  --release NAME       helm release name (default: cassandra)
  --chart DIR          in-house chart directory (default: sibling helm/)
  --probe-keyspace KS  keyspace of a table to row-count before/after
  --probe-table T      table (in --probe-keyspace) to row-count before/after
  --cql-user U         cqlsh user for the probe (default: cassandra)
  --cql-pass P         cqlsh password for the probe (required if probing)
  --timeout DUR        helm upgrade timeout (default: 15m)
  --skip-snapshot      do NOT take a nodetool snapshot first (discouraged)
  --allow-unsafe-context  override the cloud/managed-context guard (dangerous)
  --confirm            actually execute; without it this is a dry run
  -h|--help            show this help
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --context) CONTEXT="$2"; shift 2;;
    --namespace) NAMESPACE="$2"; shift 2;;
    --release) RELEASE="$2"; shift 2;;
    --values) VALUES="$2"; shift 2;;
    --chart) CHART_DIR="$2"; shift 2;;
    --probe-keyspace) PROBE_KEYSPACE="$2"; shift 2;;
    --probe-table) PROBE_TABLE="$2"; shift 2;;
    --cql-user) CQL_USER="$2"; shift 2;;
    --cql-pass) CQL_PASS="$2"; shift 2;;
    --timeout) TIMEOUT="$2"; shift 2;;
    --skip-snapshot) SKIP_SNAPSHOT="true"; shift;;
    --allow-unsafe-context) ALLOW_UNSAFE_CONTEXT="true"; shift;;
    --confirm) CONFIRM="true"; shift;;
    -h|--help) usage; exit 0;;
    *) die "unknown argument: $1";;
  esac
done

[ -n "${CONTEXT}" ]   || { usage; die "--context is required"; }
[ -n "${NAMESPACE}" ] || { usage; die "--namespace is required"; }
[ -n "${VALUES}" ]    || { usage; die "--values is required"; }
[ -f "${VALUES}" ]    || die "values file not found: ${VALUES}"
[ -d "${CHART_DIR}" ] || die "chart dir not found: ${CHART_DIR}"
command -v kubectl >/dev/null || die "kubectl not found"
command -v helm >/dev/null    || die "helm not found"

KC=(kubectl --context "${CONTEXT}" -n "${NAMESPACE}")

# run: echo always; execute only with --confirm.
run() {
  printf '  + %s\n' "$*"
  if [ "${CONFIRM}" = "true" ]; then "$@"; fi
}

PVC="data-${RELEASE}-0"

log "context=${CONTEXT} namespace=${NAMESPACE} release=${RELEASE}"
[ "${CONFIRM}" = "true" ] && log "MODE: EXECUTE (--confirm)" || log "MODE: DRY-RUN (pass --confirm to execute)"

# --- Safety check 1: context guard ------------------------------------------
# Refuse anything that looks like a managed/cloud/prod context. The whole point
# of this script is to not fire an orphan-delete at the wrong cluster.
case "${CONTEXT}" in
  *arn:aws:*|*eks*|*gke*|*aks*|*prod*|*qa-*|*-qa)
    if [ "${ALLOW_UNSAFE_CONTEXT}" != "true" ]; then
      die "context '${CONTEXT}' looks managed/cloud/prod. Refusing. Use --allow-unsafe-context only if you are certain."
    fi
    warn "context '${CONTEXT}' looks managed/cloud/prod but --allow-unsafe-context was set."
    ;;
esac
kubectl config get-contexts -o name | grep -qx "${CONTEXT}" || die "kube-context '${CONTEXT}' not found in kubeconfig"
"${KC[@]}" cluster-info >/dev/null 2>&1 || die "cannot reach cluster for context '${CONTEXT}'"

# --- Safety check 2: release + namespace ------------------------------------
"${KC[@]}" get ns "${NAMESPACE}" >/dev/null 2>&1 || die "namespace '${NAMESPACE}' not found"
helm status "${RELEASE}" --kube-context "${CONTEXT}" -n "${NAMESPACE}" >/dev/null 2>&1 \
  || die "helm release '${RELEASE}' not found in ${NAMESPACE}"

# --- Safety check 3: PVC exists and is Bound --------------------------------
pvc_phase="$("${KC[@]}" get pvc "${PVC}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
[ "${pvc_phase}" = "Bound" ] || die "PVC '${PVC}' is not Bound (got '${pvc_phase:-missing}'). Refusing."
log "found data PVC ${PVC} (Bound)"

# --- Safety check 4: PVC retention will NOT delete the PVC on STS delete -----
# If the StatefulSet's persistentVolumeClaimRetentionPolicy.whenDeleted is
# Delete, deleting the StatefulSet garbage-collects the PVC. Refuse in that case.
when_deleted="$("${KC[@]}" get statefulset "${RELEASE}" -o jsonpath='{.spec.persistentVolumeClaimRetentionPolicy.whenDeleted}' 2>/dev/null || true)"
if [ "${when_deleted}" = "Delete" ]; then
  die "StatefulSet ${RELEASE} has persistentVolumeClaimRetentionPolicy.whenDeleted=Delete; orphan-delete would remove the PVC. Refusing."
fi
log "PVC retention on delete: ${when_deleted:-Retain (default)} - safe to orphan-delete"

# --- Backup: nodetool snapshot ----------------------------------------------
pods="$("${KC[@]}" get pods -l "app.kubernetes.io/name=cassandra,app.kubernetes.io/instance=${RELEASE}" -o name)"
[ -n "${pods}" ] || die "no Cassandra pods found for release '${RELEASE}'; refusing to continue"
if [ "${SKIP_SNAPSHOT}" = "true" ]; then
  warn "skipping nodetool snapshot (--skip-snapshot). No backup will be taken."
else
  log "taking nodetool snapshot on each pod (backup)"
  for p in ${pods}; do
    run "${KC[@]}" exec "${p#pod/}" -c cassandra -- nodetool snapshot -t "pre-migrate-$(basename "${VALUES}")"
  done
fi

# --- Record pre-migration probe row count -----------------------------------
pre_count=""
if { [ -n "${PROBE_KEYSPACE}" ] && [ -z "${PROBE_TABLE}" ]; } || \
   { [ -z "${PROBE_KEYSPACE}" ] && [ -n "${PROBE_TABLE}" ]; }; then
  die "--probe-keyspace and --probe-table must be provided together"
fi
if [ -n "${PROBE_KEYSPACE}" ] && [ -n "${PROBE_TABLE}" ]; then
  [ -n "${CQL_PASS}" ] || die "--cql-pass is required when probing"
  log "recording pre-migration row count for ${PROBE_KEYSPACE}.${PROBE_TABLE}"
  if [ "${CONFIRM}" = "true" ]; then
    pre_count="$("${KC[@]}" exec "${RELEASE}-0" -c cassandra -- \
      cqlsh -u "${CQL_USER}" -p "${CQL_PASS}" -e "SELECT COUNT(*) FROM ${PROBE_KEYSPACE}.${PROBE_TABLE};" 2>/dev/null \
      | grep -E '^[[:space:]]*[0-9]+' | tr -d ' ' || true)"
    log "pre-migration count: ${pre_count:-unknown}"
  else
    printf '  + (would count %s.%s)\n' "${PROBE_KEYSPACE}" "${PROBE_TABLE}"
  fi
fi

# --- Execute the recreate + upgrade -----------------------------------------
log "STEP: orphan-delete StatefulSet ${RELEASE} (keeps pods and PVC)"
run "${KC[@]}" delete statefulset "${RELEASE}" --cascade=orphan

log "STEP: delete old pods (PVC ${PVC} is retained)"
for p in ${pods}; do
  run "${KC[@]}" delete "${p}" --wait=false
done

log "STEP: helm upgrade ${RELEASE} to the in-house chart"
run helm upgrade "${RELEASE}" "${CHART_DIR}" \
  --kube-context "${CONTEXT}" -n "${NAMESPACE}" \
  -f "${VALUES}" --timeout "${TIMEOUT}"

if [ "${CONFIRM}" != "true" ]; then
  log "DRY-RUN complete. No changes were made. Re-run with --confirm to execute."
  exit 0
fi

# --- Verify -----------------------------------------------------------------
log "STEP: verify all nodes reach UN (waiting up to ~10m)"
un_ok=false
for _ in $(seq 1 60); do
  status="$("${KC[@]}" exec "${RELEASE}-0" -c cassandra -- nodetool status 2>/dev/null || true)"
  if printf '%s\n' "${status}" | awk '
      /^[UD][NLJM]/ { seen=1; if ($1 != "UN") bad=1 }
      END { exit (!seen || bad) }
    '; then
    un_ok=true
    break
  fi
  sleep 10
done
[ "${un_ok}" = "true" ] || die "post-upgrade: one or more nodes are not UN after waiting. Data PVC ${PVC} is intact; investigate before retrying."
log "all ring members are UN"

if [ -n "${pre_count}" ]; then
  post_count="$("${KC[@]}" exec "${RELEASE}-0" -c cassandra -- \
    cqlsh -u "${CQL_USER}" -p "${CQL_PASS}" -e "SELECT COUNT(*) FROM ${PROBE_KEYSPACE}.${PROBE_TABLE};" 2>/dev/null \
    | grep -E '^[[:space:]]*[0-9]+' | tr -d ' ' || true)"
  log "post-migration count: ${post_count:-unknown} (pre: ${pre_count})"
  [ -n "${post_count}" ] && [ "${post_count}" = "${pre_count}" ] \
    || die "row-count mismatch (pre=${pre_count} post=${post_count}). Data PVC intact; investigate."
  log "row count preserved across migration"
fi

log "migration complete."
