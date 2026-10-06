# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import copy
import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('recipe_attach_reuse', HERE/'recipe.py')
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)


class AttachReuseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='recipe-attach-reuse-')
        self.addCleanup(self.tmp.cleanup)
        self.count = 0

    def installation(self, explicit=False):
        self.count += 1
        config = json.loads((HERE/'config.example.json').read_text())
        if explicit:
            config['releases'] = {'stack': 'custom-front', 'operator': 'custom-operator', 'glm': 'custom-model'}
            config['images']['repositories'] = {
                name: 'registry.example.com/custom/'+name for name in tool.COMPONENTS}
        recipe = tool.Recipe(config, pathlib.Path(self.tmp.name)/str(self.count))
        key = recipe.work/'api-key'
        key.write_text('test-only-key\n')
        recipe.state = {
            'identity': recipe.identity,
            'inventory': {'nodes': {name: name+'-uid' for name in config['nodes'].values()}},
            'runtimeSha256': 'a'*64, 'download': True, 'qualify': True,
            'stack': {'source': recipe.source_identity(), 'apiKeyFile': str(key)},
            'serve': True, 'registered': True, 'direct': True, 'gateway': True,
            'lastUpdate': {'component': 'gateway', 'previousTag': 'original', 'newTag': 'changed'},
        }
        tool.save(recipe.work/'config.json', config)
        tool.save(recipe.state_path, recipe.state)
        live = copy.deepcopy(config)
        live['releases'] = {'stack': recipe.stack, 'operator': recipe.operator, 'glm': recipe.glm}
        live['images']['repositories'] = {name: recipe.repository(name) for name in tool.COMPONENTS}
        live['apiKeyFile'] = None
        return recipe, live

    def attempt(self, recipe, live, *, rejected=False, discovery_error=None):
        before_state = copy.deepcopy(recipe.state)
        before_config = copy.deepcopy(recipe.c)
        before_files = {str(p.relative_to(recipe.work)): p.read_bytes()
                        for p in recipe.work.rglob('*') if p.is_file()}
        with patch.object(recipe, 'bound_cluster') as bound, \
             patch.object(tool, 'discover_config', return_value=copy.deepcopy(live), side_effect=discovery_error) as discover, \
             patch.object(tool, 'save') as save, patch.object(tool, 'run') as run, \
             patch.object(tool, 'output') as output, contextlib.redirect_stdout(io.StringIO()):
            if rejected:
                with self.assertRaises(RuntimeError):
                    recipe.attach_existing()
            else:
                recipe.attach_existing()
            bound.assert_called_once()
            save.assert_not_called()
            run.assert_not_called()
            output.assert_not_called()
        self.assertEqual(recipe.state, before_state)
        self.assertEqual(recipe.c, before_config)
        self.assertEqual({str(p.relative_to(recipe.work)): p.read_bytes()
                          for p in recipe.work.rglob('*') if p.is_file()}, before_files)
        return discover

    def test_repeated_attachment_preserves_installer_state_with_implicit_releases(self):
        recipe, live = self.installation()
        self.assertEqual(recipe.identity['releases'], {})
        for _ in range(2):
            discover = self.attempt(recipe, live)
            discover.assert_called_once_with(recipe.c['context'], recipe.c['namespace'])
        self.assertNotIn('attachedExisting', recipe.state)
        self.assertTrue(recipe.state['registered'])
        self.assertTrue(recipe.state['download'])
        self.assertTrue(recipe.state['gateway'])

    def test_explicit_releases_and_repositories_preserve_installer_capabilities(self):
        recipe, live = self.installation(explicit=True)
        self.attempt(recipe, live)
        self.assertNotIn('attachedExisting', recipe.state)
        self.assertEqual(recipe.state['identity']['releases'], recipe.c['releases'])
        self.assertEqual(recipe.state['stack']['apiKeyFile'], str(recipe.work/'api-key'))

    def test_live_image_tag_changes_do_not_require_replacing_saved_configuration(self):
        for explicit in (False, True):
            with self.subTest(explicit=explicit):
                recipe, live = self.installation(explicit)
                live['images']['tag'] = 'new-gateway-build'
                live['images']['prefix'] = 'registry.example.com/discovery-default'
                self.attempt(recipe, live)
                self.assertNotEqual(recipe.c['images']['tag'], live['images']['tag'])

    def test_incomplete_installer_state_is_not_promoted_to_an_attachment(self):
        for phase in ('stack', 'serve', 'registered'):
            with self.subTest(phase=phase):
                recipe, live = self.installation()
                recipe.state.pop(phase)
                self.attempt(recipe, live, rejected=True).assert_not_called()
                self.assertNotIn('attachedExisting', recipe.state)

    def test_saved_source_revision_does_not_block_attachment(self):
        recipe, live = self.installation()
        recipe.state['stack']['source']['revision'] = 'f'*40
        self.attempt(recipe, live).assert_called_once()

    def test_live_identity_and_runtime_mismatches_preserve_local_state(self):
        changes = {
            'context': 'another-context', 'namespace': 'another-namespace',
            'clusterId': 'another-cluster', 'nodes': {'control': 'c', 'leader': 'a', 'worker': 'b'},
            'runtimeClass': 'another-runtime', 'runtimeImage': 'example.com/another:runtime',
            'storageClass': 'another-storage', 'caConfigMap': 'another-ca',
        }
        for field, value in changes.items():
            with self.subTest(field=field):
                recipe, live = self.installation()
                live[field] = value
                self.attempt(recipe, live, rejected=True)

    def test_effective_release_mismatch_is_rejected(self):
        for component in ('stack', 'operator', 'glm'):
            with self.subTest(component=component):
                recipe, live = self.installation()
                live['releases'][component] = 'another-release'
                self.attempt(recipe, live, rejected=True)

    def test_each_component_repository_is_checked(self):
        for component in tool.COMPONENTS:
            with self.subTest(component=component):
                recipe, live = self.installation()
                live['images']['repositories'][component] = 'registry.example.com/foreign/'+component
                self.attempt(recipe, live, rejected=True)

    def test_live_source_or_ownership_discovery_failure_preserves_state(self):
        for message in ('Installed source differs', 'Unexpected deployment ownership'):
            with self.subTest(message=message):
                recipe, live = self.installation()
                self.attempt(recipe, live, rejected=True, discovery_error=RuntimeError(message))

    def test_changed_node_uid_is_rejected_before_any_rediscovery(self):
        recipe, _ = self.installation()
        before = recipe.state_path.read_bytes()
        nodes = {'items': [{'metadata': {'name': name, 'uid': name+'-replacement'}}
                           for name in recipe.c['nodes'].values()]}
        with patch.object(tool, 'output', return_value=json.dumps(nodes)) as output, \
             patch.object(tool, 'discover_config') as discover, patch.object(tool, 'save') as save:
            with self.assertRaisesRegex(RuntimeError, 'identities changed|another cluster'):
                recipe.attach_existing()
            output.assert_called_once_with(recipe.kc+['get', 'nodes', '-o', 'json'])
            discover.assert_not_called()
            save.assert_not_called()
        self.assertEqual(recipe.state_path.read_bytes(), before)
        self.assertNotIn('attachedExisting', recipe.state)


if __name__ == '__main__':
    unittest.main()
