# SPDX-License-Identifier: Apache-2.0
import copy
import hashlib
import io
import json
import pathlib
import subprocess
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


class CacheReuseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.cache = pathlib.Path(self.temp.name)
        self.model = {'id': 'test-model', 'repository': 'example/model', 'revision': 'a' * 40}
        self.config = {'model': self.model, 'profile': {'minFreeDiskGiB': 1000}, 'reuseCaches': True}
        self.repository = self.cache / 'huggingface/hub/models--example--model'
        self.snapshot = self.repository / 'snapshots' / self.model['revision']
        self.snapshot.mkdir(parents=True)
        (self.snapshot / 'config.json').write_text('{"model_type":"qwen3"}')
        (self.snapshot / 'tokenizer_config.json').write_text('{"tokenizer_class":"test"}')
        (self.snapshot / 'tokenizer.json').write_text('{"version":"1.0"}')
        header = json.dumps({'weight': {'dtype': 'F32', 'shape': [1], 'data_offsets': [0, 4]}}).encode()
        data = len(header).to_bytes(8, 'little') + header + b'1234'
        blob = self.repository / 'blobs' / hashlib.sha256(data).hexdigest()
        blob.parent.mkdir()
        blob.write_bytes(data)
        self.blob = blob
        (self.snapshot / 'model-00001.safetensors').symlink_to(blob)
        (self.snapshot / 'model.safetensors.index.json').write_text(json.dumps({'weight_map': {'weight': 'model-00001.safetensors'}}))
        self.marker = self.cache / (self.model['revision'] + '.complete')
        self.marker.write_text(self.model['repository'] + '\n')

    def test_reuse_verifies_local_files_without_importing_download_client(self):
        with patch.dict(sys.modules, {'huggingface_hub': None}), patch.object(runtime, 'log') as log, \
                patch.object(runtime.shutil, 'disk_usage', side_effect=AssertionError('No new disk allocation check')):
            self.assertEqual(runtime.download(self.config, self.cache), 0)
        log.assert_called_once_with('download_pass', model='test-model', revision='a' * 40, reused=True)
        self.assertEqual(json.loads(self.marker.read_text()), {'model': 'test-model', 'repository': 'example/model', 'revision': 'a' * 40})
        self.assertEqual(runtime.completed_checkpoint(self.model, self.cache), self.marker)
        self.assertEqual(runtime.reuse_snapshot(self.model, self.cache), self.snapshot)

    def test_missing_or_wrong_identity_marker_never_downloads(self):
        for marker in (None, 'another/repository', json.dumps({'model': 'wrong', 'repository': 'example/model', 'revision': 'a' * 40}),
                       json.dumps({'model': 'test-model', 'repository': 'example/model', 'revision': 'b' * 40})):
            with self.subTest(marker=marker):
                if marker is None:
                    self.marker.unlink(missing_ok=True)
                else:
                    self.marker.write_text(marker)
                with patch.dict(sys.modules, {'huggingface_hub': None}), self.assertRaisesRegex(RuntimeError, 'marker'):
                    runtime.download(self.config, self.cache)

    def test_missing_snapshot_or_tokenizer_refuses_reuse(self):
        for name in ('config.json', 'tokenizer_config.json', 'tokenizer.json'):
            path = self.snapshot / name
            original = path.read_text()
            path.unlink()
            with self.subTest(file=name), patch.dict(sys.modules, {'huggingface_hub': None}), \
                    self.assertRaisesRegex(RuntimeError, 'Missing|missing'):
                runtime.download(self.config, self.cache)
            path.write_text(original)
        other = dict(self.model, revision='b' * 40)
        (self.cache / (other['revision'] + '.complete')).write_text(other['repository'])
        with self.assertRaisesRegex(RuntimeError, 'snapshot is missing'):
            runtime.reuse_snapshot(other, self.cache)

    def test_corrupt_blob_and_missing_indexed_shard_refuse_reuse(self):
        original = self.blob.read_bytes()
        self.blob.write_bytes(original[:-1] + b'X')
        with self.assertRaisesRegex(RuntimeError, 'checksum differs'):
            runtime.reuse_snapshot(self.model, self.cache)
        self.blob.write_bytes(original)
        (self.snapshot / 'model-00001.safetensors').unlink()
        with self.assertRaisesRegex(RuntimeError, 'Missing or corrupt cached weight shard'):
            runtime.reuse_snapshot(self.model, self.cache)
        self.assertEqual(self.marker.read_text().strip(), self.model['repository'])

    def test_broken_or_external_snapshot_links_are_rejected(self):
        path = self.snapshot / 'model-00001.safetensors'
        path.unlink()
        path.symlink_to(self.cache / 'missing')
        with self.assertRaisesRegex(RuntimeError, 'invalid link'):
            runtime.reuse_snapshot(self.model, self.cache)

    def test_truncated_plain_weight_shard_is_rejected(self):
        path = self.snapshot / 'model-00001.safetensors'
        path.unlink()
        path.write_bytes(self.blob.read_bytes()[:-1])
        with self.assertRaisesRegex(RuntimeError, 'corrupt cached weight shard'):
            runtime.reuse_snapshot(self.model, self.cache)

    def test_missing_sharded_weight_index_refuses_partial_snapshot(self):
        (self.snapshot / 'model.safetensors.index.json').unlink()
        with self.assertRaisesRegex(RuntimeError, 'complete weight index'):
            runtime.reuse_snapshot(self.model, self.cache)

    def test_corrupt_git_blob_configuration_is_rejected(self):
        path = self.snapshot / 'config.json'
        data = path.read_bytes()
        digest = hashlib.sha1(('blob ' + str(len(data)) + '\0').encode() + data, usedforsecurity=False).hexdigest()
        blob = self.repository / 'blobs' / digest
        blob.write_bytes(data)
        path.unlink()
        path.symlink_to(blob)
        runtime.reuse_snapshot(self.model, self.cache)
        blob.write_text('{"model_type":"wrong"}')
        with self.assertRaisesRegex(RuntimeError, 'checksum differs'):
            runtime.reuse_snapshot(self.model, self.cache)

    def test_serving_accepts_structured_marker_and_uses_only_local_snapshot(self):
        runtime.reuse_snapshot(self.model, self.cache)
        config = all_profile_releases()[0]['values']
        config['model'] = self.model
        config['reuseCaches'] = True
        hub = MagicMock()
        hub.snapshot_download.return_value = str(self.snapshot)
        original_read = pathlib.Path.read_text
        original_complete = runtime.completed_checkpoint
        def read(path, *args, **kwargs):
            return json.dumps(config) if str(path) == '/recipe/config.json' else original_read(path, *args, **kwargs)
        with patch.dict(sys.modules, {'huggingface_hub': hub, 'torch': MagicMock()}), \
                patch.object(runtime.pathlib.Path, 'read_text', read), \
                patch.object(runtime, 'completed_checkpoint', side_effect=lambda model, _: original_complete(model, self.cache)), \
                patch.object(runtime, 'host_memory', return_value=200 * runtime.GIB), \
                patch.object(runtime, 'check_hardware'), patch.object(runtime, 'supervise', return_value=0), \
                patch.object(runtime, 'log'), patch.object(sys, 'argv', ['runtime.py', 'serve', '0']):
            self.assertEqual(runtime.main(), 0)
        hub.snapshot_download.assert_called_once_with(repo_id='example/model', revision='a' * 40, local_files_only=True)

    def test_normal_download_behavior_remains_available(self):
        hub = MagicMock()
        self.config['reuseCaches'] = False
        with patch.dict(sys.modules, {'huggingface_hub': hub}), patch.object(runtime, 'log') as log:
            self.assertEqual(runtime.download(self.config, self.cache), 0)
        hub.snapshot_download.assert_called_once_with(repo_id='example/model', revision='a' * 40)
        log.assert_called_once_with('download_pass', model='test-model', revision='a' * 40)

    def test_chart_requires_boolean_and_exposes_reuse_in_runtime_config(self):
        values = all_profile_releases()[0]['values']
        values['reuseCaches'] = True
        file = self.cache / 'values.json'
        file.write_text(json.dumps(values))
        command = ['helm', 'template', 'cache-check', str(ROOT / 'charts/sglang'), '-f', str(file), '--set', 'phase=download']
        result = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('"reuseCaches": true', result.stdout)
        self.assertIn("name: HF_HUB_OFFLINE, value: '1'", result.stdout)
        self.assertIn("name: TRANSFORMERS_OFFLINE, value: '1'", result.stdout)
        values['reuseCaches'] = False
        file.write_text(json.dumps(values))
        normal = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(normal.returncode, 0, normal.stderr)
        self.assertNotIn('name: HF_HUB_OFFLINE', normal.stdout)
        self.assertNotIn('name: TRANSFORMERS_OFFLINE', normal.stdout)
        values['reuseCaches'] = 'true'
        file.write_text(json.dumps(values))
        result = subprocess.run(command, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('reuseCaches must be a boolean', result.stderr)


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
