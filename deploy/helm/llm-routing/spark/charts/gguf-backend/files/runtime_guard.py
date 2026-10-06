# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Stop the owned runtime on low host memory or its own swapped pages."""
import datetime
import json
import os
import pathlib
import signal
import subprocess
import sys
import threading
import time


def memory():
    values = {}
    for line in pathlib.Path('/proc/meminfo').read_text().splitlines():
        name, *fields = line.split()
        if name in ('MemAvailable:', 'SwapTotal:', 'SwapFree:'):
            values[name.rstrip(':')] = int(fields[0]) * 1024
    return values['MemAvailable'], values['SwapTotal'] - values['SwapFree']


def process_memory(pid):
    try:
        lines = pathlib.Path('/proc', str(pid), 'status').read_text().splitlines()
    except FileNotFoundError:
        return {}
    return {name.rstrip(':'): int(fields[0]) * 1024 for name, *fields in map(str.split, lines)
            if name in ('VmRSS:', 'VmSwap:')}


def cgroup_memory():
    root = pathlib.Path('/sys/fs/cgroup')
    return {'currentBytes': int((root / 'memory.current').read_text()),
            'swapBytes': int((root / 'memory.swap.current').read_text()),
            'events': {name: int(value) for name, value in
                       map(str.split, (root / 'memory.events').read_text().splitlines())}}


def supervise(command, marker_path='/tmp/runtime-memory-stop.json'):
    marker = pathlib.Path(marker_path)
    if marker.exists():
        print('Runtime remains stopped after a memory guard failure. Replace this owned pod to retry.', flush=True)
        return 78
    floor = int(os.environ.get('MIN_HOST_AVAILABLE_BYTES', 1024**3))
    available, swap = memory()
    if available < floor:
        print(json.dumps({'result': 'MEMORY_GUARD_REFUSED_START', 'availableBytes': available, 'swapUsedBytes': swap}), flush=True)
        return 78
    child = subprocess.Popen(command, start_new_session=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    def copy_output():
        with child.stdout, marker.with_name('runtime-output.log').open('ab', buffering=0) as output:
            while chunk := child.stdout.read1(8192):
                output.write(chunk)
                sys.stdout.buffer.write(chunk)
                sys.stdout.buffer.flush()
    copier = threading.Thread(target=copy_output, daemon=True)
    copier.start()
    def stop_child(*_):
        if child.poll() is None:
            os.killpg(child.pid, signal.SIGTERM)
    signal.signal(signal.SIGTERM, stop_child)
    signal.signal(signal.SIGINT, stop_child)
    sample = 0
    while child.poll() is None:
        available, swap = memory()
        process = process_memory(child.pid)
        cgroup = cgroup_memory()
        reading = {'time': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                   'availableBytes': available, 'hostSwapUsedBytes': swap, 'process': process, 'cgroup': cgroup}
        reason = ('host_available' if available < floor else
                  'process_swap' if process.get('VmSwap', 0) else
                  'cgroup_swap' if cgroup['swapBytes'] else None)
        if reason:
            failure = {'time': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'result': 'MEMORY_GUARD_STOP',
                       **reading, 'reason': reason, 'minimumAvailableBytes': floor}
            marker.write_text(json.dumps(failure) + '\n')
            print(json.dumps(failure), flush=True)
            stop_child()
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(child.pid, signal.SIGKILL)
                child.wait()
            copier.join(timeout=2)
            return 78
        if sample % 10 == 0:
            with marker.with_name('runtime-memory.jsonl').open('a') as output:
                output.write(json.dumps(reading) + '\n')
            print(json.dumps({'result': 'MEMORY_SAMPLE', **reading}), flush=True)
        sample += 1
        time.sleep(1)
    copier.join(timeout=2)
    return child.returncode


if __name__ == '__main__':
    raise SystemExit(supervise(sys.argv[1:]))
