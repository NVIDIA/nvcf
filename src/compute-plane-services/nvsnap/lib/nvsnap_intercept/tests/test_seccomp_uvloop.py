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
Test seccomp interception with uvloop (statically linked libuv).

This test verifies that:
1. io_uring syscalls from uvloop are intercepted via seccomp
2. We can see SQPOLL mode being used
3. The intercepted calls proceed normally

Run with:
    source venv/bin/activate
    GPUCR_SECCOMP_ENABLED=1 GPUCR_LOG_LEVEL=3 LD_PRELOAD=./libgpucr_intercept.so python3 tests/test_seccomp_uvloop.py

For post-restore simulation:
    GPUCR_SECCOMP_ENABLED=1 GPUCR_POST_RESTORE=1 GPUCR_LOG_LEVEL=3 LD_PRELOAD=./libgpucr_intercept.so python3 tests/test_seccomp_uvloop.py
"""

import os
import sys
import asyncio

IS_POST_RESTORE = os.environ.get('GPUCR_POST_RESTORE') == '1'

def log(msg):
    import datetime
    ts = datetime.datetime.now().strftime('%H:%M:%S.%f')[:-3]
    mode = "POST-RESTORE" if IS_POST_RESTORE else "NORMAL"
    print(f"[{ts}] [{mode}] {msg}", flush=True)

async def do_io_operations():
    """Perform various async I/O operations that trigger io_uring"""
    
    log("Starting I/O operations...")
    
    # Operation 1: Simple sleep (timer)
    log("Op 1: asyncio.sleep(0.1)")
    await asyncio.sleep(0.1)
    log("Op 1 complete")
    
    # Operation 2: Another sleep
    log("Op 2: asyncio.sleep(0.1)")
    await asyncio.sleep(0.1)
    log("Op 2 complete")
    
    # Operation 3: File I/O (may use io_uring depending on loop impl)
    log("Op 3: Reading /proc/self/stat")
    loop = asyncio.get_event_loop()
    with open('/proc/self/stat', 'r') as f:
        content = f.read()
    log(f"Op 3 complete (read {len(content)} bytes)")
    
    # Operation 4: Subprocess (creates pipes, may use io_uring)
    log("Op 4: Running subprocess 'echo hello'")
    proc = await asyncio.create_subprocess_exec(
        'echo', 'hello',
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    stdout, stderr = await proc.communicate()
    log(f"Op 4 complete: {stdout.decode().strip()}")
    
    # Operation 5: Socket operation (network I/O)
    log("Op 5: Opening socket to localhost (expected to fail)")
    try:
        reader, writer = await asyncio.wait_for(
            asyncio.open_connection('127.0.0.1', 12345),
            timeout=0.1
        )
        writer.close()
        await writer.wait_closed()
    except (ConnectionRefusedError, asyncio.TimeoutError):
        log("Op 5 complete (connection refused/timeout as expected)")
    
    # Operation 6: Concurrent operations
    log("Op 6: Running 5 concurrent sleeps")
    await asyncio.gather(
        asyncio.sleep(0.05),
        asyncio.sleep(0.05),
        asyncio.sleep(0.05),
        asyncio.sleep(0.05),
        asyncio.sleep(0.05),
    )
    log("Op 6 complete")
    
    log("All I/O operations completed successfully!")

def main():
    print("=" * 70)
    print("seccomp + uvloop io_uring interception test")
    print("=" * 70)
    print(f"PID: {os.getpid()}")
    print(f"GPUCR_SECCOMP_ENABLED: {os.environ.get('GPUCR_SECCOMP_ENABLED', 'not set')}")
    print(f"GPUCR_POST_RESTORE: {os.environ.get('GPUCR_POST_RESTORE', 'not set')}")
    print(f"GPUCR_LOG_LEVEL: {os.environ.get('GPUCR_LOG_LEVEL', 'not set')}")
    print()
    
    # Try to use uvloop
    try:
        import uvloop
        print(f"uvloop version: {uvloop.__version__}")
        uvloop.install()
        print("Using uvloop event loop (statically links libuv)")
    except ImportError:
        print("WARNING: uvloop not available, using default asyncio")
        print("         io_uring interception may not be exercised")
    
    print()
    print("-" * 70)
    
    # Run the async operations
    asyncio.run(do_io_operations())
    
    print("-" * 70)
    print()
    print("=" * 70)
    print("TEST COMPLETED")
    print("Check output above for [SECCOMP] log lines showing intercepted calls")
    print("=" * 70)

if __name__ == '__main__':
    main()
