# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
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
import spark

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
        recipe = spark.Recipe(json.loads((HERE/'config.example.json').read_text()), cls.tmp.name)
        values = monitoring.chart_values(recipe)
        values['grafana']['adminPassword'] = 'test-only-admin-password'
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


if __name__ == '__main__':
    unittest.main()
