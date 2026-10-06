# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
import pathlib
import subprocess
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('cluster_setup', HERE/'cluster_setup.py')
setup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(setup)

GLM = json.loads((HERE/'glm-5.3/recipe.json').read_text())
GLM['lock'] = json.loads((HERE/'glm-5.3/model.lock.json').read_text())
GB10 = {'name': 'NVIDIA GB10', 'computeCapability': '12.1', 'memoryGiB': 121.6, 'unifiedMemory': True, 'cudaArchitectures': None}
GB300 = {'name': 'NVIDIA GB300', 'computeCapability': '10.3', 'memoryGiB': 268.0, 'unifiedMemory': False, 'cudaArchitectures': None}


def node(name, control=False):
    labels = {'kubernetes.io/hostname': name, 'kubernetes.io/arch': 'arm64', 'kubernetes.io/os': 'linux'}
    if control:
        labels['node-role.kubernetes.io/control-plane'] = 'true'
    return {'metadata': {'name': name, 'labels': labels}, 'spec': {},
            'status': {'conditions': [{'type': 'Ready', 'status': 'True'}],
                       'allocatable': {'nvidia.com/gpu': '1'},
                       'nodeInfo': {'kubeletVersion': 'v1.36.1+k3s1', 'containerRuntimeVersion': 'containerd://2.3.4-k3s1.36'}}}


def gpu_pod(name, phase='Running', init=False):
    spec = {'nodeName': name, 'containers': []}
    spec['initContainers' if init else 'containers'] = [{'resources': {'requests': {'nvidia.com/gpu': '1'}}}]
    return {'spec': spec, 'status': {'phase': phase}}


