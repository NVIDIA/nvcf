# SPDX-License-Identifier: Apache-2.0
import ipaddress
import json
import pathlib
import subprocess
import unittest

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[1]
VALUES = ROOT / 'values'


class CommittedValuesTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.examples = {path: yaml.safe_load(path.read_text()) for path in VALUES.glob('*.yaml')}
        cls.recipes = {entry['id']: entry for entry in json.loads((ROOT / 'index.json').read_text())['recipes']}

    def test_examples_cover_exactly_the_deployable_profiles(self):
        expected = {(recipe['id'], profile['id']) for recipe in self.recipes.values()
                    if recipe['availability']['deployable'] for profile in recipe['profiles']}
        actual = [(values['recipe'], values['profileName']) for values in self.examples.values()]
        self.assertEqual(set(actual), expected)
        self.assertEqual(len(actual), len(expected))
        for values in self.examples.values():
            recipe = self.recipes[values['recipe']]
            profile = next(p for p in recipe['profiles'] if p['id'] == values['profileName'])
            self.assertEqual(len(values['nodes']), profile['modelNodeCount'])
            self.assertEqual(len(set(values['nodes'])), len(values['nodes']))

    def test_examples_use_bundled_profiles_and_required_site_capabilities(self):
        models = {model['id']: model for model in json.loads((ROOT / 'catalog.json').read_text())['models']}
        for path, values in self.examples.items():
            with self.subTest(values=path.name):
                for duplicated in ('model', 'image', 'profile', 'targets', 'phase'):
                    self.assertNotIn(duplicated, values)
                self.assertEqual(values.get('mode', 'automatic'), 'automatic')
                if values['recipe'] not in models:
                    continue
                model = models[values['recipe']]
                profile = next(p for p in model['profiles'] if p['id'] == values['profileName'])
                if not (profile.get('offload') or profile.get('fabric')):
                    continue
                facts = values['nodeCapabilities']
                self.assertEqual(set(facts), set(values['nodes']))
                if profile.get('offload'):
                    self.assertTrue(all(node['localNvme'] is True for node in facts.values()))
                if profile.get('fabric'):
                    self.assertEqual(len({node['fabric'] for node in facts.values()}), 1)
                    addresses = {str(ipaddress.IPv4Address(node['address'])) for node in facts.values()}
                    self.assertEqual(len(addresses), profile['nodes'])
                    for node in facts.values():
                        self.assertTrue(node['fabric'])
                        self.assertTrue(node['interface'])
                        self.assertGreaterEqual(node['linkGbps'], profile['minFabricGbps'])

    def chart_path(self, chart):
        return ROOT / chart['localPath']

    def check_automatic_install(self):
        for recipe_id in ('qwen3.8-27b', 'qwen3.8-27b-nvfp4', 'glm-5.3'):
            recipe = self.recipes[recipe_id]
            profile = recipe['profiles'][0]
            chart = profile['deployment']['chart']
            path = self.chart_path(chart)
            with self.subTest(recipe=recipe_id, chart=str(path)):
                command = ['helm', 'template', 'default-model', str(path), '--namespace', 'llm-stack',
                           '--set', 'recipe=' + recipe_id]
                for i in range(profile['modelNodeCount']):
                    command += ['--set', f'nodes[{i}]=selected-node-{i}']
                result = subprocess.run(command, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                objects = [item for item in yaml.safe_load_all(result.stdout) if item]
                endpoints = [item for item in objects if item['kind'] == 'InferenceEndpoint']
                self.assertEqual(len(endpoints), 1)
                self.assertEqual(endpoints[0]['spec']['modelName'], recipe['servedModelId'])
                claims = [item for item in objects if item['kind'] == 'PersistentVolumeClaim']
                self.assertTrue(claims)
                self.assertTrue(all(p['metadata']['annotations']['helm.sh/resource-policy'] == 'keep' for p in claims))

    def check_model_and_placement_overrides(self):
        for path, values in self.examples.items():
            recipe = self.recipes[values['recipe']]
            profile = next(p for p in recipe['profiles'] if p['id'] == values['profileName'])
            chart = profile['deployment']['chart']
            chart_path = self.chart_path(chart)
            nodes = ['selected-node-' + str(i + 1) for i in range(profile['modelNodeCount'])]
            overrides = []
            for i, node in enumerate(nodes):
                overrides += ['--set', f'nodes[{i}]=' + node]
                for key, fact in values.get('nodeCapabilities', {}).get(values['nodes'][i], {}).items():
                    value = str(fact).lower() if isinstance(fact, bool) else str(fact)
                    overrides += ['--set', f'nodeCapabilities.{node}.{key}={value}']
            with self.subTest(values=path.name, chart=str(chart_path)):
                command = ['helm', 'template', 'values-test', str(chart_path), '--namespace', 'llm-stack',
                           '--values', str(path), *overrides]
                result = subprocess.run(command, text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                objects = [item for item in yaml.safe_load_all(result.stdout) if item]
                endpoints = [item for item in objects if item['kind'] == 'InferenceEndpoint']
                self.assertEqual(len(endpoints), 1)
                self.assertEqual(endpoints[0]['spec']['modelName'], recipe['servedModelId'])
                workloads = [item for item in objects if item['kind'] in ('Deployment', 'Job')]
                self.assertTrue(workloads)
                placements = set()
                for workload in workloads:
                    pod = workload['spec']['template']['spec']
                    if 'nodeSelector' in pod:
                        placements.add(pod['nodeSelector']['kubernetes.io/hostname'])
                    else:
                        terms = pod['affinity']['nodeAffinity']['requiredDuringSchedulingIgnoredDuringExecution']['nodeSelectorTerms']
                        self.assertEqual(len(terms), 1)
                        fields = terms[0]['matchFields']
                        self.assertEqual(fields, [{'key': 'metadata.name', 'operator': 'In', 'values': fields[0]['values']}])
                        placements.update(fields[0]['values'])
                self.assertEqual(placements, set(nodes))
                if recipe['runtime']['backend'] == 'sglang':
                    self.assertEqual([item['kind'] for item in workloads], ['Deployment'] * len(nodes))
                    runtime = next(item for item in objects if item['kind'] == 'ConfigMap' and 'config.json' in item['data'])
                    config = json.loads(runtime['data']['config.json'])
                    self.assertIs(config['automatic'], True)
                    self.assertEqual(config['profile']['id'], values['profileName'])
                    self.assertEqual([target['node'] for target in config['targets']], nodes)

    def test_automatic_source_installs_without_values_files(self):
        self.check_automatic_install()

    def test_source_charts_render_model_and_placement_overrides(self):
        self.check_model_and_placement_overrides()


if __name__ == '__main__':
    unittest.main()
