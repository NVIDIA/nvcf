# SPDX-License-Identifier: Apache-2.0
import copy
import json
import pathlib
import subprocess
import tempfile
import unittest

import yaml

from test_recipes import ROOT, all_profile_releases


class SuspensionTests(unittest.TestCase):
    def cases(self):
        for release in all_profile_releases():
            yield release['model'] + '-' + release['profile'], ROOT/'charts/sglang', dict(release['values'], phase='serve')
        definition = json.loads((ROOT/'glm-5.3/recipe.json').read_text())
        lock = json.loads((ROOT/'glm-5.3/model.lock.json').read_text())
        image = json.loads((ROOT/'glm-5.3/profiles.json').read_text())['runtimeImage']
        for count in (1, 2):
            values = {'phase': 'serve', 'image': image,
                      'targets': [{'id': 'n'+str(i), 'node': 'model-'+str(i)} for i in range(count)],
                      'gpu': {'product': 'NVIDIA-GB10'},
                      'build': {'revision': definition['llamaCppRevision'], 'cudaArchitectures': '121a-real'},
                      'runtime': {'sha256': 'a'*64},
                      'model': {'lock': lock, 'register': True, 'firstShard': lock['files'][0]['rfilename'],
                                'servedName': definition['servedName'], 'endpointName': definition['endpointName'],
                                'canary': definition['canary'], 'args': definition['serverArgs']},
                      'rpc': {'cache': {'enabled': count > 1}},
                      'chain': {'runtimeRelease': 'suspend-test', 'artifactClaim': 'suspend-test-artifacts'}}
            yield 'gguf-' + str(count), ROOT/'charts/gguf-backend', values

    def render(self, chart, values, accepted=True):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'values.json'
            path.write_text(json.dumps(values))
            result = subprocess.run(['helm', 'template', 'suspend-test', str(chart), '-n', 'model-test', '-f', str(path)],
                                    capture_output=True, text=True)
        if not accepted:
            self.assertNotEqual(result.returncode, 0)
            return result.stderr
        self.assertEqual(result.returncode, 0, result.stderr)
        return {(doc['kind'], doc['metadata']['name']): doc for doc in yaml.safe_load_all(result.stdout) if doc}

    def test_suspend_and_resume_change_only_gpu_deployment_replicas(self):
        for name, chart, values in self.cases():
            with self.subTest(profile=name):
                active = self.render(chart, values)
                suspended = self.render(chart, dict(values, suspended=True))
                resumed = self.render(chart, dict(values, suspended=False))
                self.assertEqual(active, resumed)
                self.assertEqual(active.keys(), suspended.keys())
                expected = copy.deepcopy(active)
                gpu_deployments = []
                for key, obj in expected.items():
                    if obj['kind'] != 'Deployment':
                        continue
                    containers = obj['spec']['template']['spec']['containers']
                    if any(int(container.get('resources', {}).get('requests', {}).get('nvidia.com/gpu', 0)) > 0 for container in containers):
                        self.assertGreater(obj['spec']['replicas'], 0)
                        obj['spec']['replicas'] = 0
                        gpu_deployments.append(key)
                    else:
                        self.assertEqual(suspended[key]['spec']['replicas'], obj['spec']['replicas'])
                self.assertTrue(gpu_deployments)
                self.assertEqual(expected, suspended)
                claims = [key for key in active if key[0] == 'PersistentVolumeClaim']
                self.assertTrue(claims)
                for key in claims:
                    self.assertEqual(suspended[key]['metadata']['annotations']['helm.sh/resource-policy'], 'keep')
                    self.assertEqual(active[key], suspended[key])
                endpoints = [key for key in active if key[0] == 'InferenceEndpoint']
                self.assertEqual(len(endpoints), 1)
                self.assertEqual(active[endpoints[0]], suspended[endpoints[0]])
                if name.startswith('gguf-'):
                    self.assertEqual(len(gpu_deployments), len(values['targets']))
                    self.assertEqual(suspended[('Deployment', 'suspend-test-artifacts')]['spec']['replicas'], 1)

    def test_suspend_requires_serve_phase(self):
        for name, chart, values in self.cases():
            phases = ('preflight', 'build', 'qualify', 'chain', 'download') if name.startswith('gguf-') else ('qualify', 'download')
            for phase in phases:
                with self.subTest(profile=name, phase=phase):
                    error = self.render(chart, dict(values, phase=phase, suspended=True), accepted=False)
                    self.assertIn('suspended is supported only in phase serve', error)

    def test_suspend_requires_boolean(self):
        cases = list(self.cases())
        for name, chart, values in (cases[0], cases[-1]):
            for value in ('false', 'true', 0, 1, None, []):
                with self.subTest(chart=name, value=value):
                    error = self.render(chart, dict(values, suspended=value), accepted=False)
                    self.assertIn('suspended must be a boolean', error)


if __name__ == '__main__':
    unittest.main()
