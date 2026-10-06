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
        self.template = json.loads((HERE/'config.example.json').read_text())

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
            return setup.discover_config('test-context', namespace)

    def test_unambiguous_setup_uses_live_metadata_and_public_pins(self):
        config = self.discover()
        self.assertEqual(config['nodes'], {'control': 'routing', 'leader': 'gpu-a', 'worker': 'gpu-b'})
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
        self.assertEqual(self.discover()['nodes'], {'control': 'routing', 'leader': 'gpu-a', 'worker': 'gpu-b'})

    def test_three_idle_gpus_with_shared_labels_choose_stable_placement(self):
        for item in self.resources['nodes']:
            item['metadata']['labels']['node-role.kubernetes.io/control-plane'] = 'true'
        config = self.discover()
        self.assertEqual(config['nodes'], {'control': 'routing', 'leader': 'gpu-a', 'worker': 'gpu-b'})
        self.resources['nodes'].reverse()
        self.assertEqual(self.discover()['nodes'], config['nodes'])

    def test_more_gpu_workers_do_not_require_an_extra_manual_choice(self):
        self.resources['nodes'].append(node('gpu-c'))
        config = self.discover()
        self.assertEqual(config['nodes'], {'control': 'routing', 'leader': 'gpu-a', 'worker': 'gpu-b'})
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
                with self.assertRaisesRegex(setup.ClusterSetupError, 'Free the required GPUs'):
                    self.discover()

    def test_completed_gpu_pods_do_not_block(self):
        self.resources['pods'] = [gpu_pod('gpu-a', 'Succeeded'), gpu_pod('gpu-b', 'Failed')]
        self.assertEqual(self.discover()['nodes']['leader'], 'gpu-a')

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
                with self.assertRaises(setup.ClusterSetupError):
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
                    setup.discover_config('test-context', value)
            with self.assertRaises(setup.ClusterSetupError):
                setup.discover_config('')
            query.assert_not_called()

    def test_failed_queries_hide_stderr_and_return_actionable_error(self):
        failure = subprocess.CalledProcessError(1, ['kubectl'], stderr='credential-fixture')
        with patch.object(setup.subprocess, 'check_output', side_effect=failure), \
             self.assertRaises(setup.ClusterSetupError) as result:
            setup.discover_config('test-context')
        self.assertIn('Check Kubernetes access', str(result.exception))
        self.assertNotIn('credential-fixture', str(result.exception))


if __name__ == '__main__':
    unittest.main()