class ClusterSetupTests(unittest.TestCase):
    def setUp(self):
        self.resources = {
            'namespaces': [], 'crds': [],
            'nodes': [node('routing', True), node('gpu-b'), node('gpu-a')],
            'pods': [],
            'storageclasses': [{'metadata': {'name': 'local-storage', 'annotations': {'storageclass.kubernetes.io/is-default-class': 'true'}}}],
            'runtimeclasses': [{'metadata': {'name': 'nvidia'}, 'handler': 'nvidia'},
                               {'metadata': {'name': 'nvidia-experimental'}, 'handler': 'nvidia-experimental'}],
        }
        self.calls = []
        self.probed = []
        self.gpus = {}
        self.template = json.loads((HERE/'config.example.json').read_text())

    def probe(self, names):
        self.probed.append(list(names))
        return {name: copy.deepcopy(self.gpus.get(name, GB10)) for name in names}

    def query(self, command, **kwargs):
        self.calls.append(command)
        self.assertEqual(command[:4], ['kubectl', '--context', 'test-context', 'get'])
        self.assertEqual(command[-2:], ['-o', 'json'])
        self.assertNotIn('--raw', command)
        self.assertEqual(kwargs['timeout'], 30)
        self.assertEqual(kwargs['stderr'], subprocess.PIPE)
        if command[4] == 'pods':
            self.assertIn('--all-namespaces', command)
        return json.dumps({'items': copy.deepcopy(self.resources[command[4]])})

    def discover(self, namespace=None):
        with patch.object(setup.subprocess, 'check_output', side_effect=self.query):
            return setup.discover_config('test-context', namespace, GLM, self.probe)

    def test_unambiguous_setup_uses_live_metadata_and_public_pins(self):
        config = self.discover()
        self.assertEqual(config['nodes'], {'control': 'routing', 'model': ['gpu-a', 'gpu-b']})
        self.assertEqual(config['gpu'], GB10)
        self.assertEqual(config['recipe'], 'glm-5.3')
        self.assertEqual(self.probed, [['gpu-a', 'gpu-b']])
        self.assertEqual(config['context'], 'test-context')
        self.assertEqual(config['storageClass'], 'local-storage')
        self.assertEqual(config['runtimeClass'], 'nvidia')
        self.assertEqual(config['runtimeImage'], self.template['runtimeImage'])
        self.assertEqual(config['tls'], self.template['tls'])
        self.assertIsNone(config['apiKeyFile'])
        self.assertEqual(config['images']['prefix'], 'localhost/' + config['namespace'])
        self.assertRegex(config['images']['tag'], r'^dev-\d{14}-[a-f0-9]{6}$')
        self.assertEqual(config['images']['pullPolicy'], 'Never')
        self.assertEqual(config['images']['pullSecrets'], [])
        self.assertEqual(config['containerd']['nodeNames'], ['gpu-a', 'gpu-b', 'routing'])
        self.assertEqual(config['containerd']['archiveNode'], 'routing')
        self.assertNotIn('archiveDirectory', config['containerd'])
        self.assertEqual(json.loads((HERE/'config.example.json').read_text()), self.template)
        self.assertEqual({cmd[4] for cmd in self.calls}, set(self.resources))

    def test_shared_control_plane_labels_use_only_two_idle_gpus(self):
        for item in self.resources['nodes']:
            item['metadata']['labels']['node-role.kubernetes.io/control-plane'] = 'true'
        self.resources['pods'] = [gpu_pod('routing')]
        self.assertEqual(self.discover()['nodes'], {'control': 'routing', 'model': ['gpu-a', 'gpu-b']})

    def test_three_idle_gpus_with_shared_labels_choose_stable_placement(self):
        for item in self.resources['nodes']:
            item['metadata']['labels']['node-role.kubernetes.io/control-plane'] = 'true'
        config = self.discover()
        self.assertEqual(config['nodes'], {'control': 'routing', 'model': ['gpu-a', 'gpu-b']})
        self.resources['nodes'].reverse()
        self.assertEqual(self.discover()['nodes'], config['nodes'])

    def test_more_gpu_workers_do_not_require_an_extra_manual_choice(self):
        self.resources['nodes'].append(node('gpu-c'))
        config = self.discover()
        self.assertEqual(config['nodes'], {'control': 'routing', 'model': ['gpu-a', 'gpu-b']})
        self.assertEqual(config['containerd']['nodeNames'], ['gpu-a', 'gpu-b', 'gpu-c', 'routing'])

    def test_missing_role_labels_can_use_two_idle_gpus_and_non_gpu_routing(self):
        self.resources['nodes'][0]['metadata']['labels'].pop('node-role.kubernetes.io/control-plane')
        self.resources['nodes'][0]['status']['allocatable']['nvidia.com/gpu'] = '0'
        self.assertEqual(self.discover()['nodes']['control'], 'routing')

    def test_gpu_requests_in_normal_or_init_containers_block_selection(self):
        self.resources['nodes'][0]['status']['allocatable']['nvidia.com/gpu'] = '0'
        for init in (False, True):
            with self.subTest(init=init):
                self.resources['pods'] = [gpu_pod('gpu-a', init=init)]
                with self.assertRaisesRegex(setup.ClusterSetupError, 'needs 2 idle NVIDIA GB10 nodes'):
                    self.discover()

    def test_completed_gpu_pods_do_not_block(self):
        self.resources['pods'] = [gpu_pod('gpu-a', 'Succeeded'), gpu_pod('gpu-b', 'Failed')]
        self.assertEqual(self.discover()['nodes']['model'][0], 'gpu-a')

    def test_pending_and_terminating_gpu_allocations_still_block(self):
        self.resources['nodes'][0]['status']['allocatable']['nvidia.com/gpu'] = '0'
        for phase in ('Pending', 'Running'):
            with self.subTest(phase=phase):
                pod = gpu_pod('gpu-a', phase)
                pod['metadata'] = {'deletionTimestamp': 'pending-termination'}
                self.resources['pods'] = [pod]
                with self.assertRaises(setup.ClusterSetupError):
                    self.discover()

    def test_dra_claims_are_conservatively_busy(self):
        self.resources['nodes'][0]['status']['allocatable']['nvidia.com/gpu'] = '0'
        self.resources['pods'] = [{'spec': {'nodeName': 'gpu-a', 'resourceClaims': [{'name': 'gpu'}]}, 'status': {'phase': 'Running'}}]
        with self.assertRaises(setup.ClusterSetupError):
            self.discover()

    def test_unready_cordoned_tainted_pressure_nodes_are_not_selected(self):
        changes = [lambda n: n['status']['conditions'][0].update(status='False'),
                   lambda n: n['spec'].update(unschedulable=True),
                   lambda n: n['spec'].update(taints=[{'key': 'reserved', 'effect': 'NoSchedule'}]),
                   lambda n: n['status']['conditions'].append({'type': 'MemoryPressure', 'status': 'True'}),
                   lambda n: n['metadata']['labels'].update({'kubernetes.io/arch': 'amd64'})]
        for index, change in enumerate(changes):
            with self.subTest(change=index):
                self.resources['nodes'][1] = node('gpu-b')
                change(self.resources['nodes'][1])
                config = self.discover()
                self.assertNotIn('gpu-b', [config['nodes']['control'], *config['nodes']['model']])

    def test_gb300_pair_serves_from_one_node_and_routes_from_the_control_plane(self):
        self.resources['nodes'] = [node('server', True), node('agent')]
        self.gpus = {'server': GB300, 'agent': GB300}
        config = self.discover()
        self.assertEqual(config['nodes'], {'control': 'server', 'model': ['agent']})
        self.assertEqual(config['gpu'], GB300)
        self.assertEqual(self.probed, [['agent', 'server']])

    def test_two_gb10_nodes_split_the_model_and_share_routing_with_the_leader(self):
        self.resources['nodes'] = [node('spark-a', True), node('spark-b')]
        config = self.discover()
        self.assertEqual(config['nodes'], {'control': 'spark-b', 'model': ['spark-b', 'spark-a']})

    def test_mixed_gpu_types_are_not_combined(self):
        self.gpus = {'gpu-b': GB300}
        with self.assertRaisesRegex(setup.ClusterSetupError, 'Mixed GPU types'):
            self.discover()

    def test_model_that_fits_nowhere_is_rejected_with_its_requirement(self):
        small = dict(GB300, memoryGiB=80.0)
        self.gpus = {name: small for name in ('gpu-a', 'gpu-b', 'routing')}
        with self.assertRaisesRegex(setup.ClusterSetupError, 'does not fit on 2 nodes'):
            self.discover()

    def test_custom_unused_namespace_needs_no_prior_private_settings(self):
        self.resources['namespaces'] = [{'metadata': {'name': self.template['namespace']}}]
        config = self.discover('new-installation')
        self.assertEqual(config['namespace'], 'new-installation')
        self.assertEqual(config['clusterId'], 'new-installation')
        with self.assertRaisesRegex(setup.ClusterSetupError, 'already exists'):
            self.discover()

    def test_existing_crd_blocks_even_when_other_owner_is_not_named(self):
        self.resources['crds'] = [{'metadata': {'name': 'inferenceendpoints.pylon.nvidia.com'}}]
        with self.assertRaisesRegex(setup.ClusterSetupError, 'Check its owner'):
            self.discover()
        self.assertNotIn('nodes', [cmd[4] for cmd in self.calls])

    def test_non_k3s_containerd_does_not_guess_socket(self):
        for field, value in [('kubeletVersion', 'v1.36.1'), ('containerRuntimeVersion', 'cri-o://1.36')]:
            with self.subTest(field=field):
                self.resources['nodes'][1] = node('gpu-b')
                self.resources['nodes'][1]['status']['nodeInfo'][field] = value
                with self.assertRaisesRegex(setup.ClusterSetupError, 'socketPath explicitly'):
                    self.discover()

    def test_node_hostname_mismatch_does_not_make_unschedulable_config(self):
        self.resources['nodes'][1]['metadata']['labels']['kubernetes.io/hostname'] = 'different-host'
        with self.assertRaisesRegex(setup.ClusterSetupError, 'hostname labels differ'):
            self.discover()

    def test_default_storage_class_wins_over_other_classes(self):
        self.resources['storageclasses'].append({'metadata': {'name': 'other'}})
        self.assertEqual(self.discover()['storageClass'], 'local-storage')

    def test_ambiguous_storage_classes_require_selection(self):
        for multiple_defaults in (False, True):
            with self.subTest(multiple_defaults=multiple_defaults):
                self.resources['storageclasses'] = [
                    {'metadata': {'name': name, 'annotations': {'storageclass.kubernetes.io/is-default-class': str(multiple_defaults).lower()}}}
                    for name in ('a', 'b')]
                with self.assertRaisesRegex(setup.ClusterSetupError, 'StorageClass'):
                    self.discover()

    def test_missing_compatible_runtime_is_not_silently_substituted(self):
        self.resources['runtimeclasses'] = [{'metadata': {'name': 'nvidia'}, 'handler': 'runc'}]
        with self.assertRaisesRegex(setup.ClusterSetupError, 'RuntimeClass'):
            self.discover()

    def test_valid_names_are_checked_before_kubernetes_queries(self):
        with patch.object(setup.subprocess, 'check_output') as query:
            for value in ('Bad Namespace', '../other', 'x'*64):
                with self.subTest(value=value), self.assertRaises(setup.ClusterSetupError):
                    setup.discover_config('test-context', value, GLM, self.probe)
            with self.assertRaises(setup.ClusterSetupError):
                setup.discover_config('', None, GLM, self.probe)
            query.assert_not_called()

    def test_failed_queries_hide_stderr_and_return_actionable_error(self):
        failure = subprocess.CalledProcessError(1, ['kubectl'], stderr='credential-fixture')
        with patch.object(setup.subprocess, 'check_output', side_effect=failure), \
             self.assertRaises(setup.ClusterSetupError) as result:
            setup.discover_config('test-context', None, GLM, self.probe)
        self.assertIn('Check Kubernetes access', str(result.exception))
        self.assertNotIn('credential-fixture', str(result.exception))


