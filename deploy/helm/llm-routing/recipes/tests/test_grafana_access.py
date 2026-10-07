# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(HERE))
import monitoring
import recipe as tool

try:
    import yaml
except ImportError:
    yaml = None


@unittest.skipUnless(yaml and shutil.which('helm'), 'Install requirements-monitoring.txt and Helm for chart tests')
class GrafanaAccessTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.tmp.cleanup)
        recipe = tool.Recipe(json.loads((HERE/'config.example.json').read_text()), cls.tmp.name)
        values = monitoring.chart_values(recipe)
        values['grafana']['adminPassword'] = 'test-only-admin-password'
        cls.values, cls.work = values, recipe.work
        path = recipe.work/'grafana-access-values.json'
        path.write_text(json.dumps(values))
        rendered = subprocess.check_output(['helm', 'template', 'access-test', str(monitoring.CHART), '-f', str(path)], text=True)
        cls.docs = [doc for doc in yaml.safe_load_all(rendered) if doc]
        grafana = next(doc for doc in cls.docs if doc['kind'] == 'Deployment' and doc['metadata']['name'] == 'access-test-grafana')
        cls.env = {entry['name']: entry for entry in grafana['spec']['template']['spec']['containers'][0]['env']}

    def test_anonymous_access_is_viewer_only_without_signup(self):
        self.assertEqual(self.env['GF_AUTH_ANONYMOUS_ENABLED']['value'], 'true')
        self.assertEqual(self.env['GF_AUTH_ANONYMOUS_ORG_ROLE']['value'], 'Viewer')
        self.assertEqual(self.env['GF_USERS_VIEWERS_CAN_EDIT']['value'], 'false')
        self.assertEqual(self.env['GF_USERS_ALLOW_SIGN_UP']['value'], 'false')

    def test_authenticated_admin_keeps_login_form_and_secret_credentials(self):
        self.assertEqual(self.env['GF_AUTH_DISABLE_LOGIN_FORM']['value'], 'false')
        secret = next(doc for doc in self.docs if doc['kind'] == 'Secret')
        for setting, key in [('GF_SECURITY_ADMIN_USER', 'admin-user'), ('GF_SECURITY_ADMIN_PASSWORD', 'admin-password')]:
            self.assertNotIn('value', self.env[setting])
            self.assertEqual(self.env[setting]['valueFrom']['secretKeyRef'], {'name': secret['metadata']['name'], 'key': key})
        self.assertEqual(secret['stringData'], {'admin-user': 'admin', 'admin-password': 'test-only-admin-password'})

    def render_with_ingress(self, ingress):
        values = copy.deepcopy(self.values)
        values['grafana']['ingress'] = ingress
        path = self.work/'grafana-ingress-values.json'
        path.write_text(json.dumps(values))
        return subprocess.run(['helm', 'template', 'access-test', str(monitoring.CHART), '-f', str(path)], capture_output=True, text=True)

    def test_dashboard_has_no_ingress_unless_enabled(self):
        self.assertEqual([doc for doc in self.docs if doc['kind'] == 'Ingress'], [])

    def test_enabled_ingress_routes_its_host_to_the_grafana_service(self):
        result = self.render_with_ingress({'enabled': True, 'host': 'grafana.demo.example', 'className': 'traefik'})
        self.assertEqual(result.returncode, 0, result.stderr)
        docs = [doc for doc in yaml.safe_load_all(result.stdout) if doc]
        ingress = next(doc for doc in docs if doc['kind'] == 'Ingress')
        self.assertEqual(ingress['spec']['ingressClassName'], 'traefik')
        [rule] = ingress['spec']['rules']
        self.assertEqual(rule['host'], 'grafana.demo.example')
        [path] = rule['http']['paths']
        self.assertEqual((path['path'], path['pathType']), ('/', 'Prefix'))
        backend = path['backend']['service']
        service = next(doc for doc in docs if doc['kind'] == 'Service' and doc['metadata']['name'] == backend['name'])
        self.assertEqual(service['spec']['selector']['app.kubernetes.io/component'], 'grafana')
        self.assertIn(backend['port']['name'], [port['name'] for port in service['spec']['ports']])

    def test_chart_rejects_an_enabled_ingress_without_a_host(self):
        result = self.render_with_ingress({'enabled': True, 'host': '', 'className': ''})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('grafana.ingress.host must name the dashboard host', result.stderr)


if __name__ == '__main__':
    unittest.main()
