# SPDX-License-Identifier: Apache-2.0
import copy
import hashlib
import importlib.util
import io
import json
import pathlib
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import MagicMock, patch

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[1]
CHART = ROOT / 'charts/gguf-backend'
spec = importlib.util.spec_from_file_location('automatic_gguf', CHART / 'files/automatic.py')
auto = importlib.util.module_from_spec(spec)
spec.loader.exec_module(auto)


def values(**overrides):
    return dict(recipe='glm-5.3', nodes=['model-0', 'model-1'], sharedCAConfigMap='stack-ca', **overrides)


def render(config=None, check=True):
    with tempfile.TemporaryDirectory() as directory:
        path = pathlib.Path(directory) / 'values.json'
        path.write_text(json.dumps(config if config is not None else values()))
        result = subprocess.run(['helm', 'template', 'gguf-test', str(CHART), '--namespace', 'demo', '-f', str(path)],
                                capture_output=True, text=True)
    if check:
        if result.returncode:
            raise AssertionError(result.stderr)
        return [item for item in yaml.safe_load_all(result.stdout) if item]
    return result


def object_named(objects, kind, name):
    return next(item for item in objects if item['kind'] == kind and item['metadata']['name'] == name)


class AutomaticChartTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.objects = render()
        cls.configmap = object_named(cls.objects, 'ConfigMap', 'gguf-test-automatic')
        cls.config = json.loads(cls.configmap['data']['automatic.json'])

    def test_bundled_recipe_defaults_match_the_model_owned_sources(self):
        for name in ('recipe.json', 'model.lock.json', 'profiles.json'):
            self.assertEqual((CHART / 'files/recipes/glm-5.3' / name).read_bytes(), (ROOT / 'glm-5.3' / name).read_bytes())
        self.assertEqual((CHART / 'files/placement.py').read_bytes(), (ROOT / 'charts/sglang/files/placement.py').read_bytes())
        self.assertEqual(self.config['recipe'], json.loads((ROOT / 'glm-5.3/recipe.json').read_text()))
        self.assertEqual(self.config['lock'], json.loads((ROOT / 'glm-5.3/model.lock.json').read_text()))

    def test_one_install_has_unambiguous_model_resources_and_real_readiness(self):
        identities = [(item['kind'], item['metadata']['name']) for item in self.objects]
        self.assertEqual(len(identities), len(set(identities)))
        self.assertEqual([item['metadata']['name'] for item in self.objects if item['kind'] == 'PersistentVolumeClaim'],
                         ['gguf-test-artifacts', 'gguf-test-rpc-cache-n1'])
        leader = object_named(self.objects, 'Deployment', 'gguf-test')['spec']['template']['spec']
        worker = object_named(self.objects, 'Deployment', 'gguf-test-rpc-n1')['spec']['template']['spec']
        self.assertEqual([c['name'] for c in leader['initContainers']], ['placement', 'hardware', 'prepare-runtime', 'qualify', 'download'])
        self.assertEqual([c['name'] for c in worker['initContainers']], ['placement', 'hardware', 'fetch-runtime'])
        self.assertEqual(leader['containers'][0]['command'][-1], 'serve')
        self.assertEqual(leader['containers'][0]['readinessProbe']['httpGet']['path'], '/health')
        self.assertEqual(worker['containers'][0]['readinessProbe']['exec']['command'], ['python3', '/checks/rpc-health.py'])
        endpoint = object_named(self.objects, 'InferenceEndpoint', 'gguf-test')
        self.assertEqual(endpoint['apiVersion'], 'pylon.nvidia.com/v1alpha1')
        self.assertEqual(endpoint['spec']['modelName'], self.config['recipe']['servedName'])
        self.assertEqual(endpoint['spec']['service'], {'name': 'gguf-test', 'port': 8000})
        self.assertEqual(endpoint['spec']['maxEngineConcurrency'], 1)

    def test_profile_controls_gpu_layer_balance(self):
        model = object_named(self.objects, 'Deployment', 'gguf-test')['spec']['template']['spec']['containers'][0]
        environment = {item['name']: item['value'] for item in model['env']}
        args = json.loads(environment['SERVER_ARGS'])
        split = args[args.index('--tensor-split') + 1]
        self.assertEqual(split, ','.join(map(str, self.config['profile']['tensorSplit'])))
        self.assertEqual(len(split.split(',')), self.config['profile']['nodes'])
        self.assertEqual(args[:len(self.config['recipe']['serverArgs'])], self.config['recipe']['serverArgs'])

    def test_recipe_tuning_matches_server_arguments_and_catalog_workload(self):
        spec = importlib.util.spec_from_file_location('gguf_tuning_sizing', ROOT / 'sizing.py')
        sizing = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(sizing)
        profile = self.config['profile']
        tuning = self.config['recipe']['tuning'][profile['hardware']['memoryMode']]
        self.assertEqual(self.config['tuning'], tuning)
        plan = sizing.memory_plan(dict(self.config['recipe'], lock=self.config['lock']),
                                  {'unifiedMemory': True, 'memoryGiB': profile['hardware']['minHostMemoryGiB']},
                                  profile['nodes'], tuning)
        self.assertEqual(profile['minAvailableMemoryGiB'], plan['hostAvailableGiB'])
        for role in profile['roles']:
            self.assertEqual(role['resources']['requests']['memory'], str(plan['requestGiB']) + 'Gi')
            self.assertEqual(role['resources']['limits']['memory'], str(plan['limitGiB']) + 'Gi')
        model = object_named(self.objects, 'Deployment', 'gguf-test')['spec']['template']['spec']['containers'][0]
        environment = {item['name']: item['value'] for item in model['env']}
        args = json.loads(environment['SERVER_ARGS'])
        for flag, value in zip(sizing.tuning_args(tuning)[::2], sizing.tuning_args(tuning)[1::2]):
            self.assertEqual(args.count(flag), 1)
            self.assertEqual(args[args.index(flag) + 1], value)
        exported = next(recipe for recipe in json.loads((ROOT / 'index.json').read_text())['recipes'] if recipe['id'] == 'glm-5.3')
        workload = next(p for p in exported['profiles'] if p['id'] == profile['id'])['workload']
        self.assertEqual(tuning['contextPerSlot'], workload['defaultContextTokens'])
        self.assertEqual(tuning['slots'], workload['defaultConcurrency'])
        self.assertEqual(object_named(self.objects, 'InferenceEndpoint', 'gguf-test')['spec']['maxEngineConcurrency'], tuning['slots'])

    def test_automatic_render_rejects_advertised_tuning_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            chart = pathlib.Path(directory) / 'gguf-backend'
            shutil.copytree(CHART, chart)
            source = chart / 'files/recipes/glm-5.3/recipe.json'
            recipe = json.loads(source.read_text())
            recipe['tuning']['unified']['contextPerSlot'] *= 2
            source.write_text(json.dumps(recipe))
            with patch(__name__ + '.CHART', chart):
                result = render(check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('Automatic profile context and concurrency must match recipe tuning', result.stderr)

    def test_explicit_placement_holds_one_gpu_and_model_memory_on_each_node(self):
        for index, suffix in enumerate(('', '-rpc-n1')):
            deployment = object_named(self.objects, 'Deployment', 'gguf-test' + suffix)
            spec = deployment['spec']['template']['spec']
            self.assertEqual(deployment['spec']['strategy'], {'type': 'Recreate'})
            self.assertEqual(spec['nodeSelector']['kubernetes.io/hostname'], 'model-' + str(index))
            self.assertEqual(spec['containers'][0]['resources'], self.config['profile']['roles'][index]['resources'])
            for container in spec['containers']:
                self.assertEqual(container['resources']['requests']['nvidia.com/gpu'], 1)
                self.assertEqual(container['resources']['limits']['nvidia.com/gpu'], 1)
            for container in spec['initContainers']:
                self.assertLessEqual(int(container['resources']['requests'].get('nvidia.com/gpu', 0)), 1)
                self.assertLessEqual(int(container['resources']['requests']['memory'].removesuffix('Gi').removesuffix('Mi')), 128)
            self.assertFalse(spec['automountServiceAccountToken'])

    def test_artifacts_are_available_during_leader_init_without_gpu_or_wait_dependency(self):
        pod = object_named(self.objects, 'Deployment', 'gguf-test-artifacts')['spec']['template']['spec']
        self.assertFalse(pod.get('initContainers'))
        self.assertEqual(pod['nodeSelector']['kubernetes.io/hostname'], 'model-0')
        self.assertNotIn('nvidia.com/gpu', pod['containers'][0]['resources']['requests'])
        self.assertEqual(pod['containers'][0]['resources'], self.config['profile']['artifactsService']['resources'])
        self.assertTrue(pod['containers'][0]['volumeMounts'][0]['readOnly'])
        self.assertEqual(pod['volumes'][0]['persistentVolumeClaim']['claimName'], self.config['claims'][0])
        self.assertFalse(any(item['kind'] == 'Job' for item in self.objects))

    def test_access_is_exact_read_only_and_tokens_are_only_in_placement(self):
        role = next(item for item in self.objects if item['kind'] == 'ClusterRole')
        self.assertEqual(role['rules'], [{'apiGroups': [''], 'resources': ['nodes'], 'resourceNames': ['model-0', 'model-1'], 'verbs': ['get']}])
        role = object_named(self.objects, 'Role', 'gguf-test-placement')
        self.assertEqual(role['rules'][0]['resourceNames'], self.config['claims'])
        self.assertEqual(role['rules'][0]['verbs'], ['get'])
        for name in ('gguf-test', 'gguf-test-rpc-n1'):
            pod = object_named(self.objects, 'Deployment', name)['spec']['template']['spec']
            for container in [*pod['initContainers'], *pod['containers']]:
                mounted = {volume['name'] for volume in container['volumeMounts']}
                self.assertEqual('cluster-access' in mounted, container['name'] == 'placement')
        self.assertFalse(any(item['kind'] in ('Secret', 'Namespace') for item in self.objects))
        self.assertFalse(any('gateway' in item['metadata']['name'] or 'operator' in item['metadata']['name'] for item in self.objects))

    def test_reuse_mounts_existing_claims_without_adopting_or_recreating_them(self):
        objects = render(values(reuseCaches=True, artifacts={'existingClaim': 'retained-model'}, rpc={'cache': {'existingClaim': 'retained-rpc'}}))
        self.assertFalse(any(item['kind'] == 'PersistentVolumeClaim' for item in objects))
        config = json.loads(object_named(objects, 'ConfigMap', 'gguf-test-automatic')['data']['automatic.json'])
        self.assertTrue(config['reuseCaches'])
        self.assertEqual(config['claims'], ['retained-model', 'retained-rpc'])
        for index, suffix in enumerate(('', '-rpc-n1')):
            pod = object_named(objects, 'Deployment', 'gguf-test' + suffix)['spec']['template']['spec']
            self.assertEqual(next(v for v in pod['volumes'] if v['name'] == 'cache')['persistentVolumeClaim']['claimName'], config['claims'][index])

    def test_invalid_recipe_or_topology_fails_during_render(self):
        cases = [dict(recipe='unsupported'), dict(nodes=['a']), dict(nodes=['a', 'a']), dict(nodes=['a', 'b', 'c']),
                 dict(nodes=['a', 'INVALID']), dict(sharedCAConfigMap=''), dict(profileName='not-this-profile'),
                 dict(reuseCaches=True), dict(reuseCaches='true'), dict(image='untrusted:latest'),
                 dict(artifacts={'existingClaim': 'same'}, rpc={'cache': {'existingClaim': 'same'}})]
        for changes in cases:
            config = values()
            config.update(changes)
            with self.subTest(changes=changes):
                self.assertNotEqual(render(config, check=False).returncode, 0)

    def test_suspend_retains_claims_and_stops_all_owned_deployments(self):
        objects = render(values(suspended=True))
        self.assertEqual([item['spec']['replicas'] for item in objects if item['kind'] == 'Deployment'], [0, 0, 0])
        self.assertFalse(any(item['kind'] == 'InferenceEndpoint' for item in objects))
        for item in objects:
            if item['kind'] == 'PersistentVolumeClaim':
                self.assertEqual(item['metadata']['annotations']['helm.sh/resource-policy'], 'keep')

    def test_existing_explicit_serve_phase_remains_available(self):
        config = {'phase': 'serve', 'image': self.config['image'], 'targets': [{'id': 'n0', 'node': 'a'}, {'id': 'n1', 'node': 'b'}],
                  'runtime': {'sha256': 'a' * 64}, 'model': {'lock': self.config['lock'], 'firstShard': self.config['recipe']['firstShard'],
                   'servedName': 'legacy', 'args': self.config['recipe']['serverArgs']}}
        objects = render(config)
        self.assertFalse(any(item['kind'] in ('Role', 'ClusterRole') for item in objects))
        self.assertFalse(any(item['metadata']['name'].endswith('-automatic') for item in objects))
        pod = object_named(objects, 'Deployment', 'gguf-test')['spec']['template']['spec']
        self.assertEqual(pod['containers'][0]['command'][-1], '/checks/serve.py')


class SourceBuildTests(unittest.TestCase):
    def test_interrupted_source_extraction_is_retried_before_publish(self):
        spec = importlib.util.spec_from_file_location('gguf_build', CHART / 'files/build.py')
        build = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(build)
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            source = root / 'llama.cpp-pinned'
            archive = root / 'source.tar.gz'
            with tarfile.open(archive, 'w:gz') as tar:
                info = tarfile.TarInfo('llama.cpp-pinned/src/models/glm-dsa.cpp')
                info.size = 4
                tar.addfile(info, io.BytesIO(b'code'))
            def interrupted(tar, path, **kwargs):
                (pathlib.Path(path) / source.name).mkdir()
                raise OSError('interrupted extraction')
            with patch.object(tarfile.TarFile, 'extractall', interrupted), self.assertRaisesRegex(OSError, 'interrupted'):
                build.extract_source(archive, source)
            self.assertFalse(source.exists())
            build.extract_source(archive, source)
            self.assertEqual((source / 'src/models/glm-dsa.cpp').read_bytes(), b'code')
            with patch.object(tarfile, 'open') as open_archive:
                build.extract_source(archive, source)
                open_archive.assert_not_called()


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = pathlib.Path(temporary.name)
        self.config = {'recipe': {'llamaCppRevision': 'a' * 40}, 'profile': {'cudaArchitectures': '121a-real'}, 'image': 'runtime@sha256:' + 'b' * 64,
                       'reuseCaches': True, 'rpcEndpoints': ['worker:50052']}
        self.runtime = self.root / 'runtime'
        self.runtime.mkdir()
        binaries = {}
        for name in auto.BINARIES:
            (self.runtime / name).write_bytes(name.encode())
            binaries[name] = auto.digest(self.runtime / name)
        self.manifest = {'revision': self.config['recipe']['llamaCppRevision'], 'image': self.config['image'],
                         'options': ['-DCMAKE_CUDA_ARCHITECTURES=121a-real'], 'sourceArchiveSHA256': 'c' * 64, 'binaries': binaries}
        self.write_bundle()
        patcher = patch.object(auto, 'emit')
        patcher.start()
        self.addCleanup(patcher.stop)

    def write_bundle(self):
        (self.runtime / 'build-manifest.json').write_text(json.dumps(self.manifest))
        with tarfile.open(self.root / 'runtime.tar.gz', 'w:gz') as archive:
            archive.add(self.runtime, arcname='runtime')
        (self.root / 'runtime.tar.gz.sha256').write_text(auto.digest(self.root / 'runtime.tar.gz'))

    def test_cached_runtime_requires_exact_pins_and_checks_all_binary_bytes(self):
        with patch.object(auto.subprocess, 'run') as run, patch.object(auto.urllib.request, 'urlopen') as network:
            auto.prepare_runtime(self.config, self.root)
            run.assert_not_called()
            network.assert_not_called()
        for field, value in [('revision', 'd' * 40), ('image', 'other'), ('options', [])]:
            changed = copy.deepcopy(self.manifest)
            changed[field] = value
            (self.runtime / 'build-manifest.json').write_text(json.dumps(changed))
            with self.subTest(field=field), self.assertRaisesRegex(RuntimeError, 'differs'):
                auto.verify_runtime(self.config, self.runtime)
        (self.runtime / 'build-manifest.json').write_text(json.dumps(self.manifest))
        (self.runtime / 'llama-server').write_bytes(b'corrupt')
        with self.assertRaisesRegex(RuntimeError, 'binary checksum'):
            auto.verify_runtime(self.config, self.runtime)

    def test_fresh_runtime_build_uses_pins_and_verifies_result_before_advancing(self):
        self.config['reuseCaches'] = False
        empty = self.root / 'fresh'
        empty.mkdir()
        def build(*args, **kwargs):
            for name in ('runtime', 'runtime.tar.gz', 'runtime.tar.gz.sha256'):
                source = self.root / name
                if source.is_dir():
                    auto.shutil.copytree(source, empty / name)
                else:
                    auto.shutil.copyfile(source, empty / name)
        with patch.object(auto.subprocess, 'run', side_effect=build) as run:
            auto.prepare_runtime(self.config, empty)
        env = run.call_args.kwargs['env']
        self.assertEqual(env['LLAMA_REVISION'], self.config['recipe']['llamaCppRevision'])
        self.assertEqual(env['CUDA_ARCHITECTURES'], self.config['profile']['cudaArchitectures'])
        self.assertEqual(env['BUILD_IMAGE'], self.config['image'])
        self.assertTrue(run.call_args.kwargs['check'])
        auto.emit.assert_called_once_with('runtime_pass', reused=False)

    def test_interrupted_owned_build_resumes_with_identical_pins(self):
        self.config['reuseCaches'] = False
        fresh = self.root / 'interrupted'
        fresh.mkdir()
        def interrupted(*args, **kwargs):
            (fresh / 'runtime').mkdir(exist_ok=True)
            (fresh / 'runtime/llama-server').write_bytes(b'partial')
            (fresh / 'runtime.tar.gz.partial').write_bytes(b'partial archive')
            (fresh / 'runtime.tar.gz.sha256.partial').write_bytes(b'partial checksum')
            raise subprocess.CalledProcessError(1, 'build')
        with patch.object(auto.subprocess, 'run', side_effect=interrupted), self.assertRaises(subprocess.CalledProcessError):
            auto.prepare_runtime(self.config, fresh)
        self.assertTrue((fresh / 'automatic-build.json').is_file())
        def finish(*args, **kwargs):
            auto.shutil.copytree(self.runtime, fresh / 'runtime', dirs_exist_ok=True)
            for name in ('runtime.tar.gz', 'runtime.tar.gz.sha256'):
                auto.shutil.copyfile(self.root / name, fresh / name)
        with patch.object(auto.subprocess, 'run', side_effect=finish) as run:
            auto.prepare_runtime(self.config, fresh)
        run.assert_called_once()
        auto.verify_runtime(self.config, fresh / 'runtime', fresh / 'runtime.tar.gz')

    def test_partial_foreign_cache_and_reuse_mode_never_rebuild(self):
        for reuse in (False, True):
            self.config['reuseCaches'] = reuse
            (self.root / 'runtime.tar.gz.sha256').unlink(missing_ok=True)
            with self.subTest(reuse=reuse), patch.object(auto.subprocess, 'run') as run, self.assertRaises((RuntimeError, FileNotFoundError)):
                auto.prepare_runtime(self.config, self.root)
            run.assert_not_called()

    def test_hardware_checks_available_memory_and_device_before_model_preparation(self):
        self.config['profile'].update({'hardware': {'os': 'linux', 'architecture': 'arm64', 'minHostMemoryGiB': 120, 'cudaDeviceNames': ['NVIDIA GB10'],
                                                   'computeCapability': '12.1'}, 'minAvailableMemoryGiB': 113})
        cuda = MagicMock()
        cuda.is_available.return_value = True
        cuda.device_count.return_value = 1
        cuda.get_device_name.return_value = 'NVIDIA GB10'
        cuda.get_device_capability.return_value = (12, 1)
        memory = 'MemTotal: 134217728 kB\nMemAvailable: 125829120 kB\n'
        with patch.object(auto.platform, 'system', return_value='Linux'), patch.object(auto.platform, 'machine', return_value='aarch64'), \
                patch.object(auto.pathlib.Path, 'read_text', return_value=memory), patch.object(auto.subprocess, 'run') as run:
            auto.hardware(self.config, cuda)
            self.assertEqual(run.call_args.args[0][-1], '/checks/preflight.py')
            self.assertEqual(run.call_args.kwargs['env']['EXPECTED_ARCHITECTURE'], 'aarch64')
            cuda.get_device_name.return_value = 'NVIDIA Other'
            with self.assertRaisesRegex(RuntimeError, 'device differs'):
                auto.hardware(self.config, cuda)
        with patch.object(auto.platform, 'system', return_value='Linux'), patch.object(auto.platform, 'machine', return_value='aarch64'), \
                patch.object(auto.pathlib.Path, 'read_text', return_value='MemTotal: 134217728 kB\nMemAvailable: 1024 kB\n'), \
                patch.object(auto.subprocess, 'run') as run, self.assertRaisesRegex(RuntimeError, 'available host memory'):
            auto.hardware(self.config, cuda)
        run.assert_not_called()

    def test_prebuilt_runtime_bundle_corruption_blocks_cached_start(self):
        with (self.root / 'runtime.tar.gz').open('ab') as stream:
            stream.write(b'corrupt')
        with patch.object(auto.subprocess, 'run') as run, self.assertRaisesRegex(RuntimeError, 'bundle checksum'):
            auto.prepare_runtime(self.config, self.root)
        run.assert_not_called()

    def test_missing_cached_runtime_never_builds_or_downloads(self):
        empty = self.root / 'missing'
        empty.mkdir()
        with patch.object(auto.subprocess, 'run') as run, patch.object(auto.urllib.request, 'urlopen') as network, self.assertRaises(FileNotFoundError):
            auto.prepare_runtime(self.config, empty)
        run.assert_not_called()
        network.assert_not_called()

    def test_worker_rejects_wrong_runtime_revision_before_publishing(self):
        self.manifest['revision'] = 'f' * 40
        self.write_bundle()
        work = self.root / 'work'
        work.mkdir()
        responses = [io.BytesIO((self.root / 'runtime.tar.gz.sha256').read_bytes()), io.BytesIO((self.root / 'runtime.tar.gz').read_bytes())]
        with patch.object(auto.urllib.request, 'urlopen', side_effect=responses), self.assertRaisesRegex(RuntimeError, 'revision differs'):
            auto.fetch_runtime(self.config, 'http://artifacts/runtime.tar.gz', work)
        self.assertFalse((work / 'runtime').exists())

    def test_worker_verifies_bundle_and_binaries_before_publishing(self):
        work = self.root / 'work'
        work.mkdir()
        responses = [io.BytesIO((self.root / 'runtime.tar.gz.sha256').read_bytes()), io.BytesIO((self.root / 'runtime.tar.gz').read_bytes())]
        with patch.object(auto.urllib.request, 'urlopen', side_effect=responses):
            auto.fetch_runtime(self.config, 'http://artifacts/runtime.tar.gz', work)
        self.assertEqual((work / 'runtime/llama-server').read_bytes(), b'llama-server')

    def test_worker_retries_checksum_races_without_replacing_the_verified_archive(self):
        bundle = (self.root / 'runtime.tar.gz').read_bytes()
        checksum = hashlib.sha256(bundle).hexdigest().encode()
        for failure in ('malformed', 'non-utf8', 'mismatch'):
            with self.subTest(failure=failure):
                work = self.root / failure
                work.mkdir()
                archive = work / 'runtime.tar.gz'
                archive.write_bytes(b'previous verified archive')
                first = {'malformed': [b'not-a-checksum'], 'non-utf8': [b'\xff'],
                         'mismatch': [checksum, b'replaced upstream archive']}[failure]
                responses = iter(first + [checksum, bundle])
                def response(url, **kwargs):
                    self.assertEqual(archive.read_bytes(), b'previous verified archive')
                    return io.BytesIO(next(responses))
                with patch.object(auto.urllib.request, 'urlopen', side_effect=response) as network, \
                        patch.object(auto.time, 'monotonic', return_value=0), patch.object(auto.time, 'sleep') as sleep:
                    auto.fetch_runtime(self.config, 'http://artifacts/runtime.tar.gz', work)
                sleep.assert_called_once_with(5)
                self.assertEqual(network.call_count, len(first) + 2)
                self.assertEqual(archive.read_bytes(), bundle)
                self.assertFalse((work / 'runtime.tar.gz.partial').exists())
                auto.verify_runtime(self.config, work / 'runtime')

    def test_worker_checksum_and_download_failures_stop_at_deadline_and_preserve_old_files(self):
        checksum = (self.root / 'runtime.tar.gz.sha256').read_bytes()
        for failure in ('malformed', 'mismatch', 'interrupted'):
            with self.subTest(failure=failure):
                work = self.root / failure
                work.mkdir()
                archive = work / 'runtime.tar.gz'
                archive.write_bytes(b'previous verified archive')
                runtime = work / 'runtime'
                runtime.mkdir()
                (runtime / 'existing').write_bytes(b'previous runtime')
                if failure == 'malformed':
                    responses = [io.BytesIO(b'not-a-checksum')]
                elif failure == 'mismatch':
                    responses = [io.BytesIO(checksum), io.BytesIO(b'partial archive')]
                else:
                    interrupted = MagicMock()
                    interrupted.__enter__.return_value = interrupted
                    interrupted.read.side_effect = [b'partial archive', OSError('transfer interrupted')]
                    responses = [io.BytesIO(checksum), interrupted]
                with patch.object(auto.urllib.request, 'urlopen', side_effect=responses), \
                        patch.object(auto.time, 'monotonic', side_effect=[0, 10800]), patch.object(auto.time, 'sleep') as sleep, \
                        self.assertRaisesRegex(RuntimeError, 'Timed out waiting for the prepared runtime bundle'):
                    auto.fetch_runtime(self.config, 'http://artifacts/runtime.tar.gz', work)
                sleep.assert_not_called()
                self.assertEqual(archive.read_bytes(), b'previous verified archive')
                self.assertEqual((runtime / 'existing').read_bytes(), b'previous runtime')
                self.assertFalse((work / 'runtime.tar.gz.partial').exists())

    def test_failed_distributed_check_stops_local_rpc_and_prevents_download(self):
        child = MagicMock()
        with patch.object(auto.subprocess, 'Popen', return_value=child), patch.object(auto, 'wait_endpoints') as wait, \
                patch.object(auto.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, 'qualify')) as run, \
                patch.object(auto, 'model_download') as download, self.assertRaises(subprocess.CalledProcessError):
            auto.qualify(self.config, self.root)
        wait.assert_called_once_with(['127.0.0.1:50052', 'worker:50052'])
        self.assertEqual(run.call_count, 1)
        child.terminate.assert_called_once()
        child.wait.assert_called_once_with(timeout=15)
        download.assert_not_called()

    def test_distributed_checks_finish_and_local_gpu_is_released_before_model_starts(self):
        child = MagicMock()
        with patch.object(auto.subprocess, 'Popen', return_value=child), patch.object(auto, 'wait_endpoints'), \
                patch.object(auto.subprocess, 'run') as run:
            auto.qualify(self.config, self.root)
        self.assertEqual([pathlib.Path(call.args[0][0]).name for call in run.call_args_list], ['test-rpc-multi-server', 'rpc-gpu-check'])
        self.assertTrue(all(call.args[0][-2:] == ['127.0.0.1:50052', 'worker:50052'] for call in run.call_args_list))
        child.terminate.assert_called_once()
        child.wait.assert_called_once()

    def test_reuse_verifies_entire_pinned_model_without_network(self):
        model = self.root / 'model'
        model.mkdir()
        data = b'pinned weight data'
        (model / 'weight.gguf').write_bytes(data)
        self.config['lock'] = {'model': 'example/model', 'revision': 'd' * 40, 'weightFileBytes': len(data), 'quantization': 'Q',
                               'files': [{'rfilename': 'weight.gguf', 'size': len(data), 'lfs': {'sha256': hashlib.sha256(data).hexdigest()}}]}
        marker = model / 'download-complete.json'
        marker.write_text(json.dumps({'result': 'PASS', 'model': 'example/model', 'revision': 'd' * 40, 'verifiedBytes': len(data)}))
        with patch.object(auto, 'CHECKS', CHART / 'files'), patch.object(auto.urllib.request, 'urlopen') as network, \
                patch.object(auto.shutil, 'disk_usage', side_effect=AssertionError('Reuse allocates no checkpoint space')):
            auto.model_download(self.config, self.root)
            network.assert_not_called()
        auto.emit.assert_called_with('download_pass', model='example/model', revision='d' * 40, reused=True)
        (model / 'weight.gguf').write_bytes(b'X' * len(data))
        with patch.object(auto, 'CHECKS', CHART / 'files'), patch.object(auto.urllib.request, 'urlopen') as network, \
                self.assertRaisesRegex(AssertionError, 'checksum mismatch'):
            auto.model_download(self.config, self.root)
        network.assert_not_called()

    def test_reuse_missing_or_mismatched_marker_stops_before_network(self):
        self.config['lock'] = {'model': 'example/model', 'revision': 'd' * 40, 'weightFileBytes': 1, 'files': []}
        model = self.root / 'model'
        model.mkdir()
        marker = model / 'download-complete.json'
        for contents in (None, {'result': 'PASS', 'model': 'other', 'revision': 'd' * 40, 'verifiedBytes': 1}):
            if contents:
                marker.write_text(json.dumps(contents))
            with self.subTest(contents=contents), patch.object(auto.urllib.request, 'urlopen') as network, \
                    self.assertRaises((RuntimeError, FileNotFoundError)):
                auto.model_download(self.config, self.root)
            network.assert_not_called()


if __name__ == '__main__':
    unittest.main()
