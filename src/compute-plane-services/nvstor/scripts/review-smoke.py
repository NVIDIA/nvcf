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

"""Run local release checks with AWS CLI and independent native/NBD clients."""
import importlib.util
import json
import os
from pathlib import Path
import signal
import socket
import struct
import subprocess
import tempfile
import time
import urllib.request
import uuid

repo = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('volume', repo/'scripts/volume.py')
volume = importlib.util.module_from_spec(spec)
spec.loader.exec_module(volume)
root = Path(tempfile.mkdtemp(prefix='nvstor-release-', dir='/tmp'))
print(f'Artifacts: {root}', flush=True)
env = dict(os.environ, AWS_ACCESS_KEY_ID='review-access', AWS_SECRET_ACCESS_KEY='review-secret-only-local', AWS_DEFAULT_REGION='us-east-1', AWS_EC2_METADATA_DISABLED='true', AWS_PAGER='')
ports = []
for _ in range(7):
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0)); ports.append(sock.getsockname()[1])
wire, health, raft, s3, nbd, nfs, fast_tcp = ports
subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(root/'key.pem'), '-out', str(root/'cert.pem'), '-days', '1', '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1,DNS:localhost'], check=True, capture_output=True)
config = root/'config.toml'
config.write_text(f'''[node]
id = 1
name = "review-local"
[cluster]
bootstrap = false
[storage]
data_path = "{root}/data"
direct_io = false
gc_enabled = false
[network]
grpc_port = {wire}
http_port = {health}
raft_port = {raft}
s3_port = {s3}
nbd_port = {nbd}
nfs_port = {nfs}
s3_tcp_port = 0
[tls]
cert_path = "{root}/cert.pem"
key_path = "{root}/key.pem"
[auth]
access_key = "review-access"
secret_key = "review-secret-only-local"
[kms]
root_key_passphrase = "review-only-root-not-a-production-secret"
''')
process = None
log = None
def start():
    global process, log
    log = (root/'server.log').open('a')
    process = subprocess.Popen([str(repo/'target/release/nvstor-server'), '--config', str(config)], stdout=log, stderr=subprocess.STDOUT)
    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f'server exited {process.returncode}; see {root}/server.log')
        try:
            urllib.request.urlopen(f'http://127.0.0.1:{health}/healthz', timeout=1).read()
            time.sleep(1)
            return
        except Exception:
            time.sleep(.1)
    raise TimeoutError('server readiness')
def stop():
    global process, log
    if process and process.poll() is None:
        process.send_signal(signal.SIGTERM)
        process.wait(timeout=25)
        if process.returncode != 0:
            raise RuntimeError(f'unclean SIGTERM exit {process.returncode}')
    if log: log.close()
def aws(*args):
    command = ['aws', '--endpoint-url', f'https://127.0.0.1:{s3}', '--ca-bundle', str(root/'cert.pem'), 's3api', *args]
    result = subprocess.run(command, env=env, text=True, capture_output=True, timeout=30)
    if result.returncode:
        raise RuntimeError(f'AWS {args[0]}: {result.stderr}')
    return json.loads(result.stdout) if result.stdout.strip() else {}
def nbd_connect(volume_id):
    sock = socket.create_connection(('127.0.0.1', nbd), timeout=5)
    header = volume.receive(sock, 18)
    assert header[:8] == b'NBDMAGIC'
    sock.sendall(struct.pack('>I', 3))
    name = str(volume_id).encode()
    sock.sendall(struct.pack('>QII', 0x49484156454f5054, 1, len(name))+name)
    result = volume.receive(sock, 10)
    assert struct.unpack_from('>Q', result)[0] == 8192
    return sock
def nbd_io(sock, operation, offset, data_or_len):
    count = len(data_or_len) if isinstance(data_or_len, bytes) else data_or_len
    request = struct.pack('>IHHQQI', 0x25609513, 1 if operation == 1 else 0, operation, 77, offset, count)
    sock.sendall(request + (data_or_len if operation == 1 else b''))
    magic, error, handle = struct.unpack('>IIQ', volume.receive(sock, 16))
    assert magic == 0x67446698 and error == 0 and handle == 77
    return volume.receive(sock, count) if operation == 0 else None
