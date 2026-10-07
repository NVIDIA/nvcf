# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""One-node and split placements: values, rendered charts, preflight and recovery."""
import copy
import importlib.util
import json
import pathlib
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('recipe_topology', HERE/'recipe.py')
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)

GB10 = {'name': 'NVIDIA GB10', 'computeCapability': '12.1', 'memoryGiB': 121.6, 'unifiedMemory': True, 'cudaArchitectures': None}
GB300 = {'name': 'NVIDIA GB300', 'computeCapability': '10.3', 'memoryGiB': 268.0, 'unifiedMemory': False, 'cudaArchitectures': None}


def configuration(model, gpu, control='control'):
    config = json.loads((HERE/'config.example.json').read_text())
    config['nodes'] = {'control': control, 'model': list(model)}
    config['gpu'] = copy.deepcopy(gpu)
    config['containerd']['nodeNames'] = sorted({control, *model})
    return config


SHAPES = {
    'spark-split': configuration(['model-0', 'model-1'], GB10),
    'gb300-single': configuration(['agent'], GB300, control='server'),
    'gb300-split': configuration(['server', 'agent'], GB300, control='server'),
}


class TopologyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='recipe-topology-')
        self.addCleanup(self.tmp.cleanup)

    def recipe(self, shape):
        return tool.Recipe(copy.deepcopy(SHAPES[shape]), pathlib.Path(self.tmp.name)/shape)

    def test_spark_split_values_match_the_original_two_gb10_deployment(self):
        values = self.recipe('spark-split').backend_values('serve')
        self.assertEqual(values['targets'], [{'id': 'n0', 'node': 'model-0'}, {'id': 'n1', 'node': 'model-1'}])
        self.assertEqual(values['model']['args'][-4:], ['--device', 'CUDA0,RPC0', '--tensor-split', '1,1'])
        self.assertEqual(values['build']['cudaArchitectures'], '121a-real')
        self.assertEqual(values['gpu']['product'], 'NVIDIA-GB10')
        for section in (values['model']['resources'], values['rpc']['resources']):
            self.assertEqual((section['requests']['memory'], section['limits']['memory']), ('110Gi', '114Gi'))
        self.assertTrue(values['rpc']['cache']['enabled'])

    def test_gb300_single_node_values_use_only_the_local_gpu(self):
        values = self.recipe('gb300-single').backend_values('serve')
        self.assertEqual(values['targets'], [{'id': 'n0', 'node': 'agent'}])
        self.assertEqual(values['model']['args'][-2:], ['--device', 'CUDA0'])
        self.assertNotIn('--tensor-split', values['model']['args'])
        self.assertEqual(values['build']['cudaArchitectures'], '103a-real')
        self.assertEqual(values['gpu']['product'], 'NVIDIA-GB300')
        self.assertEqual((values['model']['resources']['requests']['memory'], values['model']['resources']['limits']['memory']),
                         ('32Gi', '64Gi'))
        self.assertFalse(values['rpc']['cache']['enabled'])

    def test_gb300_split_shares_the_leader_with_routing(self):
        recipe = self.recipe('gb300-split')
        values = recipe.backend_values('serve')
        self.assertEqual([t['node'] for t in values['targets']], ['server', 'agent'])
        self.assertEqual(recipe.c['nodes']['control'], 'server')
        self.assertEqual(values['model']['args'][-4:], ['--device', 'CUDA0,RPC0', '--tensor-split', '1,1'])

    def test_architecture_override_reaches_the_build(self):
        config = copy.deepcopy(SHAPES['gb300-single'])
        config['gpu']['cudaArchitectures'] = '103f'
        recipe = tool.Recipe(config, self.tmp.name)
        self.assertEqual(recipe.backend_values('build')['build']['cudaArchitectures'], '103f')

    def test_model_node_count_and_gpu_settings_are_validated(self):
        for model in ([], ['a', 'b', 'c']):
            with self.subTest(model=model), self.assertRaisesRegex(RuntimeError, '1 or 2'):
                tool.validate(configuration(model, GB300))
        with self.assertRaisesRegex(RuntimeError, 'computeCapability'):
            tool.validate(configuration(['a'], dict(GB300, computeCapability='103')))
        with self.assertRaisesRegex(RuntimeError, 'nodes.control and nodes.model'):
            config = configuration(['a'], GB300)
            config['nodes']['leader'] = 'a'
            tool.validate(config)
        tool.validate(configuration(['a', 'b'], GB300, control='a'))


class PreflightTests(TopologyTests):
    def record(self, gpu=GB300, available=100*1024**3, free=260*1024**3):
        return {'gpu': gpu['name'], 'capability': [int(x) for x in gpu['computeCapability'].split('.')],
                'memoryBefore': {'MemAvailable': available}, 'cudaFreeBytes': free, 'cudaTotalBytes': free}

    def test_matching_hardware_passes(self):
        self.recipe('gb300-single').check_preflight([self.record()])
        spark = self.recipe('spark-split')
        spark.check_preflight([self.record(GB10, available=114*1024**3, free=1)] * 2)

    def test_hardware_that_differs_from_the_configuration_is_rejected(self):
        recipe = self.recipe('gb300-single')
        cases = {
            'Every model node': [],
            'differs from gpu settings': [self.record(GB10)],
            'Insufficient host memory': [self.record(available=60*1024**3)],
            'Insufficient GPU memory': [self.record(free=200*1024**3)],
        }
        for message, records in cases.items():
            with self.subTest(message=message), self.assertRaisesRegex(RuntimeError, message):
                recipe.check_preflight(records)

    def test_unified_memory_skips_the_gpu_memory_check_but_not_host_memory(self):
        recipe = self.recipe('spark-split')
        with self.assertRaisesRegex(RuntimeError, 'more than 113 GiB'):
            recipe.check_preflight([self.record(GB10, available=112*1024**3, free=1)] * 2)

    def test_preflight_refuses_a_placement_that_cannot_fit(self):
        config = configuration(['model-0'], GB10)
        recipe = tool.Recipe(config, self.tmp.name)
        recipe.state = {'inventory': {'nodes': {}}}
        with patch.object(recipe, 'bound_cluster'), patch.object(recipe, 'helm_apply') as helm, \
                self.assertRaisesRegex(RuntimeError, 'does not fit on 1 node'):
            recipe.backend_phase('preflight')
        helm.assert_not_called()


