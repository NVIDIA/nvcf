# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Server tuning in the saved configuration, its memory checks and the retune command."""
import copy
import importlib.util
import json
import os
import pathlib
import tempfile
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('recipe_tuning', HERE/'recipe.py')
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)

GB300 = {'name': 'NVIDIA GB300', 'computeCapability': '10.3', 'memoryGiB': 250.7, 'unifiedMemory': False, 'cudaArchitectures': None}
GLM = tool.load_recipe('glm-5.3')


def gb300_config():
    config = json.loads((HERE/'config.example.json').read_text())
    config['nodes'] = {'control': 'server', 'model': ['agent']}
    config['gpu'] = copy.deepcopy(GB300)
    config['containerd']['nodeNames'] = ['agent', 'server']
    return config


class TuningConfigurationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='recipe-tuning-')
        self.addCleanup(self.tmp.cleanup)

    def recipe(self, config):
        return tool.Recipe(copy.deepcopy(config), self.tmp.name)

    def args(self, config):
        args = self.recipe(config).backend_values('serve')['model']['args']
        return {flag: args[args.index(flag)+1] for flag in ('--ctx-size', '--parallel', '--batch-size', '--ubatch-size', '--predict')}

    def test_spark_arguments_are_the_recipe_arguments_then_unified_tuning_then_placement(self):
        config = json.loads((HERE/'config.example.json').read_text())
        args = self.recipe(config).backend_values('serve')['model']['args']
        self.assertEqual(args, GLM['serverArgs'] + tool.sizing.tuning_args(GLM['tuning']['unified']) +
                         ['--device', 'CUDA0,RPC0', '--tensor-split', '1,1'])

    def test_gb300_uses_the_discrete_defaults(self):
        self.assertEqual(self.args(gb300_config()), {'--ctx-size': '131072', '--parallel': '2', '--batch-size': '2048',
                                                     '--ubatch-size': '512', '--predict': '8192'})

    def test_saved_tuning_overrides_the_defaults_and_the_memory_plan(self):
        config = gb300_config()
        default = self.recipe(config).plan['gpuFreeGiB']
        config['tuning'] = {'contextPerSlot': 32768, 'slots': 1}
        self.assertEqual(self.args(config)['--ctx-size'], '32768')
        self.assertEqual(self.args(config)['--parallel'], '1')
        self.assertLess(self.recipe(config).plan['gpuFreeGiB'], default)

    def test_invalid_saved_tuning_is_rejected(self):
        cases = [
            ('may set only', {'ctxSize': 4096}),
            ('positive integer', {'slots': 0}),
            ('multiple of 256', {'contextPerSlot': 4000}),
            ('defaultMaxTokens must be smaller', {'contextPerSlot': 4096}),
        ]
        for message, tuning in cases:
            config = gb300_config()
            config['tuning'] = tuning
            with self.subTest(message=message), self.assertRaisesRegex(RuntimeError, message):
                tool.validate(config)


class PreflightTuningTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='recipe-tuning-preflight-')
        self.addCleanup(self.tmp.cleanup)

    def record(self, free):
        return {'gpu': 'NVIDIA GB300', 'capability': [10, 3], 'memoryBefore': {'MemAvailable': 700*1024**3},
                'cudaFreeBytes': free, 'cudaTotalBytes': 250*1024**3}

    def test_too_little_free_gpu_memory_reports_the_largest_context_that_fits(self):
        config = gb300_config()
        config['tuning'] = {'contextPerSlot': 262144}
        recipe = tool.Recipe(config, self.tmp.name)
        largest = tool.sizing.largest_context(recipe.definition, config['gpu'], 1, recipe.tuning, 249.0)
        self.assertTrue(0 < largest < 262144)
        with self.assertRaisesRegex(RuntimeError, 'Insufficient GPU memory: 249 GiB free.*262144 tokens per slot and 2 slots.*'
                                    'largest context that fits is ' + str(largest) + ' tokens per slot'):
            recipe.check_preflight([self.record(249*1024**3)])

    def test_preflight_refuses_a_context_that_cannot_fit_the_configured_gpu(self):
        config = gb300_config()
        config['tuning'] = {'contextPerSlot': 524288}
        recipe = tool.Recipe(config, self.tmp.name)
        recipe.state = {'inventory': {'nodes': {}}}
        with patch.object(recipe, 'bound_cluster'), patch.object(recipe, 'helm_apply') as helm, \
                self.assertRaisesRegex(RuntimeError, 'does not fit on 1 node.*largest context that fits'):
            recipe.backend_phase('preflight')
        helm.assert_not_called()

    def test_preflight_records_the_measured_free_gpu_memory(self):
        recipe = tool.Recipe(gb300_config(), self.tmp.name)
        recipe.state = {'inventory': {'nodes': {}}}
        with patch.object(recipe, 'bound_cluster'), patch.object(recipe, 'helm_apply'), \
                patch.object(recipe, 'logs', return_value=[self.record(249*1024**3)]):
            recipe.backend_phase('preflight')
        saved = json.loads((pathlib.Path(self.tmp.name)/'state.json').read_text())
        self.assertEqual(saved['preflightGpuFreeBytes'], 249*1024**3)
        self.assertTrue(saved['preflight'])


class RetuneTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='recipe-retune-')
        self.addCleanup(self.tmp.cleanup)
        self.config = gb300_config()

    def recipe(self, state, tuning=None):
        config = copy.deepcopy(self.config)
        if tuning is not None:
            config['tuning'] = tuning
        recipe = tool.Recipe(config, self.tmp.name)
        recipe.state = dict(state, runtimeSha256='a'*64)
        return recipe

    def retune(self, recipe, deployed, status='deployed'):
        outputs = []

        def output(command, **kwargs):
            outputs.append(command)
            if 'status' in command:
                return json.dumps({'name': recipe.backend, 'info': {'status': status}})
            return json.dumps(deployed)

        with patch.object(recipe, 'bound_cluster'), patch.object(recipe, 'helm_apply') as helm, \
                patch.object(tool, 'output', side_effect=output), patch.object(tool, 'run') as run:
            recipe.retune()
        return helm, run, outputs

    def old_values(self, recipe, register=True):
        values = recipe.backend_values(register=register)
        args = values['model']['args']
        args[args.index('--ctx-size')+1] = '2048'
        args[args.index('--parallel')+1] = '1'
        return values

    def test_retune_requires_a_loaded_model(self):
        recipe = self.recipe({'inventory': {'nodes': {}}, 'preflight': True})
        with patch.object(recipe, 'bound_cluster'), patch.object(recipe, 'helm_apply') as helm, \
                self.assertRaisesRegex(RuntimeError, 'Load the model first'):
            recipe.retune()
        helm.assert_not_called()

    def test_retune_belongs_to_the_installation_owner(self):
        recipe = self.recipe({'serve': True, 'attachedExisting': True})
        with patch.object(recipe, 'bound_cluster'), patch.object(recipe, 'helm_apply') as helm, \
                self.assertRaisesRegex(RuntimeError, 'loaded the model'):
            recipe.retune()
        helm.assert_not_called()

    def test_retune_checks_the_context_against_free_memory_measured_before_loading(self):
        recipe = self.recipe({'serve': True, 'preflightGpuFreeBytes': 240*1024**3}, {'contextPerSlot': 81920})
        self.assertTrue(recipe.plan['fits'])
        with patch.object(recipe, 'bound_cluster'), patch.object(recipe, 'helm_apply') as helm, \
                self.assertRaisesRegex(RuntimeError, 'Insufficient GPU memory: 240 GiB free.*largest context that fits'):
            recipe.retune()
        helm.assert_not_called()

    def test_retune_without_changes_leaves_the_model_running(self):
        recipe = self.recipe({'serve': True, 'registered': True, 'direct': True})
        helm, run, _ = self.retune(recipe, recipe.backend_values(register=True))
        helm.assert_not_called()
        run.assert_not_called()
        self.assertTrue(recipe.state['direct'])

    def test_retune_applies_new_tuning_and_keeps_the_endpoint_registered(self):
        recipe = self.recipe({'serve': True, 'registered': True, 'direct': True, 'gateway': True})
        helm, run, outputs = self.retune(recipe, self.old_values(recipe))
        self.assertEqual(outputs, [recipe.hm+['status', recipe.backend, '-o', 'json'],
                                   recipe.hm+['get', 'values', recipe.backend, '-o', 'json']])
        release, chart, values, timeout = helm.call_args.args
        self.assertEqual((release, chart, timeout), (recipe.backend, tool.HERE/'charts/gguf-backend', '70m'))
        self.assertEqual(values, recipe.backend_values(register=True))
        self.assertEqual(values['model']['args'][values['model']['args'].index('--ctx-size')+1], '131072')
        waited = [call.args[0] for call in run.call_args_list]
        self.assertEqual([command[-2] for command in waited],
                         ['--for=condition=Ready', '--for=condition=TransportReady', '--for=condition=Registered'])
        self.assertFalse(recipe.state['direct'])
        self.assertFalse(recipe.state['gateway'])
        evidence = list((pathlib.Path(self.tmp.name)/'evidence').glob('retune-*.json'))
        self.assertEqual(len(evidence), 1)
        record = json.loads(evidence[0].read_text())
        self.assertEqual(record['tuning'], recipe.tuning)
        self.assertIn('2048', record['previousArgs'])

    def test_retune_reapplies_after_a_failed_upgrade_with_the_same_values(self):
        # A timed-out upgrade leaves its values on a failed revision; a retry must apply them again.
        recipe = self.recipe({'serve': True, 'registered': True, 'direct': True})
        helm, _, _ = self.retune(recipe, recipe.backend_values(register=True), status='failed')
        self.assertEqual(helm.call_args.args[2], recipe.backend_values(register=True))
        self.assertFalse(recipe.state['direct'])

    def test_retune_before_registration_does_not_register(self):
        recipe = self.recipe({'serve': True})
        helm, run, _ = self.retune(recipe, self.old_values(recipe, register=False))
        self.assertFalse(helm.call_args.args[2]['model']['register'])
        run.assert_not_called()

    def test_retune_requires_the_serve_phase_release(self):
        recipe = self.recipe({'serve': True})
        with self.assertRaisesRegex(RuntimeError, 'serve phase'):
            self.retune(recipe, dict(self.old_values(recipe), phase='download'))

    def test_cli_runs_retune_with_the_saved_configuration(self):
        home = pathlib.Path(self.tmp.name)/'home'
        home.mkdir()
        config = dict(self.config, context='team-context')
        with patch.dict(os.environ, {'HOME': str(home), 'LLM_ROUTING_CONTEXT': 'team-context', 'XDG_STATE_HOME': ''}):
            tool.save(tool.default_work_dir('team-context')/'config.json', config)
            with patch.object(tool.Recipe, 'retune') as retune:
                tool.main(['retune'])
        retune.assert_called_once_with()


if __name__ == '__main__':
    unittest.main()
