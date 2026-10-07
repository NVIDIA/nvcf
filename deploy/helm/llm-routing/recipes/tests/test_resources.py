# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Optional pod resource overrides in the saved configuration."""
import copy
import importlib.util
import json
import pathlib
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('recipe_resources', HERE/'recipe.py')
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)

GB300 = {'name': 'NVIDIA GB300', 'computeCapability': '10.3', 'memoryGiB': 250.7, 'unifiedMemory': False, 'cudaArchitectures': None}


class ResourceOverrideTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='recipe-resources-')
        self.addCleanup(self.tmp.cleanup)
        self.config = json.loads((HERE/'config.example.json').read_text())

    def values(self, config, phase='serve'):
        return tool.Recipe(copy.deepcopy(config), self.tmp.name).backend_values(phase)

    def test_spark_split_defaults_are_unchanged_without_overrides(self):
        values = self.values(self.config)
        for section in (values['model']['resources'], values['rpc']['resources']):
            self.assertEqual((section['requests']['memory'], section['limits']['memory']), ('110Gi', '114Gi'))

    def test_override_applies_to_the_model_and_rpc_pods(self):
        self.config['resources'] = {
            'model': {'requests': {'memory': '40Gi'}, 'limits': {'memory': '96Gi', 'cpu': '16'}},
            'rpc': {'limits': {'memory': '120Gi'}},
        }
        values = self.values(self.config)
        model, rpc = values['model']['resources'], values['rpc']['resources']
        self.assertEqual(model['requests'], {'cpu': '4', 'memory': '40Gi', 'nvidia.com/gpu': 1})
        self.assertEqual(model['limits'], {'cpu': '16', 'memory': '96Gi', 'nvidia.com/gpu': 1})
        self.assertEqual(rpc['requests'], {'cpu': '2', 'memory': '110Gi', 'nvidia.com/gpu': 1})
        self.assertEqual(rpc['limits'], {'cpu': '8', 'memory': '120Gi', 'nvidia.com/gpu': 1})

    def test_rpc_override_does_not_change_preparation_phases(self):
        self.config['resources'] = {'rpc': {'limits': {'memory': '120Gi'}}}
        values = self.values(self.config, 'download')
        self.assertEqual(values['rpc']['resources']['limits']['memory'], '8Gi')

    def test_override_does_not_change_the_memory_checks(self):
        self.config['nodes']['model'] = ['model-0']
        self.config['gpu'] = GB300
        self.config['resources'] = {'model': {'limits': {'memory': '200Gi'}}}
        recipe = tool.Recipe(copy.deepcopy(self.config), self.tmp.name)
        self.assertEqual((recipe.plan['hostAvailableGiB'], recipe.plan['gpuFreeGiB']), (64, 242))
        self.assertEqual(recipe.backend_values('serve')['model']['resources']['limits']['memory'], '200Gi')

    def test_invalid_overrides_are_rejected(self):
        cases = [
            ('only model and rpc', {'gateway': {'limits': {'memory': '1Gi'}}}),
            ('only requests and limits', {'model': {'claims': {'memory': '1Gi'}}}),
            ('only cpu and memory', {'model': {'limits': {'ephemeral-storage': '1Gi'}}}),
            ('Kubernetes quantity', {'model': {'limits': {'memory': 'lots'}}}),
            ('Kubernetes quantity', {'model': {'limits': {'memory': 64}}}),
            ('must be an object', {'model': []}),
            ('finer than 1m', {'model': {'requests': {'cpu': '0.5m'}, 'limits': {'cpu': '1m'}}}),
            ('finer than 1m', {'rpc': {'limits': {'cpu': '0.0005'}}}),
            ('thousandths of a byte', {'model': {'limits': {'memory': '512m'}}}),
        ]
        for message, resources in cases:
            with self.subTest(resources=resources), self.assertRaisesRegex(RuntimeError, message):
                tool.validate(dict(self.config, resources=resources))

    def test_whole_millicpu_values_are_accepted(self):
        for cpu in ('1.1', '0.25', '250m', '16'):
            with self.subTest(cpu=cpu):
                tool.validate(dict(self.config, resources={'model': {'limits': {'cpu': cpu}}}))

    def test_request_above_the_merged_limit_is_rejected(self):
        for resources in ({'model': {'limits': {'memory': '100000Mi'}}}, {'rpc': {'limits': {'cpu': '1'}}}):
            with self.subTest(resources=resources), self.assertRaisesRegex(RuntimeError, 'exceeds limit'):
                self.values(dict(self.config, resources=resources))
        values = self.values(dict(self.config, resources={'model': {'requests': {'cpu': '500m'}, 'limits': {'memory': '120000Mi'}}}))
        self.assertEqual(values['model']['resources']['requests']['cpu'], '500m')

    def test_gpu_count_cannot_be_overridden(self):
        for kind in ('requests', 'limits'):
            with self.subTest(kind=kind), self.assertRaisesRegex(RuntimeError, 'nvidia.com/gpu'):
                tool.validate(dict(self.config, resources={'rpc': {kind: {'nvidia.com/gpu': 2}}}))


if __name__ == '__main__':
    unittest.main()
