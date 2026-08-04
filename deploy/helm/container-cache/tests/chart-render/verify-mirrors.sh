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
