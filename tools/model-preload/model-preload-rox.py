#!/usr/bin/env python3
#
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
"""
Preload an NGC model into a shared ReadOnlyMany (ROX) volume so N worker pods
mount one copy read-only instead of each downloading it. Turns an N-way download
herd into a single download and stores one copy instead of N.

Pattern: a CSI backend with reclaimPolicy=Retain keeps the underlying volume
alive across PVC churn, so a ROX PV can be pointed at the populated volume handle
after a RWO downloader finishes. Proven with NVMesh (nvcf-sc): a ROX volume can
be attached read-only by many pods across many nodes at once.

Flow:
  1. read the model's total size from NGC; PVC size = size * multiplier (>= floor)
  2. create a RWO PVC on the storage class + a downloader Job (ngc download)
  3. wait for the Job to complete
  4. read the bound PV's csi volumeHandle
  5. release the RWO claim (Retain keeps the volume), then create a ROX PV on the
     same handle + a ROX PVC bound to it
  6. print the ROX PVC name; worker pods mount it with readOnly: true

Prototype scope: single storage class, single model, no concurrency guard. Not a
controller. See README.md for the design considerations before productionizing.

Requires: kubectl on PATH with cluster access; python3; an NGC API key in env
NGC_API_KEY for the size lookup; a k8s secret holding the key (data key
NGC_API_KEY) for the downloader Job.
"""
import argparse
import json
import os
import subprocess
import sys
import urllib.request


def kubectl(args, inp=None, check=True):
    p = subprocess.run(["kubectl"] + args, input=inp, capture_output=True, text=True)
    if check and p.returncode != 0:
        sys.exit(f"kubectl {' '.join(args)} failed:\n{p.stderr}")
    return p.stdout


