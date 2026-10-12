# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import argparse
import copy
import importlib.util
import json
import os
import subprocess
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('developer_shared_images', HERE/'dev-artifacts/image_builder.py')
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)


class DeveloperImagesTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='shared-images-cli-')
        self.addCleanup(self.temporary.cleanup)
        self.output = Path(self.temporary.name).resolve()/'build'
        self.args = argparse.Namespace(context='test-context', namespace='test-models', output_dir=self.output,
                                       control_node=None, allow_containerd_import=True)
        self.config = {'context': 'test-context', 'namespace': 'test-models', 'controlNode': 'cpu-node', 'imagePlatform': 'linux/arm64',
                       'images': {name: {'repository': 'localhost/test-models/' + name, 'tag': 'dev-unique', 'pullPolicy': 'Never'}
                                  for name in images.COMPONENTS},
                       'containerd': {'nodeNames': ['cpu-node'], 'nodeUIDs': {'cpu-node': 'node-uid'},
                                      'archiveNode': 'cpu-node', 'socketPath': '/run/k3s/containerd/containerd.sock'}}
        self.discover = self.enterContext(patch.object(images, 'discover_config', return_value=copy.deepcopy(self.config)))
        self.prepare = self.enterContext(patch.object(images.preload, 'prepare', return_value={'completed': True}))
        self.current = self.enterContext(patch.object(images.subprocess, 'check_output', return_value='test-context\n'))
        self.enterContext(patch.dict(os.environ, {}, clear=True))
        self.enterContext(patch('builtins.print'))

    def test_prepares_images_and_values_without_installing_shared_resources(self):
        self.output.mkdir()
        (self.output/'unrelated.txt').write_text('preserve')
        path = images.prepare(self.args)
        self.prepare.assert_called_once_with(self.config, self.output, allow_containerd_import=True)
        values = json.loads(path.read_text())
        self.assertNotIn('watchNamespaces', values['operator'])
        self.assertNotIn('installCRDs', values['operator'])
        self.assertNotIn('credential', values['operator'])
        self.assertEqual(values['operator']['nodeSelector'], {'kubernetes.io/os': 'linux', 'kubernetes.io/arch': 'arm64'})
        self.assertEqual(values['operator']['pylon']['image'], self.config['images']['pylon'])
        self.assertEqual(values['gatewayStack']['llm-api-gateway']['llmApiGateway']['image'], self.config['images']['gateway'])
        self.assertEqual((self.output/'unrelated.txt').read_text(), 'preserve')
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.output/'image-build-config.json').stat().st_mode & 0o777, 0o600)

    def test_retry_after_interrupted_preparation_preserves_tags_and_node_uids(self):
        self.prepare.side_effect = [ValueError('interrupted'), {'completed': True}]
        with self.assertRaisesRegex(ValueError, 'interrupted'):
            images.prepare(self.args)
        saved = (self.output/'image-build-config.json').read_bytes()
        self.assertFalse((self.output/'shared.values.yaml').exists())
        images.prepare(self.args)
        self.discover.assert_called_once()
        self.assertEqual((self.output/'image-build-config.json').read_bytes(), saved)
        self.assertEqual(self.prepare.call_args_list[0], self.prepare.call_args_list[1])

    def test_context_and_node_mismatches_stop_before_preparation(self):
        images.prepare(self.args)
        self.prepare.reset_mock()
        different = copy.copy(self.args)
        different.context = 'other-context'
        with self.assertRaisesRegex(ValueError, 'Context or namespace differs'):
            images.prepare(different)
        with self.assertRaisesRegex(ValueError, 'Control node differs'):
            images.prepare(argparse.Namespace(**(vars(self.args) | {'control_node': 'other-node'})))
        self.prepare.assert_not_called()

    def test_explicit_context_overrides_environment_and_skips_current_lookup(self):
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': 'other-context'}):
            images.prepare(self.args)
        self.current.assert_not_called()
        self.discover.assert_called_once_with('test-context', 'test-models', None)

    def test_stale_legacy_environment_does_not_override_current_context(self):
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': 'stale-context'}):
            images.prepare(argparse.Namespace(**(vars(self.args) | {'context': None})))
        self.current.assert_called_once()
        self.discover.assert_called_once_with('test-context', 'test-models', None)

    def test_current_context_is_captured_once_and_used_for_all_operations(self):
        self.current.side_effect = ['test-context\n', 'changed-context\n']
        images.prepare(argparse.Namespace(**(vars(self.args) | {'context': None})))
        self.current.assert_called_once_with(['kubectl', 'config', 'current-context'],
                                            text=True, stderr=subprocess.PIPE, timeout=30)
        self.discover.assert_called_once_with('test-context', 'test-models', None)
        self.assertEqual(self.prepare.call_args.args[0]['context'], 'test-context')
        saved = json.loads((self.output/'image-build-config.json').read_text())
        self.assertEqual(saved['config']['context'], 'test-context')

    def test_missing_current_context_fails_before_cluster_access_or_files(self):
        self.current.return_value = '  \n'
        with self.assertRaisesRegex(ValueError, 'No valid Kubernetes context'):
            images.prepare(argparse.Namespace(**(vars(self.args) | {'context': None})))
        self.discover.assert_not_called()
        self.prepare.assert_not_called()
        self.assertFalse(self.output.exists())

    def test_current_context_command_errors_fail_before_mutations(self):
        for error in (FileNotFoundError('kubectl'), subprocess.CalledProcessError(1, 'kubectl'),
                      subprocess.TimeoutExpired('kubectl', 30)):
            with self.subTest(error=type(error).__name__):
                self.current.side_effect = error
                with self.assertRaisesRegex(ValueError, 'Could not read the current kubectl context'):
                    images.prepare(argparse.Namespace(**(vars(self.args) | {'context': None})))
        self.discover.assert_not_called()
        self.prepare.assert_not_called()
        self.assertFalse(self.output.exists())

    def test_containerd_authorization_is_required(self):
        with self.assertRaisesRegex(ValueError, '--allow-containerd-import'):
            images.prepare(argparse.Namespace(**(vars(self.args) | {'allow_containerd_import': False})))
        self.discover.assert_not_called()
        self.prepare.assert_not_called()

    def test_changed_generated_values_are_preserved_and_rejected(self):
        path = images.prepare(self.args)
        path.write_text('{"user": "edited"}\n')
        self.prepare.reset_mock()
        with self.assertRaisesRegex(ValueError, 'shared.values.yaml was changed'):
            images.prepare(self.args)
        self.assertEqual(path.read_text(), '{"user": "edited"}\n')
        self.prepare.assert_not_called()

    def test_unowned_artifacts_or_symlink_are_rejected(self):
        self.output.mkdir()
        values = self.output/'shared.values.yaml'
        values.write_text('{}')
        with self.assertRaisesRegex(ValueError, 'without their original configuration'):
            images.prepare(self.args)
        values.unlink()
        target = self.output/'private.json'
        target.write_text('{}')
        values.symlink_to(target)
        with self.assertRaisesRegex(ValueError, 'must not be symlinks'):
            images.prepare(self.args)
        self.assertEqual(target.read_text(), '{}')
        self.prepare.assert_not_called()


