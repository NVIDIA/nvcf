# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import copy
import importlib.util
import io
import json
import pathlib
import re
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('test_shared_stack_images', HERE / 'stack_images.py')
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)


class StackImageTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.work = pathlib.Path(self.tmp.name).resolve()
        self.config = {'context': 'explicit-context', 'namespace': 'new-stack', 'controlNode': 'cpu-a',
            'imagePlatform': 'linux/arm64', 'images': {name: {'repository': 'localhost/new-stack/' + name,
            'tag': 'dev-fixture', 'pullPolicy': 'Never'} for name in images.image_tools.COMPONENTS},
            'containerd': {'socketPath': '/run/containerd/containerd.sock', 'nodeNames': ['cpu-a', 'cpu-b'],
                          'nodeUIDs': {'cpu-a': 'uid-a', 'cpu-b': 'uid-b'}, 'archiveNode': 'cpu-a', 'runAsUser': 1000,
                          'clientImage': 'example.test/loader:v1', 'serverImage': 'example.test/archive:v1'}}
        self.nodes = [{'metadata': {'name': name, 'uid': uid, 'labels': {'kubernetes.io/os': 'linux', 'kubernetes.io/arch': 'arm64'}},
                       'status': {'conditions': [{'type': 'Ready', 'status': 'True'}], 'nodeInfo': {'containerRuntimeVersion': 'containerd://2.0'}}}
                      for name, uid in self.config['containerd']['nodeUIDs'].items()]
        self.namespace = None
        self.releases = {}
        self.commands = []
        self.resources = []
        self.fail_import = False
        for method in ('run', 'output'):
            mock = patch.object(images.Preparation, method, autospec=True, side_effect=getattr(self, 'fake_' + method))
            mock.start()
            self.addCleanup(mock.stop)
        self.build = patch.object(images.image_tools, 'build_images').start()
        self.addCleanup(patch.stopall)
        patch.object(images.image_tools, 'export_images', side_effect=lambda refs, path, **kwargs: path).start()
        self.importer = patch.object(images.image_tools, 'import_images', side_effect=self.import_images).start()

    def fake_output(self, preparation, command, **kwargs):
        command = [str(arg) for arg in command]
        self.commands.append(command)
        if command[:2] == ['git', 'rev-parse']:
            return 'fixture-revision\n'
        if command[:2] == ['git', 'status']:
            return ' M local-source\n'
        if command[0] == 'kubectl':
            if command[3:5] == ['get', 'nodes']:
                return json.dumps({'items': self.nodes})
            if command[3:5] == ['get', 'namespace']:
                return json.dumps(self.namespace) if self.namespace else ''
            if command[3] == 'get':
                return json.dumps({'items': self.resources})
        if command[0] == 'helm':
            if command[5] == 'list':
                pattern = command[command.index('--filter') + 1]
                return json.dumps([{'name': name, 'chart': item['chart'] + '-0.1.0'}
                                   for name, item in self.releases.items() if re.fullmatch(pattern, name)])
            if command[5:7] == ['get', 'values']:
                return json.dumps(self.releases[command[7]]['values'])
        raise AssertionError('Unexpected command: ' + str(command))

    def fake_run(self, preparation, command, **kwargs):
        command = [str(arg) for arg in command]
        self.commands.append(command)
        self.assertEqual(command[0], 'helm')
        action, release = command[5:7]
        if action in ('install', 'upgrade'):
            values = json.loads(pathlib.Path(command[command.index('-f') + 1]).read_text())
            self.namespace = self.namespace or {'metadata': {'name': preparation.namespace, 'uid': 'prep-namespace-uid'}}
            chart = 'llm-image-preparation' if release == 'image-preparation' else 'pylon-image-loader'
            self.releases[release] = {'chart': chart, 'values': values}
        elif action == 'uninstall':
            del self.releases[release]
        else:
            raise AssertionError(command)
        return Mock(returncode=0)

    def import_images(self, archive, references, **kwargs):
        kwargs['helm_apply']('image-loader', HERE / 'recipes/charts/image-loader', {'enabled': True}, wait=False)
        if self.fail_import:
            raise RuntimeError('import interrupted')

    def prepare(self):
        with contextlib.redirect_stdout(io.StringIO()):
            return images.prepare(self.config, self.work, allow_containerd_import=True)

    def test_shared_images_build_import_and_cleanup_use_only_separate_namespace(self):
        result = self.prepare()
        self.assertTrue(result['completed'])
        self.assertEqual(result['sourceRevision'], 'fixture-revision-dirty')
        self.assertNotEqual(result['namespace'], self.config['namespace'])
        self.assertEqual(self.releases, {})
        self.assertEqual(self.namespace['metadata']['uid'], result['namespaceUID'])
        args, kwargs = self.build.call_args
        self.assertEqual(set(args[1]), {'gateway', 'router', 'pylon', 'operator'})
        self.assertEqual(kwargs['platform'], 'linux/arm64')
        self.assertEqual(self.importer.call_args.kwargs['node_names'], ['cpu-a', 'cpu-b'])
        for command in self.commands:
            if command[0] == 'helm':
                self.assertEqual(command[command.index('--kube-context') + 1], 'explicit-context')
                self.assertEqual(command[command.index('-n') + 1], result['namespace'])
            elif command[0] == 'kubectl':
                self.assertEqual(command[command.index('--context') + 1], 'explicit-context')
                self.assertNotIn('create', command)
                self.assertNotIn('delete', command)
        values = json.loads((self.work / 'image-preparation/loader-values.json').read_text())
        self.assertEqual(values['clientImage'], 'example.test/loader:v1')
        self.assertEqual(values['serverImage'], 'example.test/archive:v1')
        self.assertEqual((self.work / 'image-preparation/state.json').stat().st_mode & 0o777, 0o600)

    def test_amd64_uses_matching_platform_without_gpu_configuration(self):
        self.config['imagePlatform'] = 'linux/amd64'
        for node in self.nodes:
            node['metadata']['labels']['kubernetes.io/arch'] = 'amd64'
        self.prepare()
        self.assertEqual(self.build.call_args.kwargs['platform'], 'linux/amd64')
        self.assertEqual(self.importer.call_args.kwargs['platform'], 'linux/amd64')

    def test_import_authorization_and_socket_are_checked_before_build(self):
        with self.assertRaisesRegex(ValueError, 'authorization'):
            images.prepare(self.config, self.work, allow_containerd_import=False)
        del self.config['containerd']['socketPath']
        with self.assertRaisesRegex(ValueError, 'socketPath'):
            self.prepare()
        self.build.assert_not_called()

    def test_replaced_node_blocks_build(self):
        self.nodes[0]['metadata']['uid'] = 'replacement'
        with self.assertRaisesRegex(ValueError, 'node was replaced'):
            self.prepare()
        self.build.assert_not_called()

    def test_blocked_or_wrong_architecture_node_blocks_build(self):
        cases = [('architecture', lambda n: n['metadata']['labels'].update({'kubernetes.io/arch': 'amd64'})),
                 ('cordon', lambda n: n.update(spec={'unschedulable': True})),
                 ('taint', lambda n: n.update(spec={'taints': [{'effect': 'NoSchedule'}]})),
                 ('pressure', lambda n: n['status']['conditions'].append({'type': 'DiskPressure', 'status': 'True'}))]
        for label, change in cases:
            original = copy.deepcopy(self.nodes)
            change(self.nodes[0])
            with self.subTest(label=label), self.assertRaisesRegex(ValueError, 'does not match'):
                self.prepare()
            self.nodes = original
        self.build.assert_not_called()

    def test_new_eligible_node_and_missing_uid_blocks_build(self):
        extra = copy.deepcopy(self.nodes[0])
        extra['metadata'].update(name='cpu-c', uid='uid-c')
        self.nodes.append(extra)
        with self.assertRaisesRegex(ValueError, 'New compatible nodes'):
            self.prepare()
        self.nodes.pop()
        del self.config['containerd']['nodeUIDs']['cpu-b']
        with self.assertRaisesRegex(ValueError, 'Bind every'):
            self.prepare()
        self.build.assert_not_called()

    def test_existing_workload_image_tag_blocks_build(self):
        self.resources = [{'metadata': {'name': 'live-router'}, 'spec': {'template': {'spec': {'containers': [
            {'image': 'localhost/new-stack/router:dev-fixture'}]}}}}]
        with self.assertRaisesRegex(ValueError, 'already used'):
            self.prepare()
        self.build.assert_not_called()

    def test_import_failure_keeps_owned_state_for_guarded_retry(self):
        self.fail_import = True
        with self.assertRaisesRegex(ValueError, 'import interrupted'):
            self.prepare()
        saved = json.loads((self.work / 'image-preparation/state.json').read_text())
        self.assertFalse(saved['completed'])
        self.assertEqual(set(self.releases), {'image-preparation', 'image-loader'})
        self.fail_import = False
        result = self.prepare()
        self.assertEqual(result['namespaceUID'], saved['namespaceUID'])
        self.assertEqual(result['session'], saved['session'])
        self.assertEqual(self.releases, {})

    def test_changed_namespace_or_release_ownership_blocks_retry(self):
        self.fail_import = True
        with self.assertRaises(ValueError):
            self.prepare()
        self.namespace['metadata']['uid'] = 'foreign-namespace'
        self.build.reset_mock()
        with self.assertRaisesRegex(ValueError, 'namespace changed'):
            self.prepare()
        self.build.assert_not_called()
        self.namespace['metadata']['uid'] = 'prep-namespace-uid'
        self.releases['image-preparation']['values']['preparationSession'] = 'foreign'
        with self.assertRaisesRegex(ValueError, 'ownership changed'):
            self.prepare()

    def test_changed_configuration_rejects_retry(self):
        self.prepare()
        self.config['images']['gateway']['tag'] = 'another'
        with self.assertRaisesRegex(ValueError, 'configuration changed'):
            self.prepare()

    def test_adapter_has_no_model_recipe_imports(self):
        text = (HERE / 'stack_images.py').read_text()
        self.assertNotIn('import recipe', text)
        self.assertNotIn('Recipe(', text)
        self.assertNotIn('glm', text.lower())


if __name__ == '__main__':
    unittest.main()