def ngc_total_size_bytes(model, api_key, api_base="https://api.ngc.nvidia.com"):
    # model = org/[team/]name:version
    name, ver = model.rsplit(":", 1)
    parts = name.split("/")
    org = parts[0]
    if len(parts) == 3:
        team, mdl = parts[1], parts[2]
        path = f"v2/org/{org}/team/{team}/models/{mdl}/versions/{ver}"
    else:
        mdl = parts[1]
        path = f"v2/org/{org}/models/{mdl}/versions/{ver}"
    req = urllib.request.Request(
        f"{api_base}/{path}",
        headers={"Authorization": f"Bearer {api_key}", "Accept": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=30) as r:
        d = json.load(r)
    # NGC nests the version under modelVersion on some API revisions, top-level on others.
    v = d.get("modelVersion", d)
    size = v.get("totalSizeInBytes") or v.get("total_size_in_bytes")
    if not size:
        sys.exit(f"could not read totalSizeInBytes from NGC response: {list(v)[:10]}")
    return int(size)


def apply(manifest):
    kubectl(["apply", "-f", "-"], inp=manifest)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--model", required=True, help="org/[team/]name:version")
    ap.add_argument("--namespace", required=True)
    ap.add_argument("--name", required=True, help="base name for the PVCs/Job/PV")
    ap.add_argument("--storage-class", default="nvcf-sc")
    ap.add_argument("--ngc-secret", required=True, help="k8s secret with data key NGC_API_KEY")
    ap.add_argument("--image", required=True, help="downloader image with bash+python3+curl")
    ap.add_argument("--mount", default="/models")
    ap.add_argument("--multiplier", type=float, default=1.1)
    ap.add_argument("--floor-gi", type=int, default=10, help="minimum PVC size, GiB")
    ap.add_argument("--pull-secret", default="")
    ap.add_argument("--csi-driver", default="nvmesh-csi.excelero.com")
    ap.add_argument("--cli-version", default="4.20.1")
    args = ap.parse_args()

    ns, base, sc = args.namespace, args.name, args.storage_class
    rwo_pvc = job = f"{base}-dl"
    rox_pv, rox_pvc = f"{base}-ro-pv", f"{base}-ro"

    # 1. size
    api_key = os.environ.get("NGC_API_KEY", "")
    if not api_key:
        sys.exit("set NGC_API_KEY for the size lookup")
    size_b = ngc_total_size_bytes(args.model, api_key)
    gi = max(args.floor_gi, int((size_b * args.multiplier) // (1024**3)) + 1)
    print(
        f"[preload] {args.model}: {size_b / 1024**3:.1f} GiB -> PVC {gi} Gi "
        f"({args.multiplier}x + floor {args.floor_gi})"
    )

    # 2. RWO PVC + downloader Job
    ips = f"\n      imagePullSecrets: [{{name: {args.pull_secret}}}]" if args.pull_secret else ""
    apply(
        f"""
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {{name: {rwo_pvc}, namespace: {ns}}}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: {sc}
  resources: {{requests: {{storage: {gi}Gi}}}}
"""
    )
    apply(
        f"""
apiVersion: batch/v1
kind: Job
metadata: {{name: {job}, namespace: {ns}}}
spec:
  backoffLimit: 2
  template:
    spec:
      restartPolicy: Never{ips}
      containers:
      - name: dl
        image: {args.image}
        command: ["/bin/bash","-c"]
        args:
        - |
          set -e
          ARCH=$(uname -m); Z=ngccli_linux.zip
          [ "$ARCH" = aarch64 -o "$ARCH" = arm64 ] && Z=ngccli_arm64.zip
          curl -skL -o /tmp/n.zip "https://api.ngc.nvidia.com/v2/resources/nvidia/ngc-apps/ngc_cli/versions/{args.cli_version}/files/$Z"
          python3 -c "import zipfile;zipfile.ZipFile('/tmp/n.zip').extractall('/tmp')"; chmod 755 /tmp/ngc-cli/ngc
          mkdir -p ~/.ngc; printf '[CURRENT]\\napikey = %s\\nformat_type = json\\n' "$NGC_API_KEY" > ~/.ngc/config
          /tmp/ngc-cli/ngc registry model download-version --dest {args.mount} "{args.model}"
          echo "[preload] download complete"; ls -la {args.mount}
        env:
        - name: NGC_API_KEY
          valueFrom: {{secretKeyRef: {{name: {args.ngc_secret}, key: NGC_API_KEY}}}}
        volumeMounts: [{{name: models, mountPath: {args.mount}}}]
      volumes:
      - name: models
        persistentVolumeClaim: {{claimName: {rwo_pvc}}}
"""
    )

    # 3. wait for the Job
    print(f"[preload] waiting for download Job {job} ...")
    kubectl(["-n", ns, "wait", f"job/{job}", "--for=condition=complete", "--timeout=6h"])
    print("[preload] download Job complete")

    # 4. read the CSI volume handle from the bound PV
    pv = kubectl(["-n", ns, "get", "pvc", rwo_pvc, "-o", "jsonpath={.spec.volumeName}"]).strip()
    handle = kubectl(["get", "pv", pv, "-o", "jsonpath={.spec.csi.volumeHandle}"]).strip()
    fstype = kubectl(["get", "pv", pv, "-o", "jsonpath={.spec.csi.fsType}"]).strip() or "xfs"
    print(f"[preload] populated volume handle: {handle}")

    # 5. release the RWO claim (Retain keeps the underlying volume), then bind ROX.
    #    Deleting the Job frees the single-node RWO attach before the ROX attach.
    kubectl(["-n", ns, "delete", "job", job, "--wait=true", "--timeout=120s"], check=False)
    kubectl(["-n", ns, "delete", "pvc", rwo_pvc, "--wait=true", "--timeout=120s"], check=False)

    # 6. ROX PV on the same handle + ROX PVC bound to it (static binding, no dataSource)
    apply(
        f"""
apiVersion: v1
kind: PersistentVolume
metadata: {{name: {rox_pv}}}
spec:
  capacity: {{storage: {gi}Gi}}
  accessModes: [ReadOnlyMany]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: {sc}
  csi:
    driver: {args.csi_driver}
    volumeHandle: "{handle}"
    fsType: {fstype}
    readOnly: true
"""
    )
    apply(
        f"""
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {{name: {rox_pvc}, namespace: {ns}}}
spec:
  accessModes: [ReadOnlyMany]
  storageClassName: {sc}
  volumeName: {rox_pv}
  resources: {{requests: {{storage: {gi}Gi}}}}
"""
    )
    kubectl(
        ["-n", ns, "wait", f"pvc/{rox_pvc}",
         "--for=jsonpath={.status.phase}=Bound", "--timeout=120s"],
        check=False,
    )
    print(f"[preload] DONE. Workers mount ROX PVC '{rox_pvc}' with readOnly: true at {args.mount}.")
    print(f"[preload] volume handle {handle} retained; delete PV {rox_pv} to reclaim.")


if __name__ == "__main__":
    main()
