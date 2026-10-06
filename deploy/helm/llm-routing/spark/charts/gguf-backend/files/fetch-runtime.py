# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import hashlib
import os
import pathlib
import shutil
import tarfile
import time
import urllib.request

archive = pathlib.Path('/work/runtime.tar.gz')
for attempt in range(30):
    try:
        with urllib.request.urlopen(os.environ['RUNTIME_URL'], timeout=60) as source, archive.open('wb') as target:
            shutil.copyfileobj(source, target)
        break
    except OSError:
        if attempt == 29:
            raise
        time.sleep(2)
actual = hashlib.file_digest(archive.open('rb'), 'sha256').hexdigest()
assert actual == os.environ['RUNTIME_SHA256'], 'Runtime checksum mismatch'
with tarfile.open(archive) as tar:
    tar.extractall('/work', filter='data')
print('Runtime bundle verified: ' + actual, flush=True)
