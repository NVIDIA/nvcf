# SPDX-License-Identifier: Apache-2.0
import copy
import hashlib
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import MagicMock, patch

from test_recipes import ROOT, all_profile_releases, runtime
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

    def test_current_catalog_profiles_accept_the_declared_cuda_device(self):
        for release in all_profile_releases():
            device = release['config']['profile']['hardware']['cudaDeviceNames'][0]
            self.cuda.get_device_name.return_value = device
            minimum = release['config']['profile']['hardware'].get('minDeviceMemoryGiB', 0)
            available = max(80, minimum)
            self.cuda.get_device_properties.return_value.total_memory = available * runtime.GIB
            with self.subTest(profile=release['profile']), patch.object(runtime.platform, 'machine', return_value='aarch64'):
                result = runtime.check_hardware(release['config'], self.cuda)
            self.assertEqual(result['gpu'], device)
            self.assertEqual(result['cudaTotalMemoryGiB'], available)

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
        config['profile'].update(memoryGiB=4, nodes=1)
        config['model'] = {'id': 'fixture', 'revision': 'a' * 40}
        self.cuda.get_device_name.return_value = 'unexpected-device'
        torch = MagicMock(cuda=self.cuda)
        with tempfile.TemporaryDirectory() as directory, patch.dict(sys.modules, {'torch': torch, 'torch.distributed': torch.distributed}), \
                patch.object(runtime, 'prepare_checkpoint') as prepare, patch.object(runtime, 'host_memory', return_value=200 * runtime.GIB), \
                self.assertRaisesRegex(RuntimeError, 'does not match'):
            runtime.automatic(config, 0, pathlib.Path(directory))
        prepare.assert_not_called()

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


