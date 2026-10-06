# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import hashlib
import importlib.util
import io
import pathlib
import tempfile
import unittest
from unittest.mock import patch

source = pathlib.Path(__file__).resolve().parents[1] / 'files/download.py'
spec = importlib.util.spec_from_file_location('download', source)
download = importlib.util.module_from_spec(spec)
spec.loader.exec_module(download)


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


if __name__ == '__main__':
    unittest.main()
