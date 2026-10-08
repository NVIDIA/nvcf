# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Qualify, cache, and supervise one rank of a pinned SGLang recipe."""
import datetime
import ctypes
import fcntl
import hashlib
import http.client
import http.server
import ipaddress
import json
import math
import os
import pathlib
import platform
import re
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.request
import uuid

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
    host = '0.0.0.0'
    if profile['nodes'] > 1:
        host = os.environ.get('POD_IP', '')
        try:
            address = ipaddress.ip_address(host)
        except ValueError:
            raise RuntimeError('TP2 requires POD_IP from the Kubernetes downward API') from None
        if address.is_unspecified or address.is_loopback or address.is_multicast:
            raise RuntimeError('TP2 requires a usable pod IP for the backend Service')
    flags = ['--model-path', str(model_path), '--served-model-name', config['model']['id'],
             '--tp', str(profile['nodes']), '--context-length', str(config['contextLength']),
             '--max-running-requests', str(config['concurrency']), '--max-total-tokens', str(config['contextLength'] * config['concurrency']),
             '--host', host, '--port', str(config['ports']['http'])] + profile['flags']
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


def nvme_device(device, sys_block=pathlib.Path('/sys/dev/block'), seen=None):
    """Resolve partitions/device-mapper parents and require local PCI NVMe controllers."""
    seen = set() if seen is None else seen
    if device in seen:
        return False
    seen.add(device)
    path = sys_block / device
    if not path.exists():
        return False
    resolved = path.resolve()
    for parent in (resolved, *resolved.parents):
        if parent.name.startswith('nvme') and parent.name[4:].isdigit():
            try:
                return (parent / 'transport').read_text().strip() == 'pcie'
            except OSError:
                return False
    slaves = list((resolved / 'slaves').iterdir()) if (resolved / 'slaves').is_dir() else []
    return bool(slaves) and all(nvme_device((child / 'dev').read_text().strip(), sys_block, seen.copy()) for child in slaves)


def prepare_offload(config, directory=pathlib.Path('/ple')):
    if not config['profile']['offload']:
        return
    device = directory.stat().st_dev
    if not nvme_device(f'{os.major(device)}:{os.minor(device)}'):
        raise RuntimeError('NVMe offload requires /ple to be backed by a verified local NVMe device')
    # This is the chart's disposable emptyDir, never the retained checkpoint cache.
    for path in directory.iterdir():
        if path.is_dir() and not path.is_symlink():
            shutil.rmtree(path)
        else:
            path.unlink()
    if shutil.disk_usage(directory).free < config['profile']['offloadGiB'] * GIB:
        raise RuntimeError('Insufficient free NVMe space for the profile offload reservation')


def interface_addresses(interface):
    """Read all IPv4 addresses assigned to a Linux interface, including secondary addresses."""
    class IfAddrs(ctypes.Structure):
        pass
    IfAddrs._fields_ = [('next', ctypes.POINTER(IfAddrs)), ('name', ctypes.c_char_p),
                       ('flags', ctypes.c_uint), ('address', ctypes.c_void_p)]
    libc = ctypes.CDLL(None, use_errno=True)
    libc.getifaddrs.argtypes = [ctypes.POINTER(ctypes.POINTER(IfAddrs))]
    libc.getifaddrs.restype = ctypes.c_int
    libc.freeifaddrs.argtypes = [ctypes.POINTER(IfAddrs)]
    libc.freeifaddrs.restype = None
    head = ctypes.POINTER(IfAddrs)()
    if libc.getifaddrs(ctypes.byref(head)) != 0:
        raise RuntimeError('Cannot inspect fabric interface addresses: ' + os.strerror(ctypes.get_errno()))
    addresses = set()
    try:
        current = head
        while current:
            entry = current.contents
            if entry.name == interface.encode() and entry.address and ctypes.c_ushort.from_address(entry.address).value == socket.AF_INET:
                addresses.add(socket.inet_ntoa(ctypes.string_at(entry.address + 4, 4)))
            current = entry.next
    finally:
        libc.freeifaddrs(head)
    return addresses


