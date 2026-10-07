# SPDX-License-Identifier: Apache-2.0
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
            nodes = [target['node'] for target in values['targets']] if 'targets' in values else values['nodes']
            self.assertEqual(len(nodes), profile['modelNodeCount'])
            self.assertEqual(len(set(nodes)), len(nodes))

    def test_phased_pins_and_profiles_match_catalog(self):
        models = {model['id']: model for model in json.loads((ROOT / 'catalog.json').read_text())['models']}
        for path, values in self.examples.items():
            if values.get('mode') != 'phased':
                continue
            with self.subTest(values=path.name):
                model = models[values['recipe']]
                profile = next(p for p in model['profiles'] if p['id'] == values['profileName'])
                self.assertEqual(values['model'], {key: model[key] for key in ('id', 'repository', 'revision', 'precision')})
                self.assertEqual(values['image'], model['image'])
                self.assertEqual(values['profile'], profile)
                self.assertEqual(values['phase'], 'qualify')
                if profile.get('fabric'):
                    self.assertEqual(len({target['address'] for target in values['targets']}), profile['nodes'])
                    self.assertTrue(all(target['interface'] for target in values['targets']))

    def test_source_and_packaged_charts_render_model_and_placement_overrides(self):
        for path, values in self.examples.items():
            recipe = self.recipes[values['recipe']]
            profile = next(p for p in recipe['profiles'] if p['id'] == values['profileName'])
            chart = profile['deployment']['chart']
            charts = [ROOT / chart['localPath'], ROOT.parent / 'dev-images/charts' / chart['archive']]
            phased = values.get('mode') == 'phased'
            phases = ('qualify', 'download', 'serve') if phased else (None,)
            nodes = ['selected-node-' + str(i + 1) for i in range(profile['modelNodeCount'])]
            overrides = []
            for i, node in enumerate(nodes):
                key = f'targets[{i}].node' if phased else f'nodes[{i}]'
                overrides += ['--set', key + '=' + node]
            for chart_path in charts:
                for phase in phases:
                    with self.subTest(values=path.name, chart=str(chart_path), phase=phase):
                        command = ['helm', 'template', 'values-test', str(chart_path), '--namespace', 'llm-stack',
                                   '--values', str(path), *overrides]
                        if phase:
                            command += ['--set', 'phase=' + phase]
                        result = subprocess.run(command, text=True, capture_output=True)
                        self.assertEqual(result.returncode, 0, result.stderr)
                        objects = [item for item in yaml.safe_load_all(result.stdout) if item]
                        endpoints = [item for item in objects if item['kind'] == 'InferenceEndpoint']
                        serving = phase in (None, 'serve')
                        self.assertEqual(len(endpoints), int(serving))
                        if serving:
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
                        if phased:
                            expected_kind = 'Deployment' if serving else 'Job'
                            self.assertEqual([item['kind'] for item in workloads], [expected_kind] * len(nodes))


if __name__ == '__main__':
    unittest.main()
