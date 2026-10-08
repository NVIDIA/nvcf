# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
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
        self.charts = self.work/'charts'
        self.charts.mkdir()
        (self.charts/'previous-chart.tgz').write_bytes(b'previous chart bundle')
        self.output = self.work/'builds'
        self.args = ['--context', 'test-context', '--namespace', 'test-models',
                     '--control-node', 'cpu-node', '--output-dir', str(self.output),
                     '--allow-containerd-import']
        selector = {'kubernetes.io/os': 'linux', 'kubernetes.io/arch': 'arm64'}
        self.expected = {'gatewayStack': {}, 'operator': {
            'image': self.image('operator'), 'pylon': {'image': self.image('pylon')},
            'nodeSelector': selector}}
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
        self.enterContext(patch.object(builder, 'CHARTS', self.charts))
        self.build = self.enterContext(patch.object(builder.images, 'prepare', side_effect=self.build_values))
        self.package = self.enterContext(patch.object(builder.subprocess, 'run', side_effect=self.package_charts))
        self.enterContext(patch('builtins.print'))

    @staticmethod
    def image(name):
        return {'repository': 'localhost/shared/' + name, 'tag': 'dev-example', 'pullPolicy': 'Never'}

    def build_values(self, args):
        output = args.output_dir
        output.mkdir(parents=True, exist_ok=True)
        generated = output/'shared.values.yaml'
        generated.write_text(json.dumps(self.generated))
        return generated

    def package_charts(self, args, **kwargs):
        output = Path(args[args.index('--output-dir') + 1])
        output.mkdir(parents=True, exist_ok=True)
        (output/'test-chart.tgz').write_bytes(b'prepared chart bundle')

    def test_success_publishes_only_shared_images_and_placement(self):
        result = builder.main(self.args)
        self.assertEqual(result, self.values)
        self.assertEqual(json.loads(self.values.read_text()), self.expected)
        self.assertEqual((self.charts/'test-chart.tgz').read_bytes(), b'prepared chart bundle')
        self.assertFalse((self.charts/'previous-chart.tgz').exists())
        args = self.build.call_args.args[0]
        self.assertEqual(args.context, 'test-context')
        self.assertEqual(args.namespace, 'test-models')
        self.assertEqual(args.control_node, 'cpu-node')
        self.assertTrue(args.allow_containerd_import)
        output = args.output_dir
        package_args = self.package.call_args.args[0]
        self.assertEqual(package_args[:3], ['bash', str(HERE/'package-charts.sh'), '--output-dir'])
        self.assertEqual(Path(package_args[-1]).parent, output)
        self.assertTrue(Path(package_args[-1]).name.startswith('charts-'))

    def test_each_build_gets_a_fresh_external_directory(self):
        builder.main(self.args)
        builder.main(self.args)
        outputs = [call.args[0].output_dir for call in self.build.call_args_list]
        self.assertNotEqual(outputs[0], outputs[1])
        for output in outputs:
            self.assertEqual(output.parent, self.output)
            self.assertTrue(output.is_dir())

    def test_resume_reuses_bound_build_directory_and_fresh_chart_output(self):
        work = self.work/'existing-build'
        work.mkdir()
        (work/'image-build-config.json').write_text('{}')
        args = ['--context', 'test-context', '--namespace', 'test-models',
                '--resume-from', str(work), '--allow-containerd-import']
        builder.main(args)
        builder.main(args)
        self.assertEqual([call.args[0].output_dir for call in self.build.call_args_list], [work, work])
        packages = [Path(call.args[0][-1]) for call in self.package.call_args_list]
        self.assertNotEqual(packages[0], packages[1])
        self.assertTrue(all(path.parent == work for path in packages))

    def test_resume_requires_existing_external_build_before_preparation(self):
        with self.assertRaisesRegex(ValueError, 'existing image build'):
            builder.main(['--resume-from', str(self.work/'missing'), '--allow-containerd-import'])
        self.build.assert_not_called()
        self.package.assert_not_called()

    def test_default_namespace_is_forwarded_without_forcing_a_context(self):
        builder.main(['--output-dir', str(self.output), '--allow-containerd-import'])
        args = self.build.call_args.args[0]
        self.assertEqual(args.namespace, 'llm-stack')
        self.assertIsNone(args.context)
        self.assertIsNone(args.control_node)

    def test_failed_build_preserves_published_values(self):
        self.build.side_effect = ValueError('image import failed')
        with self.assertRaisesRegex(ValueError, 'image import failed'):
            builder.main(self.args)
        self.package.assert_not_called()
        self.assertEqual(self.values.read_text(), self.previous)

    def test_failed_chart_packaging_preserves_published_values(self):
        self.package.side_effect = subprocess.CalledProcessError(1, 'package-charts.sh')
        with self.assertRaises(subprocess.CalledProcessError):
            builder.main(self.args)
        self.build.assert_called_once()
        self.package.assert_called_once()
        self.assertEqual(self.values.read_text(), self.previous)

    def test_values_publication_failure_restores_previous_chart_bundle(self):
        publish = builder.publish_values

        def fail_final_publication(source, target):
            if target == self.values:
                raise OSError('simulated values publication failure')
            return publish(source, target)

        with patch.object(builder, 'publish_values', side_effect=fail_final_publication):
            with self.assertRaisesRegex(OSError, 'simulated values publication failure'):
                builder.main(self.args)
        self.assertEqual(self.values.read_text(), self.previous)
        self.assertEqual((self.charts/'previous-chart.tgz').read_bytes(), b'previous chart bundle')
        self.assertFalse((self.charts/'test-chart.tgz').exists())

    def test_import_authorization_is_required_before_build_or_local_output(self):
        with self.assertRaisesRegex(ValueError, '--allow-containerd-import'):
            builder.main(self.args[:-1])
        self.build.assert_not_called()
        self.package.assert_not_called()
        self.assertFalse(self.output.exists())
        self.assertEqual(self.values.read_text(), self.previous)

    def test_output_inside_checkout_is_rejected_before_build(self):
        args = self.args.copy()
        args[args.index('--output-dir') + 1] = str(HERE/'unexpected-build-output')
        with self.assertRaises(ValueError):
            builder.main(args)
        self.build.assert_not_called()
        self.package.assert_not_called()
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
        self.package.assert_not_called()
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