def check_fabric(config, rank, net=pathlib.Path('/sys/class/net')):
    targets, profile = config['targets'], config['profile']
    if len(targets) != 2 or targets[0].get('fabric') != targets[1].get('fabric') or not targets[0].get('fabric'):
        raise RuntimeError('TP2 requires two targets on the same verified fabric')
    try:
        addresses = [ipaddress.ip_address(target['address']) for target in targets]
    except ValueError:
        raise RuntimeError('TP2 requires distinct usable IPv4 fabric addresses') from None
    if len(set(addresses)) != 2 or any(address.version != 4 or address.is_unspecified or address.is_loopback or address.is_multicast or address.is_reserved for address in addresses):
        raise RuntimeError('TP2 requires distinct usable IPv4 fabric addresses')
    target = targets[rank]
    interface = target['interface']
    if not re.fullmatch(r'[a-zA-Z0-9_.-]{1,15}', interface) or interface in ('.', '..'):
        raise RuntimeError('Invalid fabric interface name')
    if target['address'] not in interface_addresses(interface):
        raise RuntimeError('Configured fabric address does not belong to the selected interface')
    if (net / interface / 'operstate').read_text().strip() != 'up':
        raise RuntimeError('The selected fabric interface is not up')
    speed = int((net / interface / 'speed').read_text().strip())
    if speed < profile['minFabricGbps'] * 1000:
        raise RuntimeError('The selected fabric link is below the profile bandwidth minimum')


