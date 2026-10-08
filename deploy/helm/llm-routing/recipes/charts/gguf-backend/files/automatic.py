# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Prepare and verify a pinned distributed GGUF runtime within its owning pods."""
import concurrent.futures
import hashlib
import importlib.util
import json
import os
import pathlib
import platform
import re
import shutil
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request

ROOT = pathlib.Path('/artifacts')
CHECKS = pathlib.Path('/checks')
BINARIES = ('llama-server', 'llama-cli', 'ggml-rpc-server', 'test-rpc-multi-server', 'rpc-gpu-check')


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def emit(event, **fields):
    print(json.dumps({'event': event, **fields}), flush=True)


def digest(path):
    with path.open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def verify_runtime(config, runtime, archive=None):
    manifest = json.loads((runtime / 'build-manifest.json').read_text())
    require(manifest.get('revision') == config['recipe']['llamaCppRevision'], 'Cached runtime revision differs from recipe')
    require(manifest.get('image') == config['image'], 'Cached runtime build image differs from recipe')
    option = '-DCMAKE_CUDA_ARCHITECTURES=' + config['profile']['cudaArchitectures']
    require(option in manifest.get('options', []), 'Cached runtime CUDA architecture differs from profile')
    require(re.fullmatch('[a-f0-9]{64}', manifest.get('sourceArchiveSHA256', '')) is not None, 'Runtime source digest is missing')
    for name in BINARIES:
        path = runtime / name
        require(path.is_file() and not path.is_symlink(), 'Runtime binary missing: ' + name)
        require(digest(path) == manifest.get('binaries', {}).get(name), 'Runtime binary checksum mismatch: ' + name)
    if archive is not None:
        checksum = pathlib.Path(str(archive) + '.sha256').read_text().strip()
        require(re.fullmatch('[a-f0-9]{64}', checksum) is not None and digest(archive) == checksum, 'Runtime bundle checksum mismatch')
    return manifest


