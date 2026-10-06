# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Build an immutable upstream CUDA/RPC runtime and a bounded GPU check."""
import datetime
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import tarfile
import urllib.request

root = pathlib.Path('/artifacts')
revision = os.environ['LLAMA_REVISION']
assert re.fullmatch(r'[0-9a-f]{40}', revision), 'Use a full immutable commit'
source_url = 'https://codeload.github.com/ggml-org/llama.cpp/tar.gz/' + revision
archive = root / (revision + '.tar.gz')
if not archive.exists():
    partial = archive.with_suffix('.partial')
    with urllib.request.urlopen(source_url, timeout=120) as response, partial.open('wb') as out:
        shutil.copyfileobj(response, out)
    partial.rename(archive)
source = root / ('llama.cpp-' + revision)
if not source.exists():
    with tarfile.open(archive) as tar:
        tar.extractall(root, filter='data')
assert (source / 'src/models/glm-dsa.cpp').is_file()
wrapper = root / 'qualification'
wrapper.mkdir(exist_ok=True)
shutil.copy2('/checks/rpc-gpu-check.cpp', wrapper)
(wrapper / 'CMakeLists.txt').write_text('''cmake_minimum_required(VERSION 3.18)
project(rpc_qualification LANGUAGES C CXX)
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/bin)
add_subdirectory("''' + str(source) + '''" llama)
add_executable(rpc-gpu-check rpc-gpu-check.cpp)
target_link_libraries(rpc-gpu-check PRIVATE ggml ggml-rpc)
target_compile_features(rpc-gpu-check PRIVATE cxx_std_17)
''')
options = [
    '-DCMAKE_BUILD_TYPE=Release', '-DBUILD_SHARED_LIBS=OFF',
    '-DGGML_CUDA=ON', '-DGGML_RPC=ON', '-DGGML_NATIVE=OFF',
    '-DCMAKE_CUDA_ARCHITECTURES=' + os.environ['CUDA_ARCHITECTURES'],
    '-DLLAMA_BUILD_COMMON=ON', '-DLLAMA_BUILD_TESTS=ON', '-DLLAMA_BUILD_TOOLS=ON',
    '-DLLAMA_BUILD_SERVER=ON', '-DLLAMA_BUILD_APP=OFF',
    '-DLLAMA_BUILD_EXAMPLES=OFF', '-DLLAMA_BUILD_UI=OFF',
    '-DLLAMA_USE_PREBUILT_UI=OFF', '-DLLAMA_OPENSSL=OFF',
]
build = root / 'build'
subprocess.run(['cmake', '-S', str(wrapper), '-B', str(build), '-G', 'Ninja', *options], check=True)
targets = ['llama-server', 'llama-cli', 'ggml-rpc-server', 'test-rpc-multi-server', 'rpc-gpu-check']
subprocess.run(['cmake', '--build', str(build), '--parallel', os.environ['BUILD_PARALLEL'], '--target', *targets], check=True)
runtime = root / 'runtime'
runtime.mkdir(exist_ok=True)
hashes = {}
for name in targets:
    binary = build / 'bin' / name
    shutil.copy2(binary, runtime / name)
    hashes[name] = hashlib.file_digest(binary.open('rb'), 'sha256').hexdigest()
    subprocess.run(['ldd', str(binary)], check=True)
shutil.copy2(source / 'LICENSE', runtime / 'LICENSE.llama.cpp')
manifest = {
    'time': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'revision': revision, 'sourceURL': source_url,
    'sourceArchiveSHA256': hashlib.file_digest(archive.open('rb'), 'sha256').hexdigest(),
    'image': os.environ['BUILD_IMAGE'], 'options': options, 'binaries': hashes,
}
(runtime / 'build-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
with tarfile.open(root / 'runtime.tar.gz', 'w:gz') as tar:
    tar.add(runtime, arcname='runtime')
bundle_hash = hashlib.file_digest((root / 'runtime.tar.gz').open('rb'), 'sha256').hexdigest()
(root / 'runtime.tar.gz.sha256').write_text(bundle_hash + '\n')
print(json.dumps({'result': 'PASS', 'runtimeSHA256': bundle_hash, **manifest}), flush=True)
