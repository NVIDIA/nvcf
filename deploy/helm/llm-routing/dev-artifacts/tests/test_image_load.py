# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import io
import json
import os
import shutil
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('load_development_images', HERE / 'image_builder.py')
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)


class LoadExistingImagesTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.config = {'context': 'selected-cluster', 'namespace': 'llm-stack', 'controlNode': 'node',
                       'imagePlatform': 'linux/arm64', 'containerd': {'nodeNames': ['node'], 'nodeUIDs': {'node': 'current-uid'}},
                       'images': {name: {'repository': 'localhost/llm-stack/' + name, 'tag': 'dev-test', 'pullPolicy': 'Never'}
                                  for name in images.COMPONENTS}}
        self.values = self.root / 'values.json'
        self.values.write_text(json.dumps(images.helm_values(self.config)))
        from argparse import Namespace
        self.args = Namespace(context=None, namespace='llm-stack', control_node=None, values=self.values,
                              output_dir=None, allow_containerd_import=True)
        self.context = self.enterContext(patch.object(images, 'selected_context', return_value='selected-cluster'))
        self.discover = self.enterContext(patch.object(images, 'discover_config', return_value=copy.deepcopy(self.config)))
        self.factory = self.enterContext(patch.object(images.preload, 'Preparation'))
        self.preparation = self.factory.return_value
        self.preparation.images = {name: image['repository'] + ':' + image['tag'] for name, image in self.config['images'].items()}
        self.missing = self.enterContext(patch.object(images.preload, 'missing_nodes', return_value=['node']))
        self.enterContext(patch.dict(os.environ, {'XDG_STATE_HOME': str(self.root)}))
        self.enterContext(patch('builtins.print'))

    def archive(self, tag='dev-test', architecture='arm64', name='saved'):
        path = self.root / 'llm-routing/image-builds' / name / 'image-preparation/images.tar'
        path.parent.mkdir(parents=True, exist_ok=True)
        with tarfile.open(path, 'w') as archive:
            manifest = [{'Config': 'config.json', 'RepoTags': ['localhost/llm-stack/' + component + ':' + tag
                                                            for component in images.COMPONENTS]}]
            for name, content in [('manifest.json', manifest), ('config.json', {'os': 'linux', 'architecture': architecture})]:
                data = json.dumps(content).encode()
                info = tarfile.TarInfo(name)
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
        config = copy.deepcopy(self.config)
        config['imagePlatform'] = 'linux/' + architecture
        for image in config['images'].values():
            image['tag'] = tag
        config['context'] = 'original-cluster'
        config['containerd']['nodeUIDs']['node'] = 'original-node-uid'
        (path.parent.parent / 'image-build-config.json').write_text(json.dumps({
            'schemaVersion': 1, 'kind': 'llm-shared-image-build', 'config': config}))
        return path

    def test_load_finds_exact_bundle_and_preserves_values(self):
        archive = self.archive()
        self.archive(tag='other-tag', name='newer-unrelated')
        before = self.values.read_bytes()
        self.assertEqual(images.load(self.args), self.values)
        self.preparation.execute.assert_called_once_with(archive=archive.resolve())
        self.discover.assert_called_once_with('selected-cluster', 'llm-stack', None, architecture='arm64')
        self.assertEqual(self.factory.call_args.args[0]['images'], self.config['images'])
        self.assertEqual(self.values.read_bytes(), before)
        self.factory.assert_called_once()

    def test_missing_or_wrong_platform_archive_stops_before_import(self):
        for platform in (None, 'amd64'):
            with self.subTest(platform=platform):
                if platform:
                    self.archive(architecture=platform)
                with self.assertRaisesRegex(ValueError, 'No matching image archive'):
                    images.load(self.args)
                self.preparation.execute.assert_not_called()

    def test_saved_build_rebinds_cluster_identity_and_reuses_retry_directory(self):
        self.archive()
        images.load(self.args)
        work = self.factory.call_args.args[1]
        images.load(self.args)
        self.assertEqual(self.factory.call_args.args[1], work)
        self.assertIn('/image-loads/', str(work))
        selected = self.factory.call_args.args[0]
        self.assertEqual(selected['context'], 'selected-cluster')
        self.assertEqual(selected['containerd']['nodeUIDs'], {'node': 'current-uid'})

    def test_all_images_present_skips_archive_lookup_and_import(self):
        self.missing.return_value = []
        self.args.allow_containerd_import = False
        images.load(self.args)
        self.preparation.execute.assert_not_called()
        self.factory.assert_not_called()

    def test_registry_images_need_no_cluster_access_or_import_permission(self):
        self.values.write_text(self.values.read_text().replace('Never', 'IfNotPresent'))
        self.args.allow_containerd_import = False
        images.load(self.args)
        self.context.assert_not_called()
        self.discover.assert_not_called()
        self.factory.assert_not_called()

    def test_mixed_policies_only_request_local_images(self):
        values = json.loads(self.values.read_text())
        values['operator']['image']['pullPolicy'] = 'Always'
        self.values.write_text(json.dumps(values))
        del self.preparation.images['operator']
        self.archive()
        images.load(self.args)
        self.assertNotIn('operator', self.factory.call_args.args[0]['images'])
        self.assertIn('pylon', self.factory.call_args.args[0]['images'])

    def test_missing_images_require_authorization_before_import(self):
        self.args.allow_containerd_import = False
        with self.assertRaisesRegex(ValueError, '--allow-containerd-import'):
            images.load(self.args)
        self.discover.assert_called_once()
        self.missing.assert_called_once()
        self.factory.assert_not_called()

    def test_invalid_archive_is_not_imported(self):
        archive = self.archive()
        archive.write_text('not an image archive')
        with self.assertRaisesRegex(ValueError, 'No matching image archive'):
            images.load(self.args)
        self.preparation.execute.assert_not_called()

    def test_wrong_tag_saved_build_is_not_loaded(self):
        self.archive(tag='wrong-tag', name='other')
        with self.assertRaisesRegex(ValueError, 'No matching image archive'):
            images.load(self.args)
        self.factory.assert_not_called()

    def test_archive_content_must_match_saved_build_settings(self):
        archive = self.archive(tag='wrong-tag')
        saved = archive.parent.parent / 'image-build-config.json'
        data = json.loads(saved.read_text())
        for image in data['config']['images'].values():
            image['tag'] = 'dev-test'
        saved.write_text(json.dumps(data))
        with self.assertRaisesRegex(ValueError, 'No matching image archive'):
            images.load(self.args)
        self.factory.assert_not_called()

    def test_mixed_or_missing_architecture_fails_before_cluster_access(self):
        values = json.loads(self.values.read_text())
        values['operator']['nodeSelector']['kubernetes.io/arch'] = 'amd64'
        self.values.write_text(json.dumps(values))
        with self.assertRaisesRegex(ValueError, 'one shared image architecture'):
            images.load(self.args)
        self.context.assert_not_called()

    def test_same_tag_with_conflicting_archive_platforms_is_rejected(self):
        archive = self.archive()
        references = ['localhost/llm-stack/' + component + ':dev-test' for component in images.COMPONENTS]
        with tarfile.open(archive, 'a') as saved:
            manifest = [{'Config': 'config.json', 'RepoTags': references},
                        {'Config': 'other-platform.json', 'RepoTags': references[:1]}]
            for name, content in [('manifest.json', manifest),
                                  ('other-platform.json', {'os': 'linux', 'architecture': 'amd64'})]:
                data = json.dumps(content).encode()
                info = tarfile.TarInfo(name)
                info.size = len(data)
                saved.addfile(info, io.BytesIO(data))
        with self.assertRaisesRegex(ValueError, 'No matching image archive'):
            images.load(self.args)
        self.factory.assert_not_called()

    def test_invalid_local_tag_or_missing_component_fails_before_cluster_access(self):
        original = json.loads(self.values.read_text())
        for change in ('tag', 'component'):
            values = copy.deepcopy(original)
            if change == 'tag':
                values['operator']['image']['tag'] = 'latest'
            else:
                del values['operator']['pylon']
            self.values.write_text(json.dumps(values))
            with self.subTest(change=change), self.assertRaises(ValueError):
                images.load(self.args)
        self.context.assert_not_called()

    @unittest.skipUnless(shutil.which('helm'), 'Helm is required for YAML input.')
    def test_yaml_values_are_read_with_helm_without_cluster_access(self):
        self.values.write_text('gateway:\n  tag: dev-example\n  enabled: true\n')
        self.assertEqual(images.read_values(self.values), {'gateway': {'tag': 'dev-example', 'enabled': True}})
        self.context.assert_not_called()

    def test_json_values_do_not_invoke_helm(self):
        with patch.object(images.subprocess, 'check_output') as command:
            self.assertEqual(images.read_values(self.values), json.loads(self.values.read_text()))
        command.assert_not_called()


if __name__ == '__main__':
    unittest.main()
