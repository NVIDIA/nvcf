# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
spec = importlib.util.spec_from_file_location('runtime', ROOT / 'charts/sglang/files/runtime.py')
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


def profile_release(model_id, profile_id=None):
    """Build runtime/chart fixtures from the catalog without invoking a planner."""
    catalog = json.loads((ROOT / 'catalog.json').read_text())
    model = next(item for item in catalog['models'] if item['id'] == model_id)
    profile = next(item for item in model['profiles'] if profile_id is None or item['id'] == profile_id)
    nodes = ['spark-' + str(rank) for rank in range(profile['nodes'])]
    capabilities = {node: {'localNvme': True, 'fabric': 'test-fabric', 'linkGbps': 200,
                          'interface': 'enp1s0', 'address': '192.0.2.' + str(rank + 1)}
                    for rank, node in enumerate(nodes)}
    config = {'model': {key: model[key] for key in ('id', 'repository', 'revision', 'precision')},
              'profile': copy.deepcopy(profile), 'contextLength': 8192, 'concurrency': 1,
              'ports': {'http': 18000, 'rendezvous': 21000, 'bootstrap': 24000},
              'targets': [dict(capabilities[node], node=node) for node in nodes]}
    values = {'recipe': model_id, 'profileName': profile['id'], 'nodes': nodes,
              'nodeCapabilities': capabilities, 'sharedCAConfigMap': 'routing-ca',
              'runtimeClassName': 'nvidia', 'storageClassName': 'local-path',
              'contextLength': 8192, 'concurrency': 1, 'ports': config['ports']}
    return {'name': model_id.replace('.', '-'), 'model': model_id, 'profile': profile['id'],
            'nodes': nodes, 'values': values, 'config': config}



def all_profile_releases():
    catalog = json.loads((ROOT / 'catalog.json').read_text())
    return [profile_release(model['id'], profile['id'])
            for model in catalog['models'] for profile in model['profiles']]


class RuntimeTests(unittest.TestCase):
    def test_nvfp4_uses_checkpoint_quantization_and_bounded_spark_pools(self):
        values = profile_release('qwen3.8-27b-nvfp4')['config']
        args = runtime.command(values, '/snapshot', 0)
        self.assertEqual(values['model']['precision'], 'NVFP4')
        self.assertEqual(args[args.index('--served-model-name') + 1], 'qwen3.8-27b-nvfp4')
        self.assertEqual(args[args.index('--mem-fraction-static') + 1], '0.80')
        self.assertEqual(args[args.index('--max-running-requests') + 1], '1')
        self.assertEqual(args[args.index('--kv-cache-dtype') + 1], 'fp8_e4m3')
        self.assertNotIn('--quantization', args)
        self.assertNotIn('--speculative-algorithm', args)
        self.assertNotIn('--ple-offload-embedding', args)

    def test_served_name_matches_endpoint_and_checkpoint_is_local(self):
        for release in all_profile_releases():
            args = runtime.command(release['config'], '/cache/snapshot', 0)
            self.assertEqual(args[args.index('--served-model-name') + 1], release['model'])
            self.assertEqual(args[args.index('--model-path') + 1], '/cache/snapshot')
            self.assertEqual(args[args.index('--context-length') + 1], '8192')

    def test_distributed_ranks_match_topology(self):
        values = profile_release('qwen3.8-flash-next', 'spark-nvfp4-tp2')['config']
        for rank in (0, 1):
            args = runtime.command(values, '/snapshot', rank)
            self.assertEqual(args[args.index('--nnodes') + 1], '2')
            self.assertEqual(args[args.index('--node-rank') + 1], str(rank))
            self.assertEqual(args[args.index('--tp') + 1], '2')
            self.assertIn('--no-ple-offload-embedding', args)
            self.assertNotIn('--ple-offload-backend', args)

    def test_one_node_offload_is_file_backed(self):
        values = profile_release('qwen3.8-flash-next', 'spark-nvfp4-nvme')['config']
        args = runtime.command(values, '/snapshot', 0)
        self.assertEqual(args[args.index('--ple-offload-backend') + 1], 'file')
        self.assertNotIn('--nnodes', args)

    def test_27b_uses_spark_precision_and_memory_settings(self):
        values = profile_release('qwen3.8-27b')['config']
        args = runtime.command(values, '/snapshot', 0)
        self.assertEqual(values['model']['precision'], 'FP8')
        self.assertEqual(args[args.index('--mem-fraction-static') + 1], '0.80')
        self.assertEqual(args[args.index('--mamba-ssm-dtype') + 1], 'float32')
        self.assertNotIn('--speculative-algorithm', args)


class HelmTests(unittest.TestCase):
    def render(self, values):
        with tempfile.TemporaryDirectory() as d:
            p = pathlib.Path(d) / 'values.json'
            p.write_text(json.dumps(values))
            return subprocess.run(['helm', 'template', 'model-test', str(ROOT / 'charts/sglang'), '-n', 'llm-stack',
                                   '-f', str(p)], text=True, capture_output=True)

    def test_all_profiles_deploy_with_retained_caches_and_one_endpoint(self):
        for release in all_profile_releases():
            with self.subTest(profile=release['profile']):
                result = self.render(release['values'])
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.count('kind: PersistentVolumeClaim'), len(release['nodes']))
                self.assertEqual(result.stdout.count('kind: Deployment'), len(release['nodes']))
                self.assertEqual(result.stdout.count('kind: InferenceEndpoint'), 1)
                self.assertNotIn('kind: Secret', result.stdout)
                self.assertNotIn('kind: Job', result.stdout)
                self.assertIn('helm.sh/resource-policy: keep', result.stdout)
                self.assertIn('modelName: "' + release['model'] + '"', result.stdout)
                if len(release['nodes']) == 2:
                    self.assertIn('hostNetwork: true', result.stdout)
                    self.assertIn('NCCL_SOCKET_IFNAME', result.stdout)
                    self.assertIn('NCCL_IB_DISABLE', result.stdout)

    def test_invalid_topology_profile_and_workload_fail(self):
        original = profile_release('qwen3.8-27b')['values']
        for changes in ({'concurrency': 999}, {'nodes': []}, {'nodes': ['same', 'same']}, {'profileName': 'unknown'}):
            with self.subTest(changes=changes):
                self.assertNotEqual(self.render(dict(original, **changes)).returncode, 0)

    def test_can_render_without_registering(self):
        values = dict(profile_release('qwen3.8-27b')['values'], register=False)
        result = self.render(values)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('kind: InferenceEndpoint', result.stdout)


if __name__ == '__main__':
    unittest.main()
