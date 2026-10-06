# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
import pathlib
import shutil
import tempfile
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('recipe_definitions', HERE/'recipe.py')
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)


class RecipeDefinitionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='recipe-definitions-')
        self.addCleanup(self.tmp.cleanup)
        self.config = json.loads((HERE/'config.example.json').read_text())

    def copy_recipe(self, name, **changes):
        root = pathlib.Path(self.tmp.name)/'recipes'
        shutil.copytree(HERE/'glm-5.3', root/name)
        definition = json.loads((root/name/'recipe.json').read_text())
        definition.update(name=name, **changes)
        (root/name/'recipe.json').write_text(json.dumps(definition))
        return root

    def test_glm_recipe_reads_its_folder_and_model_lock(self):
        recipe = tool.load_recipe('glm-5.3')
        self.assertEqual(recipe['servedName'], 'GLM-5.3-UD-IQ2_M')
        self.assertEqual(recipe['endpointName'], 'glm53-iq2')
        self.assertEqual(recipe['releaseName'], 'glm')
        self.assertEqual(len(recipe['lock']['files']), recipe['lock']['weightFiles'])
        self.assertEqual(recipe['firstShard'], recipe['lock']['files'][0]['rfilename'])

    def test_available_recipes_are_folders_with_a_definition(self):
        self.assertEqual(tool.available_recipes(), ['glm-5.3'])

    def test_recipe_arguments_must_not_choose_placement(self):
        base = json.loads((HERE/'glm-5.3/recipe.json').read_text())['serverArgs']
        for flag in ('--device', '--tensor-split', '--rpc'):
            root = pathlib.Path(self.tmp.name)/flag.strip('-')
            shutil.copytree(HERE/'glm-5.3', root/'bad')
            definition = json.loads((root/'bad/recipe.json').read_text())
            definition.update(name='bad', serverArgs=base + [flag, 'x'])
            (root/'bad/recipe.json').write_text(json.dumps(definition))
            with self.subTest(flag=flag), patch.object(tool, 'HERE', root), \
                    self.assertRaisesRegex(RuntimeError, 'placement'):
                tool.load_recipe('bad')

    def test_recipe_name_must_match_its_folder(self):
        root = self.copy_recipe('renamed')
        definition = json.loads((root/'renamed/recipe.json').read_text())
        definition['name'] = 'something-else'
        (root/'renamed/recipe.json').write_text(json.dumps(definition))
        with patch.object(tool, 'HERE', root), self.assertRaisesRegex(RuntimeError, 'folder'):
            tool.load_recipe('renamed')

    def test_unknown_recipe_is_rejected(self):
        self.config['recipe'] = 'missing-model'
        with self.assertRaisesRegex(RuntimeError, 'Unknown recipe'):
            tool.validate(self.config)

    def test_recipe_defaults_to_the_only_available_recipe(self):
        self.config.pop('recipe', None)
        self.assertEqual(tool.recipe_name(self.config), 'glm-5.3')

    def test_recipe_must_be_explicit_when_several_exist(self):
        root = self.copy_recipe('second-model')
        shutil.copytree(HERE/'glm-5.3', root/'glm-5.3')
        self.config.pop('recipe', None)
        with patch.object(tool, 'HERE', root), self.assertRaisesRegex(RuntimeError, 'Set recipe'):
            tool.recipe_name(self.config)

    def test_backend_values_take_model_settings_from_the_recipe(self):
        recipe = tool.Recipe(copy.deepcopy(self.config), self.tmp.name)
        definition = tool.load_recipe('glm-5.3')
        values = recipe.backend_values('serve')
        model = values['model']
        self.assertEqual(model['servedName'], definition['servedName'])
        self.assertEqual(model['endpointName'], definition['endpointName'])
        self.assertEqual(model['firstShard'], definition['firstShard'])
        self.assertEqual(model['canary'], definition['canary'])
        self.assertEqual(model['lock'], definition['lock'])
        self.assertEqual(model['args'][:len(definition['serverArgs'])], definition['serverArgs'])
        self.assertEqual(values['build']['revision'], definition['llamaCppRevision'])
        self.assertEqual(values['runtime']['env'], definition['runtimeEnv'])
        self.assertEqual(values['artifacts']['size'], definition['artifactsSize'])
        self.assertEqual(values['rpc']['cache']['size'], definition['rpcCacheSize'])

    def test_model_release_name_comes_from_the_recipe(self):
        recipe = tool.Recipe(copy.deepcopy(self.config), self.tmp.name)
        self.assertEqual(recipe.backend, self.config['releasePrefix'] + '-glm')

    def test_explicit_model_release_overrides_the_recipe_default(self):
        self.config['releases'] = {'model': 'custom-model'}
        recipe = tool.Recipe(copy.deepcopy(self.config), self.tmp.name)
        self.assertEqual(recipe.backend, 'custom-model')


if __name__ == '__main__':
    unittest.main()