class MmapLoaderPatchTests(unittest.TestCase):
    def test_opt_in_patch_checks_both_pins_and_is_idempotent(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'utils.py'
            source = 'fixture\n' + runtime.MMAP_LOADER_ANCHOR + '            return func(*args, **kwargs)\n'
            patched = source.replace(runtime.MMAP_LOADER_ANCHOR, runtime.MMAP_LOADER_REPLACEMENT)
            path.write_text(source)
            runtime.patch_mmap_weight_loader({'profile': {}}, path)
            self.assertEqual(path.read_text(), source)
            config = {'profile': {'materializeMmapWeights': True}}
            with self.assertRaisesRegex(RuntimeError, 'exact pinned runtime'):
                runtime.patch_mmap_weight_loader(config, path)
            self.assertEqual(path.read_text(), source)
            with patch.object(runtime, 'MMAP_LOADER_SOURCE_SHA256', hashlib.sha256(source.encode()).hexdigest()):
                with self.assertRaisesRegex(RuntimeError, 'pinned result'):
                    runtime.patch_mmap_weight_loader(config, path)
                self.assertEqual(path.read_text(), source)
                with patch.object(runtime, 'MMAP_LOADER_PATCHED_SHA256', hashlib.sha256(patched.encode()).hexdigest()):
                    runtime.patch_mmap_weight_loader(config, path)
                    runtime.patch_mmap_weight_loader(config, path)
                    self.assertEqual(path.read_text(), patched)

    def test_worker_materializes_cpu_inputs_without_changing_gpu_parameters(self):
        import textwrap
        from types import SimpleNamespace
        class Tensor:
            def __init__(self, device, value):
                self.device = SimpleNamespace(type=device)
                self.value = value
            def clone(self):
                return Tensor(self.device.type, self.value)
        source = textwrap.dedent(runtime.MMAP_LOADER_REPLACEMENT + '            return func(*args, **kwargs)\n')
        namespace = {'torch': SimpleNamespace(Tensor=Tensor), 'func': lambda *args, **kwargs: (args, kwargs)}
        exec(compile(source, '<pinned-worker-patch>', 'exec'), namespace)
        cpu, gpu = Tensor('cpu', b'weights'), Tensor('cuda', b'parameter')
        args, kwargs = namespace['device_aware_wrapper'](gpu, cpu, 7, scale=cpu)
        self.assertIs(args[0], gpu)
        self.assertIsNot(args[1], cpu)
        self.assertEqual(args[1].value, cpu.value)
        self.assertEqual(args[2], 7)
        self.assertIsNot(kwargs['scale'], cpu)
        self.assertEqual(kwargs['scale'].value, cpu.value)

    def test_nonboolean_opt_in_is_rejected(self):
        with self.assertRaisesRegex(RuntimeError, 'must be a boolean'):
            runtime.patch_mmap_weight_loader({'profile': {'materializeMmapWeights': 'true'}})


class CacheReuseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.cache = pathlib.Path(self.temp.name).resolve()
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
            self.assertEqual(runtime.prepare_checkpoint(self.config, self.cache), self.snapshot)
        log.assert_called_once_with('download_pass', model='test-model', revision='a' * 40, reused=True)
        self.assertEqual(self.marker.read_text().strip(), self.model['repository'])
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
                    runtime.prepare_checkpoint(self.config, self.cache)

    def test_missing_snapshot_or_tokenizer_refuses_reuse(self):
        for name in ('config.json', 'tokenizer_config.json', 'tokenizer.json'):
            path = self.snapshot / name
            original = path.read_text()
            path.unlink()
            with self.subTest(file=name), patch.dict(sys.modules, {'huggingface_hub': None}), \
                    self.assertRaisesRegex(RuntimeError, 'Missing|missing'):
                runtime.prepare_checkpoint(self.config, self.cache)
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

    def shared_blob(self):
        # Xet's file hash differs from the LFS SHA256 ETag in the repository.
        shared = self.repository.parent / 'blobs' / 'ab' / ('ab' * 32)
        shared.parent.mkdir(parents=True)
        self.blob.rename(shared)
        self.blob.symlink_to(pathlib.Path('../../blobs/ab') / shared.name)
        path = self.snapshot / 'model-00001.safetensors'
        path.unlink()
        path.symlink_to(pathlib.Path('../../blobs') / self.blob.name)
        return shared

    def test_shared_xet_blob_uses_repository_sha256_without_download_client(self):
        self.shared_blob()
        with patch.dict(sys.modules, {'huggingface_hub': None}):
            self.assertEqual(runtime.prepare_checkpoint(self.config, self.cache), self.snapshot)

    def test_corrupt_shared_xet_blob_is_rejected(self):
        shared = self.shared_blob()
        shared.write_bytes(shared.read_bytes()[:-1] + b'X')
        with self.assertRaisesRegex(RuntimeError, 'checksum differs'):
            runtime.reuse_snapshot(self.model, self.cache)

    def test_shared_xet_aliases_each_require_matching_checksums(self):
        shared = self.shared_blob()
        alias = self.blob.with_name('c' * 64)
        alias.symlink_to(shared)
        (self.snapshot / 'duplicate.safetensors').symlink_to(alias)
        with self.assertRaisesRegex(RuntimeError, 'checksum differs'):
            runtime.reuse_snapshot(self.model, self.cache)

    def test_shared_xet_target_must_have_exact_store_layout(self):
        shared = self.shared_blob()
        for target in (self.cache / shared.name, shared.parent / 'bad-hash',
                       shared.parent.with_name('cd') / shared.name):
            with self.subTest(target=target):
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(shared.read_bytes())
                self.blob.unlink()
                self.blob.symlink_to(target)
                with self.assertRaisesRegex(RuntimeError, 'invalid link'):
                    runtime.reuse_snapshot(self.model, self.cache)

    def test_shared_xet_store_cannot_escape_cache_through_symlinks(self):
        shared = self.shared_blob()
        external = self.cache / 'outside-store'
        shared.parent.rename(external)
        shared.parent.symlink_to(external, target_is_directory=True)
        with self.assertRaisesRegex(RuntimeError, 'invalid link'):
            runtime.reuse_snapshot(self.model, self.cache)

    def test_shared_xet_snapshot_cannot_bypass_repository_etag(self):
        shared = self.shared_blob()
        path = self.snapshot / 'model-00001.safetensors'
        path.unlink()
        path.symlink_to(shared)
        with self.assertRaisesRegex(RuntimeError, 'invalid link'):
            runtime.reuse_snapshot(self.model, self.cache)

    def test_file_cache_release_is_opt_in_and_preserves_checkpoint(self):
        before = {p.name: p.read_bytes() for p in self.snapshot.iterdir()}
        advised = []
        def advise(fd, offset, length, advice):
            self.assertEqual((offset, length, advice), (0, 0, 4))
            advised.append(runtime.os.read(fd, 1024))
        with patch.object(runtime.os, 'posix_fadvise', side_effect=advise, create=True) as call, \
                patch.object(runtime.os, 'POSIX_FADV_DONTNEED', 4, create=True):
            runtime.release_checkpoint_file_cache(self.config, self.snapshot)
            call.assert_not_called()
            self.config['profile']['releaseCheckpointFileCache'] = True
            runtime.release_checkpoint_file_cache(self.config, self.snapshot)
        self.assertEqual(advised, [self.blob.read_bytes()])
        self.assertEqual(before, {p.name: p.read_bytes() for p in self.snapshot.iterdir()})

    def test_file_cache_release_rejects_nonboolean_and_propagates_io_failure(self):
        self.config['profile']['releaseCheckpointFileCache'] = 'true'
        with self.assertRaisesRegex(RuntimeError, 'must be a boolean'):
            runtime.release_checkpoint_file_cache(self.config, self.snapshot)
        self.config['profile']['releaseCheckpointFileCache'] = True
        with patch.object(runtime.os, 'posix_fadvise', side_effect=OSError('read error'), create=True), \
                patch.object(runtime.os, 'POSIX_FADV_DONTNEED', 4, create=True), self.assertRaises(OSError):
            runtime.release_checkpoint_file_cache(self.config, self.snapshot)

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


    def test_chart_requires_boolean_and_exposes_reuse_in_runtime_config(self):
        values = all_profile_releases()[0]['values']
        values['reuseCaches'] = True
        file = self.cache / 'values.json'
        file.write_text(json.dumps(values))
        command = ['helm', 'template', 'cache-check', str(ROOT / 'charts/sglang'), '-f', str(file)]
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


if __name__ == '__main__':
    unittest.main()