class ImageDiscoveryTests(unittest.TestCase):
    @staticmethod
    def node(name, architecture='arm64'):
        return {'metadata': {'name': name, 'uid': 'uid-' + name, 'labels': {
            'kubernetes.io/os': 'linux', 'kubernetes.io/arch': architecture}}, 'spec': {},
            'status': {'conditions': [{'type': 'Ready', 'status': 'True'}],
                       'nodeInfo': {'kubeletVersion': 'v1.34.1+k3s1', 'containerRuntimeVersion': 'containerd://2.1.0'}}}

    def discover(self, nodes, control=None):
        with patch.object(images.subprocess, 'check_output', return_value=json.dumps({'items': nodes})) as read:
            config = images.discover_config('test-context', 'test-models', control)
        read.assert_called_once_with(['kubectl', '--context', 'test-context', 'get', 'nodes', '-o', 'json'],
                                     text=True, timeout=60)
        return config

    def test_discovery_reads_only_nodes_and_never_selects_crd_ownership(self):
        nodes = [self.node('arm-a'), self.node('arm-b'), self.node('x86', 'amd64')]
        config = self.discover(nodes)
        self.assertEqual(config['containerd']['nodeUIDs'], {'arm-a': 'uid-arm-a', 'arm-b': 'uid-arm-b'})
        self.assertEqual(config['containerd']['socketPath'], '/run/k3s/containerd/containerd.sock')
        self.assertEqual(config['imagePlatform'], 'linux/arm64')
        for obsolete in ('installCRDs', 'operatorRelease', 'stackRelease', 'caConfigMap', 'clusterId'):
            self.assertNotIn(obsolete, config)
        values = images.helm_values(config)
        self.assertEqual(set(values['operator']), {'image', 'pylon', 'nodeSelector'})
        config = self.discover(nodes, 'x86')
        self.assertEqual(config['containerd']['nodeUIDs'], {'x86': 'uid-x86'})
        self.assertEqual(config['imagePlatform'], 'linux/amd64')

    def test_discovery_skips_pressure_taints_cordons_and_rejects_unidentified_nodes(self):
        nodes = [self.node('a'), self.node('b'), self.node('c'), self.node('d')]
        nodes[0]['spec']['unschedulable'] = True
        nodes[1]['spec']['taints'] = [{'effect': 'NoSchedule'}]
        nodes[2]['status']['conditions'].append({'type': 'MemoryPressure', 'status': 'True'})
        self.assertEqual(self.discover(nodes)['containerd']['nodeNames'], ['d'])
        nodes[3]['metadata'].pop('uid')
        with self.assertRaisesRegex(ValueError, 'stable UIDs'):
            self.discover(nodes)

    def test_other_containerd_distributions_need_explicit_socket_configuration(self):
        nodes = [self.node('node')]
        nodes[0]['status']['nodeInfo']['kubeletVersion'] = 'v1.34.1'
        self.assertNotIn('socketPath', self.discover(nodes)['containerd'])

    def test_load_discovery_respects_values_architecture_before_selecting_nodes(self):
        nodes = [self.node('a-x86', 'amd64'), self.node('b-arm')]
        with patch.object(images.subprocess, 'check_output', return_value=json.dumps({'items': nodes})):
            config = images.discover_config('test-context', 'test-models', architecture='arm64')
        self.assertEqual(config['imagePlatform'], 'linux/arm64')
        self.assertEqual(config['controlNode'], 'b-arm')
        self.assertEqual(config['containerd']['nodeUIDs'], {'b-arm': 'uid-b-arm'})


if __name__ == '__main__':
    unittest.main()
