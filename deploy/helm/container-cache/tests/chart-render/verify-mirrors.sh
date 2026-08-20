#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT_FILE="$(mktemp)"
trap 'rm -f "${OUT_FILE}"' EXIT

cd "${ROOT_DIR}"

# Render a multi-domain scenario covering both CRI-O and containerd paths.
helm template container-cache ./deploy \
  --set-string targetHost="nvcr.io\,stg.nvcr.io\,docker.io" \
  > "${OUT_FILE}"

# Use `grep -F` (fixed-string) and POSIX-portable flags. Every assertion below
# is a literal substring match on a single rendered line, so we do not need
# regex semantics or ripgrep (which is not installed on the CI tools image).
assert_has() {
  local needle="$1"
  if ! grep -F -q -- "${needle}" "${OUT_FILE}"; then
    echo "FAILED: expected pattern not found: ${needle}" >&2
    exit 1
  fi
}

assert_not_has() {
  local needle="$1"
  if grep -F -q -- "${needle}" "${OUT_FILE}"; then
    echo "FAILED: unexpected pattern found: ${needle}" >&2
    exit 1
  fi
}

echo "Checking Service shape..."
assert_has 'kind: Service'
assert_has 'name: nvcf-container-cache'
# externalTrafficPolicy: Local was removed -- it caused connection-refused on
# nodes without a local cache pod (kube-proxy drops NodePort traffic).
assert_not_has 'externalTrafficPolicy: Local'
assert_not_has 'internalTrafficPolicy: Local'
# The registry mirror endpoint is ${NODE_IP}:${port}, so the port must be
# published on every node. A ClusterIP service renders no nodePort and the
# mirror then points at a closed port, which fails as a silent fallback to the
# upstream registry rather than a visible error.
assert_has 'type: NodePort'

echo "Checking service.type cannot be overridden..."
# Capture rather than pipe: `set -o pipefail` would otherwise report helm's
# intentional non-zero exit as the pipeline's failure.
override_out="$(helm template container-cache ./deploy --set service.type=ClusterIP 2>&1 || true)"
if ! printf '%s' "${override_out}" | grep -F -q -- 'is not supported'; then
  echo "FAILED: service.type=ClusterIP should fail the render with an explanation" >&2
  echo "${override_out}" >&2
  exit 1
fi
# NodePort is still accepted, so existing values files keep working.
helm template container-cache ./deploy --set service.type=NodePort >/dev/null

echo "Checking containerd pending-restart reporting..."
# containerd reads registry.config_path only at daemon start and has no config
# reload, so correcting config.toml does not take effect on its own. Report the
# node; never restart containerd, which would be disruptive on nodes running
# function workloads.
assert_has 'before="$(sha256sum /host/etc/containerd/config.toml | cut -d'"'"' '"'"' -f1)"'
assert_has 'if [ "${before}" != "${after}" ]; then'
assert_not_has 'systemctl restart containerd'
assert_not_has 'nsenter'
# The hash diff is a log signal only. Readiness derived from it reports
# false-ready after a pod restart, because update_config.py is idempotent and
# the hashes match even while the running containerd holds the stale config.
assert_not_has 'containerd_restart_pending'

echo "Checking readiness reflects whether cache routing is live..."
# A DaemonSet ready count below its desired count is the signal that some nodes
# still pull straight from the upstream registry.
assert_has 'test -f /tmp/nvcf-cc-ready'
assert_has 'readinessProbe:'
assert_has 'touch "${READY_MARKER}"'
# Readiness comes from live runtime state: containerd's process start time
# versus the mtime of the config it only reads at startup.
assert_has 'containerd_config_active() {'
assert_has 'pgrep -x containerd'
assert_has 'proc_start_epoch() {'
assert_has '[ "${start}" -gt "${mtime}" ]'
# Self-correcting in both directions, so a node that loses activation stops
# reporting ready.
assert_has 'rm -f "${READY_MARKER}"'
# Re-evaluated on a short interval, so a node flips to ready on its own once an
# operator restarts containerd. The old 24h sleep never re-checked.
assert_has 'reconcile_ready_marker() {'
assert_has 'while true; do'
assert_has 'sleep 60'
assert_not_has 'sleep 86400'
# The reconcile loop must only read state. Rewriting host config there races
# with the OS / container toolkit, so the updater must be invoked exactly once,
# in the one-shot setup phase above the loop.
updater_calls="$(grep -F -c -- 'python3 update_config.py' "${OUT_FILE}" || true)"
if [ "${updater_calls}" != "1" ]; then
  echo "FAILED: expected update_config.py to be invoked once, found ${updater_calls}" >&2
  exit 1
fi

echo "Checking CRI-O reload..."
# SIGHUP is a documented CRI-O reload, not a kill, so it is safe to signal.
# Needed because auto_reload_registries only applies once CRI-O has read the
# crio.conf.d drop-in this DaemonSet writes.
assert_has 'kill -HUP "${crio_pid}"'
assert_has 'pgrep -x crio'
# A failed SIGHUP must not mark the node ready: the reload marker is stamped
# only on success, and readiness compares it against the drop-in mtime.
assert_has 'touch "${CRIO_RELOAD_MARKER}"'
assert_has 'crio_config_active() {'
assert_has '[ "${reloaded}" -ge "${mtime}" ]'

echo "Checking multi-domain NodePort listeners..."
assert_has 'nodePort: 30346'
assert_has 'nodePort: 30347'
assert_has 'nodePort: 30348'
assert_has 'name: crio-nvcr-io'
assert_has 'name: crio-stg-nvcr-i'
assert_has 'name: crio-docker-io'

echo "Checking generated registry->port map used by both runtimes..."
assert_has 'CRIO_PORTS["nvcr.io"]="30346"'
assert_has 'CRIO_PORTS["stg.nvcr.io"]="30347"'
assert_has 'CRIO_PORTS["docker.io"]="30348"'

echo "Checking containerd mirror behavior..."
assert_has '[host."https://${NODE_IP}:${port}"]'

echo "Checking CRI-O static mirror behavior..."
# CRI-O mirror points at NODE_IP (NodePort), not cluster service DNS, because
# the CRI-O daemon runs in the host network namespace and typically cannot
# resolve *.svc.cluster.local.
assert_has 'location = "%s:%s"'
assert_not_has 'registry_mirror_host='
# We do not rewrite /etc/containers/registries.conf -- only the drop-in.
assert_not_has 'crio_main='
# No 5-minute refresh loop.
assert_not_has 'sleep 300'
# pull-from-mirror must live in the [[registry.mirror]] block, not on the
# parent [[registry]]; CRI-O / containers-image rejects the latter.
assert_has 'pull-from-mirror = "all"'
# We mirror the drop-in into $HOME/.config/containers/registries.conf.d/ for
# distros (e.g. Oracle Linux) that ship a user-level registries.conf for root.
assert_has '/host/root/.config/containers'
assert_has 'user_drop_in_dir='

echo "Mirror render checks passed."
