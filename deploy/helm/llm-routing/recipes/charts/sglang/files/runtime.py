# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Qualify, cache, and supervise one rank of a pinned SGLang recipe."""
import datetime
import http.server
import json
import math
import os
import pathlib
import platform
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.request

GIB = 1024 ** 3


def log(event, **fields):
    print(json.dumps({'time': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'event': event, **fields}), flush=True)


def host_memory():
    for line in pathlib.Path('/proc/meminfo').read_text().splitlines():
        if line.startswith('MemAvailable:'):
            return int(line.split()[1]) * 1024
    raise RuntimeError('Cannot read host MemAvailable')


def command(config, model_path, rank):
    profile = config['profile']
    flags = ['--model-path', str(model_path), '--served-model-name', config['model']['id'],
             '--tp', str(profile['nodes']), '--context-length', str(config['contextLength']),
             '--max-running-requests', str(config['concurrency']), '--max-total-tokens', str(config['contextLength'] * config['concurrency']),
             '--host', '0.0.0.0', '--port', str(config['ports']['http'])] + profile['flags']
    if profile['nodes'] > 1:
        flags += ['--nnodes', str(profile['nodes']), '--node-rank', str(rank),
                  '--dist-init-addr', config['targets'][0]['address'] + ':' + str(config['ports']['rendezvous'])]
    return ['python3', '-m', 'sglang.launch_server'] + flags


