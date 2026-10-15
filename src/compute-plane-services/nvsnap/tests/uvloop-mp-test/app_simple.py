#!/usr/bin/env python3
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
Simple uvloop test app WITHOUT multiprocessing.
Tests only uvloop/io_uring checkpoint/restore.
"""

import asyncio
import os
import sys

from aiohttp import web

async def health_handler(request):
    """Health check endpoint."""
    return web.json_response({"status": "ok", "pid": os.getpid()})

async def root_handler(request):
    """Root endpoint."""
    return web.json_response({
        "message": "uvloop simple test (no multiprocessing)",
        "pid": os.getpid(),
        "uvloop": True
    })

def main():
    print(f"[Main] Starting simple uvloop test app (NO multiprocessing)", flush=True)
    print(f"[Main] PID={os.getpid()}", flush=True)
    print(f"[Main] Python {sys.version}", flush=True)
    
    # Create web app
    app = web.Application()
    app.router.add_get('/health', health_handler)
    app.router.add_get('/', root_handler)
    
    print(f"[Main] Starting HTTP server on port 8000", flush=True)
    web.run_app(app, host='0.0.0.0', port=8000, print=None)

if __name__ == '__main__':
    main()