class ProbeTests(unittest.TestCase):
    def test_discrete_gpu_memory_comes_from_nvidia_smi(self):
        gpu = setup.parse_probe('NVIDIA GB300, 10.3, 281250\nMemTotal:       503316480 kB\n')
        self.assertEqual(gpu, dict(GB300, memoryGiB=274.7))

    def test_unified_gpu_memory_comes_from_host_memory(self):
        for reported in ('[N/A]', '[Not Supported]'):
            with self.subTest(reported=reported):
                gpu = setup.parse_probe('NVIDIA GB10, 12.1, ' + reported + '\nMemTotal: 127526000 kB\n')
                self.assertEqual(gpu, GB10)

    def test_unexpected_probe_output_is_rejected(self):
        for text in ('MemTotal: 1 kB\n', 'NVIDIA GB300, 10.3, 1\nNVIDIA GB300, 10.3, 1\nMemTotal: 1 kB\n',
                     'NVIDIA GB300, 10.3\nMemTotal: 1 kB\n', 'NVIDIA GB300, 10.3, lots\nMemTotal: 1 kB\n',
                     'NVIDIA GB300, 103, 1\nMemTotal: 1 kB\n', 'NVIDIA GB300, 10.3, 1\n'):
            with self.subTest(text=text), self.assertRaises(setup.ClusterSetupError):
                setup.parse_probe(text)

    def test_probe_runs_gpu_pods_in_a_temporary_namespace_and_always_deletes_it(self):
        for fail in (False, True):
            calls = []

            def kubectl(context, *args, stdin=None, timeout=60):
                calls.append((args, json.loads(stdin) if stdin else None))
                if args[:1] == ('create',) and fail and stdin:
                    raise setup.ClusterSetupError('quota exceeded')
                if 'logs' in args:
                    return 'NVIDIA GB300, 10.3, 281250\nMemTotal: 503316480 kB\n'
                return ''

            with self.subTest(fail=fail), patch.object(setup, 'kubectl', side_effect=kubectl):
                if fail:
                    with self.assertRaisesRegex(setup.ClusterSetupError, 'quota'):
                        setup.probe_gpus('test-context', ['agent'], 'runtime:pinned', 'nvidia')
                else:
                    self.assertEqual(setup.probe_gpus('test-context', ['agent'], 'runtime:pinned', 'nvidia'),
                                     {'agent': dict(GB300, memoryGiB=274.7)})
                namespace = calls[0][0][2]
                self.assertRegex(namespace, r'^llm-routing-probe-[a-f0-9]{6}$')
                self.assertEqual(calls[-1][0][:3], ('delete', 'namespace', namespace))
                pods = [manifest for _, manifest in calls if manifest]
                self.assertEqual(len(pods), 1)
                spec = pods[0]['spec']
                self.assertEqual(spec['runtimeClassName'], 'nvidia')
                self.assertEqual(spec['nodeSelector'], {'kubernetes.io/hostname': 'agent'})
                self.assertEqual(spec['containers'][0]['image'], 'runtime:pinned')
                self.assertEqual(spec['containers'][0]['resources']['limits']['nvidia.com/gpu'], 1)


if __name__ == '__main__':
    unittest.main()
