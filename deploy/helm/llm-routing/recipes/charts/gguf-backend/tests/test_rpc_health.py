# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import pathlib
import tempfile
import unittest

source = pathlib.Path(__file__).resolve().parents[1] / 'files/rpc-health.py'
spec = importlib.util.spec_from_file_location('rpc_health', source)
health = importlib.util.module_from_spec(spec)
spec.loader.exec_module(health)


class RPCHealthTests(unittest.TestCase):
    def test_connected_server_is_ready_without_a_new_connection(self):
        with tempfile.TemporaryDirectory() as directory:
            proc = pathlib.Path(directory)
            server = proc / '42'
            (server / 'net').mkdir(parents=True)
            (server / 'cmdline').write_bytes(b'/work/runtime/ggml-rpc-server\0--port\x0050052\0')
            (server / 'status').write_text('State:\tS (sleeping)\n')
            tcp = server / 'net/tcp'
            tcp.write_text('header\n0: 00000000:C384 00000000:0000 0A 00000000:00000006\n1: 0100007F:C384 0200007F:1234 01 0:0\n')
            self.assertTrue(health.ready(proc))
            tcp.write_text('header\n1: 0100007F:C384 0200007F:1234 01 0:0\n')
            self.assertFalse(health.ready(proc))

    def test_stopped_process_is_not_ready(self):
        with tempfile.TemporaryDirectory() as directory:
            proc = pathlib.Path(directory)
            server = proc / '42'
            server.mkdir()
            (server / 'cmdline').write_bytes(b'/work/runtime/ggml-rpc-server\0')
            (server / 'status').write_text('State:\tT (stopped)\n')
            self.assertFalse(health.ready(proc))


if __name__ == '__main__':
    unittest.main()