class RecoveryTests(TopologyTests):
    def interrupt(self, shape):
        recipe = self.recipe(shape)
        recipe.state = {'registered': True}
        applied = []
        with patch.object(recipe, 'bound_cluster'), patch.object(recipe, 'verify'), \
             patch.object(recipe, 'helm_apply', side_effect=lambda release, chart, values, *a, **k: applied.append(copy.deepcopy(values))), \
             patch.object(recipe, 'forward', side_effect=RuntimeError('no endpoints')), \
             patch.object(tool, 'output', return_value=json.dumps({'items': []})), patch.object(tool.time, 'sleep'):
            recipe.recovery(True, 18443)
        return applied

    def test_split_recovery_stops_the_rpc_workers(self):
        down, restored = self.interrupt('spark-split')
        self.assertEqual(down['rpc']['replicas'], 0)
        self.assertNotIn('replicas', down['model'])
        self.assertNotIn('replicas', restored['rpc'])

    def test_single_node_recovery_stops_the_model_server(self):
        down, restored = self.interrupt('gb300-single')
        self.assertEqual(down['model']['replicas'], 0)
        self.assertNotIn('replicas', restored['model'])


@unittest.skipUnless(shutil.which('helm'), 'helm is required to render the backend chart')
class RenderedChartTests(TopologyTests):
    def render(self, shape, phase):
        values = self.recipe(shape).backend_values(phase, register=phase == 'serve', render=True)
        path = pathlib.Path(self.tmp.name)/(shape+'-'+phase+'.json')
        path.write_text(json.dumps(values))
        text = subprocess.run(['helm', 'template', 'llm-poc-glm', str(HERE/'charts/gguf-backend'), '-f', str(path)],
                              check=True, capture_output=True, text=True).stdout
        return [doc for doc in self.documents(text) if doc]

    @staticmethod
    def documents(text):
        import re
        return [chunk for chunk in re.split(r'^---\s*$', text, flags=re.M) if chunk.strip()]

    def names(self, documents, kind):
        import re
        found = []
        for doc in documents:
            if re.search(r'^kind: '+kind+r'$', doc, flags=re.M):
                found.append(re.search(r'^  name: (\S+)$', doc, flags=re.M).group(1))
        return found

    def test_single_node_serve_has_no_rpc_workers_or_rpc_caches(self):
        docs = self.render('gb300-single', 'serve')
        self.assertEqual(self.names(docs, 'Deployment'), ['llm-poc-glm-artifacts', 'llm-poc-glm'])
        self.assertEqual(self.names(docs, 'PersistentVolumeClaim'), ['llm-poc-glm-artifacts'])
        serve = next(doc for doc in docs if 'name: RPC_ENDPOINTS' in doc)
        self.assertIn('{name: RPC_ENDPOINTS, value: ""}', serve)
        endpoint = next(doc for doc in docs if 'kind: InferenceEndpoint' in doc)
        self.assertIn('gpu: {product: "NVIDIA-GB300"}', endpoint)

    def test_split_serve_runs_one_cached_rpc_worker_per_extra_node(self):
        docs = self.render('spark-split', 'serve')
        self.assertIn('llm-poc-glm-rpc-n1', self.names(docs, 'Deployment'))
        self.assertNotIn('llm-poc-glm-rpc-n0', self.names(docs, 'Deployment'))
        self.assertEqual(self.names(docs, 'PersistentVolumeClaim'), ['llm-poc-glm-artifacts', 'llm-poc-glm-rpc-cache-n1'])
        serve = next(doc for doc in docs if 'name: RPC_ENDPOINTS' in doc)
        self.assertIn('{name: RPC_ENDPOINTS, value: "llm-poc-glm-rpc-n1:50052"}', serve)

    def test_qualification_targets_every_model_gpu(self):
        for shape, endpoints in (('gb300-single', 'llm-poc-glm-rpc-n0:50052'),
                                 ('spark-split', 'llm-poc-glm-rpc-n0:50052,llm-poc-glm-rpc-n1:50052')):
            with self.subTest(shape=shape):
                docs = self.render(shape, 'qualify')
                qualify = next(doc for doc in docs if 'name: RPC_ENDPOINTS' in doc)
                self.assertIn('{name: RPC_ENDPOINTS, value: "'+endpoints+'"}', qualify)

    def test_build_job_name_changes_with_the_build_inputs(self):
        def job(shape, **gpu):
            if gpu:
                SHAPES[shape]['gpu'].update(gpu)
                self.addCleanup(SHAPES[shape]['gpu'].update, {key: GB300[key] for key in gpu})
            docs = self.render(shape, 'build')
            return [name for name in self.names(docs, 'Job') if '-build-' in name][0]
        first = job('gb300-single')
        self.assertEqual(job('gb300-single'), first)
        self.assertNotEqual(job('gb300-single', cudaArchitectures='103f'), first)

    def test_build_compiles_for_the_configured_architecture(self):
        docs = self.render('gb300-single', 'build')
        self.assertTrue(any('{name: CUDA_ARCHITECTURES, value: "103a-real"}' in doc for doc in docs))


if __name__ == '__main__':
    unittest.main()
