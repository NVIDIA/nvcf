# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Resume pinned GGUF downloads and verify every expected byte and digest."""
import concurrent.futures
import datetime
import hashlib
import json
import pathlib
import re
import shutil
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

lock = {}
root = pathlib.Path('/artifacts/model')
progress = {}
mutex = threading.Lock()


class RestartDownload(Exception):
    """The retained prefix or response cannot be used for a pinned download."""


def emit(record):
    print(json.dumps({'time': datetime.datetime.now(datetime.timezone.utc).isoformat(), **record}), flush=True)


def verify(path, expected):
    assert path.stat().st_size == expected['size'], 'Incorrect file size'
    with path.open('rb') as stream:
        digest = hashlib.file_digest(stream, 'sha256').hexdigest()
        if hasattr(__import__('os'), 'posix_fadvise'):
            import os
            os.posix_fadvise(stream.fileno(), 0, 0, os.POSIX_FADV_DONTNEED)
    assert digest == expected['lfs']['sha256'], 'Checkpoint checksum mismatch: ' + path.name
    return digest


def download(expected):
    relative = expected['rfilename']
    assert '..' not in pathlib.PurePosixPath(relative).parts
    destination = root / relative
    destination.parent.mkdir(parents=True, exist_ok=True)
    partial = destination.with_suffix('.gguf.partial')
    if destination.exists():
        digest = verify(destination, expected)
        with mutex:
            progress[relative] = expected['size']
        emit({'verified': relative, 'bytes': expected['size'], 'sha256': digest, 'cached': True})
        return
    for attempt in range(12):
        offset = partial.stat().st_size if partial.exists() else 0
        if offset > expected['size']:
            partial.unlink()
            emit({'restart': relative, 'reason': 'oversized_partial', 'bytes': offset})
            offset = 0
        with mutex:
            progress[relative] = offset
        if offset == expected['size']:
            break
        url = 'https://huggingface.co/' + lock['model'] + '/resolve/' + lock['revision'] + '/' + urllib.parse.quote(relative)
        url += '?download=true&attempt=' + str(time.time_ns())
        request = urllib.request.Request(url, headers={'Range': 'bytes=' + str(offset) + '-', 'User-Agent': 'pylon-gguf-validation'})
        try:
            with urllib.request.urlopen(request, timeout=120) as response, partial.open('ab') as output:
                response_limit = expected['size']
                if offset:
                    if response.status != 206:
                        raise RestartDownload('Server refused resume')
                    content_range = re.fullmatch(r'bytes ([0-9]+)-([0-9]+)/([0-9]+)', response.headers.get('Content-Range') or '')
                    if content_range is None:
                        raise RestartDownload('Missing or malformed resume range')
                    try:
                        start, end, total = map(int, content_range.groups())
                    except ValueError:
                        raise RestartDownload('Invalid resume range numbers') from None
                    if start != offset or not start <= end < expected['size'] or total != expected['size']:
                        raise RestartDownload('Incorrect resume range')
                    response_limit = end + 1
                while chunk := response.read(8 * 1024 * 1024):
                    if offset + len(chunk) > response_limit:
                        raise RestartDownload('Download exceeded pinned range or size')
                    output.write(chunk)
                    offset += len(chunk)
                    with mutex:
                        progress[relative] = offset
            if offset != expected['size']:
                raise OSError('Incomplete response')
            break
        except (OSError, RestartDownload) as error:
            if isinstance(error, RestartDownload) or (offset and isinstance(error, urllib.error.HTTPError) and error.code == 416):
                partial.unlink(missing_ok=True)
                offset = 0
                with mutex:
                    progress[relative] = 0
            emit({'retry': relative, 'attempt': attempt + 1, 'bytes': offset, 'errorType': type(error).__name__})
            if attempt == 11:
                raise RuntimeError('Download retries exhausted for ' + relative) from None
            time.sleep(min(30, 2 ** attempt))
    try:
        digest = verify(partial, expected)
    except AssertionError:
        partial.unlink(missing_ok=True)
        raise
    partial.rename(destination)
    emit({'verified': relative, 'bytes': expected['size'], 'sha256': digest, 'cached': False})


def main():
    global lock
    lock = json.loads(pathlib.Path('/checks/model-lock.json').read_text())
    root.mkdir(exist_ok=True)
    assert shutil.disk_usage(root).free > lock['weightFileBytes'] + 20_000_000_000
    started = time.monotonic()
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
        futures = [executor.submit(download, f) for f in lock['files']]
        while not all(f.done() for f in futures):
            with mutex:
                transferred = sum(progress.values())
            emit({'downloadedBytes': transferred, 'totalBytes': lock['weightFileBytes'], 'elapsedSeconds': round(time.monotonic() - started, 1)})
            concurrent.futures.wait(futures, timeout=30, return_when=concurrent.futures.ALL_COMPLETED)
        for future in futures:
            future.result()
    result = {'result': 'PASS', 'model': lock['model'], 'revision': lock['revision'], 'quantization': lock['quantization'],
              'verifiedFiles': len(lock['files']), 'verifiedBytes': lock['weightFileBytes'], 'elapsedSeconds': round(time.monotonic() - started, 1)}
    (root / 'download-complete.json').write_text(json.dumps(result, indent=2) + '\n')
    emit(result)


if __name__ == '__main__':
    main()