def check_hardware(config, cuda):
    hardware = config['profile']['hardware']
    architecture = platform.machine().lower()
    architecture = {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(architecture, architecture)
    if platform.system().lower() != hardware['os'] or architecture != hardware['architecture']:
        raise RuntimeError('Runtime OS or architecture does not match the hardware profile')
    if type(hardware['gpuCount']) is not int or hardware['gpuCount'] != 1 or cuda.device_count() != 1:
        raise RuntimeError('The hardware profile requires one exclusively assigned GPU per rank')
    gpu = cuda.get_device_name(0)
    allowed = hardware['cudaDeviceNames']
    if not isinstance(allowed, list) or gpu not in allowed:
        raise RuntimeError('Assigned CUDA device does not match the hardware profile: ' + gpu)
    total_gib = cuda.get_device_properties(0).total_memory / GIB
    if hardware['memoryMode'] == 'discrete':
        minimum = hardware.get('minDeviceMemoryGiB')
        if type(minimum) not in (int, float) or not math.isfinite(minimum) or minimum <= 0:
            raise RuntimeError('Discrete hardware profiles require a positive minDeviceMemoryGiB')
        if total_gib < minimum:
            raise RuntimeError('Assigned CUDA device has insufficient memory for the hardware profile')
    elif hardware['memoryMode'] != 'unified':
        raise RuntimeError('Hardware memoryMode must be unified or discrete')
    return {'gpu': gpu, 'cudaTotalMemoryGiB': total_gib}


def qualify(config, rank):
    import torch
    import torch.distributed as dist
    hardware = check_hardware(config, torch.cuda)
    torch.cuda.set_device(0)
    world = config['profile']['nodes']
    if world > 1:
        addr = config['targets'][0]['address']
        dist.init_process_group('nccl', init_method=f"tcp://{addr}:{config['ports']['rendezvous']}",
                                rank=rank, world_size=world, timeout=datetime.timedelta(seconds=180))
    try:
        x = torch.ones(1024 * 1024, device='cuda', dtype=torch.float32) * (rank + 1)
        if world > 1:
            dist.all_reduce(x)
        expected = world * (world + 1) // 2
        if not torch.all(x == expected).item():
            raise RuntimeError('GPU collective returned incorrect data')
        torch.cuda.synchronize()
        log('qualification_pass', rank=rank, nodes=world, nccl=torch.cuda.nccl.version(), **hardware)
    finally:
        if world > 1:
            dist.destroy_process_group()


def wait_worker(config):
    url = f"http://{config['targets'][1]['address']}:{config['ports']['bootstrap']}/started"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        try:
            with opener.open(url, timeout=2) as response:
                body = json.load(response)
                if body == {'model': config['model']['id'], 'rank': 1, 'revision': config['model']['revision']}:
                    return
        except (OSError, ValueError):
            pass
        time.sleep(1)
    raise RuntimeError('Worker did not start on the configured fabric within 180 seconds')


def marker_server(config, rank):
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path != '/started':
                self.send_error(404)
                return
            data = json.dumps({'model': config['model']['id'], 'rank': rank, 'revision': config['model']['revision']}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        def log_message(self, *_):
            pass
    server = http.server.ThreadingHTTPServer(('0.0.0.0', config['ports']['bootstrap']), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def stop(child):
    if child.poll() is not None:
        return
    os.killpg(child.pid, signal.SIGTERM)
    try:
        child.wait(timeout=15)
    except subprocess.TimeoutExpired:
        os.killpg(child.pid, signal.SIGKILL)
        child.wait()


def supervise(argv, config, state_dir=pathlib.Path('/tmp'), cgroup=pathlib.Path('/sys/fs/cgroup')):
    latch = state_dir / 'recipe-memory-stop.json'
    if latch.exists():
        raise RuntimeError('Memory guard previously stopped this pod. Inspect the cause before replacing the pod.')
    swap = cgroup / 'memory.swap.current'
    if not swap.exists():
        raise RuntimeError('Runtime memory guard requires cgroup v2')
    child = subprocess.Popen(argv, start_new_session=True)
    def terminate(*_):
        stop(child)
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    try:
        while child.poll() is None:
            available, swapped = host_memory(), int(swap.read_text())
            if available < 4 * GIB or swapped:
                failure = {'model': config['model']['id'], 'availableBytes': available, 'swapBytes': swapped}
                latch.write_text(json.dumps(failure))
                log('memory_guard_stop', **failure)
                stop(child)
                return 78
            time.sleep(1)
        return child.returncode
    finally:
        stop(child)


def main():
    config = json.loads(pathlib.Path('/recipe/config.json').read_text())
    phase, rank = sys.argv[1], int(sys.argv[2])
    profile, model = config['profile'], config['model']
    log('phase_start', phase=phase, rank=rank, model=model['id'], revision=model['revision'])
    if phase == 'download':
        from huggingface_hub import snapshot_download
        complete = pathlib.Path('/cache') / (model['revision'] + '.complete')
        if not complete.exists() and shutil.disk_usage('/cache').free < profile['minFreeDiskGiB'] * GIB:
            raise RuntimeError('Insufficient cache disk space for the model checkpoint')
        snapshot_download(repo_id=model['repository'], revision=model['revision'])
        complete.write_text(model['repository'] + '\n')
        log('download_pass', model=model['id'], revision=model['revision'])
        return 0
    if host_memory() < profile['memoryGiB'] * GIB:
        raise RuntimeError('Insufficient host MemAvailable for this profile; capacity may have changed since planning')
    if phase == 'qualify':
        qualify(config, rank)
        return 0
    if phase != 'serve':
        raise RuntimeError('Unknown phase')
    import torch
    check_hardware(config, torch.cuda)
    from huggingface_hub import snapshot_download
    complete = pathlib.Path('/cache') / (model['revision'] + '.complete')
    if not complete.exists() or complete.read_text().strip() != model['repository']:
        raise RuntimeError('The download phase has not completed for this pinned checkpoint')
    model_path = snapshot_download(repo_id=model['repository'], revision=model['revision'], local_files_only=True)
    if profile['offload']:
        # This mount is this pod's disposable emptyDir, never the retained model cache.
        for path in pathlib.Path('/ple').iterdir():
            if path.is_dir():
                shutil.rmtree(path)
            else:
                path.unlink()
        if shutil.disk_usage('/ple').free < 50 * GIB:
            raise RuntimeError('NVMe offload requires at least 50 GiB free on the pod ephemeral disk')
    server = None
    if profile['nodes'] > 1:
        if rank == 0:
            wait_worker(config)
        else:
            server = marker_server(config, rank)
    try:
        return supervise(command(config, model_path, rank), config)
    finally:
        if server:
            server.shutdown()
            server.server_close()


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception as e:
        log('phase_failed', reason=str(e))
        sys.exit(1)
