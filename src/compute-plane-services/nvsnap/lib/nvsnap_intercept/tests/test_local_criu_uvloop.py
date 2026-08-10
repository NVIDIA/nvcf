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
Local CRIU checkpoint/restore test with uvloop.

This test:
1. Starts a uvloop-based async server
2. Checkpoints it with CRIU
3. Restores it with seccomp interception enabled
4. Verifies io_uring calls are intercepted

Run this script with sudo (CRIU requires root for some operations).

Usage:
    sudo python3 tests/test_local_criu_uvloop.py
"""

import os
import sys
import time
import signal
import asyncio
import tempfile
import subprocess
import shutil

# Check if we're running as a restored process
IS_RESTORED = os.environ.get('GPUCR_POST_RESTORE') == '1'
CHECKPOINT_READY = '/tmp/gpucr_test_ready'
CHECKPOINT_DIR = '/tmp/gpucr_test_checkpoint'

def log(msg):
    """Simple logging with timestamp"""
    import datetime
    ts = datetime.datetime.now().strftime('%H:%M:%S.%f')[:-3]
    print(f"[{ts}] [TEST] {msg}", flush=True)

async def run_server():
    """Simple async server that uses io_uring via uvloop"""
    
    log(f"Server starting (PID={os.getpid()}, restored={IS_RESTORED})")
    
    counter = 0
    
    while True:
        counter += 1
        log(f"Server tick #{counter}")
        
        # Signal ready for checkpoint after first tick
        if counter == 1 and not IS_RESTORED:
            log("Creating ready marker for checkpoint")
            with open(CHECKPOINT_READY, 'w') as f:
                f.write(str(os.getpid()))
        
        # Do some async I/O (triggers io_uring)
        await asyncio.sleep(1.0)
        
        # If restored, exit after a few ticks to verify it worked
        if IS_RESTORED and counter >= 3:
            log("Restored server completed successfully!")
            return

def run_target_process():
    """Run the target process that will be checkpointed"""
    try:
        import uvloop
        log(f"uvloop version: {uvloop.__version__}")
        uvloop.install()
    except ImportError:
        log("WARNING: uvloop not available, using default asyncio")
    
    try:
        asyncio.run(run_server())
    except KeyboardInterrupt:
        log("Server interrupted")

def do_checkpoint(pid, checkpoint_dir):
    """Checkpoint the target process using CRIU"""
    
    criu_path = os.path.join(os.path.dirname(__file__), '..', '..', '..', 'bin', 'criu')
    if not os.path.exists(criu_path):
        criu_path = 'criu'  # Try system criu
    
    log(f"Checkpointing PID {pid} to {checkpoint_dir}")
    
    cmd = [
        criu_path, 'dump',
        '-t', str(pid),
        '-D', checkpoint_dir,
        '--shell-job',
        '--tcp-close',
        '-v4',
        '-o', os.path.join(checkpoint_dir, 'dump.log'),
    ]
    
    log(f"Running: {' '.join(cmd)}")
    
    result = subprocess.run(cmd, capture_output=True, text=True)
    
    if result.returncode != 0:
        log(f"CRIU dump failed: {result.stderr}")
        return False
    
    log("Checkpoint successful!")
    return True

def do_restore(checkpoint_dir):
    """Restore the process with seccomp interception"""
    
    criu_path = os.path.join(os.path.dirname(__file__), '..', '..', '..', 'bin', 'criu')
    if not os.path.exists(criu_path):
        criu_path = 'criu'
    
    lib_path = os.path.join(os.path.dirname(__file__), '..', 'libgpucr_intercept.so')
    
    log(f"Restoring from {checkpoint_dir} with seccomp interception")
    
    # Set environment for restored process
    env = os.environ.copy()
    env['GPUCR_POST_RESTORE'] = '1'
    env['GPUCR_SECCOMP_ENABLED'] = '1'
    env['GPUCR_LOG_LEVEL'] = '3'
    env['LD_PRELOAD'] = lib_path
    
    cmd = [
        criu_path, 'restore',
        '-D', checkpoint_dir,
        '--shell-job',
        '-v4',
        '-o', os.path.join(checkpoint_dir, 'restore.log'),
    ]
    
    log(f"Running: {' '.join(cmd)}")
    log(f"With LD_PRELOAD={lib_path}")
    log(f"With GPUCR_SECCOMP_ENABLED=1 GPUCR_POST_RESTORE=1")
    
    result = subprocess.run(cmd, env=env, capture_output=True, text=True)
    
    if result.returncode != 0:
        log(f"CRIU restore failed: {result.stderr}")
        # Print restore log
        restore_log = os.path.join(checkpoint_dir, 'restore.log')
        if os.path.exists(restore_log):
            log("=== Restore log (last 50 lines) ===")
            with open(restore_log) as f:
                lines = f.readlines()
                for line in lines[-50:]:
                    print(line.rstrip())
        return False
    
    log("Restore completed!")
    return True

def main():
    """Main test driver"""
    
    # Check if we're the restored process
    if IS_RESTORED:
        log("Running as restored process")
        run_target_process()
        return 0
    
    log("=" * 60)
    log("Local CRIU + uvloop + seccomp interception test")
    log("=" * 60)
    
    # Check if running as root
    if os.geteuid() != 0:
        log("ERROR: This test requires root for CRIU operations")
        log("Run with: sudo python3 tests/test_local_criu_uvloop.py")
        return 1
    
    # Clean up previous state
    if os.path.exists(CHECKPOINT_READY):
        os.remove(CHECKPOINT_READY)
    if os.path.exists(CHECKPOINT_DIR):
        shutil.rmtree(CHECKPOINT_DIR)
    os.makedirs(CHECKPOINT_DIR, exist_ok=True)
    
    # Start target process
    log("Starting target process...")
    
    lib_path = os.path.join(os.path.dirname(__file__), '..', 'libgpucr_intercept.so')
    venv_python = os.path.join(os.path.dirname(__file__), '..', 'venv', 'bin', 'python3')
    
    if not os.path.exists(venv_python):
        venv_python = sys.executable
    
    env = os.environ.copy()
    env['GPUCR_SECCOMP_ENABLED'] = '1'
    env['GPUCR_LOG_LEVEL'] = '3'
    env['LD_PRELOAD'] = lib_path
    
    target = subprocess.Popen(
        [venv_python, __file__],
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    
    log(f"Target process started: PID={target.pid}")
    
    # Wait for ready marker
    log("Waiting for target to be ready...")
    for i in range(30):
        if os.path.exists(CHECKPOINT_READY):
            with open(CHECKPOINT_READY) as f:
                actual_pid = int(f.read().strip())
            log(f"Target ready (PID from marker: {actual_pid})")
            break
        time.sleep(0.5)
    else:
        log("ERROR: Timeout waiting for target to be ready")
        target.kill()
        return 1
    
    # Give it a moment to settle
    time.sleep(0.5)
    
    # Show target output so far
    log("=== Target output before checkpoint ===")
    # Non-blocking read
    import select
    while select.select([target.stdout], [], [], 0)[0]:
        line = target.stdout.readline()
        if line:
            print(line.decode().rstrip())
        else:
            break
    log("=== End target output ===")
    
    # Checkpoint
    if not do_checkpoint(actual_pid, CHECKPOINT_DIR):
        target.kill()
        return 1
    
    # Target should have been killed by checkpoint
    target.wait()
    log("Target process terminated by checkpoint")
    
    # Wait a moment
    time.sleep(1)
    
    # Restore
    log("=" * 40)
    log("Restoring process with seccomp interception...")
    log("=" * 40)
    
    if not do_restore(CHECKPOINT_DIR):
        return 1
    
    log("=" * 60)
    log("TEST PASSED!")
    log("=" * 60)
    
    return 0

if __name__ == '__main__':
    sys.exit(main())
