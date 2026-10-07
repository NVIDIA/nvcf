# SPDX-License-Identifier: Apache-2.0
import copy
import json
import os
import pathlib
import subprocess
import tempfile
import unittest

import jsonschema
import yaml

from test_recipes import ROOT, all_profile_releases

REPO = pathlib.Path(os.environ.get('RECIPE_REPO_ROOT', ROOT.parents[3]))


class SchemaTests(unittest.TestCase):
    def test_endpoint_against_real_operator_crd(self):
        crd_path = REPO / 'src/compute-plane-services/pylon-operator/config/crd/bases/pylon.nvidia.com_inferenceendpoints.yaml'
        crd = yaml.safe_load(crd_path.read_text())
        schema = next(v['schema']['openAPIV3Schema'] for v in crd['spec']['versions'] if v['name'] == 'v1alpha1')
        # Reject unknown properties before Kubernetes prunes them from the endpoint spec.
        schema = copy.deepcopy(schema)
        def close_objects(value):
            if not isinstance(value, dict):
                return
            if value.get('type') == 'object' and 'properties' in value:
                value['additionalProperties'] = False
            for child in value.get('properties', {}).values():
                close_objects(child)
            close_objects(value.get('items'))
        close_objects(schema['properties']['spec'])
        releases = all_profile_releases()
        for release in releases:
            with self.subTest(profile=release['profile']), tempfile.TemporaryDirectory() as d:
                p = pathlib.Path(d) / 'values.json'
                p.write_text(json.dumps(release['values']))
                rendered = subprocess.check_output(['helm', 'template', release['name'], str(ROOT / 'charts/sglang'),
                    '-n', 'llm-gateway', '-f', str(p), '--set', 'phase=serve'], text=True)
                docs = list(yaml.safe_load_all(rendered))
                endpoint = next(v for v in docs if v['kind'] == 'InferenceEndpoint')
                jsonschema.Draft7Validator(schema).validate(endpoint)
                service = next(v for v in docs if v['kind'] == 'Service')
                head = next(v for v in docs if v['kind'] == 'Deployment' and v['spec']['template']['metadata']['labels']['recipe-rank'] == '0')
                self.assertEqual(service['spec']['selector']['recipe-rank'], '0')
                self.assertEqual(endpoint['spec']['service']['name'], service['metadata']['name'])
                self.assertEqual(endpoint['spec']['maxEngineConcurrency'], release['values']['concurrency'])
                selected = [v for v in docs if v['kind'] == 'Deployment' and all(
                    v['spec']['template']['metadata']['labels'].get(k) == val for k, val in service['spec']['selector'].items())]
                self.assertEqual(selected, [head])
                # SGLang health generates a token and polls at one-second intervals.
                for deployment in (v for v in docs if v['kind'] == 'Deployment'):
                    container = deployment['spec']['template']['spec']['containers'][0]
                    for probe in ('startupProbe', 'readinessProbe'):
                        self.assertGreaterEqual(container[probe].get('timeoutSeconds', 1), 5)


if __name__ == '__main__':
    unittest.main()
