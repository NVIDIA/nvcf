# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import hashlib
import importlib.util
import io
import http.server
import threading
import urllib.error
import urllib.request
import pathlib
import tempfile
import unittest
from unittest.mock import patch

source = pathlib.Path(__file__).resolve().parents[1] / 'files/download.py'
spec = importlib.util.spec_from_file_location('download', source)
download = importlib.util.module_from_spec(spec)
spec.loader.exec_module(download)


class Response(io.BytesIO):
    def __init__(self, content, status=200, content_range=None):
        super().__init__(content)
        self.status = status
        self.headers = {} if content_range is None else {'Content-Range': content_range}


class DownloadTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        download.root = pathlib.Path(directory.name)
        download.lock = {'model': 'example/model', 'revision': 'pinned'}
        download.progress.clear()
        self.content = b'valid checkpoint'
        self.expected = {'rfilename': 'model.gguf', 'size': len(self.content),
                         'lfs': {'sha256': hashlib.sha256(self.content).hexdigest()}}
        self.partial = download.root / 'model.gguf.partial'
        self.destination = download.root / 'model.gguf'

    def test_corrupt_complete_partial_is_removed_and_next_run_downloads_again(self):
        self.partial.write_bytes(b'x' * len(self.content))
        with patch.object(download.urllib.request, 'urlopen') as request:
            with self.assertRaisesRegex(AssertionError, 'checksum mismatch'):
                download.download(self.expected)
            request.assert_not_called()
        self.assertFalse(self.partial.exists())
        self.assertFalse(self.destination.exists())
        with patch.object(download.urllib.request, 'urlopen', return_value=io.BytesIO(self.content)) as request:
            download.download(self.expected)
        self.assertEqual(request.call_args.args[0].get_header('Range'), 'bytes=0-')
        self.assertEqual(self.destination.read_bytes(), self.content)
        self.assertFalse(self.partial.exists())

    def test_valid_complete_partial_is_published_without_downloading(self):
        self.partial.write_bytes(self.content)
        with patch.object(download.urllib.request, 'urlopen') as request:
            download.download(self.expected)
            request.assert_not_called()
        self.assertEqual(self.destination.read_bytes(), self.content)
        self.assertFalse(self.partial.exists())

    def test_network_failures_preserve_partial_for_resume(self):
        self.partial.write_bytes(self.content[:4])
        with patch.object(download.urllib.request, 'urlopen', side_effect=OSError('offline')), \
             patch.object(download.time, 'sleep'):
            with self.assertRaisesRegex(RuntimeError, 'retries exhausted'):
                download.download(self.expected)
        self.assertEqual(self.partial.read_bytes(), self.content[:4])
        self.assertFalse(self.destination.exists())


    def test_invalid_resume_response_restarts_from_zero(self):
        for status, header in [(200, None), (206, None), (206, 'invalid'),
                               (206, 'bytes 0-15/16'), (206, 'bytes 4-15/99'),
                               (206, 'bytes 4-3/16'), (206, 'bytes 4-16/16')]:
            with self.subTest(status=status, content_range=header):
                self.destination.unlink(missing_ok=True)
                self.partial.write_bytes(self.content[:4])
                replies = [Response(self.content, status, header), Response(self.content)]
                with patch.object(download.urllib.request, 'urlopen', side_effect=replies) as request, \
                     patch.object(download.time, 'sleep'):
                    download.download(self.expected)
                self.assertEqual([c.args[0].get_header('Range') for c in request.call_args_list],
                                 ['bytes=4-', 'bytes=0-'])
                self.assertEqual(self.destination.read_bytes(), self.content)
                self.assertFalse(self.partial.exists())

    def test_oversized_chunk_is_never_written_and_restarts(self):
        self.partial.write_bytes(self.content[:4])
        replies = [Response(self.content[4:] + b'excess', 206, 'bytes 4-15/16'), Response(self.content)]
        maximum = []
        original = pathlib.Path.open

        def open_file(path, *args, **kwargs):
            stream = original(path, *args, **kwargs)
            if path != self.partial or not args or args[0] != 'ab':
                return stream
            class CheckedWriter:
                def __enter__(self):
                    return self
                def __exit__(self, *exc):
                    stream.close()
                def write(self, chunk):
                    count = stream.write(chunk)
                    stream.flush()
                    maximum.append(path.stat().st_size)
                    self_case.assertLessEqual(maximum[-1], self_case.expected['size'])
                    return count
            return CheckedWriter()

        self_case = self
        with patch.object(download.urllib.request, 'urlopen', side_effect=replies), \
             patch.object(download.time, 'sleep'), patch.object(pathlib.Path, 'open', open_file):
            download.download(self.expected)
        self.assertEqual(self.destination.read_bytes(), self.content)
        self.assertEqual(maximum, [len(self.content)])

    def test_oversized_partial_from_an_earlier_run_is_recovered(self):
        self.partial.write_bytes(self.content + b'excess')
        with patch.object(download.urllib.request, 'urlopen', return_value=Response(self.content)) as request:
            download.download(self.expected)
        self.assertEqual(request.call_args.args[0].get_header('Range'), 'bytes=0-')
        self.assertEqual(self.destination.read_bytes(), self.content)

    def test_range_not_satisfiable_restarts_from_zero(self):
        self.partial.write_bytes(self.content[:4])
        error = urllib.error.HTTPError('https://example.invalid/model', 416, 'invalid range', {}, None)
        with patch.object(download.urllib.request, 'urlopen', side_effect=[error, Response(self.content)]) as request, \
             patch.object(download.time, 'sleep'):
            download.download(self.expected)
        self.assertEqual([c.args[0].get_header('Range') for c in request.call_args_list],
                         ['bytes=4-', 'bytes=0-'])
        self.assertEqual(self.destination.read_bytes(), self.content)

    def test_invalid_responses_exhaust_retry_budget_without_oversized_partial(self):
        self.partial.write_bytes(self.content[:4])
        with patch.object(download.urllib.request, 'urlopen', side_effect=lambda *a, **k: Response(self.content + b'excess')) as request, \
             patch.object(download.time, 'sleep'):
            with self.assertRaisesRegex(RuntimeError, 'retries exhausted'):
                download.download(self.expected)
        self.assertEqual(request.call_count, 12)
        self.assertFalse(self.partial.exists())
        self.assertFalse(self.destination.exists())

    def test_valid_resume_keeps_existing_bytes(self):
        self.partial.write_bytes(self.content[:4])
        with patch.object(download.urllib.request, 'urlopen', return_value=Response(self.content[4:], 206, 'bytes 4-15/16')) as request:
            download.download(self.expected)
        self.assertEqual(request.call_count, 1)
        self.assertEqual(request.call_args.args[0].get_header('Range'), 'bytes=4-')
        self.assertEqual(self.destination.read_bytes(), self.content)

    def test_resume_recovery_over_real_http(self):
        requests = []
        content = self.content
        self.partial.write_bytes(content[:4])

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                requests.append(self.headers.get('Range'))
                # The first server response ignores Range; the second is a fresh full download.
                self.send_response(200)
                self.send_header('Content-Length', str(len(content)))
                self.end_headers()
                self.wfile.write(content)
            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        urlopen = urllib.request.urlopen

        def request_local(request, timeout):
            local = urllib.request.Request('http://127.0.0.1:' + str(server.server_port) + '/model',
                                           headers=dict(request.header_items()))
            return urlopen(local, timeout=timeout)

        try:
            with patch.object(download.urllib.request, 'urlopen', side_effect=request_local), \
                 patch.object(download.time, 'sleep'):
                download.download(self.expected)
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
        self.assertEqual(requests, ['bytes=4-', 'bytes=0-'])
        self.assertEqual(self.destination.read_bytes(), content)
        self.assertFalse(self.partial.exists())


if __name__ == '__main__':
    unittest.main()
