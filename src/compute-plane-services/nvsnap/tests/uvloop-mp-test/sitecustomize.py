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
Auto-enable uvloop when available.

Python automatically imports sitecustomize on startup (if present on sys.path),
so placing this file alongside the app lets us enable uvloop without changing
app code. Set GPUCR_DISABLE_UVLOOP=1 to opt out.
"""

import os


def _install_uvloop() -> None:
    if os.environ.get("GPUCR_DISABLE_UVLOOP", "").lower() in {"1", "true", "yes"}:
        return
    try:
        import uvloop  # type: ignore
    except Exception:
        return
    try:
        uvloop.install()
    except Exception:
        return


_install_uvloop()
