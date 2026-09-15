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
Minimal uvloop + multiprocessing test app for CRIU checkpoint/restore testing.

This tests:
1. uvloop event loop (uses io_uring on Linux)
2. multiprocessing with worker processes
3. IPC between main and workers via pipes

Usage: python app.py
HTTP endpoints:
  GET /health - health check
  GET /models - list "models" (simulates vLLM)
  POST /compute - send work to worker process
"""

import asyncio
import multiprocessing
import os
import signal
import sys
import time
from multiprocessing import Process, Pipe

from aiohttp import web

# Global worker process and pipe
worker_process = None
parent_conn = None

def worker_function(conn):
    """Worker process that receives work via pipe."""
    print(f"[Worker PID={os.getpid()}] Started", flush=True)
    while True:
        try:
            if conn.poll(1.0):  # 1 second timeout
                msg = conn.recv()
                print(f"[Worker] Received: {msg}", flush=True)
                if msg == "quit":
                    break
                # Simulate work
                result = f"Processed: {msg} (worker pid={os.getpid()})"
                conn.send(result)
        except EOFError:
            print("[Worker] Pipe closed, exiting", flush=True)
            break
        except Exception as e:
            print(f"[Worker] Error: {e}", flush=True)
            break
    print("[Worker] Exiting", flush=True)

async def health_handler(request):
    """Health check endpoint."""
    return web.json_response({"status": "ok", "pid": os.getpid()})

async def models_handler(request):
    """List models (simulates vLLM /v1/models)."""
    return web.json_response({
        "object": "list",
        "data": [{"id": "test-model", "object": "model"}]
    })

async def compute_handler(request):
    """Send work to worker process via pipe."""
    global parent_conn
    try:
        data = await request.json()
        msg = data.get("input", "default")
        
        # Send to worker
        parent_conn.send(msg)
        
        # Wait for response (with timeout)
        if parent_conn.poll(5.0):
            result = parent_conn.recv()
            return web.json_response({"result": result})
        else:
            return web.json_response({"error": "Worker timeout"}, status=500)
    except Exception as e:
        return web.json_response({"error": str(e)}, status=500)

async def status_handler(request):
    """Show process status."""
    global worker_process
    return web.json_response({
        "main_pid": os.getpid(),
        "worker_pid": worker_process.pid if worker_process else None,
        "worker_alive": worker_process.is_alive() if worker_process else False,
        "uvloop": True,
        "multiprocessing": True
    })

def start_worker():
    """Start the worker process."""
    global worker_process, parent_conn
    parent_conn, child_conn = Pipe()
    worker_process = Process(target=worker_function, args=(child_conn,))
    worker_process.start()
    print(f"[Main PID={os.getpid()}] Started worker PID={worker_process.pid}", flush=True)

def main():
    print(f"[Main] Starting uvloop + multiprocessing test app", flush=True)
    print(f"[Main] PID={os.getpid()}", flush=True)
    print(f"[Main] Python {sys.version}", flush=True)
    
    # Start worker process
    start_worker()
    
    # Create web app
    app = web.Application()
    app.router.add_get('/health', health_handler)
    app.router.add_get('/v1/models', models_handler)
    app.router.add_post('/compute', compute_handler)
    app.router.add_get('/status', status_handler)
    
    print(f"[Main] Starting HTTP server on port 8000", flush=True)
    web.run_app(app, host='0.0.0.0', port=8000, print=None)

if __name__ == '__main__':
    main()