try:
    start()
    aws('create-bucket', '--bucket', 'review-bucket')
    body = root/'body'; body.write_bytes(b'inline original')
    aws('put-object', '--bucket', 'review-bucket', '--key', 'overwrite', '--body', str(body))
    body.write_bytes(b'chunked generation\n' * 10000)
    aws('put-object', '--bucket', 'review-bucket', '--key', 'overwrite', '--body', str(body))
    aws('get-object', '--bucket', 'review-bucket', '--key', 'overwrite', str(root/'download'))
    assert (root/'download').read_bytes() == body.read_bytes()
    original = body.read_bytes()
    body.write_bytes(b'invalid checksum must preserve the existing object')
    try:
        aws('put-object', '--bucket', 'review-bucket', '--key', 'overwrite', '--body', str(body), '--checksum-crc32', 'AAAAAA==')
        raise AssertionError('invalid checksum was accepted')
    except RuntimeError as error:
        assert 'BadDigest' in str(error), str(error)
    aws('get-object', '--bucket', 'review-bucket', '--key', 'overwrite', str(root/'preserved'))
    assert (root/'preserved').read_bytes() == original
    print('PASS TLS + AWS overwrite + checksum failure preserves prior contents', flush=True)
    secret = b'encrypted data survives restart'
    body.write_bytes(secret)
    aws('put-object', '--bucket', 'review-bucket', '--key', 'encrypted', '--body', str(body), '--server-side-encryption', 'AES256')
    aws('get-object', '--bucket', 'review-bucket', '--key', 'encrypted', str(root/'secret'))
    assert (root/'secret').read_bytes() == secret
    upload = aws('create-multipart-upload', '--bucket', 'review-bucket', '--key', 'selected')['UploadId']
    parts = []
    for number, data in [(1, b'ignored'), (2, b'selected')]:
        body.write_bytes(data)
        result = aws('upload-part', '--bucket', 'review-bucket', '--key', 'selected', '--upload-id', upload, '--part-number', str(number), '--body', str(body))
        parts.append({'PartNumber': number, 'ETag': result['ETag']})
    aws('complete-multipart-upload', '--bucket', 'review-bucket', '--key', 'selected', '--upload-id', upload, '--multipart-upload', json.dumps({'Parts': [parts[1]]}))
    aws('get-object', '--bucket', 'review-bucket', '--key', 'selected', str(root/'selected'))
    assert (root/'selected').read_bytes() == b'selected'
    print('PASS AWS multipart completion selects only requested parts', flush=True)
    endpoint = f'127.0.0.1:{wire}'
    identity = uuid.uuid4()
    volume.create(endpoint, identity, 8192)
    volume.write(endpoint, identity, 0, b'native data')
    sock = nbd_connect(identity)
    assert nbd_io(sock, 0, 0, 11) == b'native data'
    nbd_io(sock, 1, 4096, b'nbd data')
    assert volume.read(endpoint, identity, 4096, 8) == b'nbd data'
    # Keep idle native, NFS and NBD clients open while requesting shutdown.
    idle_wire = socket.create_connection(('127.0.0.1', wire))
    idle_nfs = socket.create_connection(('127.0.0.1', nfs))
    begin = time.monotonic(); stop(); elapsed = time.monotonic()-begin
    sock.close(); idle_wire.close(); idle_nfs.close()
    assert elapsed < 20, elapsed
    print(f'PASS native/NBD interoperability and SIGTERM drain ({elapsed:.2f}s)', flush=True)
    start()
    assert volume.read(endpoint, identity, 0, 11) == b'native data'
    assert volume.read(endpoint, identity, 4096, 8) == b'nbd data'
    aws('get-object', '--bucket', 'review-bucket', '--key', 'encrypted', str(root/'secret-restarted'))
    assert (root/'secret-restarted').read_bytes() == secret
    aws('get-object', '--bucket', 'review-bucket', '--key', 'selected', str(root/'selected-restarted'))
    assert (root/'selected-restarted').read_bytes() == b'selected'
    print('PASS disk restart preserves encrypted objects, selected parts, and shared volumes', flush=True)
    stop()
    # Fast object transports are currently started in clustered mode. Bootstrap
    # a disposable single-node Raft cluster for local PM transport validation.
    config.write_text(config.read_text().replace('bootstrap = false', 'bootstrap = true').replace(f'{root}/data', f'{root}/fast-data').replace('s3_tcp_port = 0', f's3_tcp_port = {fast_tcp}'))
    start()
    stress_env = dict(env, NVSTOR_ACCESS_KEY='review-access', NVSTOR_SECRET_KEY='review-secret-only-local')
    stress = [str(repo/'target/release/examples/s3_stress'), '--transport', 'tcp', '--addr', f'127.0.0.1:{fast_tcp}', '--workers', '4', '--cycles', '24', '--max-bytes', '1048576', '--prefix', 'release-stress']
    for phase in ['lifecycle', 'write']:
        result = subprocess.run(stress + ['--phase', phase], env=stress_env, check=True, text=True, timeout=60)
    stop(); start()
    result = subprocess.run(stress + ['--phase', 'verify'], env=stress_env, check=True, text=True, timeout=60)
    print('PASS fast TCP object stress: byte verification, overwrite, deletion, and restart', flush=True)
finally:
    stop()
