# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import base64
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
from test_monitoring import monitoring_values

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
        values = monitoring_values()
        values['grafana']['adminPassword'] = 'test-only-admin-password'
        cls.values, cls.work = values, pathlib.Path(cls.tmp.name)
        path = cls.work/'grafana-access-values.json'
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

    def test_root_url_is_unset_by_default(self):
        self.assertNotIn('GF_SERVER_ROOT_URL', self.env)

    def test_root_url_reaches_grafana(self):
        for root_url in ('%(protocol)s://%(domain)s:%(http_port)s/grafana/', 'https://example.com/grafana/'):
            with self.subTest(root_url=root_url):
                values = copy.deepcopy(self.values)
                values['grafana']['rootURL'] = root_url
                result = self.render_credentials(values)
                self.assertEqual(result.returncode, 0, result.stderr)
                grafana = next(doc for doc in yaml.safe_load_all(result.stdout)
                               if doc and doc['kind'] == 'Deployment' and doc['metadata']['name'] == 'test-monitor-grafana')
                env = {entry['name']: entry for entry in grafana['spec']['template']['spec']['containers'][0]['env']}
                self.assertEqual(env['GF_SERVER_ROOT_URL']['value'], root_url)

    def test_root_url_rejects_invalid_helm_values(self):
        for root_url in ('/grafana/', 'grafana.example.com/', 'https://example.com/grafana',
                         'https://example.com/a b/', 5, True):
            with self.subTest(root_url=root_url):
                values = copy.deepcopy(self.values)
                values['grafana']['rootURL'] = root_url
                result = self.render_credentials(values)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('grafana.rootURL must be a URL ending in /', result.stderr)

    def render_credentials(self, values, existing=None, upgrade=False):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            chart = root/'chart'
            shutil.copytree(monitoring.CHART, chart)
            if existing is not None:
                path = chart/'templates/grafana.yaml'
                source = path.read_text().replace('lookup "v1" "Secret" .Release.Namespace $name',
                    '('+json.dumps(json.dumps(existing))+' | fromJson)')
                path.write_text(source)
            path = root/'values.json'
            path.write_text(json.dumps(values))
            command = ['helm', 'template', 'test-monitor', str(chart), '-n', 'llm-stack', '-f', str(path)]
            if upgrade:
                command.append('--is-upgrade')
            return subprocess.run(command, capture_output=True, text=True)

    def test_default_credentials_are_generated_kept_and_reused_on_upgrade(self):
        first = self.render_credentials(monitoring_values())
        self.assertEqual(first.returncode, 0, first.stderr)
        secret = next(d for d in yaml.safe_load_all(first.stdout) if d and d['kind'] == 'Secret')
        self.assertEqual(secret['metadata']['name'], 'test-monitor-grafana-admin')
        self.assertEqual(secret['metadata']['annotations']['helm.sh/resource-policy'], 'keep')
        self.assertGreaterEqual(len(secret['stringData']['admin-password']), 40)
        secret['metadata']['annotations'].update({'meta.helm.sh/release-name': 'test-monitor',
                                                 'meta.helm.sh/release-namespace': 'llm-stack'})
        secret['data'] = {key: base64.b64encode(value.encode()).decode() for key, value in secret.pop('stringData').items()}
        upgraded = self.render_credentials(monitoring_values(), existing=secret, upgrade=True)
        self.assertEqual(upgraded.returncode, 0, upgraded.stderr)
        result = next(d for d in yaml.safe_load_all(upgraded.stdout) if d and d['kind'] == 'Secret')
        self.assertEqual(result['stringData']['admin-password'], base64.b64decode(secret['data']['admin-password']).decode())
        secret['metadata']['annotations']['meta.helm.sh/release-name'] = 'foreign'
        failed = self.render_credentials(monitoring_values(), existing=secret)
        self.assertNotEqual(failed.returncode, 0)
        self.assertIn('belongs to another Helm release', failed.stderr)

    def test_missing_upgrade_credentials_fail_instead_of_rotating(self):
        result = self.render_credentials(monitoring_values(), upgrade=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('restore it before upgrading', result.stderr)

    def test_explicit_external_secret_is_referenced_without_adoption(self):
        values = monitoring_values()
        values['grafana']['adminSecret'] = 'external-grafana'
        result = self.render_credentials(values)
        self.assertEqual(result.returncode, 0, result.stderr)
        objects = [d for d in yaml.safe_load_all(result.stdout) if d]
        self.assertFalse(any(d['kind'] == 'Secret' for d in objects))
        pod = next(d for d in objects if d['kind'] == 'Deployment' and d['metadata']['name'].endswith('-grafana'))
        env = {v['name']: v for v in pod['spec']['template']['spec']['containers'][0]['env']}
        self.assertEqual(env['GF_SECURITY_ADMIN_PASSWORD']['valueFrom']['secretKeyRef']['name'], 'external-grafana')

    def test_generated_credentials_lint_strictly(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'values.json'
            path.write_text(json.dumps(monitoring_values()))
            result = subprocess.run(['helm', 'lint', str(monitoring.CHART), '--strict', '-f', str(path)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout+result.stderr)

    def test_no_ingress_by_default(self):
        self.assertFalse(any(doc['kind'] == 'Ingress' for doc in self.docs))
        self.assertNotIn('GF_SERVER_SERVE_FROM_SUB_PATH', self.env)

    def test_ingress_serves_grafana_under_its_path(self):
        for path, host, ingress_class in (('/grafana', '', ''),
                                          ('/demo/metrics', 'demo.example.com', 'traefik')):
            with self.subTest(path=path):
                values = copy.deepcopy(self.values)
                root_url = 'https://demo.example.com:8443'+path+'/'
                values['grafana']['rootURL'] = root_url
                values['grafana']['ingress'].update(enabled=True, className=ingress_class, host=host, path=path)
                result = self.render_credentials(values)
                self.assertEqual(result.returncode, 0, result.stderr)
                docs = [doc for doc in yaml.safe_load_all(result.stdout) if doc]
                ingress = next(doc for doc in docs if doc['kind'] == 'Ingress')
                self.assertEqual(ingress['spec'].get('ingressClassName', ''), ingress_class)
                rule, = ingress['spec']['rules']
                self.assertEqual(rule.get('host', ''), host)
                self.assertEqual(rule['http']['paths'], [{
                    'path': path, 'pathType': 'Prefix',
                    'backend': {'service': {'name': 'test-monitor-grafana', 'port': {'name': 'http'}}}}])
                grafana = next(doc for doc in docs if doc['kind'] == 'Deployment' and doc['metadata']['name'].endswith('-grafana'))
                container = grafana['spec']['template']['spec']['containers'][0]
                env = {entry['name']: entry for entry in container['env']}
                self.assertEqual(env['GF_SERVER_ROOT_URL']['value'], root_url)
                self.assertEqual(env['GF_SERVER_SERVE_FROM_SUB_PATH']['value'], 'true')
                self.assertEqual(container['readinessProbe']['httpGet']['path'], path+'/api/health')
                collector = next(doc for doc in docs if doc['kind'] == 'ConfigMap' and doc['metadata']['name'].endswith('-collector'))
                jobs = yaml.safe_load(collector['data']['config.yaml'])['receivers']['prometheus']['config']['scrape_configs']
                scrape = next(job for job in jobs if job['job_name'] == 'monitoring-grafana')
                self.assertEqual(scrape['metrics_path'], path+'/metrics')
                self.assertEqual(scrape['static_configs'][0]['targets'], ['test-monitor-grafana:3000'])

    def test_ingress_requires_a_public_root_url_with_the_same_path(self):
        for root_url in ('', '%(protocol)s://%(domain)s:%(http_port)s/grafana/',
                         'https://demo.example.com/', 'https://demo.example.com/other/',
                         'https://demo.example.com/grafana/?q=/', 'https://demo.example.com/grafana/#fragment/'):
            with self.subTest(root_url=root_url):
                values = copy.deepcopy(self.values)
                values['grafana']['ingress']['enabled'] = True
                values['grafana']['rootURL'] = root_url
                result = self.render_credentials(values)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('grafana.rootURL', result.stderr)

    def test_ingress_rejects_invalid_values_before_installation(self):
        for field, value in (('path', '/grafana/'), ('path', '/'), ('path', '/a b'),
                             ('className', 'not a class'), ('className', 123),
                             ('host', 'https://example.com')):
            with self.subTest(field=field, value=value):
                values = copy.deepcopy(self.values)
                values['grafana']['rootURL'] = 'https://demo.example.com/grafana/'
                values['grafana']['ingress'].update(enabled=True, **{field: value})
                result = self.render_credentials(values)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('grafana.ingress.'+field, result.stderr)


if __name__ == '__main__':
    unittest.main()
