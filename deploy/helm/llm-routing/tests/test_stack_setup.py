# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import copy
import hashlib
import io
import json
import os
import pathlib
import tempfile
import unittest
from unittest.mock import Mock, patch

from test_stack import stack


def node(name, architecture='arm64', ready=True, k3s=True):
    return {'metadata': {'name': name, 'uid': name + '-uid', 'labels': {
        'kubernetes.io/hostname': name, 'kubernetes.io/os': 'linux', 'kubernetes.io/arch': architecture}},
        'spec': {}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True' if ready else 'False'}],
        'allocatable': {'cpu': '8', 'memory': '16Gi'}, 'nodeInfo': {
            'kubeletVersion': 'v1.34.1+k3s1' if k3s else 'v1.34.1', 'containerRuntimeVersion': 'containerd://2.1.0'}}}


class SetupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='stack-setup-test-')
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name).resolve()
        self.environment = patch.dict(os.environ, XDG_STATE_HOME=str(self.root/'state'), LLM_ROUTING_CONTEXT='test-cluster')
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.nodes = [node('cpu-node'), node('other-node', 'amd64'), node('not-ready', ready=False)]
        self.crds = []
        self.namespaces = []
        self.calls = []
        self.get = patch.object(stack, 'get', side_effect=self.read)
        self.get_mock = self.get.start()
        self.addCleanup(self.get.stop)
        self.stdout = io.StringIO()

    def read(self, config, kind, name=None):
        self.calls.append((copy.deepcopy(config), kind, name))
        return {'items': copy.deepcopy({'nodes': self.nodes, 'crds': self.crds, 'namespaces': self.namespaces}[kind])}

    def cli(self, *args):
        with contextlib.redirect_stdout(self.stdout):
            return stack.main(list(args))

    def config(self, namespace='llm-stack'):
        return json.loads((stack.default_work_dir('test-cluster', namespace)/'config.json').read_text())

    def mock_stack(self, events):
        def construct(config, work):
            instance = Mock(config=config, work=work)
            instance.install.side_effect = lambda **kwargs: events.append('install')
            instance.verify.side_effect = lambda *args: events.append('verify')
            return instance
        return patch.object(stack, 'Stack', side_effect=construct)

    def test_default_paths_are_private_and_isolate_contexts_and_namespaces(self):
        scope = hashlib.sha256(b'test-cluster').hexdigest()[:20]
        expected = self.root/'state/nvcf/llm-routing/stacks'/scope/'llm-stack'
        self.assertEqual(stack.default_work_dir('test-cluster', 'llm-stack'), expected)
        self.assertNotEqual(expected, stack.default_work_dir('another-cluster', 'llm-stack'))
        self.assertNotEqual(expected, stack.default_work_dir('test-cluster', 'another-stack'))
        self.cli('paths', '--field', 'connection')
        self.assertEqual(self.stdout.getvalue().strip(), str(expected/'connection.json'))
        self.assertFalse(expected.exists())
        self.get_mock.assert_not_called()

    def test_no_implicit_current_context_and_relative_xdg_is_rejected(self):
        os.environ.pop('LLM_ROUTING_CONTEXT')
        with patch.object(stack, 'run') as run, self.assertRaisesRegex(ValueError, 'current kubectl context is not selected'):
            self.cli('init')
        run.assert_not_called()
        os.environ['LLM_ROUTING_CONTEXT'] = 'test-cluster'
        os.environ['XDG_STATE_HOME'] = 'relative-state'
        with self.assertRaisesRegex(ValueError, 'absolute directory'):
            self.cli('init')
        self.get_mock.assert_not_called()

    def test_init_uses_example_registry_defaults_and_cpu_only_read_only_discovery(self):
        self.cli('init')
        config = self.config()
        example = json.loads((stack.HERE/'stack.config.example.json').read_text())
        self.assertEqual(config['images'], example['images'])
        self.assertEqual(config['controlNode'], 'cpu-node')
        self.assertEqual(config['imagePlatform'], 'linux/arm64')
        self.assertEqual(config['containerd']['nodeNames'], ['cpu-node'])
        self.assertEqual(config['containerd']['nodeUIDs'], {'cpu-node': 'cpu-node-uid'})
        self.assertEqual(config['containerd']['socketPath'], '/run/k3s/containerd/containerd.sock')
        self.assertTrue(config['installCRDs'])
        self.assertEqual([kind for _, kind, _ in self.calls], ['nodes', 'crds'])
        self.assertTrue(all(config['context'] == 'test-cluster' for config, _, _ in self.calls))
        work = stack.default_work_dir('test-cluster', 'llm-stack')
        self.assertEqual((work/'config.json').stat().st_mode & 0o777, 0o600)
        self.assertEqual(work.stat().st_mode & 0o777, 0o700)
        self.assertFalse((work/'install.json').exists())

    def test_init_refuses_overwrite_before_cluster_reads(self):
        self.cli('init')
        path = stack.default_work_dir('test-cluster', 'llm-stack')/'config.json'
        before = path.read_bytes()
        self.get_mock.reset_mock()
        with self.assertRaisesRegex(ValueError, 'never overwrites'):
            self.cli('init')
        self.get_mock.assert_not_called()
        self.assertEqual(path.read_bytes(), before)

    def test_init_skips_unschedulable_nodes_and_accepts_explicit_architecture(self):
        blocked = node('a-cordoned')
        blocked['spec']['unschedulable'] = True
        tainted = node('b-tainted')
        tainted['spec']['taints'] = [{'key': 'reserved', 'effect': 'NoSchedule'}]
        pressure = node('c-pressure')
        pressure['status']['conditions'].append({'type': 'MemoryPressure', 'status': 'True'})
        self.nodes.extend([blocked, tainted, pressure])
        self.cli('init', '--control-node', 'other-node')
        config = self.config()
        self.assertEqual(config['controlNode'], 'other-node')
        self.assertEqual(config['imagePlatform'], 'linux/amd64')
        self.assertEqual(config['containerd']['nodeNames'], ['other-node'])
        self.cli('--namespace', 'unblocked-stack', 'init')
        self.assertEqual(self.config('unblocked-stack')['containerd']['nodeNames'], ['cpu-node'])
        with self.assertRaisesRegex(ValueError, 'Ready, schedulable'):
            self.cli('--namespace', 'blocked-stack', 'init', '--control-node', 'a-cordoned')

    def test_init_non_k3s_does_not_guess_containerd_socket(self):
        self.nodes = [node('standard-node', k3s=False)]
        self.cli('init')
        self.assertNotIn('socketPath', self.config()['containerd'])
        self.assertEqual(self.config()['containerd']['nodeUIDs'], {'standard-node': 'standard-node-uid'})

    def test_init_registry_override_and_namespace_derived_names(self):
        self.cli('--namespace', 'another-stack', 'init', '--image-prefix', 'registry.test/team', '--image-tag', 'build-7')
        config = self.config('another-stack')
        self.assertEqual(config['namespace'], 'another-stack')
        self.assertEqual(config['clusterId'], 'another-stack')
        self.assertEqual(config['stackRelease'], 'another-stack')
        self.assertEqual(config['operatorRelease'], 'another-stack-operator')
        for component, image in config['images'].items():
            self.assertEqual(image, {'repository': 'registry.test/team/' + component, 'tag': 'build-7', 'pullPolicy': 'IfNotPresent'})

    def test_init_reuses_only_compatible_existing_crd(self):
        self.crds = [{'metadata': {'name': stack.CRD}, 'spec': {'group': 'pylon.nvidia.com', 'scope': 'Namespaced',
            'names': {'kind': 'InferenceEndpoint'}, 'versions': [{'name': 'v1alpha1', 'served': True,
            'schema': {'openAPIV3Schema': {'properties': {'spec': {'properties': {name: {} for name in
                ('modelName', 'service', 'inferenceAPIFormat', 'health', 'maxEngineConcurrency')}}}}}}]}}]
        self.cli('init')
        self.assertFalse(self.config()['installCRDs'])
        self.crds[0]['spec']['scope'] = 'Cluster'
        with self.assertRaisesRegex(ValueError, 'Incompatible'):
            self.cli('--namespace', 'second-stack', 'init')
        self.assertFalse((stack.default_work_dir('test-cluster', 'second-stack')/'config.json').exists())

    def test_saved_context_and_namespace_mismatch_fail_before_cluster_access(self):
        self.cli('init')
        work = stack.default_work_dir('test-cluster', 'llm-stack')
        self.get_mock.reset_mock()
        for flag, value, message in (('--context', 'other', 'context differs'), ('--namespace', 'other', 'namespace differs')):
            with self.subTest(flag=flag), self.assertRaisesRegex(ValueError, message):
                self.cli('--work-dir', str(work), flag, value, 'install')
        self.get_mock.assert_not_called()

    def test_temporary_build_flag_bootstraps_local_images_before_install_and_verify(self):
        events = []
        def build(config, work, allow_containerd_import):
            self.assertTrue(allow_containerd_import)
            self.assertEqual(work, stack.default_work_dir('test-cluster', 'llm-stack'))
            self.assertTrue((work/'config.json').is_file())
            tags = {image['tag'] for image in config['images'].values()}
            self.assertEqual(len(tags), 1)
            self.assertRegex(next(iter(tags)), r'^dev-[0-9]{14}-[a-f0-9]{6}$')
            for name, image in config['images'].items():
                self.assertEqual(image['repository'], 'localhost/llm-stack/' + name)
                self.assertEqual(image['pullPolicy'], 'Never')
            events.append('images')
        with self.mock_stack(events), patch.object(stack, 'prepare_images', side_effect=build):
            self.cli('install', '--build-images')
        self.assertEqual(events, ['images', 'install', 'verify'])

    def test_install_without_build_flag_rejects_placeholders_before_mutation(self):
        with patch.object(stack, 'run') as run, self.assertRaisesRegex(ValueError, 'install --build-images'):
            self.cli('install')
        run.assert_not_called()
        self.assertFalse((stack.default_work_dir('test-cluster', 'llm-stack')/'install.json').exists())
        stack.validate_config(self.config())

    def test_build_flag_refuses_existing_installation_or_existing_namespace(self):
        self.cli('init')
        work = stack.default_work_dir('test-cluster', 'llm-stack')
        with patch.object(stack, 'prepare_images') as build:
            for filename in ('install.json', 'connection.json'):
                (work/filename).write_text('{}')
                self.get_mock.reset_mock()
                with self.subTest(filename=filename), self.assertRaisesRegex(ValueError, 'fresh stack'):
                    self.cli('install', '--build-images')
                self.get_mock.assert_not_called()
                (work/filename).unlink()
            self.namespaces = [{'metadata': {'name': 'llm-stack'}}]
            with self.assertRaisesRegex(ValueError, 'fresh stack namespace'):
                self.cli('install', '--build-images')
            build.assert_not_called()

    def test_build_flag_uses_fresh_local_refs_without_changing_explicit_source(self):
        self.cli('init', '--image-prefix', 'registry.test/team', '--image-tag', 'configured-tag')
        before = self.config()
        source = self.root/'registry-config.json'
        source.write_text(json.dumps(before))
        work = self.root/'build-work'
        events = []
        with self.mock_stack(events), patch.object(stack, 'prepare_images') as build:
            self.cli('--config', str(source), '--work-dir', str(work), 'install', '--build-images')
        built = build.call_args.args[0]
        for name, image in built['images'].items():
            self.assertEqual(image['repository'], 'localhost/llm-stack/' + name)
            self.assertEqual(image['pullPolicy'], 'Never')
            self.assertNotEqual(image['tag'], 'configured-tag')
        self.assertEqual(json.loads(source.read_text()), before)
        self.assertEqual(json.loads((work/'local-images.json').read_text())['config'], built)

    def test_build_flag_converts_only_unresolved_template_images_in_unused_setup(self):
        self.cli('init')
        before = self.config()
        events = []
        with self.mock_stack(events), patch.object(stack, 'prepare_images'):
            self.cli('install', '--build-images')
        after = self.config()
        self.assertNotEqual(before['images'], after['images'])
        self.assertEqual({key: value for key, value in before.items() if key != 'images'},
                         {key: value for key, value in after.items() if key != 'images'})

    def test_failed_build_retry_reuses_exact_saved_refs_and_node_bindings(self):
        with patch.object(stack, 'prepare_images', side_effect=ValueError('build failed')), self.assertRaisesRegex(ValueError, 'build failed'):
            self.cli('install', '--build-images')
        previous = self.config()
        self.get_mock.reset_mock()
        events = []
        with self.mock_stack(events), patch.object(stack, 'prepare_images') as build:
            self.cli('install', '--build-images')
        self.assertEqual(build.call_args.args[0], previous)
        self.assertEqual([call.args[1] for call in self.get_mock.call_args_list], ['namespaces'])
        self.assertEqual(self.config(), previous)

    def test_failed_build_retry_refuses_modified_saved_images(self):
        with patch.object(stack, 'prepare_images', side_effect=ValueError('build failed')), self.assertRaises(ValueError):
            self.cli('install', '--build-images')
        config = self.config()
        config['images']['gateway']['tag'] = 'changed'
        (stack.default_work_dir('test-cluster', 'llm-stack')/'config.json').write_text(json.dumps(config))
        with patch.object(stack, 'prepare_images') as build, self.assertRaisesRegex(ValueError, 'build settings changed'):
            self.cli('install', '--build-images')
        build.assert_not_called()

    def test_partial_containerd_config_preserves_custom_socket_and_merges_identity(self):
        self.cli('init')
        config = self.config()
        config['containerd'] = {'socketPath': '/run/custom/containerd.sock', 'clientImage': 'custom-ctr:pinned'}
        path = stack.default_work_dir('test-cluster', 'llm-stack')/'config.json'
        path.write_text(json.dumps(config))
        events = []
        with self.mock_stack(events), patch.object(stack, 'prepare_images') as build:
            self.cli('install', '--build-images')
        containerd = build.call_args.args[0]['containerd']
        self.assertEqual(containerd['socketPath'], '/run/custom/containerd.sock')
        self.assertEqual(containerd['clientImage'], 'custom-ctr:pinned')
        self.assertEqual(containerd['nodeUIDs'], {'cpu-node': 'cpu-node-uid'})
        self.assertEqual(containerd['nodeNames'], ['cpu-node'])

    def test_local_build_rejects_unsupported_architecture_before_building(self):
        self.nodes = [node('different-node', 's390x')]
        with patch.object(stack, 'prepare_images') as build, self.assertRaisesRegex(ValueError, 'amd64 and linux/arm64'):
            self.cli('install', '--build-images')
        build.assert_not_called()
        self.assertFalse((stack.default_work_dir('test-cluster', 'llm-stack')/'local-images.json').exists())

    def test_init_explicit_config_also_saves_default_private_configuration(self):
        path = self.root/'custom-config.json'
        self.cli('--config', str(path), 'init')
        self.assertEqual(json.loads(path.read_text()), self.config())
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.stdout = io.StringIO()
        self.cli('paths', '--field', 'config')
        self.assertEqual(self.stdout.getvalue().strip(), str(stack.default_work_dir('test-cluster', 'llm-stack')/'config.json'))


if __name__ == '__main__':
    unittest.main()
