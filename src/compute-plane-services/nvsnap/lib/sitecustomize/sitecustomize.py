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

"""NvSnap runtime sitecustomize — prepends version-matched site-packages.

Imported automatically by CPython's site.py during interpreter startup
because the directory containing this file is on PYTHONPATH (set by every
NvSnap-enabled pod's env). Detects the running interpreter's ABI tag and
prepends /nvsnap-lib/site-packages-cpXY to sys.path so `import uvloop`
(and any future patched packages) resolve to the NvSnap-patched copies
before any vendored or venv-installed stock copy.

The patched native libraries (libzmq, libuv) are not Python-version-
keyed; they live directly under /nvsnap-lib and are routed via
LD_LIBRARY_PATH — see docs/GENERIC-PYTHON-INJECTION-DESIGN.md.

This file is intentionally minimal:
  - No imports beyond os/sys (no risk of breaking interpreter startup).
  - No-op if /nvsnap-lib/site-packages-cpXY is absent (graceful when the
    pod runs without NvSnap init containers).
  - No chain-load of a customer-provided sitecustomize. If a future BYOC
    workload ships its own sitecustomize.py, add an importlib chain here.
"""

import os
import sys

_PYVER = "cp{}{}".format(sys.version_info.major, sys.version_info.minor)
_PKG_DIR = "/nvsnap-lib/site-packages-" + _PYVER

if os.path.isdir(_PKG_DIR) and _PKG_DIR not in sys.path:
    sys.path.insert(0, _PKG_DIR)
