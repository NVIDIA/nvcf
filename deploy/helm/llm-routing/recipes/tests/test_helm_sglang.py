# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import fcntl
import importlib.util
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

import yaml
from test_recipes import ROOT, all_profile_releases, runtime
import test_runtime_and_gateway as runtime_tests

CHART = ROOT/'charts/sglang'
spec = importlib.util.spec_from_file_location('sglang_placement', CHART/'files/placement.py')
placement = importlib.util.module_from_spec(spec)
spec.loader.exec_module(placement)


class HelmSGLangTests(unittest.TestCase):
    def test_placement_sync_reports_drift_and_repairs_gguf_chart_copy(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            shutil.copy2(ROOT / 'sync-placement.py', root / 'sync-placement.py')
            source = root / 'charts/sglang/files/placement.py'
            source.parent.mkdir(parents=True)
            shutil.copy2(CHART / 'files/placement.py', source)
            target = root / 'charts/gguf-backend/files/placement.py'
            target.parent.mkdir(parents=True)
            target.write_text('stale copy')
            command = [sys.executable, str(root / 'sync-placement.py')]
            stale = subprocess.run(command + ['--check'], text=True, capture_output=True)
            self.assertNotEqual(stale.returncode, 0)
            self.assertIn('Stale GGUF placement copy', stale.stderr)
            self.assertEqual(target.read_text(), 'stale copy')
            subprocess.run(command, check=True, capture_output=True)
            subprocess.run(command + ['--check'], check=True, capture_output=True)
            self.assertEqual(target.read_bytes(), source.read_bytes())

    def values(self, recipe='qwen3.8-27b'):
        return {'recipe': recipe, 'nodes': ['gpu-node-1'], 'storageClassName': 'local-path',
                'runtimeClassName': 'nvidia', 'sharedCAConfigMap': 'routing-ca'}

    def render(self, values, success=True, release='test-model', upgrade=False):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'values.json'
            path.write_text(json.dumps(values))
            command = ['helm', 'template', release, str(CHART), '--namespace', 'test-stack', '-f', str(path)]
            result = subprocess.run(command + (['--is-upgrade'] if upgrade else []),
                                    capture_output=True, text=True)
        if not success:
            self.assertNotEqual(result.returncode, 0)
            return result.stderr
        self.assertEqual(result.returncode, 0, result.stderr)
        return [doc for doc in yaml.safe_load_all(result.stdout) if doc]

    def configuration(self, docs):
        return json.loads(next(doc for doc in docs if doc['kind'] == 'ConfigMap')['data']['config.json'])

    def test_bundled_profiles_match_catalog_pins_and_workload_envelopes(self):
        bundled = json.loads((CHART/'files/profiles.json').read_text())
        catalog = {model['id']: model for model in json.loads((ROOT/'catalog.json').read_text())['models']}
        self.assertEqual(set(bundled), {'qwen3.8-27b', 'qwen3.8-27b-nvfp4', 'qwen3.8-flash-next'})
        for name, model in bundled.items():
            for field in ('id', 'repository', 'revision', 'image', 'profiles'):
                self.assertEqual(model[field], catalog[name][field])

    def test_small_values_install_both_pinned_profiles_with_automatic_startup(self):
        pins = json.loads((CHART/'files/profiles.json').read_text())
        for name in ('qwen3.8-27b', 'qwen3.8-27b-nvfp4'):
            with self.subTest(recipe=name):
                docs = self.render(self.values(name))
                config = self.configuration(docs)
                self.assertTrue(config['automatic'])
                self.assertEqual(config['model']['revision'], pins[name]['revision'])
                self.assertEqual(config['profile'], pins[name]['profiles'][0])
                self.assertFalse(any(doc['kind'] == 'Job' for doc in docs))
                deployment = next(doc for doc in docs if doc['kind'] == 'Deployment')
                self.assertEqual(deployment['spec']['strategy'], {'type': 'Recreate'})
                pod = deployment['spec']['template']['spec']
                main = pod['containers'][0]
                self.assertEqual(main['image'], pins[name]['image'])
                self.assertEqual(main['command'], ['python3', '-u', '/recipe/runtime.py', 'automatic', '0'])
                for field in ('requests', 'limits'):
                    self.assertEqual(main['resources'][field]['nvidia.com/gpu'], '1')
                self.assertEqual(main['startupProbe']['httpGet']['path'], '/health')
                self.assertEqual(main['readinessProbe']['httpGet']['path'], '/health')
                self.assertEqual(pod['initContainers'][0]['command'], ['python3', '-u', '/recipe/placement.py'])
                self.assertFalse(pod['automountServiceAccountToken'])
                self.assertNotIn('cluster-access', [mount['name'] for mount in main['volumeMounts']])
                self.assertEqual(pod['affinity']['nodeAffinity']['requiredDuringSchedulingIgnoredDuringExecution']['nodeSelectorTerms'][0]['matchFields'][0]['values'], ['gpu-node-1'])
                endpoint = next(doc for doc in docs if doc['kind'] == 'InferenceEndpoint')
                self.assertEqual(endpoint['spec']['modelName'], name)

    def test_each_automatic_recipe_installs_without_a_values_file_or_cache_flag(self):
        for recipe in ('qwen3.8-27b', 'qwen3.8-27b-nvfp4'):
            with self.subTest(recipe=recipe):
                result = subprocess.run(['helm', 'template', 'test-model', str(CHART),
                                         '--namespace', 'test-stack', '--set', 'recipe=' + recipe,
                                         '--set', 'nodes[0]=selected-node'], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                docs = [doc for doc in yaml.safe_load_all(result.stdout) if doc]
                config = self.configuration(docs)
                self.assertEqual(config['model']['id'], recipe)
                self.assertFalse(config['reuseCaches'])
                self.assertEqual(config['sharedCAConfigMap'], 'llm-gateway-stack-ca')
                self.assertEqual(config['storageClassName'], 'local-path')
                pod = next(doc for doc in docs if doc['kind'] == 'Deployment')['spec']['template']['spec']
                self.assertEqual(pod['runtimeClassName'], 'nvidia')
                self.assertEqual(next(v for v in pod['volumes'] if v['name'] == 'cache')['persistentVolumeClaim']['claimName'],
                                 'test-model-cache-0')

    def test_automatic_mode_uses_bundled_pins_even_with_legacy_overrides(self):
        values = self.values()
        values.update(image='untrusted:latest', model={'id': 'wrong', 'revision': 'main'}, profile={'memoryGiB': 1})
        docs = self.render(values)
        config = self.configuration(docs)
        self.assertEqual(config['model']['id'], 'qwen3.8-27b')
        self.assertGreater(config['profile']['memoryGiB'], 1)
        self.assertNotIn('untrusted', next(doc for doc in docs if doc['kind'] == 'Deployment')['spec']['template']['spec']['containers'][0]['image'])

    def test_invalid_selection_and_missing_explicit_placement_fail_render(self):
        cases = [({'nodes': []}, 'one explicit node'), ({'nodes': ['a', 'b']}, 'one explicit node'),
                 ({'nodes': ['bad/name']}, 'Kubernetes node name'), ({'recipe': 'missing-model'}, 'bundled recipe'),
                 ({'profileName': 'spark-nvfp4'}, 'does not belong'), ({'sharedCAConfigMap': ''}, 'sharedCAConfigMap is required'),
                 ({'contextLength': 100000}, 'Workload exceeds'), ({'reuseCaches': 'true'}, 'must be a boolean'),
                 ({'mode': 'phased'}, 'explicit phase'), ({'cache': {'existingClaim': '../claim'}}, 'PVC name')]
        for changes, error in cases:
            with self.subTest(changes=changes):
                self.assertIn(error, self.render(dict(self.values(), **changes), success=False))

    def test_only_exact_node_and_cache_get_permissions_are_created(self):
        docs = self.render(self.values())
        cluster = next(doc for doc in docs if doc['kind'] == 'ClusterRole')
        role = next(doc for doc in docs if doc['kind'] == 'Role')
        self.assertEqual(cluster['rules'], [{'apiGroups': [''], 'resources': ['nodes'], 'resourceNames': ['gpu-node-1'], 'verbs': ['get']}])
        self.assertEqual(role['rules'], [{'apiGroups': [''], 'resources': ['persistentvolumeclaims'], 'resourceNames': ['test-model-cache-0'], 'verbs': ['get']}])
        self.assertFalse(any(doc['kind'] == 'Secret' for doc in docs))
        self.assertEqual([doc['metadata']['name'] for doc in docs if doc['kind'] == 'ConfigMap'], ['test-model-runtime'])

    def test_model_install_and_upgrade_cannot_replace_shared_release(self):
        for upgrade in (False, True):
            with self.subTest(upgrade=upgrade):
                error = self.render(self.values(), success=False, release='llm-stack', upgrade=upgrade)
                self.assertIn('llm-stack is reserved for the shared stack', error)

    def test_retained_claim_is_mounted_without_ownership_or_network_downloads(self):
        values = dict(self.values(), reuseCaches=True, cache={'existingClaim': 'previous-model-cache'})
        docs = self.render(values)
        self.assertFalse(any(doc['kind'] == 'PersistentVolumeClaim' for doc in docs))
        pod = next(doc for doc in docs if doc['kind'] == 'Deployment')['spec']['template']['spec']
        self.assertEqual(next(volume for volume in pod['volumes'] if volume['name'] == 'cache')['persistentVolumeClaim']['claimName'], 'previous-model-cache')
        env = {value['name']: value.get('value') for value in pod['containers'][0]['env']}
        self.assertEqual(env['HF_HUB_OFFLINE'], '1')
        self.assertEqual(env['TRANSFORMERS_OFFLINE'], '1')
        self.assertEqual(self.configuration(docs)['claimName'], 'previous-model-cache')

    def test_automatic_suspend_withdraws_endpoint_and_keeps_cache_service_and_pins(self):
        original = self.render(self.values())
        suspended = self.render(dict(self.values(), suspended=True))
        resumed = self.render(dict(self.values(), suspended=False))
        self.assertEqual(original, resumed)
        self.assertEqual(sum(doc['kind'] == 'InferenceEndpoint' for doc in original), 1)
        self.assertFalse(any(doc['kind'] == 'InferenceEndpoint' for doc in suspended))
        for docs in (original, suspended):
            deployment = next(doc for doc in docs if doc['kind'] == 'Deployment')
            self.assertEqual(deployment['spec'].pop('replicas'), 0 if docs is suspended else 1)
            pvc = next(doc for doc in docs if doc['kind'] == 'PersistentVolumeClaim')
            self.assertEqual(pvc['metadata']['annotations']['helm.sh/resource-policy'], 'keep')
        self.assertEqual([doc for doc in original if doc['kind'] != 'InferenceEndpoint'], suspended)

    def test_legacy_serve_suspend_keeps_endpoint_for_existing_phase_workflows(self):
        for release in all_profile_releases():
            with self.subTest(profile=release['profile']):
                docs = self.render(dict(release['values'], phase='serve', suspended=True, register=True))
                self.assertEqual(sum(doc['kind'] == 'InferenceEndpoint' for doc in docs), 1)
                self.assertTrue(all(doc['spec']['replicas'] == 0 for doc in docs if doc['kind'] == 'Deployment'))

    def test_profile_rollback_restores_pinned_config_and_retains_cache_identity(self):
        first = self.render(self.values())
        second = self.render(self.values('qwen3.8-27b-nvfp4'))
        restored = self.render(self.values())
        self.assertEqual(first, restored)
        self.assertNotEqual(self.configuration(first)['model']['revision'], self.configuration(second)['model']['revision'])
        claims = [[doc for doc in docs if doc['kind'] == 'PersistentVolumeClaim'] for docs in (first, second, restored)]
        self.assertEqual(claims[0], claims[1])
        self.assertEqual(claims[1], claims[2])

    def test_explicit_legacy_phases_remain_available_for_all_profiles(self):
        for release in all_profile_releases():
            for phase in ('qualify', 'download', 'serve'):
                with self.subTest(profile=release['profile'], phase=phase):
                    docs = self.render(dict(release['values'], phase=phase))
                    self.assertFalse(self.configuration(docs)['automatic'])
                    self.assertFalse(any(doc['kind'] in ('ClusterRole', 'Role', 'ServiceAccount') for doc in docs))
                    workload = next(doc for doc in docs if doc['kind'] in ('Deployment', 'Job'))
                    self.assertEqual(workload['spec']['template']['spec']['containers'][0]['command'][3], phase)


class PlacementTests(unittest.TestCase):
    def setUp(self):
        self.config = {'targets': [{'node': 'node-one'}], 'profile': {'hardware': {'os': 'linux', 'architecture': 'arm64',
            'gpuProducts': ['NVIDIA-GB10'], 'gpuCount': 1}, 'cacheGiB': 80},
            'claimName': 'model-cache', 'namespace': 'models', 'storageClassName': 'local-path'}
        self.node = {'metadata': {'name': 'node-one', 'uid': 'node-uid', 'labels': {'kubernetes.io/os': 'linux',
            'kubernetes.io/arch': 'arm64', 'nvidia.com/gpu.product': 'NVIDIA-GB10'}}, 'spec': {},
            'status': {'conditions': [{'type': 'Ready', 'status': 'True'}], 'capacity': {'nvidia.com/gpu': '1'}, 'allocatable': {'nvidia.com/gpu': '1'}}}
        self.claim = {'metadata': {'name': 'model-cache', 'namespace': 'models', 'uid': 'claim-uid'},
            'spec': {'accessModes': ['ReadWriteOnce'], 'storageClassName': 'local-path', 'volumeName': 'model-pv'},
            'status': {'phase': 'Bound', 'capacity': {'storage': '80Gi'}}}

    def test_valid_exclusive_gpu_and_bound_cache_return_identities(self):
        self.assertEqual(placement.validate_placement(self.config, self.node, self.claim),
                         {'node': 'node-one', 'nodeUID': 'node-uid', 'claim': 'model-cache', 'claimUID': 'claim-uid'})

    def test_non_mig_capable_full_gpu_accepts_device_plugin_single_strategy(self):
        self.node['metadata']['labels'].update({'nvidia.com/mig.capable': 'false', 'nvidia.com/mig.strategy': 'single'})
        placement.validate_placement(self.config, self.node, self.claim)
        self.node['status']['allocatable']['nvidia.com/mig-1g.10gb'] = '1'
        with self.assertRaisesRegex(RuntimeError, 'MIG disabled'):
            placement.validate_placement(self.config, self.node, self.claim)

    def test_shared_mig_wrong_architecture_and_changed_gpu_capacity_are_rejected(self):
        changes = [lambda n: n['metadata']['labels'].update({'nvidia.com/gpu.sharing-strategy': 'time-slicing'}),
                   lambda n: n['metadata']['labels'].update({'nvidia.com/gpu.replicas': '4'}),
                   lambda n: n['metadata']['labels'].update({'nvidia.com/mig.strategy': 'mixed'}),
                   lambda n: n['status']['allocatable'].update({'nvidia.com/mig-1g.10gb': '1'}),
                   lambda n: n['status']['allocatable'].update({'nvidia.com/gpu': '2'}),
                   lambda n: n['metadata']['labels'].update({'kubernetes.io/arch': 'amd64'}),
                   lambda n: n['metadata']['labels'].update({'nvidia.com/gpu.product': 'another-device'}),
                   lambda n: n['spec'].update(unschedulable=True),
                   lambda n: n['spec'].update(taints=[{'effect': 'NoExecute'}])]
        for index, change in enumerate(changes):
            node = copy.deepcopy(self.node)
            change(node)
            with self.subTest(case=index), self.assertRaises(RuntimeError):
                placement.validate_placement(self.config, node, self.claim)

    def test_unbound_wrong_class_small_or_differently_placed_cache_is_rejected(self):
        changes = [lambda p: p['status'].update(phase='Pending'), lambda p: p['spec'].update(storageClassName='other'),
                   lambda p: p['status']['capacity'].update(storage='79Gi'), lambda p: p['spec'].update(accessModes=['ReadWriteMany']),
                   lambda p: p['metadata'].update(namespace='other'),
                   lambda p: p['metadata'].update(annotations={'volume.kubernetes.io/selected-node': 'other-node'})]
        for index, change in enumerate(changes):
            claim = copy.deepcopy(self.claim)
            change(claim)
            with self.subTest(case=index), self.assertRaises(RuntimeError):
                placement.validate_placement(self.config, self.node, claim)

    def test_preflight_validates_shared_ca_and_reads_only_selected_node_and_cache(self):
        with patch.dict(os.environ, POD_NODE_NAME='node-one'), patch.object(placement.ssl, 'create_default_context') as tls, \
             patch.object(placement, 'cluster_object', side_effect=[self.node, self.claim]) as read, patch.object(placement, 'log'):
            self.assertEqual(placement.preflight(self.config), 0)
        self.assertEqual([call.args[0] for call in read.call_args_list], ['/api/v1/nodes/node-one', '/api/v1/namespaces/models/persistentvolumeclaims/model-cache'])
        tls.assert_called_once_with(cafile='/shared-ca/ca.crt')


class AutomaticRuntimeTests(unittest.TestCase):
    def setUp(self):
        fixture = runtime_tests.CacheReuseTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        self.fixture = fixture
        self.cache = fixture.cache
        self.config = copy.deepcopy(fixture.config)
        self.config['profile'].update(nodes=1, memoryGiB=4, flags=[], offload=False)
        self.config.update(contextLength=128, concurrency=1, ports={'http': 30000})

    def test_existing_cache_is_validated_locally_and_partial_shards_fail_before_serve(self):
        with patch.dict('sys.modules', {'huggingface_hub': None}), patch.object(runtime, 'log'):
            self.assertEqual(runtime.prepare_checkpoint(self.config, self.cache), self.fixture.snapshot)
            self.fixture.blob.write_bytes(b'truncated')
            with self.assertRaisesRegex(RuntimeError, 'checksum differs'):
                runtime.prepare_checkpoint(self.config, self.cache)

    def test_default_automatically_reuses_complete_cache_without_network_or_free_disk(self):
        self.config['reuseCaches'] = False
        with patch.dict('sys.modules', {'huggingface_hub': None}), \
             patch.object(runtime.shutil, 'disk_usage', side_effect=AssertionError('No download space needed')), \
             patch.object(runtime, 'log'):
            self.assertEqual(runtime.prepare_checkpoint(self.config, self.cache), self.fixture.snapshot)

    def test_complete_unmarked_download_is_reused_without_network(self):
        self.config['reuseCaches'] = False
        self.fixture.marker.unlink()
        with patch.dict('sys.modules', {'huggingface_hub': None}), \
             patch.object(runtime.shutil, 'disk_usage', side_effect=AssertionError('No download space needed')), \
             patch.object(runtime, 'log'):
            self.assertEqual(runtime.prepare_checkpoint(self.config, self.cache), self.fixture.snapshot)
        self.assertEqual(json.loads(self.fixture.marker.read_text())['revision'], self.config['model']['revision'])

    def test_existing_legacy_marker_is_preserved_for_exact_previous_runtime_rollback(self):
        original = self.fixture.marker.read_bytes()
        with patch.dict('sys.modules', {'huggingface_hub': None}), patch.object(runtime, 'log'):
            runtime.prepare_checkpoint(self.config, self.cache)
        self.assertEqual(self.fixture.marker.read_bytes(), original)
        self.assertEqual(self.fixture.marker.read_text().strip(), self.config['model']['repository'])

    def test_offline_missing_cache_never_imports_a_downloader(self):
        self.fixture.marker.unlink()
        with patch.dict('sys.modules', {'huggingface_hub': None}), self.assertRaisesRegex(RuntimeError, 'marker'):
            runtime.prepare_checkpoint(self.config, self.cache)

    def test_download_is_validated_before_atomic_completion_marker(self):
        self.config['reuseCaches'] = False
        self.fixture.marker.unlink()
        hub = Mock()
        config_file = self.fixture.snapshot / 'config.json'
        original_config = config_file.read_bytes()
        config_file.unlink()
        hub.snapshot_download.side_effect = lambda **kwargs: config_file.write_bytes(original_config)
        with patch.dict('sys.modules', {'huggingface_hub': hub}), patch.object(runtime.shutil, 'disk_usage', return_value=Mock(free=2000 * runtime.GIB)), patch.object(runtime, 'log'):
            self.assertEqual(runtime.prepare_checkpoint(self.config, self.cache), self.fixture.snapshot)
        hub.snapshot_download.assert_called_once_with(repo_id='example/model', revision='a'*40, cache_dir=str(self.cache/'huggingface/hub'))
        self.assertEqual(json.loads(self.fixture.marker.read_text())['revision'], 'a'*40)
        self.fixture.marker.unlink()
        self.fixture.blob.write_bytes(b'truncated')
        with patch.dict('sys.modules', {'huggingface_hub': hub}), patch.object(runtime.shutil, 'disk_usage', return_value=Mock(free=2000 * runtime.GIB)), self.assertRaises(RuntimeError):
            runtime.prepare_checkpoint(self.config, self.cache)
        self.assertFalse(self.fixture.marker.exists())

    def test_automatic_qualifies_then_prepares_then_serves_with_offline_environment(self):
        events = []
        def serve(argv, config):
            self.assertEqual(os.environ['HF_HUB_OFFLINE'], '1')
            self.assertEqual(os.environ['TRANSFORMERS_OFFLINE'], '1')
            events.append('serve')
            return 0
        torch = Mock()
        with patch.dict(os.environ), patch.dict('sys.modules', {'torch': torch}), patch.object(runtime, 'host_memory', return_value=8 * runtime.GIB), \
             patch.object(runtime, 'qualify', side_effect=lambda *args: events.append('qualify')), \
             patch.object(runtime, 'prepare_checkpoint', side_effect=lambda *args: events.append('cache') or self.fixture.snapshot), \
             patch.object(runtime, 'supervise', side_effect=serve), patch.object(runtime, 'log'):
            self.assertEqual(runtime.automatic(self.config, 0, self.cache), 0)
        self.assertEqual(events, ['qualify', 'cache', 'serve'])
        torch.cuda.empty_cache.assert_called_once_with()

    def test_failure_or_cache_contention_never_starts_model_server(self):
        with (self.cache/'.recipe.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with patch.object(runtime, 'qualify') as qualify, patch.object(runtime, 'supervise') as serve, self.assertRaisesRegex(RuntimeError, 'Another model process'):
                runtime.automatic(self.config, 0, self.cache)
            qualify.assert_not_called()
            serve.assert_not_called()
        with patch.object(runtime, 'host_memory', return_value=8 * runtime.GIB), patch.object(runtime, 'qualify', side_effect=RuntimeError('qualification failed')), \
             patch.object(runtime, 'prepare_checkpoint') as cache, patch.object(runtime, 'supervise') as serve, self.assertRaisesRegex(RuntimeError, 'qualification failed'):
            runtime.automatic(self.config, 0, self.cache)
        cache.assert_not_called()
        serve.assert_not_called()


if __name__ == '__main__':
    unittest.main()
