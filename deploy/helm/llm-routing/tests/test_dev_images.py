# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('shared_dev_image_builder', HERE/'dev-images/build.py')
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class SharedDevImagesTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='dev-images-test-')
        self.addCleanup(self.temporary.cleanup)
        self.work = Path(self.temporary.name).resolve()
        self.values = self.work/'values.yaml'
        self.previous = '{"previous": "published image set"}\n'
        self.values.write_text(self.previous)
        self.output = self.work/'builds'
        self.args = ['--context', 'test-context', '--namespace', 'test-models',
                     '--control-node', 'cpu-node', '--output-dir', str(self.output),
                     '--allow-containerd-import']
        selector = {'kubernetes.io/os': 'linux', 'kubernetes.io/arch': 'arm64'}
        self.expected = {'gatewayStack': {}, 'operator': {
            'image': self.image('operator'), 'pylon': {'image': self.image('pylon')},
            'nodeSelector': selector, 'watchNamespaces': ['llm-stack']}}
        for name, chart, field in [('gateway', 'llm-api-gateway', 'llmApiGateway'),
                                   ('router', 'llm-request-router', 'llmRequestRouter')]:
            self.expected['gatewayStack'][chart] = {field: {
                'image': self.image(name), 'nodeSelector': selector}}
        self.generated = copy.deepcopy(self.expected)
        self.generated['operator'].update({
            'clusterId': 'private-cluster', 'watchNamespaces': ['test-models'],
            'installCRDs': False, 'credential': {'existingSecret': 'private-secret'}})
        self.generated['operator']['nodeSelector']['kubernetes.io/hostname'] = 'private-node'
        self.generated['callerKey'] = {'existingSecret': 'private-caller'}
        self.generated['gatewayStack']['tls'] = {'caFile': '/private/ca.pem'}
        self.enterContext(patch.object(builder, 'VALUES', self.values))
        self.build = self.enterContext(patch.object(builder.images, 'main', side_effect=self.build_values))
        self.enterContext(patch('builtins.print'))

    @staticmethod
    def image(name):
        return {'repository': 'localhost/shared/' + name, 'tag': 'dev-example', 'pullPolicy': 'Never'}

    def build_values(self, args):
        output = Path(args[args.index('--output-dir') + 1])
        output.mkdir(parents=True, exist_ok=True)
        generated = output/'shared.values.yaml'
        generated.write_text(json.dumps(self.generated))
        return generated

    def test_success_publishes_only_shared_images_and_placement(self):
        result = builder.main(self.args)
        self.assertEqual(result, self.values)
        self.assertEqual(json.loads(self.values.read_text()), self.expected)
        args = self.build.call_args.args[0]
        for option, expected in [('--context', 'test-context'), ('--namespace', 'test-models'),
                                 ('--control-node', 'cpu-node')]:
            self.assertEqual(args[args.index(option) + 1], expected)
        self.assertIn('--allow-containerd-import', args)

    def test_each_build_gets_a_fresh_external_directory(self):
        builder.main(self.args)
        builder.main(self.args)
        outputs = [Path(call.args[0][call.args[0].index('--output-dir') + 1])
                   for call in self.build.call_args_list]
        self.assertNotEqual(outputs[0], outputs[1])
        for output in outputs:
            self.assertEqual(output.parent, self.output)
            self.assertTrue(output.is_dir())

    def test_default_namespace_is_forwarded_without_forcing_a_context(self):
        builder.main(['--output-dir', str(self.output), '--allow-containerd-import'])
        args = self.build.call_args.args[0]
        self.assertEqual(args[args.index('--namespace') + 1], 'llm-stack')
        self.assertNotIn('--context', args)
        self.assertNotIn('--control-node', args)

    def test_failed_build_preserves_published_values(self):
        self.build.side_effect = ValueError('image import failed')
        with self.assertRaisesRegex(ValueError, 'image import failed'):
            builder.main(self.args)
        self.assertEqual(self.values.read_text(), self.previous)

    def test_import_authorization_is_required_before_build_or_local_output(self):
        with self.assertRaisesRegex(ValueError, '--allow-containerd-import'):
            builder.main(self.args[:-1])
        self.build.assert_not_called()
        self.assertFalse(self.output.exists())
        self.assertEqual(self.values.read_text(), self.previous)

    def test_output_inside_checkout_is_rejected_before_build(self):
        args = self.args.copy()
        args[args.index('--output-dir') + 1] = str(HERE/'unexpected-build-output')
        with self.assertRaises(ValueError):
            builder.main(args)
        self.build.assert_not_called()
        self.assertFalse((HERE/'unexpected-build-output').exists())
        self.assertEqual(self.values.read_text(), self.previous)

    def test_values_symlink_is_rejected_before_build(self):
        private = self.work/'private-values.yaml'
        private.write_text(self.previous)
        self.values.unlink()
        self.values.symlink_to(private)
        with self.assertRaises(ValueError):
            builder.main(self.args)
        self.build.assert_not_called()
        self.assertEqual(private.read_text(), self.previous)
        self.assertTrue(self.values.is_symlink())

    def test_invalid_generated_values_preserve_published_values(self):
        source = self.work/'invalid.yaml'
        invalid = [[], {}, {'operator': {}}, copy.deepcopy(self.generated)]
        invalid[-1]['operator']['image'].pop('tag')
        for value in invalid:
            with self.subTest(value=value):
                source.write_text(json.dumps(value))
                with self.assertRaises((ValueError, KeyError, TypeError)):
                    builder.publish_values(source, self.values)
                self.assertEqual(self.values.read_text(), self.previous)

    def test_unreadable_json_preserves_published_values(self):
        source = self.work/'invalid.yaml'
        source.write_text('incomplete JSON {')
        with self.assertRaises(ValueError):
            builder.publish_values(source, self.values)
        self.assertEqual(self.values.read_text(), self.previous)


if __name__ == '__main__':
    unittest.main()