class StartupBarrier:
    """Pair fresh rank processes before NCCL and serving, without Kubernetes write access."""
    def __init__(self, config, rank):
        self.config, self.rank = config, rank
        identity = {key: config[key] for key in ('release', 'generation', 'model', 'profile', 'targets', 'contextLength', 'concurrency', 'ports')}
        self.state = {'identity': hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest(),
                      'rank': rank, 'attempt': uuid.uuid4().hex, 'peer': None, 'phase': 'preparing'}
        self.peer_attempt = None
        self.last_peer = time.monotonic()
        self.server = None
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def start(self):
        state = self.state
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path not in ('/status', '/started'):
                    self.send_error(404)
                    return
                body = json.dumps(state.copy()).encode()
                self.send_response(200 if self.path == '/status' or state['phase'] == 'serving' else 503)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            def log_message(self, *_):
                pass
        self.server = http.server.ThreadingHTTPServer((self.config['targets'][self.rank]['address'], self.config['ports']['bootstrap']), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.state['phase'] = 'stopped'
        if self.server:
            self.server.shutdown()
            self.server.server_close()

    def peer(self):
        url = f"http://{self.config['targets'][1-self.rank]['address']}:{self.config['ports']['bootstrap']}/status"
        try:
            with self.opener.open(url, timeout=2) as response:
                value = json.load(response)
        except (OSError, ValueError, http.client.IncompleteRead):
            return None
        if not isinstance(value, dict) or value.get('identity') != self.state['identity'] or value.get('rank') != 1-self.rank:
            return None
        attempt = value.get('attempt')
        if not isinstance(attempt, str) or len(attempt) != 32 or any(c not in '0123456789abcdef' for c in attempt):
            return None
        if self.peer_attempt and (attempt != self.peer_attempt or value.get('peer') not in (None, self.state['attempt'])):
            raise RuntimeError('TP2 peer restarted or belongs to a different startup attempt')
        return value

    def wait(self, phases, timeout=600):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            value = self.peer()
            if value and value.get('phase') in phases:
                self.last_peer = time.monotonic()
                return value
            time.sleep(1)
        raise RuntimeError('Timed out waiting for the matching TP2 peer startup phase')

    def pair(self):
        self.state['phase'] = 'ready'
        # Cache preparation may finish hours apart. Do not begin a short NCCL timeout yet.
        value = self.wait(('ready', 'paired'), timeout=14400)
        if value.get('peer') not in (None, self.state['attempt']):
            raise RuntimeError('TP2 peer is already paired with another startup attempt')
        self.peer_attempt = value['attempt']
        self.state.update(peer=self.peer_attempt, phase='paired')
        self.wait(('paired', 'qualified', 'serving'))

    def qualified(self):
        self.state['phase'] = 'qualified'
        self.wait(('qualified', 'serving'))

    def guard(self):
        value = self.peer()
        if value and value.get('phase') in ('qualified', 'serving') and value.get('peer') == self.state['attempt']:
            self.last_peer = time.monotonic()
        elif time.monotonic() - self.last_peer > 30:
            raise RuntimeError('TP2 peer stopped responding; stopping this rank')


def stop(child):
    if child.poll() is not None:
        return
    os.killpg(child.pid, signal.SIGTERM)
    try:
        child.wait(timeout=15)
    except subprocess.TimeoutExpired:
        os.killpg(child.pid, signal.SIGKILL)
        child.wait()


def check_memory_latch(state_dir=pathlib.Path('/tmp')):
    latch = state_dir / 'recipe-memory-stop.json'
    if latch.exists():
        cause = latch.read_text()
        try:
            cause = json.loads(cause)
        except ValueError:
            pass
        log('memory_guard_latched', cause=cause)
        raise RuntimeError('Memory guard previously stopped this pod. Inspect the recorded cause before replacing the pod: ' + str(cause))


def supervise(argv, config, state_dir=pathlib.Path('/tmp'), cgroup=pathlib.Path('/sys/fs/cgroup'), peer_guard=None):
    check_memory_latch(state_dir)
    latch = state_dir / 'recipe-memory-stop.json'
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
            if peer_guard:
                peer_guard()
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


def completed_checkpoint(model, cache):
    complete = cache / (model['revision'] + '.complete')
    if not complete.is_file():
        raise RuntimeError('Cache reuse requires a completed download marker for this pinned checkpoint')
    content = complete.read_text().strip()
    # Earlier releases recorded the repository, with the revision in the filename.
    if content == model['repository']:
        return complete
    try:
        identity = json.loads(content)
    except ValueError:
        identity = None
    expected = {'model': model['id'], 'repository': model['repository'], 'revision': model['revision']}
    if not isinstance(identity, dict) or any(identity.get(key) != value for key, value in expected.items()):
        raise RuntimeError('Cached completion marker does not match the model, repository and revision')
    return complete


def reuse_snapshot(model, cache=pathlib.Path('/cache'), require_complete=True, write_marker=True):
    """Validate retained Hugging Face files directly; never import a download client."""
    complete = completed_checkpoint(model, cache) if require_complete else cache / (model['revision'] + '.complete')
    repository = cache / 'huggingface' / 'hub' / ('models--' + model['repository'].replace('/', '--'))
    snapshot = repository / 'snapshots' / model['revision']
    if not repository.resolve().is_relative_to(cache.resolve()) or not snapshot.is_dir() or not snapshot.resolve().is_relative_to(repository.resolve()):
        raise RuntimeError('Pinned model snapshot is missing from the retained cache')
    checked = set()
    for path in snapshot.rglob('*'):
        if path.is_dir():
            continue
        resolved = path.resolve()
        if not path.is_file() or not resolved.is_relative_to(repository.resolve()):
            raise RuntimeError('Cached snapshot has a missing file or invalid link: ' + path.name)
        # Hub blobs use SHA256 for LFS files and Git blob SHA1 for other files.
        if resolved not in checked and len(resolved.name) in (40, 64) and all(c in '0123456789abcdef' for c in resolved.name):
            digest = hashlib.sha256() if len(resolved.name) == 64 else hashlib.sha1(usedforsecurity=False)
            if len(resolved.name) == 40:
                digest.update(('blob ' + str(path.stat().st_size) + '\0').encode())
            with path.open('rb') as stream:
                for chunk in iter(lambda: stream.read(8 * 1024 * 1024), b''):
                    digest.update(chunk)
            if digest.hexdigest() != resolved.name:
                raise RuntimeError('Cached model blob checksum differs: ' + path.name)
            checked.add(resolved)
    def read_json(name):
        try:
            value = json.loads((snapshot / name).read_text())
        except (OSError, ValueError) as error:
            raise RuntimeError('Missing or invalid cached model file: ' + name) from error
        if not isinstance(value, dict) or not value:
            raise RuntimeError('Empty or invalid cached model file: ' + name)
        return value
    read_json('config.json')
    read_json('tokenizer_config.json')
    if (snapshot / 'tokenizer.json').is_file():
        read_json('tokenizer.json')
    elif not (snapshot / 'tokenizer.model').is_file() or (snapshot / 'tokenizer.model').stat().st_size == 0:
        raise RuntimeError('Cached tokenizer files are missing')
    index = snapshot / 'model.safetensors.index.json'
    weight_map = read_json(index.name).get('weight_map') if index.exists() else None
    if index.exists() and (not isinstance(weight_map, dict) or not weight_map or not all(isinstance(name, str) for name in weight_map.values())):
        raise RuntimeError('Cached weight index is empty or invalid')
    weights = set(weight_map.values()) if weight_map else {path.name for path in snapshot.glob('*.safetensors')}
    if not weight_map and weights != {'model.safetensors'}:
        raise RuntimeError('Cached sharded model requires its complete weight index')
    if not weights or any(not isinstance(name, str) or pathlib.PurePath(name).name != name or not name.endswith('.safetensors') for name in weights):
        raise RuntimeError('Cached model weights are missing or invalid')
    for name in weights:
        path = snapshot / name
        try:
            size = path.stat().st_size
            with path.open('rb') as stream:
                length = int.from_bytes(stream.read(8), 'little')
                if not 0 < length <= min(size - 8, 64 * 1024 * 1024):
                    raise ValueError('Invalid safetensors header length')
                header = json.loads(stream.read(length))
            offsets = [value['data_offsets'] for key, value in header.items() if key != '__metadata__']
            if not offsets or any(not isinstance(pair, list) or len(pair) != 2 or
                    any(type(value) is not int for value in pair) or not 0 <= pair[0] <= pair[1] <= size - 8 - length for pair in offsets):
                raise ValueError('Invalid tensor offsets')
            if max(pair[1] for pair in offsets) != size - 8 - length:
                raise ValueError('Truncated or unexpected tensor data')
            if weight_map and any(tensor not in header for tensor, shard in weight_map.items() if shard == name):
                raise ValueError('Weight index refers to missing tensors')
        except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
            raise RuntimeError('Missing or corrupt cached weight shard: ' + name) from error
    if write_marker:
        temporary = complete.with_suffix('.pending')
        temporary.write_text(json.dumps({'model': model['id'], 'repository': model['repository'], 'revision': model['revision']}) + '\n')
        os.replace(temporary, complete)
    return snapshot


def downloaded_bytes(model, cache):
    """Credit only blobs belonging to the pinned revision, without changing the cache."""
    from huggingface_hub import HfApi
    info = HfApi().model_info(model['repository'], revision=model['revision'], files_metadata=True, timeout=30)
    if info.sha != model['revision'] or not info.siblings:
        raise RuntimeError('Download metadata does not match the pinned model revision')
    blobs = cache / 'huggingface/hub' / ('models--' + model['repository'].replace('/', '--')) / 'blobs'
    if not blobs.resolve().is_relative_to(cache.resolve()):
        raise RuntimeError('Model blob directory escapes the cache')
    total, seen = 0, set()
    for file in info.siblings:
        digest = file.lfs.sha256 if file.lfs else file.blob_id
        size = file.size
        if not isinstance(digest, str) or not re.fullmatch(r'[a-f0-9]{64}|[a-f0-9]{40}', digest) or type(size) is not int or size < 0:
            raise RuntimeError('Pinned download metadata lacks a valid blob identity or size')
        if digest in seen:
            continue
        seen.add(digest)
        path = blobs / digest
        if path.is_file() and not path.is_symlink() and path.stat().st_size == size:
            checksum = hashlib.sha256() if len(digest) == 64 else hashlib.sha1(usedforsecurity=False)
            if len(digest) == 40:
                checksum.update(('blob ' + str(size) + '\0').encode())
            with path.open('rb') as stream:
                for chunk in iter(lambda: stream.read(8 * 1024 * 1024), b''):
                    checksum.update(chunk)
            if checksum.hexdigest() == digest:
                total += size
            continue
        path = blobs / (digest + '.incomplete')
        # hf_transfer restarts incomplete files from the beginning.
        if os.environ.get('HF_HUB_ENABLE_HF_TRANSFER', '').upper() not in ('1', 'ON', 'YES', 'TRUE') and path.is_file() and not path.is_symlink():
            stat = path.stat()
            if stat.st_size <= size:
                total += min(stat.st_size, stat.st_blocks * 512)
    return total


def prepare_checkpoint(config, cache):
    model = config['model']
    if config.get('reuseCaches') or (cache / (model['revision'] + '.complete')).exists():
        snapshot = reuse_snapshot(model, cache, write_marker=False)
        log('download_pass', model=model['id'], revision=model['revision'], reused=True)
        return snapshot
    # External downloads can contain a complete, locally verifiable snapshot.
    # Validate locally before importing the download client or requiring free space.
    try:
        snapshot = reuse_snapshot(model, cache, require_complete=False)
    except RuntimeError:
        pass
    else:
        log('download_pass', model=model['id'], revision=model['revision'], reused=True)
        return snapshot
    required = config['profile']['minFreeDiskGiB'] * GIB
    free = shutil.disk_usage(cache).free
    if free < required:
        cached = downloaded_bytes(model, cache)
        if free < max(0, required - cached):
            raise RuntimeError(f'Insufficient cache disk space for the model checkpoint: {free} free bytes, {max(0, required - cached)} still required')
    from huggingface_hub import snapshot_download
    snapshot_download(repo_id=model['repository'], revision=model['revision'], cache_dir=str(cache / 'huggingface/hub'))
    snapshot = reuse_snapshot(model, cache, require_complete=False)
    log('download_pass', model=model['id'], revision=model['revision'], reused=False)
    return snapshot


def automatic(config, rank, cache=pathlib.Path('/cache'), state_dir=pathlib.Path('/tmp')):
    check_memory_latch(state_dir)
    world = config['profile']['nodes']
    if world not in (1, 2) or rank not in range(world):
        raise RuntimeError('Automatic startup requires one or two valid profile ranks')
    with (cache / '.recipe.lock').open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError('Another model process is using this cache claim') from None
        if host_memory() < config['profile']['memoryGiB'] * GIB:
            raise RuntimeError('Insufficient host MemAvailable for the pinned profile')
        barrier = None
        try:
            if world == 2:
                check_fabric(config, rank)
                import torch
                check_hardware(config, torch.cuda)
                barrier = StartupBarrier(config, rank)
                barrier.start()
                model_path = prepare_checkpoint(config, cache)
                barrier.pair()
            qualify(config, rank)
            import torch
            torch.cuda.empty_cache()
            if barrier:
                barrier.qualified()
            else:
                model_path = prepare_checkpoint(config, cache)
            prepare_offload(config)
            if host_memory() < config['profile']['memoryGiB'] * GIB:
                raise RuntimeError('Insufficient host MemAvailable after checkpoint preparation')
            os.environ['HF_HUB_OFFLINE'] = '1'
            os.environ['TRANSFORMERS_OFFLINE'] = '1'
            log('serve_start', model=config['model']['id'], revision=config['model']['revision'], rank=rank)
            if barrier:
                barrier.state['phase'] = 'serving'
                return supervise(command(config, model_path, rank), config, state_dir=state_dir, peer_guard=barrier.guard)
            return supervise(command(config, model_path, rank), config, state_dir=state_dir)
        finally:
            if barrier:
                barrier.close()


def main():
    config = json.loads(pathlib.Path('/recipe/config.json').read_text())
    rank = int(sys.argv[1])
    if type(config.get('reuseCaches', False)) is not bool:
        raise RuntimeError('reuseCaches must be a boolean')
    log('phase_start', phase='automatic', rank=rank, model=config['model']['id'], revision=config['model']['revision'])
    return automatic(config, rank)


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception as e:
        log('phase_failed', reason=str(e))
        sys.exit(1)
