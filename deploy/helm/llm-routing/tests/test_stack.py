# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import base64
import contextlib
import copy
import hashlib
import importlib.util
import io
import json
import pathlib
import shutil
import tempfile
import unittest
import urllib.error
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('shared_routing_stack', HERE/'stack.py')
stack = importlib.util.module_from_spec(spec)
spec.loader.exec_module(stack)


class StackTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='shared-stack-test-')
        self.addCleanup(self.tmp.cleanup)
        self.work = pathlib.Path(self.tmp.name).resolve()
        self.config = {'context': 'test-context', 'namespace': 'isolated-models', 'clusterId': 'isolated-cluster',
                       'stackRelease': 'test-stack', 'operatorRelease': 'test-operator',
                       'controlNode': 'cpu-node', 'caConfigMap': 'test-ca', 'installCRDs': False,
                       'images': {name: {'repository': 'registry.example.com/test/' + name,
                                        'tag': 'test-pinned', 'pullPolicy': 'Never'} for name in stack.COMPONENTS}}
        self.key_path = self.work/'existing-caller-key'
        self.key_path.write_text('test-key\n')
        self.ca = 'test-public-ca'
        self.node = {'metadata': {'name': self.config['controlNode'], 'uid': 'cpu-node-uid'},
                     'status': {'conditions': [{'type': 'Ready', 'status': 'True'}],
                                'allocatable': {'cpu': '4', 'memory': '8Gi'}}}
        self.connection = {key: self.config[key] for key in ('context', 'namespace', 'clusterId', 'stackRelease',
                                                           'operatorRelease', 'controlNode', 'caConfigMap')}
        self.connection.update(schemaVersion=1, kind='llm-stack-connection', controlNodeUID=self.node['metadata']['uid'],
                               apiKeyFile=str(self.key_path), apiKeySHA256=hashlib.sha256(b'test-key').hexdigest(),
                               caSHA256=hashlib.sha256(self.ca.encode()).hexdigest())
        self.resources = {}
        for index, (key, release) in enumerate(stack.connection_resources(self.connection).items()):
            metadata = {'name': key.split('/', 1)[1], 'uid': 'resource-' + str(index)}
            if release:
                metadata.update(namespace=self.config['namespace'], annotations={
                    'meta.helm.sh/release-name': release,
                    'meta.helm.sh/release-namespace': self.config['namespace']})
            resource = {'metadata': metadata}
            if key.startswith('deployment/'):
                metadata['generation'] = 2
                resource.update(spec={'template': {'spec': {'containers': [{'args': []}]}}},
                                status={'observedGeneration': 2, 'availableReplicas': 1})
            self.resources[key] = resource
        self.crd = self.resources['customresourcedefinition/' + stack.CRD]
        self.crd['metadata']['annotations'] = {'meta.helm.sh/release-name': 'original-operator',
                                               'meta.helm.sh/release-namespace': 'original-models'}
        self.crd['spec'] = {'group': 'pylon.nvidia.com', 'scope': 'Namespaced', 'names': {'kind': 'InferenceEndpoint'},
                            'versions': [{'name': 'v1alpha1', 'served': True, 'schema': {'openAPIV3Schema': {
                                'properties': {'spec': {'properties': {field: {} for field in (
                                    'modelName', 'service', 'inferenceAPIFormat', 'health', 'maxEngineConcurrency')}}}}}}]}
        self.operator = self.resources['deployment/' + self.config['operatorRelease']]
        self.operator['spec']['template']['spec']['containers'][0]['args'] = [
            '--pylon-image=registry.example.com/test/pylon:test-pinned',
            '--cluster-id=' + self.config['clusterId'], '--watch-namespaces=' + self.config['namespace'],
            '--router-grpc-address=http://llm-request-router.' + self.config['namespace'] + '.svc.cluster.local:50071',
            '--trust-bundle-configmap=' + self.config['caConfigMap']]
        self.resources['configmap/' + self.config['caConfigMap']]['data'] = {'ca.crt': self.ca}
        self.connection['resources'] = {key: value['metadata']['uid'] for key, value in self.resources.items()}
        self.other_operator = copy.deepcopy(self.operator)
        self.other_operator['metadata'].update(name='original-operator', namespace='original-models')
        self.other_operator['spec']['template']['spec']['containers'][0]['args'][2] = '--watch-namespaces=original-models'
        self.watchers = [self.other_operator, self.operator]
        self.namespace_exists = False
        self.crd_exists = True
        self.commands = []
        self.applied = []
        self.instance = stack.Stack(self.config, self.work)

    def get(self, config, kind, name=None):
        self.assertEqual(config['context'], self.config['context'])
        self.assertEqual(config['namespace'], self.config['namespace'])
        if kind == 'node':
            self.assertEqual(name, self.node['metadata']['name'])
            return copy.deepcopy(self.node)
        if kind == 'nodes':
            return {'items': [copy.deepcopy(self.node)]}
        if kind == 'namespaces':
            return {'items': [self.resources['namespace/' + self.config['namespace']]] if self.namespace_exists else []}
        if kind == 'crds':
            return {'items': [copy.deepcopy(self.crd)] if self.crd_exists else []}
        if kind == 'secret':
            self.assertEqual(name, self.config['operatorRelease'] + '-cluster-credential')
            return {'data': {'cluster-token': base64.b64encode(b'test-cluster-token').decode()}}
        return copy.deepcopy(self.resources[kind + '/' + name])

    def command(self, command):
        self.commands.append(command)
        if command[0] == 'kubectl':
            self.assertEqual(command, stack.kube(self.config, 'get', 'deployments', '-A', '-o', 'json'))
            return json.dumps({'items': self.watchers})
        if command[1:4] == ['dependency', 'build', '--skip-refresh']:
            return ''
        self.assertEqual(command[:5], ['helm', '--kube-context', self.config['context'], '-n', self.config['namespace']])
        self.assertEqual(command[5:7], ['upgrade', '--install'])
        self.applied.append((command[7], json.loads(pathlib.Path(command[command.index('-f') + 1]).read_text())))
        self.namespace_exists = True
        self.crd_exists = True
        return ''

    @contextlib.contextmanager
    def cluster(self):
        with patch.object(stack, 'get', side_effect=self.get), patch.object(stack, 'run', side_effect=self.command), \
             contextlib.redirect_stdout(io.StringIO()):
            yield

    def install(self):
        with self.cluster():
            self.instance.install()

    def test_work_directory_inside_checkout_is_rejected_before_disk_creation(self):
        alias = self.work/'checkout-link'
        alias.symlink_to(stack.REPO, target_is_directory=True)
        for path in (stack.REPO, stack.REPO/'uncreated-stack-test-work'/'private', alias/'uncreated-stack-test-work'):
            with self.subTest(path=path), patch.object(pathlib.Path, 'mkdir') as mkdir, \
                 self.assertRaisesRegex(ValueError, 'outside the checkout'):
                stack.Stack(self.config, path)
            mkdir.assert_not_called()

    def test_config_accepts_cpu_only_routing_without_any_model_fields(self):
        self.assertEqual(stack.validate_config(self.config), self.config)
        for field in ('nodes', 'model', 'runtimeClass', 'runtimeImage', 'storageClass'):
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'Model configuration'):
                stack.validate_config(dict(self.config, **{field: 'model-specific'}))

    def test_config_requires_explicit_crd_policy_and_pinned_images(self):
        for value in (None, 0, 'false'):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, 'installCRDs'):
                stack.validate_config(dict(self.config, installCRDs=value))
        invalid = copy.deepcopy(self.config)
        invalid['images']['gateway']['tag'] = 'latest'
        with self.assertRaisesRegex(ValueError, 'Pin images.gateway'):
            stack.validate_config(invalid)

    def test_distinct_releases_and_valid_namespace_required(self):
        for change in ({'namespace': '-bad'}, {'stackRelease': self.config['operatorRelease']}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                stack.validate_config(dict(self.config, **change))

    def test_reuse_crd_accepts_foreign_owner_but_rejects_incompatible_schema(self):
        stack.check_crd(self.crd)
        for field in ('modelName', 'service', 'inferenceAPIFormat', 'health', 'maxEngineConcurrency'):
            changed = copy.deepcopy(self.crd)
            del changed['spec']['versions'][0]['schema']['openAPIV3Schema']['properties']['spec']['properties'][field]
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'required model registration'):
                stack.check_crd(changed)
        changed = copy.deepcopy(self.crd)
        changed['spec']['versions'][0]['served'] = False
        with self.assertRaisesRegex(ValueError, 'must be served'):
            stack.check_crd(changed)

    def test_overlapping_or_global_operator_is_rejected_but_other_namespace_is_safe(self):
        stack.check_watchers(self.watchers, self.config['namespace'], self.config['namespace'], self.config['operatorRelease'])
        for watches in ([], ['--watch-namespaces='], ['--watch-namespaces=original-models,' + self.config['namespace']],
                        ['--watch-namespaces', self.config['namespace']]):
            other = copy.deepcopy(self.other_operator)
            other['spec']['template']['spec']['containers'][0]['args'] = ['--pylon-image=test/image:pinned', *watches]
            with self.subTest(watches=watches), self.assertRaisesRegex(ValueError, 'Another Pylon operator'):
                stack.check_watchers([other], self.config['namespace'], self.config['namespace'], self.config['operatorRelease'])

    def test_connection_load_requires_full_uid_and_fingerprint_binding(self):
        path = self.work/'connection.json'
        stack.save(path, self.connection)
        self.assertEqual(stack.load_connection(path), self.connection)
        for field, value in [('resources', {}), ('caSHA256', 'bad'), ('apiKeySHA256', ''), ('apiKeyFile', 'relative-key')]:
            with self.subTest(field=field), self.assertRaises(ValueError):
                stack.validate_connection(dict(self.connection, **{field: value}))

    def test_inspection_is_read_only_and_accepts_no_registered_models(self):
        before = copy.deepcopy(self.resources)
        with self.cluster():
            inspected = stack.inspect_connection(self.connection)
        self.assertEqual(inspected, {'nodes': {self.config['controlNode']: self.node['metadata']['uid']},
                                     'ca': self.ca, 'resources': self.connection['resources']})
        self.assertEqual(self.resources, before)
        self.assertEqual(self.applied, [])
        self.assertTrue(all(command[0] == 'kubectl' and 'get' in command for command in self.commands))

    def test_inspection_rejects_every_replaced_shared_resource(self):
        for key in self.resources:
            original = self.resources[key]['metadata']['uid']
            self.resources[key]['metadata']['uid'] = 'replacement'
            with self.subTest(key=key), self.cluster(), self.assertRaisesRegex(ValueError, 'Shared resource was replaced'):
                stack.inspect_connection(self.connection)
            self.resources[key]['metadata']['uid'] = original

    def test_inspection_rejects_changed_owner_even_when_uid_is_preserved(self):
        for field in ('meta.helm.sh/release-name', 'meta.helm.sh/release-namespace'):
            metadata = self.resources['deployment/llm-api-gateway']['metadata']
            original = metadata['annotations'][field]
            metadata['annotations'][field] = 'another-owner'
            with self.subTest(field=field), self.cluster(), self.assertRaisesRegex(ValueError, 'Unexpected Helm owner'):
                stack.inspect_connection(self.connection)
            metadata['annotations'][field] = original

    def test_inspection_rejects_replaced_or_unready_control_node(self):
        self.node['metadata']['uid'] = 'replaced-node'
        with self.cluster(), self.assertRaisesRegex(ValueError, 'routing node was replaced'):
            stack.inspect_connection(self.connection)
        self.node['metadata']['uid'] = self.connection['controlNodeUID']
        self.node['status']['conditions'][0]['status'] = 'False'
        with self.cluster(), self.assertRaisesRegex(ValueError, 'not Ready'):
            stack.inspect_connection(self.connection)

    def test_inspection_rejects_stale_unavailable_or_retargeted_operator(self):
        for status in ({'observedGeneration': 1, 'availableReplicas': 1},
                       {'observedGeneration': 2, 'availableReplicas': 0}):
            with self.subTest(status=status), patch.dict(self.operator, status=status), self.cluster(), \
                 self.assertRaisesRegex(ValueError, 'not available'):
                stack.inspect_connection(self.connection)
        args = self.operator['spec']['template']['spec']['containers'][0]['args']
        args[2] = '--watch-namespaces=another-namespace'
        with self.cluster(), self.assertRaisesRegex(ValueError, 'Operator connection changed'):
            stack.inspect_connection(self.connection)

    def test_inspection_rejects_changed_ca_and_caller_key(self):
        self.resources['configmap/' + self.config['caConfigMap']]['data']['ca.crt'] = 'replacement-ca'
        with self.cluster(), self.assertRaisesRegex(ValueError, 'Shared CA changed'):
            stack.inspect_connection(self.connection)
        self.resources['configmap/' + self.config['caConfigMap']]['data']['ca.crt'] = self.ca
        self.key_path.write_text('replacement-key\n')
        with self.cluster(), self.assertRaisesRegex(ValueError, 'Caller credential changed'):
            stack.inspect_connection(self.connection)

    def test_fresh_install_reuses_crd_without_adopting_or_mutating_its_owner(self):
        original_crd = copy.deepcopy(self.crd)
        self.config['apiKeyFile'] = str(self.key_path)
        self.install()
        self.assertEqual([release for release, _ in self.applied], [self.config['operatorRelease'], self.config['stackRelease']])
        operator_values, shared_values = [values for _, values in self.applied]
        self.assertFalse(operator_values['installCRDs'])
        self.assertEqual(operator_values['watchNamespaces'], [self.config['namespace']])
        self.assertEqual(shared_values['apiKeys'], [{'id': 'stack-client', 'sha256': hashlib.sha256(b'test-key').hexdigest()}])
        self.assertEqual(self.crd, original_crd)
        self.assertEqual(self.key_path.read_text(), 'test-key\n')
        connection = stack.load_connection(self.work/'connection.json')
        self.assertEqual(connection, self.connection)
        for name in ('connection.json', 'install.json', 'ca.crt'):
            self.assertEqual((self.work/name).stat().st_mode & 0o777, 0o600)
        self.assertFalse((self.work/'api-key').exists())

    def test_first_stack_can_install_missing_crd_and_generate_private_key(self):
        self.config['installCRDs'] = True
        self.crd_exists = False
        self.install()
        self.assertTrue(self.applied[0][1]['installCRDs'])
        connection = stack.load_connection(self.work/'connection.json')
        key = (self.work/'api-key').read_text().strip()
        self.assertEqual(connection['apiKeySHA256'], hashlib.sha256(key.encode()).hexdigest())
        self.assertEqual((self.work/'api-key').stat().st_mode & 0o777, 0o600)

    def test_install_refuses_existing_namespace_or_foreign_crd_before_any_helm_write(self):
        self.namespace_exists = True
        with self.cluster(), self.assertRaisesRegex(ValueError, 'new namespace'):
            self.instance.install()
        self.assertEqual(self.applied, [])
        self.assertFalse((self.work/'install.json').exists())
        self.namespace_exists = False
        self.config['installCRDs'] = True
        with self.cluster(), self.assertRaisesRegex(ValueError, 'Unexpected Helm owner'):
            self.instance.install()
        self.assertEqual(self.applied, [])

    def test_install_reuse_requires_existing_compatible_crd(self):
        self.crd_exists = False
        with self.cluster(), self.assertRaisesRegex(ValueError, 'No shared CRD'):
            self.instance.install()
        self.assertEqual(self.applied, [])

    def test_repeat_install_verifies_identity_without_upgrades_or_key_rotation(self):
        self.install()
        original_key = (self.work/'api-key').read_bytes()
        self.applied.clear()
        self.install()
        self.assertEqual(self.applied, [])
        self.assertEqual((self.work/'api-key').read_bytes(), original_key)
        self.resources['service/llm-api-gateway']['metadata']['uid'] = 'replacement'
        with self.cluster(), self.assertRaisesRegex(ValueError, 'Shared resource was replaced'):
            self.instance.install()
        self.assertEqual(self.applied, [])

    def test_repeat_install_rejects_config_changes(self):
        self.install()
        changed = copy.deepcopy(self.config)
        changed['images']['gateway']['tag'] = 'changed-tag'
        instance = stack.Stack(changed, self.work)
        with self.cluster(), self.assertRaisesRegex(ValueError, 'original stack config'):
            instance.install()

    def test_incomplete_install_rejects_replaced_namespace_or_control_before_helm_writes(self):
        self.namespace_exists = True
        checkpoint = {'config': self.config, 'controlNodeUID': self.node['metadata']['uid'],
                      'namespaceUID': self.resources['namespace/' + self.config['namespace']]['metadata']['uid']}
        stack.save(self.work/'install.json', checkpoint)
        for field, value in [('namespaceUID', 'previous-namespace'), ('controlNodeUID', 'previous-node')]:
            stack.save(self.work/'install.json', dict(checkpoint, **{field: value}))
            with self.subTest(field=field), self.cluster(), self.assertRaisesRegex(ValueError, 'Namespace was replaced|checkpoint differs'):
                self.instance.install()
            self.assertEqual(self.applied, [])

    def test_operator_timeout_can_resume_with_preserved_namespace_and_caller_key(self):
        def timeout_after_namespace_creation(command):
            result = self.command(command)
            if command[0] == 'helm' and 'upgrade' in command:
                raise ValueError('operator readiness timed out')
            return result
        with patch.object(stack, 'get', side_effect=self.get), \
             patch.object(stack, 'run', side_effect=timeout_after_namespace_creation), \
             contextlib.redirect_stdout(io.StringIO()), self.assertRaisesRegex(ValueError, 'readiness timed out'):
            self.instance.install()
        checkpoint = json.loads((self.work/'install.json').read_text())
        self.assertEqual(checkpoint['namespaceUID'], self.resources['namespace/' + self.config['namespace']]['metadata']['uid'])
        self.assertFalse((self.work/'connection.json').exists())
        original_key = (self.work/'api-key').read_bytes()
        self.install()
        self.assertEqual((self.work/'api-key').read_bytes(), original_key)
        self.assertTrue((self.work/'connection.json').is_file())

    def gateway_opener(self, models=None, registry=None, valid_status=404, invalid_status=401):
        def open_request(request, timeout):
            self.assertEqual(timeout, 30)
            if request.full_url.endswith('/models'):
                return io.BytesIO(json.dumps({'data': models or []}).encode())
            if request.full_url.endswith('/registry'):
                return io.BytesIO(json.dumps({'models': registry or []}).encode())
            self.assertTrue(request.full_url.endswith('/chat/completions'))
            self.assertEqual(json.loads(request.data)['model'], 'uninstalled-model')
            code = valid_status if request.get_header('Authorization') == 'Bearer test-key' else invalid_status
            if code == 200:
                return io.BytesIO(b'{}')
            raise urllib.error.HTTPError(request.full_url, code, 'fixture', {}, None)
        return Mock(open=Mock(side_effect=open_request))

    def verify_empty(self, opener):
        stack.save(self.work/'connection.json', self.connection)
        with self.cluster(), patch.object(self.instance, 'forward', return_value=contextlib.nullcontext('https://127.0.0.1:18477/v1')), \
             patch.object(stack.ssl, 'create_default_context'), patch.object(stack.urllib.request, 'build_opener', return_value=opener):
            self.instance.verify(True, None, 18477)

    def test_empty_stack_acceptance_checks_discovery_registry_missing_model_and_authentication(self):
        opener = self.gateway_opener()
        self.verify_empty(opener)
        report = json.loads((self.work/'empty-verification.json').read_text())
        self.assertEqual(report, {'passed': True, 'models': [], 'registry': [], 'missingModelStatus': 404, 'invalidKeyStatus': 401})
        self.assertEqual(opener.open.call_count, 4)
        self.assertEqual(self.applied, [])

    def test_empty_acceptance_rejects_registered_models_and_wrong_authentication(self):
        for arguments in ({'models': [{'id': 'already-installed'}]}, {'registry': [{'model': 'already-installed'}]},
                          {'valid_status': 200}, {'valid_status': 503}, {'invalid_status': 404}):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                self.verify_empty(self.gateway_opener(**arguments))
            self.assertFalse((self.work/'empty-verification.json').exists())

    def test_render_preserves_installed_values_credentials_and_connection(self):
        self.install()
        before = {path.name: path.read_bytes() for path in self.work.iterdir() if path.is_file()}
        with patch.object(stack, 'run', return_value='rendered fixture\n'), contextlib.redirect_stdout(io.StringIO()):
            self.instance.render()
        for name, content in before.items():
            with self.subTest(name=name):
                self.assertEqual((self.work/name).read_bytes(), content)
        for release in (self.config['operatorRelease'], self.config['stackRelease']):
            self.assertTrue((self.work/'render'/(release + '.yaml')).is_file())
            self.assertTrue((self.work/'render'/(release + '-values.json')).is_file())
        release = self.config['stackRelease']
        installed = json.loads((self.work/(release + '-values.json')).read_text())
        rendered = json.loads((self.work/'render'/(release + '-values.json')).read_text())
        self.assertNotEqual(installed['apiKeys'], rendered['apiKeys'])
        self.assertEqual(installed['apiKeys'][0]['sha256'], stack.load_connection(self.work/'connection.json')['apiKeySHA256'])

    @unittest.skipUnless(shutil.which('helm'), 'Helm is required for chart rendering')
    def test_real_chart_render_contains_only_cpu_infrastructure_and_scoped_operator(self):
        source = self.work/'charts'
        for name in ('pylon-operator', 'llm-gateway-stack', 'llm-api-gateway', 'llm-request-router'):
            shutil.copytree(HERE.parent/name/name, source/name/name)
        operator_chart = source/'pylon-operator/pylon-operator'
        gateway_chart = source/'llm-gateway-stack/llm-gateway-stack'
        with patch.object(stack, 'chart_paths', return_value=(operator_chart, gateway_chart)), \
             contextlib.redirect_stdout(io.StringIO()):
            self.instance.render()
        operator = (self.work/'render'/(self.config['operatorRelease'] + '.yaml')).read_text()
        gateway = (self.work/'render'/(self.config['stackRelease'] + '.yaml')).read_text()
        combined = operator + '\n' + gateway
        self.assertEqual(combined.count('\nkind: Deployment\n'), 3)
        self.assertIn('--watch-namespaces=' + self.config['namespace'], operator)
        self.assertIn('name: llm-api-gateway\n', gateway)
        self.assertIn('name: llm-request-router\n', gateway)
        self.assertNotRegex(combined, r'(?m)^kind: (?:InferenceEndpoint|PersistentVolumeClaim|Job|StatefulSet|CustomResourceDefinition)$')
        self.assertNotIn('nvidia.com/gpu:', combined)
        self.assertNotIn('runtimeClassName:', combined)
        self.assertNotRegex(combined, r'(?im)^\s*(?:name|modelName|image):.*glm')
        self.assertFalse((self.work/'connection.json').exists())


if __name__ == '__main__':
    unittest.main()
