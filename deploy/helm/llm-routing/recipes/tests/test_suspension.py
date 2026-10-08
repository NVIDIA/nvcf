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
            yield release['model'] + '-' + release['profile'], ROOT/'charts/sglang', release['values']
        yield 'gguf', ROOT/'charts/gguf-backend', {'recipe': 'glm-5.3', 'nodes': ['model-0', 'model-1']}

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

    def test_suspend_withdraws_endpoint_stops_workloads_and_preserves_claims(self):
        for name, chart, values in self.cases():
            with self.subTest(profile=name):
                active = self.render(chart, values)
                suspended = self.render(chart, dict(values, suspended=True))
                self.assertEqual(active, self.render(chart, dict(values, suspended=False)))
                expected = copy.deepcopy(active)
                endpoints = [key for key in expected if key[0] == 'InferenceEndpoint']
                self.assertEqual(len(endpoints), 1)
                del expected[endpoints[0]]
                deployments = [obj for obj in expected.values() if obj['kind'] == 'Deployment']
                self.assertTrue(deployments)
                for obj in deployments:
                    self.assertGreater(obj['spec']['replicas'], 0)
                    obj['spec']['replicas'] = 0
                self.assertEqual(expected, suspended)
                claims = [obj for obj in suspended.values() if obj['kind'] == 'PersistentVolumeClaim']
                self.assertTrue(claims)
                for claim in claims:
                    self.assertEqual(claim['metadata']['annotations']['helm.sh/resource-policy'], 'keep')

    def test_removed_phase_settings_are_rejected(self):
        cases = list(self.cases())
        for name, chart, values in (cases[0], cases[-1]):
            for phase in ('preflight', 'build', 'qualify', 'chain', 'download', 'serve'):
                with self.subTest(chart=name, phase=phase):
                    self.assertIn('removed', self.render(chart, dict(values, phase=phase), accepted=False))
        name, chart, values = cases[0]
        self.assertIn('removed', self.render(chart, dict(values, mode='phased'), accepted=False))

    def test_suspend_requires_boolean(self):
        cases = list(self.cases())
        for name, chart, values in (cases[0], cases[-1]):
            for value in ('false', 'true', 0, 1, None, []):
                with self.subTest(chart=name, value=value):
                    error = self.render(chart, dict(values, suspended=value), accepted=False)
                    self.assertIn('suspended must be a boolean', error)


if __name__ == '__main__':
    unittest.main()
