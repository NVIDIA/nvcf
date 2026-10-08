# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import datetime
import json
import os
import pathlib
import platform
import shutil
import subprocess


def command(args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=20)
    return {"returncode": result.returncode, "stdout": result.stdout, "stderr": result.stderr}


def memory():
    fields = {}
    for line in pathlib.Path("/proc/meminfo").read_text().splitlines():
        name, value = line.split(":", 1)
        fields[name] = int(value.split()[0]) * 1024
    return {k: fields[k] for k in ("MemTotal", "MemAvailable", "SwapTotal", "SwapFree")}


report = {
    "time": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "architecture": platform.machine(),
    "memoryBefore": memory(),
    "gpuBefore": command(["nvidia-smi"]),
    "computeBefore": command(["nvidia-smi", "--query-compute-apps=pid,process_name,used_memory", "--format=csv,noheader"]),
    "tools": {name: shutil.which(name) for name in ("nvcc", "cmake", "ninja", "git", "g++", "curl", "ibv_devinfo")},
}
print(json.dumps({"preflightBeforeCuda": report}), flush=True)
assert not report["computeBefore"]["stdout"].strip(), "GPU already has a compute process"
assert report["architecture"] == os.environ.get("EXPECTED_ARCHITECTURE", "aarch64")

import torch

assert torch.cuda.is_available() and torch.cuda.device_count() == 1
torch.backends.cuda.matmul.allow_tf32 = False
torch.manual_seed(29)
a = torch.randn((512, 512), device="cuda", dtype=torch.float32)
b = torch.randn((512, 512), device="cuda", dtype=torch.float32)
actual = a @ b
torch.cuda.synchronize()
reference = a.cpu() @ b.cpu()
assert torch.allclose(actual.cpu(), reference, atol=0.001, rtol=0.001)
free_bytes, total_bytes = torch.cuda.mem_get_info()
report.update({
    "torch": torch.__version__, "cuda": torch.version.cuda,
    "gpu": torch.cuda.get_device_name(), "capability": torch.cuda.get_device_capability(),
    "tensorDevice": str(actual.device),
    "cudaFreeBytes": free_bytes, "cudaTotalBytes": total_bytes,
    "maximumAbsoluteError": float((actual.cpu() - reference).abs().max()),
    "memoryAfter": memory(), "result": "PASS",
})
print(json.dumps(report), flush=True)