def hardware(config, cuda=None):
    profile = config['profile']
    hardware_profile = profile['hardware']
    architecture = platform.machine().lower()
    architecture = {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(architecture, architecture)
    require(platform.system().lower() == hardware_profile['os'] and architecture == hardware_profile['architecture'],
            'Runtime OS or architecture differs from the hardware profile')
    memory = {}
    for line in pathlib.Path('/proc/meminfo').read_text().splitlines():
        name, *fields = line.split()
        if name in ('MemTotal:', 'MemAvailable:'):
            memory[name[:-1]] = int(fields[0]) * 1024
    require(memory['MemTotal'] >= profile['hardware']['minHostMemoryGiB'] * 1024**3, 'Insufficient total host memory for profile')
    require(memory['MemAvailable'] >= profile['minAvailableMemoryGiB'] * 1024**3, 'Insufficient available host memory for model placement')
    # The bounded CUDA computation also proves the runtime can use the assigned GPU.
    expected_arch = {'arm64': 'aarch64', 'amd64': 'x86_64'}.get(hardware_profile['architecture'], hardware_profile['architecture'])
    subprocess.run([sys.executable, '-u', str(CHECKS / 'preflight.py')],
                   env=dict(os.environ, EXPECTED_ARCHITECTURE=expected_arch), check=True, timeout=120)
    if cuda is None:
        import torch
        cuda = torch.cuda
    require(cuda.is_available() and cuda.device_count() == 1, 'Profile requires one exclusively assigned CUDA GPU')
    require(cuda.get_device_name() in profile['hardware']['cudaDeviceNames'], 'CUDA device differs from profile')
    capability = '.'.join(map(str, cuda.get_device_capability()))
    require(capability == profile['hardware']['computeCapability'], 'CUDA compute capability differs from profile')
    emit('hardware_pass', gpu=cuda.get_device_name(), capability=capability)


def prepare_runtime(config, root=ROOT):
    runtime, archive = root / 'runtime', root / 'runtime.tar.gz'
    complete = root / 'runtime.tar.gz.sha256'
    if complete.exists() or config['reuseCaches']:
        verify_runtime(config, runtime, archive)
        emit('runtime_pass', reused=True)
        return
    identity = {'revision': config['recipe']['llamaCppRevision'], 'image': config['image'],
                'cudaArchitectures': config['profile']['cudaArchitectures']}
    journal = root / 'automatic-build.json'
    if journal.exists():
        require(json.loads(journal.read_text()) == identity, 'Incomplete runtime build belongs to different recipe pins')
    else:
        require(not runtime.exists() and not archive.exists(), 'Incomplete cached runtime requires its matching automatic build journal')
        temporary = root / 'automatic-build.pending'
        temporary.write_text(json.dumps(identity) + '\n')
        temporary.replace(journal)
    env = dict(os.environ, LLAMA_REVISION=identity['revision'], CUDA_ARCHITECTURES=identity['cudaArchitectures'],
               BUILD_PARALLEL='8', BUILD_IMAGE=identity['image'])
    subprocess.run([sys.executable, '-u', str(CHECKS / 'build.py')], env=env, check=True, timeout=7200)
    verify_runtime(config, runtime, archive)
    emit('runtime_pass', reused=False)


def fetch_runtime(config, url, work=pathlib.Path('/work')):
    archive = work / 'runtime.tar.gz'
    partial = work / 'runtime.tar.gz.partial'
    deadline = time.monotonic() + 10800
    while True:
        try:
            with urllib.request.urlopen(url + '.sha256', timeout=20) as response:
                checksum = response.read(256).decode().strip()
            if re.fullmatch('[a-f0-9]{64}', checksum) is None:
                raise ValueError('Runtime bundle checksum is invalid')
            with urllib.request.urlopen(url, timeout=120) as response, partial.open('wb') as target:
                shutil.copyfileobj(response, target)
            # The producer can replace the bundle between these two requests.
            if digest(partial) != checksum:
                raise ValueError('Runtime bundle checksum mismatch')
            partial.replace(archive)
            break
        except (OSError, ValueError):
            partial.unlink(missing_ok=True)
            if time.monotonic() >= deadline:
                raise RuntimeError('Timed out waiting for the prepared runtime bundle') from None
            time.sleep(5)
    # Verify extracted files in a temporary directory before publishing the executable tree.
    with tempfile.TemporaryDirectory(dir=work) as staging:
        with tarfile.open(archive) as tar:
            members = tar.getmembers()
            require(all(not member.issym() and not member.islnk() and
                        (member.isdir() or member.isfile()) and pathlib.PurePosixPath(member.name).parts[0] == 'runtime' for member in members) and sum(member.size for member in members) <= 2 * 1024**3,
                    'Unexpected runtime archive member')
            tar.extractall(staging, filter='data')
        runtime = pathlib.Path(staging) / 'runtime'
        verify_runtime(config, runtime)
        shutil.rmtree(work / 'runtime', ignore_errors=True)
        runtime.rename(work / 'runtime')
    emit('worker_runtime_pass', sha256=checksum)


def wait_endpoints(endpoints, timeout=600):
    for endpoint in endpoints:
        host, port = endpoint.rsplit(':', 1)
        deadline = time.monotonic() + timeout
        while True:
            try:
                with socket.create_connection((host, int(port)), timeout=5):
                    break
            except OSError:
                if time.monotonic() >= deadline:
                    raise RuntimeError('Timed out waiting for RPC endpoint: ' + endpoint) from None
                time.sleep(2)


def qualify(config, root=ROOT):
    verify_runtime(config, root / 'runtime', root / 'runtime.tar.gz')
    endpoint = '127.0.0.1:50052'
    endpoints = [endpoint, *config['rpcEndpoints']]
    child = subprocess.Popen([str(root / 'runtime/ggml-rpc-server'), '--host', '127.0.0.1',
                              '--port', '50052', '--device', 'CUDA0'])
    try:
        wait_endpoints(endpoints)
        subprocess.run([str(root / 'runtime/test-rpc-multi-server'), *endpoints], check=True, timeout=120)
        subprocess.run([str(root / 'runtime/rpc-gpu-check'), *endpoints], check=True, timeout=180)
        # Existing dependent-graph check uses the pinned build objects and stops on remote fallback.
        if (root / 'build').is_dir():
            env = dict(os.environ, RPC_ENDPOINTS=','.join(endpoints), LLAMA_REVISION=config['recipe']['llamaCppRevision'])
            subprocess.run([sys.executable, '-u', str(CHECKS / 'chain-check.py')], env=env, check=True, timeout=240)
        emit('distributed_qualification_pass', gpus=len(endpoints))
    finally:
        child.terminate()
        try:
            child.wait(timeout=15)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait(timeout=15)


def model_download(config, root=ROOT):
    lock, destination = config['lock'], root / 'model'
    destination.mkdir(exist_ok=True)
    marker = destination / 'download-complete.json'
    if config['reuseCaches']:
        result = json.loads(marker.read_text())
        require(result.get('result') == 'PASS' and result.get('model') == lock['model']
                and result.get('revision') == lock['revision'] and result.get('verifiedBytes') == lock['weightFileBytes'],
                'Cached model completion marker differs from the pinned model')
    spec = importlib.util.spec_from_file_location('gguf_download', CHECKS / 'download.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.root, module.lock = destination, lock
    for record in lock['files']:
        relative = pathlib.PurePosixPath(record['rfilename'])
        require(not relative.is_absolute() and '..' not in relative.parts, 'Invalid checkpoint path')
        path = destination / relative
        require(path.resolve().is_relative_to(destination.resolve()), 'Checkpoint leaves the model cache')
        if config['reuseCaches']:
            module.verify(path, record)
    if not config['reuseCaches']:
        remaining = sum(max(0, record['size'] - (destination / record['rfilename']).with_suffix('.gguf.partial').stat().st_size)
                        if (destination / record['rfilename']).with_suffix('.gguf.partial').exists() else record['size']
                        for record in lock['files'] if not (destination / record['rfilename']).exists())
        require(shutil.disk_usage(destination).free > remaining + 20_000_000_000, 'Insufficient free disk for the pinned checkpoint')
        with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
            list(pool.map(module.download, lock['files']))
    result = {'result': 'PASS', 'model': lock['model'], 'revision': lock['revision'],
              'quantization': lock['quantization'], 'verifiedFiles': len(lock['files']), 'verifiedBytes': lock['weightFileBytes']}
    partial = destination / 'download-complete.json.partial'
    partial.write_text(json.dumps(result) + '\n')
    partial.replace(marker)
    emit('download_pass', model=lock['model'], revision=lock['revision'], reused=config['reuseCaches'])


def main():
    config = json.loads((CHECKS / 'automatic.json').read_text())
    action = sys.argv[1]
    if action == 'hardware':
        hardware(config)
    elif action == 'prepare-runtime':
        prepare_runtime(config)
    elif action == 'fetch-runtime':
        fetch_runtime(config, config['runtimeURL'])
    elif action == 'qualify':
        qualify(config)
    elif action == 'download':
        model_download(config)
    elif action == 'serve':
        verify_runtime(config, ROOT / 'runtime', ROOT / 'runtime.tar.gz')
        os.execv(sys.executable, [sys.executable, '-u', str(CHECKS / 'serve.py')])
    else:
        raise RuntimeError('Unknown automatic lifecycle stage: ' + action)


if __name__ == '__main__':
    main()
