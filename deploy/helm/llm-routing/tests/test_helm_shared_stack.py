# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import base64
import copy
import hashlib
import json
import pathlib
import py_compile
import re
import shutil
import subprocess
import tarfile
import tempfile
import unittest

import yaml

HERE = pathlib.Path(__file__).resolve().parents[1]
HELM = HERE.parent


def decode(secret, key):
    return base64.b64decode(secret['data'][key]).decode()


@unittest.skipUnless(shutil.which('helm'), 'Helm is required')
class SharedHelmTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix='helm-shared-')
        cls.root = pathlib.Path(cls.temporary.name)
        for name in ('llm-gateway-stack', 'llm-api-gateway', 'llm-request-router', 'pylon-operator'):
            shutil.copytree(HELM/name/name, cls.root/name/name,
                            ignore=shutil.ignore_patterns('charts', 'Chart.lock'))
        cls.chart = cls.root/'llm-routing/charts/shared-stack'
        shutil.copytree(HERE/'charts/shared-stack', cls.chart,
                        ignore=shutil.ignore_patterns('charts', 'Chart.lock'))
        for chart in (cls.root/'llm-gateway-stack/llm-gateway-stack', cls.chart):
            subprocess.run(['helm', 'dependency', 'build', '--skip-refresh', str(chart)], check=True, capture_output=True, text=True)
        cls.values = yaml.safe_load((cls.chart/'values.local.example.yaml').read_text())
        cls.values['operator']['watchNamespaces'] = ['test-models']
        cls.values['operator'].pop('installCRDs', None)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def render(self, values=None, *, chart=None, upgrade=False, api=True, fail=None):
        values_path = self.root/'test-values.json'
        values_path.write_text(json.dumps(values or self.values))
        command = ['helm', 'template', 'test-stack', str(chart or self.chart),
                   '--namespace', 'test-models', '-f', str(values_path)]
        if api:
            command.extend(['--api-versions', 'pylon.nvidia.com/v1alpha1'])
        if upgrade:
            command.append('--is-upgrade')
        result = subprocess.run(command, text=True, capture_output=True)
        if fail:
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(fail, result.stderr)
            return
        self.assertEqual(result.returncode, 0, result.stderr)
        return [obj for obj in yaml.safe_load_all(result.stdout) if obj]

    def lookup_chart(self, resources):
        """Replace only Helm's API reads in a disposable copy, never the source."""
        chart = self.root/'lookup-chart'
        if chart.exists():
            shutil.rmtree(chart)
        shutil.copytree(self.chart, chart)
        while archives := list(chart.rglob('*.tgz')):
            for archive in archives:
                subprocess.run(['tar', '-xzf', str(archive), '-C', str(archive.parent)], check=True)
                archive.unlink()
        fixture = {}
        for item in resources:
            metadata = item.get('metadata', {})
            fixture['/'.join((item['apiVersion'], item['kind'], metadata.get('namespace', ''), metadata.get('name', '')))] = item
        fixtures = json.dumps(json.dumps(fixture))
        (chart/'templates/_lookup-test.tpl').write_text(
            '{{- define "shared.test.lookup" -}}'
            '{{- $data := ' + fixtures + ' | fromJson -}}'
            '{{- index $data (join "/" .) | default dict | toJson -}}'
            '{{- end -}}')
        arg = r'(?:"[^"]*"|[.$][A-Za-z0-9_.]+)'
        pattern = re.compile(r'lookup\s+(' + arg + r')\s+(' + arg + r')\s+(' + arg + r')\s+(' + arg + r')')
        for path in chart.rglob('*'):
            if path.suffix not in ('.tpl', '.yaml'):
                continue
            source = path.read_text()
            path.write_text(pattern.sub(lambda match: '(include "shared.test.lookup" (list ' + ' '.join(match.groups()) + ') | fromJson)', source))
        return chart

    def installed(self):
        resources = self.render()
        for obj in resources:
            metadata = obj['metadata']
            metadata.setdefault('annotations', {}).update({
                'meta.helm.sh/release-name': 'test-stack',
                'meta.helm.sh/release-namespace': 'test-models'})
        return resources

    def test_empty_stack_is_gpu_and_model_free(self):
        resources = self.render()
        self.assertEqual(sum(item['kind'] == 'Deployment' for item in resources), 3)
        forbidden = {'InferenceEndpoint', 'PersistentVolumeClaim', 'PersistentVolume', 'RuntimeClass'}
        self.assertFalse(forbidden & {item['kind'] for item in resources})
        for item in resources:
            if item['kind'] != 'CustomResourceDefinition':
                self.assertNotIn('nvidia.com/gpu', json.dumps(item))
            if item['kind'] == 'Deployment':
                pod = item['spec']['template']['spec']
                self.assertNotIn('runtimeClassName', pod)
                self.assertNotIn('nodeSelector', pod)
        operator = next(item for item in resources if item['kind'] == 'Deployment' and any(
            '--watch-namespaces=test-models' in container.get('args', []) for container in item['spec']['template']['spec']['containers']))
        self.assertEqual(operator['metadata']['namespace'], 'test-models')

    def test_gateway_verification_hook_has_bounded_cpu_only_execution(self):
        resources = self.render()
        jobs = [item for item in resources if item['kind'] == 'Job']
        self.assertEqual(len(jobs), 1)
        job = jobs[0]
        annotations = job['metadata']['annotations']
        self.assertEqual(annotations['helm.sh/hook'], 'post-install,post-upgrade')
        self.assertEqual(set(annotations['helm.sh/hook-delete-policy'].split(',')), {'before-hook-creation', 'hook-succeeded'})
        self.assertEqual(job['spec']['backoffLimit'], 0)
        self.assertLessEqual(job['spec']['activeDeadlineSeconds'], 180)
        self.assertNotIn('ttlSecondsAfterFinished', job['spec'])
        pod = job['spec']['template']['spec']
        self.assertFalse(pod['automountServiceAccountToken'])
        self.assertNotIn('serviceAccountName', pod)
        self.assertNotIn('runtimeClassName', pod)
        self.assertNotIn('nvidia.com/gpu', json.dumps(pod))
        self.assertEqual(pod['restartPolicy'], 'Never')
        self.assertTrue(pod['securityContext']['runAsNonRoot'])
        container = pod['containers'][0]
        self.assertTrue(container['securityContext']['readOnlyRootFilesystem'])
        self.assertEqual(container['securityContext']['capabilities']['drop'], ['ALL'])
        self.assertEqual({volume['name'] for volume in pod['volumes']}, {'checks', 'ca', 'caller'})
        self.assertTrue(all(mount['readOnly'] for mount in container['volumeMounts']))
        script = next(item for item in resources if item['kind'] == 'ConfigMap' and item['metadata']['name'] == job['metadata']['name'])
        self.assertEqual(script['data']['verify-gateway.py'].strip(), (self.chart/'verify-gateway.py').read_text().strip())
        self.assertNotIn('helm.sh/hook', script['metadata'].get('annotations', {}))

    def test_verification_uses_selected_credentials_service_and_image(self):
        values = copy.deepcopy(self.values)
        values['callerKey'] = {'secretName': 'selected-caller'}
        values['gatewayStack']['tls'] = {'selfSigned': {'caName': 'selected-ca'}}
        gateway = values['gatewayStack']['llm-api-gateway']['llmApiGateway']
        gateway.update({'fullnameOverride': 'selected-gateway', 'service': {'port': 8443},
                        'nodeSelector': {'kubernetes.io/arch': 'amd64'}})
        values['operator']['trustBundle'] = {'configMap': 'selected-ca'}
        values['verification'] = {'image': {'repository': 'localhost/verifier', 'tag': 'test', 'pullPolicy': 'Never'},
                                  'imagePullSecrets': [{'name': 'pull-access'}], 'nodeSelector': {'kubernetes.io/hostname': 'verification-node'}}
        job = next(item for item in self.render(values) if item['kind'] == 'Job')
        pod = job['spec']['template']['spec']
        self.assertEqual(pod['nodeSelector'], {'kubernetes.io/hostname': 'verification-node'})
        self.assertEqual(pod['imagePullSecrets'], [{'name': 'pull-access'}])
        self.assertEqual(pod['containers'][0]['image'], 'localhost/verifier:test')
        self.assertEqual(pod['containers'][0]['imagePullPolicy'], 'Never')
        self.assertEqual(pod['containers'][0]['env'], [{'name': 'GATEWAY_URL', 'value': 'https://selected-gateway.test-models.svc:8443'}])
        volumes = {item['name']: item for item in pod['volumes']}
        self.assertEqual(volumes['ca']['configMap']['name'], 'selected-ca')
        self.assertEqual(volumes['caller']['secret']['secretName'], 'selected-caller')

    def test_generated_credentials_match_both_auth_files(self):
        secrets = {item['metadata']['name']: item for item in self.render() if item['kind'] == 'Secret'}
        caller = decode(secrets['llm-shared-caller-key'], 'api-key')
        token = decode(secrets['llm-shared-cluster-token'], 'cluster-token')
        keys = json.loads(decode(secrets['llm-gateway-stack-api-keys'], 'api-keys.json'))
        workers = yaml.safe_load(decode(secrets['llm-gateway-stack-worker-credentials'], 'credentials.yaml'))
        self.assertEqual(keys, {'keys': [{'id': 'stack-client', 'sha256': hashlib.sha256(caller.encode()).hexdigest()}]})
        self.assertEqual(workers, {'clusters': {'llm-stack': ['sha256:' + hashlib.sha256(token.encode()).hexdigest()]}})
        for name in ('llm-shared-caller-key', 'llm-shared-cluster-token', 'llm-gateway-stack-ca'):
            self.assertEqual(secrets[name]['metadata']['annotations']['helm.sh/resource-policy'], 'keep')
        self.assertNotEqual(caller, token)
        self.assertGreaterEqual(len(caller), 48)

    def test_upgrade_reuses_all_credentials_and_tls(self):
        first = self.installed()
        second = self.render(chart=self.lookup_chart(first), upgrade=True)
        before = {item['metadata']['name']: item['data'] for item in first if item['kind'] == 'Secret'}
        after = {item['metadata']['name']: item['data'] for item in second if item['kind'] == 'Secret'}
        self.assertEqual(after, before)

    def test_upgrade_rebuilds_missing_or_stale_authentication_from_source_credentials(self):
        original = self.installed()
        expected = {item['metadata']['name']: item['data'] for item in original if item['kind'] == 'Secret'}
        stale_hash = hashlib.sha256(b'obsolete-credential').hexdigest()
        for name, key, stale in (
                ('llm-gateway-stack-api-keys', 'api-keys.json',
                 {'keys': [{'id': 'stack-client', 'sha256': stale_hash}]}),
                ('llm-gateway-stack-worker-credentials', 'credentials.yaml',
                 {'clusters': {'llm-stack': ['sha256:' + stale_hash]}})):
            for state in ('missing', 'stale'):
                with self.subTest(secret=name, state=state):
                    resources = copy.deepcopy(original)
                    if state == 'missing':
                        resources = [obj for obj in resources if obj['metadata']['name'] != name]
                    else:
                        secret = next(obj for obj in resources if obj['metadata']['name'] == name)
                        secret['data'][key] = base64.b64encode(json.dumps(stale).encode()).decode()
                    rendered = self.render(chart=self.lookup_chart(resources), upgrade=True)
                    actual = {item['metadata']['name']: item['data'] for item in rendered if item['kind'] == 'Secret'}
                    self.assertEqual(actual, expected)

    def test_upgrade_rejects_foreign_owned_authentication(self):
        original = self.installed()
        for name in ('llm-gateway-stack-api-keys', 'llm-gateway-stack-worker-credentials'):
            with self.subTest(secret=name):
                resources = copy.deepcopy(original)
                secret = next(obj for obj in resources if obj['metadata']['name'] == name)
                secret['metadata']['annotations']['meta.helm.sh/release-name'] = 'other-stack'
                self.render(chart=self.lookup_chart(resources), upgrade=True,
                            fail='not owned by this Helm release')

    def test_upgrade_switches_to_selected_existing_credentials(self):
        original = self.installed()
        secrets = {item['metadata']['name']: item for item in original if item['kind'] == 'Secret'}
        for switch_caller, switch_transport in ((True, False), (False, True), (True, True)):
            with self.subTest(caller=switch_caller, transport=switch_transport):
                values = copy.deepcopy(self.values)
                resources = copy.deepcopy(original)
                caller = decode(secrets['llm-shared-caller-key'], 'api-key')
                token = decode(secrets['llm-shared-cluster-token'], 'cluster-token')
                external = []
                if switch_caller:
                    values['callerKey'] = {'existingSecret': 'external-caller'}
                    caller = 'selected-external-caller'
                    external.append(('external-caller', 'api-key', caller))
                if switch_transport:
                    values['clusterCredential'] = {'create': False}
                    values['operator']['credential'] = {'existingSecret': 'external-transport'}
                    token = 'selected-external-transport'
                    external.append(('external-transport', 'cluster-token', token))
                resources.extend({'apiVersion': 'v1', 'kind': 'Secret',
                                  'metadata': {'name': name, 'namespace': 'test-models'},
                                  'data': {key: base64.b64encode(value.encode()).decode()}}
                                 for name, key, value in external)
                rendered = self.render(values, chart=self.lookup_chart(resources), upgrade=True)
                after = {item['metadata']['name']: item for item in rendered if item['kind'] == 'Secret'}
                self.assertEqual(json.loads(decode(after['llm-gateway-stack-api-keys'], 'api-keys.json')),
                                 {'keys': [{'id': 'stack-client', 'sha256': hashlib.sha256(caller.encode()).hexdigest()}]})
                self.assertEqual(yaml.safe_load(decode(after['llm-gateway-stack-worker-credentials'], 'credentials.yaml')),
                                 {'clusters': {'llm-stack': ['sha256:' + hashlib.sha256(token.encode()).hexdigest()]}})
                for name, _, _ in external:
                    self.assertNotIn(name, after)

    def test_upgrade_missing_key_fails_instead_of_rotating(self):
        first = [item for item in self.installed() if item['metadata']['name'] != 'llm-shared-caller-key']
        self.render(chart=self.lookup_chart(first), upgrade=True, fail='required Secret llm-shared-caller-key is missing')

    def test_upgrade_missing_ca_fails_instead_of_rotating(self):
        first = [item for item in self.installed() if not (item['kind'] == 'Secret' and item['metadata']['name'] == 'llm-gateway-stack-ca')]
        self.render(chart=self.lookup_chart(first), upgrade=True, fail='TLS Secret llm-gateway-stack-ca is missing')

    def test_changed_tls_names_require_explicit_replacement(self):
        values = copy.deepcopy(self.values)
        values['gatewayStack']['tls'] = {'selfSigned': {'gatewayDnsNames': ['localhost', 'extra.example.org']}}
        self.render(values, chart=self.lookup_chart(self.installed()), upgrade=True, fail='TLS names changed')

    def test_foreign_owned_generated_secret_rejected(self):
        resources = self.installed()
        for obj in resources:
            if obj['metadata']['name'] == 'llm-shared-caller-key':
                obj['metadata']['annotations']['meta.helm.sh/release-name'] = 'other-stack'
        self.render(chart=self.lookup_chart(resources), fail='not owned by this Helm release')

    def test_existing_credentials_are_referenced_without_adoption(self):
        values = copy.deepcopy(self.values)
        values['callerKey'] = {'existingSecret': 'external-caller'}
        values['clusterCredential'] = {'create': False}
        values['operator']['credential'] = {'existingSecret': 'external-transport'}
        resources = [{'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': name, 'namespace': 'test-models'},
                      'data': {key: base64.b64encode(token.encode()).decode()}}
                     for name, key, token in [('external-caller', 'api-key', 'owned-outside-chart'),
                                              ('external-transport', 'cluster-token', 'transport-outside-chart')]]
        result = self.render(values, chart=self.lookup_chart(resources))
        names = {item['metadata']['name'] for item in result if item['kind'] == 'Secret'}
        self.assertNotIn('external-caller', names)
        self.assertNotIn('external-transport', names)
        job = next(item for item in result if item['kind'] == 'Job')
        caller = next(volume for volume in job['spec']['template']['spec']['volumes'] if volume['name'] == 'caller')
        self.assertEqual(caller['secret']['secretName'], 'external-caller')
        keys = next(item for item in result if item['metadata']['name'] == 'llm-gateway-stack-api-keys')
        self.assertEqual(json.loads(decode(keys, 'api-keys.json'))['keys'][0]['sha256'], hashlib.sha256(b'owned-outside-chart').hexdigest())

    def test_external_caller_hash_matches_trimmed_client_key(self):
        values = copy.deepcopy(self.values)
        values['callerKey'] = {'existingSecret': 'external-caller'}
        for token in ('caller-from-file\n', ' \tcaller-from-file\r\n'):
            with self.subTest(token=repr(token)):
                secret = {'apiVersion': 'v1', 'kind': 'Secret',
                          'metadata': {'name': 'external-caller', 'namespace': 'test-models'},
                          'data': {'api-key': base64.b64encode(token.encode()).decode()}}
                result = self.render(values, chart=self.lookup_chart([secret]))
                keys = next(item for item in result if item['metadata']['name'] == 'llm-gateway-stack-api-keys')
                self.assertEqual(json.loads(decode(keys, 'api-keys.json'))['keys'][0]['sha256'],
                                 hashlib.sha256(token.strip().encode()).hexdigest())
                self.assertFalse(any(item['kind'] == 'Secret' and item['metadata']['name'] == 'external-caller'
                                     for item in result))

    def test_watch_scope_must_equal_release_namespace(self):
        for watch in ([], ['other'], ['test-models', 'other']):
            with self.subTest(watch=watch):
                values = copy.deepcopy(self.values)
                values['operator']['watchNamespaces'] = watch
                self.render(values, fail='must contain exactly the Helm release namespace')

    def test_crd_default_is_owned_by_the_operator_chart(self):
        shared_values = yaml.safe_load((HERE/'charts/shared-stack/values.yaml').read_text())
        operator_values = yaml.safe_load((HELM/'pylon-operator/pylon-operator/values.yaml').read_text())
        self.assertNotIn('installCRDs', shared_values['operator'])
        self.assertIs(operator_values['installCRDs'], True)

    def test_default_true_creates_one_kept_crd_without_existing_api(self):
        resources = self.render(api=False)
        crds = [item for item in resources if item['kind'] == 'CustomResourceDefinition']
        self.assertEqual(len(crds), 1)
        self.assertEqual(crds[0]['metadata']['name'], 'inferenceendpoints.pylon.nvidia.com')
        self.assertEqual(crds[0]['metadata']['annotations']['helm.sh/resource-policy'], 'keep')

    def test_explicit_false_requires_an_existing_api(self):
        values = copy.deepcopy(self.values)
        values['operator']['installCRDs'] = False
        self.render(values, api=False, fail='no served InferenceEndpoint API')
        resources = self.render(values)
        self.assertFalse(any(item['kind'] == 'CustomResourceDefinition' for item in resources))

    def test_explicit_true_creates_one_crd_without_existing_api(self):
        values = copy.deepcopy(self.values)
        values['operator']['installCRDs'] = True
        resources = self.render(values, api=False)
        self.assertEqual(sum(item['kind'] == 'CustomResourceDefinition' for item in resources), 1)

    def test_owned_crd_upgrade_repairs_an_older_schema(self):
        resources = self.installed()
        crd = next(item for item in resources if item['kind'] == 'CustomResourceDefinition')
        version = next(item for item in crd['spec']['versions'] if item['name'] == 'v1alpha1')
        fields = version['schema']['openAPIV3Schema']['properties']['spec']['properties']
        expected_fields = copy.deepcopy(fields)
        fields.pop('health')
        fields.pop('maxEngineConcurrency')
        rendered = self.render(chart=self.lookup_chart(resources), upgrade=True)
        crds = [item for item in rendered if item['kind'] == 'CustomResourceDefinition']
        self.assertEqual(len(crds), 1)
        upgraded = next(item for item in crds[0]['spec']['versions'] if item['name'] == 'v1alpha1')
        self.assertEqual(upgraded['schema']['openAPIV3Schema']['properties']['spec']['properties'], expected_fields)
        self.assertEqual(crds[0]['metadata']['annotations']['helm.sh/resource-policy'], 'keep')

    def test_invalid_crd_management_modes_are_rejected(self):
        for mode in ('auto', 'always', 'true', 'false', 1):
            with self.subTest(mode=mode):
                values = copy.deepcopy(self.values)
                values['operator']['installCRDs'] = mode
                self.render(values, fail='installCRDs')

    def test_missing_or_placeholder_images_are_rejected(self):
        values = copy.deepcopy(self.values)
        values['operator']['image']['repository'] = 'registry.example.com/operator'
        self.render(values, fail='placeholder images cannot be installed')
        values['operator']['image']['repository'] = 'localhost/operator'
        values['operator']['image']['tag'] = ''
        self.render(values, fail='placeholder images cannot be installed')

    def test_external_tls_and_ca_are_referenced_without_adoption(self):
        values = copy.deepcopy(self.values)
        values['gatewayStack']['tls'] = {'selfSigned': {'enabled': False}}
        resources = [item for item in self.installed() if item['kind'] in ('Secret', 'ConfigMap')
                     and item['metadata']['name'] in ('llm-gateway-stack-ca', 'llm-gateway-stack-router-tls', 'llm-gateway-stack-gateway-tls')]
        for resource in resources:
            resource['metadata']['annotations'] = {'meta.helm.sh/release-name': 'external-pki'}
        rendered = self.render(values, chart=self.lookup_chart(resources))
        self.assertFalse({'llm-gateway-stack-ca', 'llm-gateway-stack-router-tls', 'llm-gateway-stack-gateway-tls'} &
                         {item['metadata']['name'] for item in rendered})
        self.render(values, chart=self.lookup_chart([]), fail='pre-create TLS Secret')

    def test_missing_or_incomplete_existing_credential_is_rejected(self):
        values = copy.deepcopy(self.values)
        values['callerKey'] = {'existingSecret': 'external-caller'}
        self.render(values, fail='required Secret external-caller is missing')
        malformed = {'apiVersion': 'v1', 'kind': 'Secret',
                     'metadata': {'name': 'external-caller', 'namespace': 'test-models'}, 'data': {}}
        self.render(values, chart=self.lookup_chart([malformed]), fail='missing api-key')

    def test_foreign_or_unowned_crd_requires_explicit_external_management(self):
        original = next(item for item in self.render(api=False) if item['kind'] == 'CustomResourceDefinition')
        for owner in (None, ('original-owner', 'original-namespace')):
            with self.subTest(owner=owner):
                crd = copy.deepcopy(original)
                if owner:
                    crd['metadata']['annotations'].update({'meta.helm.sh/release-name': owner[0],
                                                            'meta.helm.sh/release-namespace': owner[1]})
                chart = self.lookup_chart([crd])
                self.render(chart=chart, fail='not owned by this Helm release')
                values = copy.deepcopy(self.values)
                values['operator']['installCRDs'] = False
                rendered = self.render(values, chart=chart)
                self.assertFalse(any(item['kind'] == 'CustomResourceDefinition' for item in rendered))

    def test_external_management_rejects_an_incompatible_schema(self):
        crd = next(item for item in self.render(api=False) if item['kind'] == 'CustomResourceDefinition')
        crd['metadata']['annotations'].update({'meta.helm.sh/release-name': 'original-owner',
                                                'meta.helm.sh/release-namespace': 'original-namespace'})
        version = next(item for item in crd['spec']['versions'] if item['name'] == 'v1alpha1')
        version['schema']['openAPIV3Schema']['properties']['spec']['properties'].pop('health')
        values = copy.deepcopy(self.values)
        values['operator']['installCRDs'] = False
        self.render(values, chart=self.lookup_chart([crd]), fail='existing InferenceEndpoint CRD is incompatible')

    def test_crd_identity_changes_are_rejected(self):
        original = next(item for item in self.installed() if item['kind'] == 'CustomResourceDefinition')
        for manage in (False, True):
            for key, value in (('group', 'other.invalid'), ('scope', 'Cluster'), ('kind', 'OtherEndpoint')):
                with self.subTest(manage=manage, key=key):
                    crd = copy.deepcopy(original)
                    if key == 'kind':
                        crd['spec']['names'][key] = value
                    else:
                        crd['spec'][key] = value
                    values = copy.deepcopy(self.values)
                    values['operator']['installCRDs'] = manage
                    self.render(values, chart=self.lookup_chart([crd]), fail='existing InferenceEndpoint CRD is incompatible')

    def test_duplicate_secret_names_and_argument_overrides_are_rejected(self):
        values = copy.deepcopy(self.values)
        values['callerKey'] = {'secretName': 'llm-shared-cluster-token'}
        self.render(values, fail='Secret names must be distinct')
        values = copy.deepcopy(self.values)
        values['operator']['extraArgs'] = ['--watch-namespaces=other']
        self.render(values, fail='operator.extraArgs must be empty')

    def test_tls_bypass_is_rejected(self):
        values = copy.deepcopy(self.values)
        values['operator']['devInsecureTransport'] = True
        self.render(values, fail='verified gateway and router TLS is required')

    def test_strict_helm_lint(self):
        values = copy.deepcopy(self.values)
        values_path = self.root/'lint-values.json'
        values_path.write_text(json.dumps(values))
        result = subprocess.run(['helm', 'lint', str(self.chart), '--strict', '--namespace', 'test-models',
                                 '-f', str(values_path)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_packaged_verifier_excludes_generated_bytecode(self):
        chart = self.root/'bytecode-chart'
        shutil.copytree(self.chart, chart)
        source = chart/'verify-gateway.py'
        py_compile.compile(str(source), doraise=True)
        py_compile.compile(str(source), cfile=str(source.with_suffix('.pyc')), doraise=True)
        self.assertTrue(list(chart.rglob('*.pyc')))
        destination = self.root/'bytecode-package'
        destination.mkdir()
        result = subprocess.run(['helm', 'package', str(chart), '--destination', str(destination)],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(next(destination.glob('*.tgz'))) as archive:
            names = archive.getnames()
            self.assertFalse(any('__pycache__' in name.split('/') or name.endswith('.pyc') for name in names))
            verifier_path = next(name for name in names if name.endswith('/verify-gateway.py'))
            self.assertEqual(archive.extractfile(verifier_path).read(), source.read_bytes())

    def test_packaged_chart_contains_all_dependencies(self):
        package = self.root/'packages'
        package.mkdir(exist_ok=True)
        result = subprocess.run(['helm', 'package', str(self.chart), '--destination', str(package)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        resources = self.render(chart=next(package.glob('*.tgz')))
        self.assertEqual(sum(item['kind'] == 'Deployment' for item in resources), 3)


if __name__ == '__main__':
    unittest.main()
