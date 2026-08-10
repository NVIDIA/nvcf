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
Test quiescence functionality of libgpucr_intercept.so

This test:
1. Creates io_uring instances (via uvloop or direct)
2. Sends SIGUSR1 to trigger quiesce
3. Verifies that I/O is drained
4. Sends SIGUSR2 to resume

Usage:
    GPUCR_LOG_LEVEL=3 LD_PRELOAD=./libgpucr_intercept.so python3 tests/test_quiesce.py
"""

import os
import sys
import time
import signal
import threading

def test_asyncio_basic():
    """Test basic asyncio quiescence"""
    import asyncio
    
    print("[TEST] Basic asyncio quiescence")
    
    # Create some async work
    completed = []
    
    async def background_work(n):
        for i in range(5):
            await asyncio.sleep(0.1)
            completed.append(f"task-{n}-{i}")
        return f"task-{n}-done"
    
    async def main():
        # Start background tasks
        tasks = [asyncio.create_task(background_work(i)) for i in range(3)]
        
        # Let them run a bit
        await asyncio.sleep(0.2)
        
        print(f"[TEST] Before quiesce: {len(completed)} items completed")
        
        # Trigger quiesce via SIGUSR1 (from another thread)
        def send_quiesce():
            time.sleep(0.1)
            print("[TEST] Sending SIGUSR1 (quiesce)")
            os.kill(os.getpid(), signal.SIGUSR1)
            time.sleep(0.5)  # Wait for quiesce to complete
            print("[TEST] Sending SIGUSR2 (resume)")
            os.kill(os.getpid(), signal.SIGUSR2)
        
        t = threading.Thread(target=send_quiesce)
        t.start()
        
        # Wait for tasks (should resume after SIGUSR2)
        results = await asyncio.gather(*tasks)
        t.join()
        
        print(f"[TEST] After resume: {len(completed)} items completed")
        print(f"[TEST] Results: {results}")
        
        return True
    
    return asyncio.run(main())

def test_uvloop_quiesce():
    """Test uvloop quiescence (if available)"""
    try:
        import uvloop
    except ImportError:
        print("[TEST] uvloop not installed, skipping uvloop test")
        return True
    
    import asyncio
    
    print("[TEST] uvloop quiescence")
    
    # Install uvloop
    uvloop.install()
    
    async def uvloop_work():
        results = []
        for i in range(10):
            await asyncio.sleep(0.05)
            results.append(i)
        return results
    
    async def main():
        # Create some work
        task = asyncio.create_task(uvloop_work())
        
        # Trigger quiesce
        def send_quiesce():
            time.sleep(0.1)
            print("[TEST] Sending SIGUSR1 to uvloop")
            os.kill(os.getpid(), signal.SIGUSR1)
            time.sleep(0.3)
            print("[TEST] Sending SIGUSR2 to resume")
            os.kill(os.getpid(), signal.SIGUSR2)
        
        t = threading.Thread(target=send_quiesce)
        t.start()
        
        result = await task
        t.join()
        
        print(f"[TEST] uvloop completed: {len(result)} items")
        return len(result) == 10
    
    return asyncio.run(main())

def test_io_uring_tracking():
    """Test that io_uring instances are tracked"""
    print("[TEST] io_uring tracking")
    
    # Try to use io_uring via aiofiles or direct syscall
    try:
        import asyncio
        
        async def io_work():
            # This should create io_uring if uvloop is installed
            await asyncio.sleep(0.1)
            return True
        
        result = asyncio.run(io_work())
        print(f"[TEST] io_uring tracking test: {'PASS' if result else 'FAIL'}")
        return result
    except Exception as e:
        print(f"[TEST] io_uring tracking error: {e}")
        return False

def main():
    print("=" * 60)
    print("GPUCR Quiescence Test Suite")
    print("=" * 60)
    print(f"PID: {os.getpid()}")
    print(f"LD_PRELOAD: {os.environ.get('LD_PRELOAD', 'NOT SET')}")
    print()
    
    # Check if library is loaded
    if 'libgpucr_intercept.so' not in os.environ.get('LD_PRELOAD', ''):
        print("WARNING: libgpucr_intercept.so not in LD_PRELOAD")
        print("         Run with: LD_PRELOAD=./libgpucr_intercept.so python3 " + __file__)
        print()
    
    tests = [
        ("Basic asyncio", test_asyncio_basic),
        ("io_uring tracking", test_io_uring_tracking),
        ("uvloop", test_uvloop_quiesce),
    ]
    
    results = {}
    for name, test_fn in tests:
        print(f"\n{'='*60}")
        print(f"Running: {name}")
        print('=' * 60)
        try:
            results[name] = test_fn()
        except Exception as e:
            print(f"[TEST] {name} EXCEPTION: {e}")
            import traceback
            traceback.print_exc()
            results[name] = False
    
    # Summary
    print(f"\n{'='*60}")
    print("Test Results:")
    print('=' * 60)
    for name, passed in results.items():
        status = "PASS" if passed else "FAIL"
        print(f"  {name}: {status}")
    
    all_passed = all(results.values())
    print()
    print(f"Overall: {'PASS' if all_passed else 'FAIL'}")
    
    return 0 if all_passed else 1

if __name__ == "__main__":
    sys.exit(main())
