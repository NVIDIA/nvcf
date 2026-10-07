# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import io
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(HERE))
import image_tools
import test_image_import as import_tests


class ImageToolsTests(unittest.TestCase):
    def test_module_loads_without_recipe_or_model_dependencies(self):
        code = ('import sys; sys.path.insert(0, sys.argv[1]); import image_tools; '
                'assert not ({"recipe", "sizing", "monitoring", "stack_binding"} & sys.modules.keys())')
        subprocess.run([sys.executable, '-c', code, str(HERE)], check=True)

    def test_component_builds_use_selected_platform_sources_and_targets(self):
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory)
            for path in image_tools.COMPONENTS.values():
                (source/path).mkdir(parents=True, exist_ok=True)
            images = {name: 'registry.example.com/routing/'+name+':edited' for name in image_tools.COMPONENTS}
            for platform in image_tools.PLATFORMS:
                with self.subTest(platform=platform):
                    runner = Mock()
                    revision = Mock(return_value='current-dirty')
                    image_tools.build_images(source, images, run=runner, platform=platform, source_revision=revision)
                    commands = [call.args[0] for call in runner.call_args_list]
                    self.assertEqual(commands[0], ['docker', 'info'])
                    self.assertEqual(len(commands), len(images)+1)
                    for name, command in zip(images, commands[1:]):
                        self.assertEqual(command[:5], ['docker', 'buildx', 'build', '--platform', platform])
                        self.assertIn(images[name], command)
                        self.assertEqual(command[-1], str(source/image_tools.COMPONENTS[name]))
                        if name in ('router', 'pylon'):
                            self.assertIn('stargate-runtime' if name == 'router' else 'pylon-runtime', command)
                            self.assertIn('CARGO_PROFILE=integration', command)
                        if name == 'operator':
                            self.assertIn('SOURCE_REVISION=current-dirty', command)
                    revision.assert_called_once_with()

    def test_invalid_build_platform_fails_before_docker(self):
        runner = Mock()
        with self.assertRaisesRegex(RuntimeError, 'linux/arm64 or linux/amd64'):
            image_tools.build_images(HERE, {'gateway': 'example/gateway:tag'}, run=runner, platform='linux/ppc64le')
        runner.assert_not_called()

    def test_neutral_import_uses_explicit_namespace_platform_and_archive_name(self):
        fixture = import_tests.ImageImportTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        fixture.config['containerd'].update(clientImage='example/runtime-client:custom', serverImage='example/archive-server:custom')
        values = []
        with patch.object(image_tools.time, 'sleep'), contextlib.redirect_stdout(io.StringIO()):
            image_tools.import_images(
                fixture.archive, [fixture.recipe.image('gateway', 'edited')],
                context=fixture.config['context'], namespace=fixture.config['namespace'],
                release=fixture.release, work=fixture.recipe.work,
                containerd=fixture.config['containerd'], node_names=fixture.nodes,
                control_node=fixture.config['nodes']['control'],
                helm_apply=lambda *args, **kwargs: values.append((args, kwargs)),
                run=fixture.execute, output=fixture.output, save=import_tests.tool.save,
                platform='linux/amd64')
        applied = values[0][0][2]
        self.assertEqual(applied['platform'], 'linux/amd64')
        self.assertEqual(applied['archiveName'], 'images.tar')
        self.assertEqual(applied['clientImage'], 'example/runtime-client:custom')
        self.assertEqual(applied['serverImage'], 'example/archive-server:custom')
        self.assertEqual(fixture.destination.read_bytes(), fixture.archive.read_bytes())
        upload = next(command for command in fixture.commands if 'exec' in command)
        self.assertIn('/images/images.tar', upload)
        for command in fixture.commands + fixture.calls:
            self.assertEqual(command[command.index('-n')+1], fixture.config['namespace'])
        evidence = json.loads((fixture.recipe.work/'evidence/image-import.json').read_text())
        self.assertEqual(evidence['nodeNames'], fixture.nodes)

    def test_import_rejects_unsafe_name_and_platform_before_inspection(self):
        runner, output, helm_apply, save = Mock(), Mock(), Mock(), Mock()
        args = dict(context='test', namespace='image-prep', release='images', work=HERE,
                    containerd={'socketPath': '/run/containerd.sock'}, node_names=['node'], control_node='node',
                    helm_apply=helm_apply, run=runner, output=output, save=save)
        for changes, expected in [({'archive_name': '../images.tar'}, 'plain filename'),
                                  ({'platform': 'linux/ppc64le'}, 'linux/arm64 or linux/amd64')]:
            with self.subTest(changes=changes), self.assertRaisesRegex(RuntimeError, expected):
                image_tools.import_images('unused.tar', ['example/gateway:tag'], **args, **changes)
        for action in (runner, output, helm_apply, save):
            action.assert_not_called()


if __name__ == '__main__':
    unittest.main()
