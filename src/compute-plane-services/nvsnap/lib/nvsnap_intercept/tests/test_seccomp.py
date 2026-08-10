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
Test seccomp-bpf interception of io_uring syscalls.

This test verifies that the seccomp filter intercepts io_uring_enter
even when called from statically-linked code (uvloop).

Run with:
    GPUCR_SECCOMP_ENABLED=1 GPUCR_LOG_LEVEL=4 LD_PRELOAD=./libgpucr_intercept.so python3 tests/test_seccomp.py
"""

import os
import sys
import asyncio

def test_with_uvloop():
    """Test io_uring interception via uvloop (statically links libuv)"""
    try:
        import uvloop
        print("uvloop available, testing with uvloop event loop")
        uvloop.install()
    except ImportError:
        print("uvloop not available, testing with default asyncio")

    async def simple_io():
        """Do some async I/O to trigger io_uring operations"""
        print("Starting async I/O operations...")
        
        # These operations may use io_uring internally
        await asyncio.sleep(0.1)
        print("After sleep 1")
        
        await asyncio.sleep(0.1)
        print("After sleep 2")
        
        # Try some actual I/O
        proc = await asyncio.create_subprocess_exec(
            'echo', 'hello',
            stdout=asyncio.subprocess.PIPE
        )
        stdout, _ = await proc.communicate()
        print(f"Subprocess output: {stdout.decode().strip()}")
        
        print("Async I/O completed")

    asyncio.run(simple_io())

def main():
    print("=" * 60)
    print("seccomp-bpf io_uring interception test")
    print("=" * 60)
    
    print(f"PID: {os.getpid()}")
    print(f"GPUCR_SECCOMP_ENABLED: {os.environ.get('GPUCR_SECCOMP_ENABLED', 'not set')}")
    print(f"GPUCR_POST_RESTORE: {os.environ.get('GPUCR_POST_RESTORE', 'not set')}")
    print(f"GPUCR_LOG_LEVEL: {os.environ.get('GPUCR_LOG_LEVEL', 'not set')}")
    print()
    
    test_with_uvloop()
    
    print()
    print("=" * 60)
    print("Test completed - check output for [SECCOMP] log lines")
    print("=" * 60)

if __name__ == "__main__":
    main()
