# SPDX-License-Identifier: Apache-2.0
import copy
import io
import json
import pathlib
import sys
import tempfile
import unittest
import urllib.error
from unittest.mock import MagicMock, patch

from test_recipes import ROOT, all_profile_releases, runtime
sys.path.insert(0, str(ROOT))
import verify


def stream(model='qwen3.8-27b'):
    chunks = [{'model': model, 'choices': [{'delta': {'content': 'Hello'}}]},
              {'model': model, 'choices': [], 'usage': {'completion_tokens': 1}}]
    return (''.join('data: ' + json.dumps(x) + '\n\n' for x in chunks) + 'data: [DONE]\n\n').encode()


class HardwareProfileTests(unittest.TestCase):
    def setUp(self):
        self.cuda = MagicMock()
        self.cuda.device_count.return_value = 1
        self.cuda.get_device_name.return_value = 'NVIDIA Example GPU'
        self.cuda.get_device_properties.return_value.total_memory = 80 * runtime.GIB
        self.hardware = {'os': 'linux', 'architecture': 'amd64', 'gpuCount': 1,
                         'cudaDeviceNames': ['NVIDIA Example GPU'], 'memoryMode': 'discrete', 'minDeviceMemoryGiB': 72}
        self.config = {'profile': {'hardware': self.hardware, 'nodes': 1}}
        for field, value in (('system', 'Linux'), ('machine', 'x86_64')):
            patcher = patch.object(runtime.platform, field, return_value=value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_discrete_gpu_qualification_uses_profile_and_reports_device_memory(self):
        torch = MagicMock()
        torch.cuda = self.cuda
        torch.all.return_value.item.return_value = True
        with patch.dict(sys.modules, {'torch': torch, 'torch.distributed': torch.distributed}), \
             patch.object(runtime, 'log') as log:
            runtime.qualify(self.config, 0)
        torch.ones.assert_called_once()
        self.cuda.synchronize.assert_called_once()
        log.assert_called_once_with('qualification_pass', rank=0, nodes=1, nccl=self.cuda.nccl.version(),
                                    gpu='NVIDIA Example GPU', cudaTotalMemoryGiB=80)

    def test_current_unified_catalog_profiles_accept_the_declared_cuda_device(self):
        self.cuda.get_device_name.return_value = 'NVIDIA GB10'
        for release in all_profile_releases():
            with self.subTest(profile=release['profile']), patch.object(runtime.platform, 'machine', return_value='aarch64'):
                result = runtime.check_hardware(release['values'], self.cuda)
            self.assertEqual(result['gpu'], 'NVIDIA GB10')
            self.assertEqual(result['cudaTotalMemoryGiB'], 80)

    def test_wrong_device_or_gpu_count_fails_before_collective(self):
        for count, name in ((0, 'NVIDIA Example GPU'), (2, 'NVIDIA Example GPU'), (1, 'NVIDIA GB10'),
                            (1, 'Example')):
            self.cuda.device_count.return_value = count
            self.cuda.get_device_name.return_value = name
            torch = MagicMock()
            torch.cuda = self.cuda
            with self.subTest(count=count, name=name), patch.dict(sys.modules, {'torch': torch, 'torch.distributed': torch.distributed}), \
                 self.assertRaisesRegex(RuntimeError, 'exclusively assigned|does not match'):
                runtime.qualify(self.config, 0)
            torch.ones.assert_not_called()
            torch.distributed.init_process_group.assert_not_called()

    def test_serving_rechecks_hardware_before_loading_model_artifacts(self):
        config = copy.deepcopy(self.config)
        config['profile']['memoryGiB'] = 4
        config['model'] = {'id': 'fixture', 'revision': 'a' * 40}
        self.cuda.get_device_name.return_value = 'unexpected-device'
        torch = MagicMock()
        torch.cuda = self.cuda
        hub = MagicMock()
        with patch.dict(sys.modules, {'torch': torch, 'huggingface_hub': hub}), \
             patch.object(sys, 'argv', ['runtime.py', 'serve', '0']), \
             patch.object(runtime.pathlib.Path, 'read_text', return_value=json.dumps(config)), \
             patch.object(runtime, 'host_memory', return_value=8 * runtime.GIB), patch.object(runtime, 'log'), \
             self.assertRaisesRegex(RuntimeError, 'does not match the hardware profile'):
            runtime.main()
        hub.snapshot_download.assert_not_called()

    def test_profile_rejects_mismatched_os_architecture_and_unsupported_gpu_count(self):
        for field, value in (('os', 'windows'), ('architecture', 'arm64'), ('gpuCount', 2), ('gpuCount', True)):
            config = copy.deepcopy(self.config)
            config['profile']['hardware'][field] = value
            with self.subTest(field=field), self.assertRaisesRegex(RuntimeError, 'hardware profile|exclusively assigned'):
                runtime.check_hardware(config, self.cuda)

    def test_discrete_profile_requires_explicit_valid_device_memory_minimum(self):
        for value in (None, 0, -1, True, '72', float('inf'), float('nan')):
            config = copy.deepcopy(self.config)
            config['profile']['hardware']['minDeviceMemoryGiB'] = value
            with self.subTest(value=value), self.assertRaisesRegex(RuntimeError, 'positive minDeviceMemoryGiB'):
                runtime.check_hardware(config, self.cuda)
        del self.hardware['minDeviceMemoryGiB']
        with self.assertRaisesRegex(RuntimeError, 'positive minDeviceMemoryGiB'):
            runtime.check_hardware(self.config, self.cuda)

    def test_discrete_profile_rechecks_actual_cuda_memory(self):
        self.cuda.get_device_properties.return_value.total_memory = 64 * runtime.GIB
        with self.assertRaisesRegex(RuntimeError, 'insufficient memory'):
            runtime.check_hardware(self.config, self.cuda)


class MemoryGuardTests(unittest.TestCase):
    def test_low_memory_and_swap_stop_owned_runtime_and_latch(self):
        for available, swap in [(runtime.GIB, 0), (10 * runtime.GIB, 4096)]:
            with self.subTest(available=available), tempfile.TemporaryDirectory() as d:
                path = pathlib.Path(d)
                (path / 'memory.swap.current').write_text(str(swap))
                child = MagicMock()
                child.poll.return_value = None
                with patch.object(runtime.subprocess, 'Popen', return_value=child), \
                        patch.object(runtime, 'host_memory', return_value=available), \
                        patch.object(runtime.signal, 'signal'), patch.object(runtime, 'stop') as stop:
                    self.assertEqual(runtime.supervise(['fake'], {'model': {'id': 'test'}}, path, path), 78)
                    self.assertTrue(stop.called)
                    self.assertTrue((path / 'recipe-memory-stop.json').exists())
                with patch.object(runtime.subprocess, 'Popen') as spawn, self.assertRaisesRegex(RuntimeError, 'previously stopped'):
                    runtime.supervise(['fake'], {'model': {'id': 'test'}}, path, path)
                spawn.assert_not_called()

    def test_missing_cgroup_fails_before_process_start(self):
        with tempfile.TemporaryDirectory() as d, patch.object(runtime.subprocess, 'Popen') as spawn:
            with self.assertRaisesRegex(RuntimeError, 'cgroup v2'):
                runtime.supervise(['fake'], {}, pathlib.Path(d), pathlib.Path(d))
            spawn.assert_not_called()


class GatewayTests(unittest.TestCase):
    def test_stream(self):
        self.assertEqual(verify.parse_stream(stream(), 'qwen3.8-27b')['reply'], 'Hello')

    def test_missing_done_wrong_model_error_and_no_usage_fail(self):
        for data in [stream().replace(b'data: [DONE]\n\n', b''), stream('wrong'),
                     b'data: {"error":"bad"}\n\n',
                     b'data: {"model":"qwen3.8-27b","choices":[{"delta":{"content":"Hi"}}]}\n\ndata: [DONE]\n\n']:
            with self.subTest(data=data), self.assertRaises(ValueError):
                verify.parse_stream(data, 'qwen3.8-27b')

    def test_bounds_and_redirect(self):
        with self.assertRaisesRegex(ValueError, 'limit'):
            verify.read_bounded(io.BytesIO(b'x' * (verify.MAX_BYTES + 1)))
        with self.assertRaisesRegex(ValueError, 'redirect'):
            verify.NoRedirect().redirect_request(None, None, None, None, None, None)

    def test_both_models_share_url_and_key(self):
        models = ['qwen3.8-27b', 'qwen3.8-flash-next']
        outputs = [io.BytesIO(json.dumps({'data': [{'id': m} for m in models]}).encode()),
                   io.BytesIO(json.dumps({'models': [{'model': m, 'health': 'Healthy'} for m in models]}).encode())]
        for m in models:
            outputs += [io.BytesIO(json.dumps({'model': m, 'choices': [{'message': {'content': 'Hello'}}],
                                              'usage': {'completion_tokens': 1}}).encode()), io.BytesIO(stream(m))]
        outputs += [urllib.error.HTTPError('url', 401, 'unauthorized', {}, None)]
        opener = MagicMock()
        opener.open.side_effect = outputs
        with patch.object(verify.urllib.request, 'build_opener', return_value=opener), patch.object(verify.ssl, 'create_default_context'):
            result = verify.verify('https://gateway/v1', None, 'test-key', models)
        self.assertTrue(result['passed'])
        calls = opener.open.call_args_list[2:6]
        self.assertEqual(len(calls), 4)
        for call in calls:
            request = call.args[0]
            self.assertEqual(request.full_url, 'https://gateway/v1/chat/completions')
            self.assertEqual(request.get_header('Authorization'), 'Bearer test-key')

    def test_insecure_url_is_rejected(self):
        for url in ['http://gateway/v1', 'https://user:pass@gateway/v1', 'https://gateway/v1?secret=yes']:
            with self.subTest(url=url), self.assertRaises(ValueError):
                verify.verify(url, None, 'key', ['model'])


if __name__ == '__main__':
    unittest.main()
