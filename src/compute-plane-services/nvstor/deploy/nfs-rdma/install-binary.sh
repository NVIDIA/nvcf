#!/bin/bash
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

# Install an nvstor binary (nvstor-nfs-export, nvstor-peer) on nodes at
# /var/lib/nvstor/bin (host path), under its own name, where source-pod.yaml
# and peer-fanout.yaml run it through chroot /host. The binary lives on the
# node and runs against the node's rdma-core (or OFED) libraries.
#
#   install-binary.sh BINARY NODE...
#
# BINARY: an aarch64 release build (build-aarch64.sh leaves it in
# target/aarch64-unknown-linux-gnu/release/).
# For each node: a short-lived pod with the host directory mounted, kubectl
# cp, atomic rename, pod deleted. DRYRUN=1 validates the manifest only.
set -u
BIN=${1:?usage: install-binary.sh BINARY NODE...}
shift
[ $# -ge 1 ] || { echo "usage: install-binary.sh BINARY NODE..."; exit 1; }
NS=${NS:-default}
IMG=nvcr.io/nvidia/cuda:13.0.0-base-ubuntu22.04
DIR=/var/lib/nvstor/bin
NAME=$(basename "$BIN")

manifest() { # pod node
  cat <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: $1
  labels: {app: nvstor-install}
spec:
  nodeName: "$2"
  restartPolicy: Never
  tolerations: [{operator: Exists}]
  containers:
  - name: t
    image: $IMG
    imagePullPolicy: IfNotPresent
    command: ["sleep", "600"]
    volumeMounts: [{name: bin, mountPath: /dst}]
  volumes:
  - name: bin
    hostPath: {path: $DIR, type: DirectoryOrCreate}
YAML
}

if [ -n "${DRYRUN:-}" ]; then
  manifest nvstor-install-dryrun "$1" | kubectl apply -n "$NS" --dry-run=server -f - || exit 1
  exit 0
fi
[ -f "$BIN" ] || { echo "missing $BIN"; exit 1; }
file "$BIN" | grep -q "ARM aarch64" || { echo "$BIN is not an aarch64 binary"; exit 1; }

rc=0
for node in "$@"; do
  pod=nvstor-install-$(date +%s)-$RANDOM
  if manifest "$pod" "$node" | kubectl apply -n "$NS" -f - \
     && kubectl wait -n "$NS" --for=condition=Ready "pod/$pod" --timeout=120s \
     && kubectl cp "$BIN" "$NS/$pod:/dst/.$NAME.tmp" \
     && kubectl exec -n "$NS" "$pod" -- sh -c "chmod 755 /dst/.$NAME.tmp && mv /dst/.$NAME.tmp /dst/$NAME" </dev/null; then
    echo "$node: installed $DIR/$NAME"
  else
    echo "$node: install FAILED"; rc=1
  fi
  kubectl delete pod -n "$NS" "$pod" --wait=false
done
exit $rc
