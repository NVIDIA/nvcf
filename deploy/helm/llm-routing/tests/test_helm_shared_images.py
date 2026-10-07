# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
import os
import subprocess
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('developer_shared_images', HERE/'build-shared-images.py')
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)


class DeveloperImagesTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='shared-images-cli-')
        self.addCleanup(self.temporary.cleanup)
        self.output = Path(self.temporary.name).resolve()/'build'
        self.args = ['--context', 'test-context', '--namespace', 'test-models', '--output-dir', str(self.output), '--allow-containerd-import']
        self.config = {'context': 'test-context', 'namespace': 'test-models', 'clusterId': 'test-models',
                       'stackRelease': 'test-models', 'operatorRelease': 'test-models-operator', 'controlNode': 'cpu-node',
                       'caConfigMap': 'test-models-ca', 'installCRDs': False, 'imagePlatform': 'linux/arm64',
                       'images': {name: {'repository': 'localhost/test-models/' + name, 'tag': 'dev-unique', 'pullPolicy': 'Never'}
                                  for name in images.stack.COMPONENTS},
                       'containerd': {'nodeNames': ['cpu-node'], 'nodeUIDs': {'cpu-node': 'node-uid'},
                                      'archiveNode': 'cpu-node', 'socketPath': '/run/k3s/containerd/containerd.sock'}}
        self.discover = self.enterContext(patch.object(images.stack, 'discover_config', return_value=copy.deepcopy(self.config)))
        self.get = self.enterContext(patch.object(images.stack, 'get', return_value={'items': []}))
        self.prepare = self.enterContext(patch.object(images.stack, 'prepare_images', return_value={'completed': True}))
        self.install = self.enterContext(patch.object(images.stack, 'Stack', side_effect=AssertionError('Shared installation must not run')))
        self.current = self.enterContext(patch.object(images.subprocess, 'check_output', return_value='test-context\n'))
        self.enterContext(patch.dict(os.environ, {}, clear=True))
        self.enterContext(patch('builtins.print'))

    def test_prepares_images_and_values_without_installing_shared_resources(self):
        self.output.mkdir()
        (self.output/'unrelated.txt').write_text('preserve')
        path = images.main(self.args)
        self.prepare.assert_called_once_with(self.config, self.output, allow_containerd_import=True)
        self.install.assert_not_called()
        values = json.loads(path.read_text())
        self.assertEqual(values['operator']['watchNamespaces'], ['test-models'])
        self.assertFalse(values['operator']['installCRDs'])
        self.assertEqual(values['operator']['nodeSelector'], {'kubernetes.io/os': 'linux', 'kubernetes.io/arch': 'arm64'})
        self.assertEqual(values['operator']['pylon']['image'], self.config['images']['pylon'])
        self.assertEqual(values['gatewayStack']['llm-api-gateway']['llmApiGateway']['image'], self.config['images']['gateway'])
        self.assertEqual((self.output/'unrelated.txt').read_text(), 'preserve')
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.output/'image-build-config.json').stat().st_mode & 0o777, 0o600)

    def test_retry_after_interrupted_preparation_preserves_tags_and_node_uids(self):
        self.prepare.side_effect = [ValueError('interrupted'), {'completed': True}]
        with self.assertRaisesRegex(ValueError, 'interrupted'):
            images.main(self.args)
        saved = (self.output/'image-build-config.json').read_bytes()
        self.assertFalse((self.output/'shared.values.yaml').exists())
        images.main(self.args)
        self.discover.assert_called_once()
        self.assertEqual((self.output/'image-build-config.json').read_bytes(), saved)
        self.assertEqual(self.prepare.call_args_list[0], self.prepare.call_args_list[1])

    def test_context_and_node_mismatches_stop_before_preparation(self):
        images.main(self.args)
        self.prepare.reset_mock()
        different = self.args.copy()
        different[different.index('test-context')] = 'other-context'
        with self.assertRaisesRegex(ValueError, 'Context or namespace differs'):
            images.main(different)
        with self.assertRaisesRegex(ValueError, 'Control node differs'):
            images.main(self.args + ['--control-node', 'other-node'])
        self.prepare.assert_not_called()

    def test_existing_stack_namespace_allows_new_images_without_installing(self):
        self.get.return_value = {'items': [{'metadata': {'name': 'test-models'}}]}
        path = images.main(self.args)
        self.assertTrue(path.is_file())
        self.prepare.assert_called_once_with(self.config, self.output, allow_containerd_import=True)
        self.install.assert_not_called()

    def test_explicit_context_overrides_environment_and_skips_current_lookup(self):
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': 'other-context'}):
            images.main(self.args)
        self.current.assert_not_called()
        self.discover.assert_called_once_with('test-context', 'test-models', None, build_images=True)

    def test_stale_legacy_environment_does_not_override_current_context(self):
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': 'stale-context'}):
            images.main(self.args[2:])
        self.current.assert_called_once()
        self.discover.assert_called_once_with('test-context', 'test-models', None, build_images=True)

    def test_current_context_is_captured_once_and_used_for_all_operations(self):
        self.current.side_effect = ['test-context\n', 'changed-context\n']
        images.main(self.args[2:])
        self.current.assert_called_once_with(['kubectl', 'config', 'current-context'],
                                            text=True, stderr=subprocess.PIPE, timeout=30)
        self.discover.assert_called_once_with('test-context', 'test-models', None, build_images=True)
        self.assertEqual(self.prepare.call_args.args[0]['context'], 'test-context')
        saved = json.loads((self.output/'image-build-config.json').read_text())
        self.assertEqual(saved['config']['context'], 'test-context')

    def test_missing_current_context_fails_before_cluster_access_or_files(self):
        self.current.return_value = '  \n'
        with self.assertRaisesRegex(ValueError, 'No valid Kubernetes context'):
            images.main(self.args[2:])
        self.discover.assert_not_called()
        self.get.assert_not_called()
        self.prepare.assert_not_called()
        self.assertFalse(self.output.exists())

    def test_current_context_command_errors_fail_before_mutations(self):
        for error in (FileNotFoundError('kubectl'), subprocess.CalledProcessError(1, 'kubectl'),
                      subprocess.TimeoutExpired('kubectl', 30)):
            with self.subTest(error=type(error).__name__):
                self.current.side_effect = error
                with self.assertRaisesRegex(ValueError, 'Could not read the current kubectl context'):
                    images.main(self.args[2:])
        self.discover.assert_not_called()
        self.get.assert_not_called()
        self.prepare.assert_not_called()
        self.assertFalse(self.output.exists())

    def test_containerd_authorization_is_required(self):
        with self.assertRaisesRegex(ValueError, '--allow-containerd-import'):
            images.main(self.args[:-1])
        self.discover.assert_not_called()
        self.prepare.assert_not_called()

    def test_changed_generated_values_are_preserved_and_rejected(self):
        path = images.main(self.args)
        path.write_text('{"user": "edited"}\n')
        self.prepare.reset_mock()
        with self.assertRaisesRegex(ValueError, 'shared.values.yaml was changed'):
            images.main(self.args)
        self.assertEqual(path.read_text(), '{"user": "edited"}\n')
        self.prepare.assert_not_called()

    def test_unowned_artifacts_or_symlink_are_rejected(self):
        self.output.mkdir()
        values = self.output/'shared.values.yaml'
        values.write_text('{}')
        with self.assertRaisesRegex(ValueError, 'without their original configuration'):
            images.main(self.args)
        values.unlink()
        target = self.output/'private.json'
        target.write_text('{}')
        values.symlink_to(target)
        with self.assertRaisesRegex(ValueError, 'must not be symlinks'):
            images.main(self.args)
        self.assertEqual(target.read_text(), '{}')
        self.prepare.assert_not_called()


if __name__ == '__main__':
    unittest.main()
