# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
from contextlib import redirect_stdout
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('monitoring_setup_recipe', HERE/'spark.py')
spark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(spark)
setup = spark.monitoring_setup


class MonitoringSetupTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.work = pathlib.Path(self.tmp.name).resolve()/'monitoring'

        def deployment(name, release, node='cpu-control'):
            return {'metadata': {'name': name, 'namespace': 'routing', 'annotations': {
                        'meta.helm.sh/release-name': release, 'meta.helm.sh/release-namespace': 'routing'}},
                    'spec': {'selector': {'matchLabels': {'app': name}},
                             'template': {'spec': {'nodeSelector': {'kubernetes.io/hostname': node}}}}}

        self.gateway = deployment('llm-api-gateway', 'assistant-stack')
        self.router = deployment('llm-request-router', 'assistant-stack', 'other-routing-node')
        self.operator = deployment('custom-operator-controller', 'assistant-operator', 'another-node')
        self.gateways = [self.gateway]
        self.releases = [{'name': 'assistant-operator', 'chart': 'pylon-operator-1.0'}]
        self.values = {
            'assistant-stack': {'clusterId': 'routing', 'sparkRecipeSource': {'revision': 'unrelated-revision'},
                                'apiKeys': [{'sha256': 'DO-NOT-COPY'}], 'tls': {'privateKey': 'DO-NOT-COPY'},
                                'llm-api-gateway': {'llmApiGateway': {'image': {'pullPolicy': 'IfNotPresent'},
                                                                    'auth': {'mode': 'external'}}}},
            'assistant-operator': {'clusterId': 'routing', 'watchNamespaces': ['routing'],
                                   'router': {'grpcAddress': 'http://llm-request-router.routing.svc.cluster.local:50071'},
                                   'trustBundle': {'configMap': 'routing-ca'}}}
        self.nodes = [{'metadata': {'name': 'cpu-control', 'uid': 'node-one', 'labels': {'kubernetes.io/arch': 'amd64'}},
                       'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}]
        self.storage = [{'metadata': {'name': 'fast-storage'}}]
        self.ca = '-----BEGIN CERTIFICATE-----\nfixture-ca\n'
        self.commands = []
        self.config = {'context': 'monitoring-context', 'namespace': 'routing', 'releasePrefix': 'assistant',
                       'clusterId': 'routing', 'nodes': {'control': 'cpu-control'},
                       'releases': {'stack': 'assistant-stack', 'operator': 'assistant-operator'},
                       'storageClass': 'fast-storage', 'caConfigMap': 'routing-ca',
                       'images': {'pullPolicy': 'IfNotPresent'}, 'monitoring': {'enabled': True},
                       'apiKeyFile': None, 'containerd': None}

    def output(self, command):
        self.commands.append(command)
        if command[0] == 'helm':
            self.assertEqual(command[command.index('--kube-context')+1], 'monitoring-context')
            if 'list' in command:
                return json.dumps(self.releases)
            self.assertIn('--all', command)
            return json.dumps(self.values[command[command.index('values')+1]])
        self.assertEqual(command[command.index('--context')+1], 'monitoring-context')
        args = command[command.index('get')+1:]
        if args[0] == 'deployments':
            return json.dumps({'items': [self.operator] if '-l' in args else self.gateways})
        if args[:2] == ['deployment', 'llm-request-router']:
            return json.dumps(self.router)
        if args[0] == 'nodes':
            return json.dumps({'items': self.nodes})
        if args[0] == 'storageclasses':
            return json.dumps({'items': self.storage})
        if args[:2] == ['configmap', 'routing-ca']:
            return json.dumps({'data': {'ca.crt': self.ca}})
        if args[0] == 'pods':
            return json.dumps({'items': [{'metadata': {'name': 'gateway-pod'}, 'spec': {'nodeName': 'cpu-control'},
                                         'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}]})
        self.fail('Unexpected resource request: '+str(command))

    def discover(self, namespace=None):
        with patch.object(spark, 'run') as mutate:
            result = setup.discover_config('monitoring-context', namespace, self.output)
        mutate.assert_not_called()
        return result

    def recipe(self):
        return spark.Recipe(self.config, self.work, monitoring_only=True)

    def attach(self, recipe):
        with patch.object(spark, 'output', side_effect=self.output), patch.object(spark, 'run') as mutate, redirect_stdout(io.StringIO()):
            recipe.attach_monitoring()
        mutate.assert_not_called()

    def test_discovers_stack_without_model_gpu_recipe_pin_or_auth_assumptions(self):
        self.assertEqual(self.discover(), self.config)
        setup.validate(self.config)
        recipe = self.recipe()
        self.assertIsNone(recipe.glm)
        self.assertNotIn('DO-NOT-COPY', json.dumps(self.config))
        self.assertFalse(any('inferenceendpoint' in c or 'runtimeclasses' in c for c in self.commands))
        with self.assertRaisesRegex(RuntimeError, 'runtimeClass'):
            spark.Recipe(self.config, self.work)

    def test_namespace_is_required_for_multiple_stacks(self):
        another = copy.deepcopy(self.gateway)
        another['metadata']['namespace'] = 'second'
        self.gateways.append(another)
        with self.assertRaisesRegex(RuntimeError, 'Multiple.*--namespace'):
            self.discover()
        self.assertEqual(len(self.commands), 1)

    def test_selected_namespace_is_explicit_on_discovery(self):
        self.discover('routing')
        self.assertNotIn('--all-namespaces', self.commands[0])
        self.assertEqual(self.commands[0][self.commands[0].index('-n')+1], 'routing')

    def test_foreign_router_or_operator_ownership_is_rejected(self):
        for obj in (self.router, self.operator):
            with self.subTest(name=obj['metadata']['name']):
                owner = obj['metadata']['annotations']['meta.helm.sh/release-name']
                obj['metadata']['annotations']['meta.helm.sh/release-name'] = 'foreign'
                with self.assertRaisesRegex(RuntimeError, 'different releases|Helm-owned'):
                    self.discover()
                obj['metadata']['annotations']['meta.helm.sh/release-name'] = owner

    def test_no_ready_gateway_placement_requires_readiness(self):
        self.gateway['spec']['template']['spec']['nodeSelector'] = {}
        self.assertEqual(self.discover()['nodes'], {'control': 'cpu-control'})

    def test_default_storage_class_disambiguates_and_ambiguous_storage_fails(self):
        self.storage.append({'metadata': {'name': 'other-storage'}})
        with self.assertRaisesRegex(RuntimeError, 'default StorageClass'):
            self.discover()
        self.storage[0]['metadata']['annotations'] = {'storageclass.kubernetes.io/is-default-class': 'true'}
        self.assertEqual(self.discover()['storageClass'], 'fast-storage')

    def test_operator_watching_all_namespaces_is_supported(self):
        self.values['assistant-operator']['watchNamespaces'] = []
        self.assertEqual(self.discover()['releases']['operator'], 'assistant-operator')

    def test_multiple_matching_operators_are_rejected(self):
        self.releases.append({'name': 'other-operator', 'chart': 'pylon-operator-1.0'})
        self.values['other-operator'] = copy.deepcopy(self.values['assistant-operator'])
        with self.assertRaisesRegex(RuntimeError, 'Expected one Pylon Operator'):
            self.discover()

    def test_discovered_importer_keeps_socket_settings_without_model_nodes(self):
        self.releases.append({'name': 'assistant-images', 'chart': 'pylon-image-loader-1.0'})
        self.values['assistant-images'] = {'archiveNode': 'cpu-control', 'nodeNames': ['cpu-control'],
                                          'runAsUser': 999, 'socketPath': '/run/containerd/containerd.sock'}
        self.assertEqual(self.discover()['containerd'], self.values['assistant-images'])

    def test_fresh_attachment_saves_only_monitoring_binding_and_ca(self):
        key = self.work.parent/'caller-key'
        key.write_text('private-fixture')
        self.config['apiKeyFile'] = str(key)
        recipe = self.recipe()
        self.attach(recipe)
        self.assertEqual(recipe.state, {'identity': recipe.identity, 'attachedMonitoring': True,
                                      'inventory': {'nodes': {'cpu-control': 'node-one'}},
                                      'stack': {'apiKeyFile': str(key)}})
        self.assertEqual((self.work/'ca.crt').read_text(), self.ca)
        self.assertNotIn('private-fixture', (self.work/'state.json').read_text())
        self.assertEqual((self.work/'state.json').stat().st_mode & 0o777, 0o600)

    def test_existing_installer_identity_checkpoints_and_credentials_are_preserved(self):
        recipe = self.recipe()
        key = self.work/'api-key'
        key.write_text('private-fixture')
        recipe.stamp('inventory', {'nodes': {'cpu-control': 'node-one', 'another-node': 'unrelated-uid'}})
        recipe.stamp('stack', {'apiKeyFile': str(key), 'source': 'different-version'})
        recipe.stamp('serve', {'completed': 'earlier'})
        recipe.stamp('registered', {'models': ['chat-model-a', 'chat-model-b']})
        spark.save(self.work/'ca.crt', self.ca)
        state = recipe.state_path.read_bytes()
        config = copy.deepcopy(self.config)
        self.attach(recipe)
        self.assertEqual(recipe.state_path.read_bytes(), state)
        self.assertEqual(self.config, config)
        self.assertEqual(key.read_text(), 'private-fixture')
        self.assertNotIn('attachedMonitoring', recipe.state)

    def test_different_node_identity_ca_or_stack_cannot_be_adopted(self):
        recipe = self.recipe()
        self.attach(recipe)
        state = recipe.state_path.read_bytes()
        self.nodes[0]['metadata']['uid'] = 'replaced-node'
        with self.assertRaisesRegex(RuntimeError, 'node identities changed'):
            self.attach(recipe)
        self.nodes[0]['metadata']['uid'] = 'node-one'
        self.ca = 'different-ca'
        with self.assertRaisesRegex(RuntimeError, 'Saved gateway CA differs'):
            self.attach(recipe)
        self.assertEqual(recipe.state_path.read_bytes(), state)

    def test_cli_attaches_and_uses_minimal_config_for_monitoring_commands(self):
        args = ['--context', 'monitoring-context', '--work-dir', str(self.work)]
        with patch.object(spark, 'output', side_effect=self.output), redirect_stdout(io.StringIO()):
            spark.main(args+['attach-monitoring'])
        self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)
        for phase, method, expected in [('monitoring', 'install', ()), ('dashboard', 'dashboard', (18443,)),
                                         ('verify-monitoring', 'verify', (18443, False, None)),
                                         ('export-monitoring-images', 'export_images', (self.work/'monitoring-arm64-images.tar',))]:
            with self.subTest(phase=phase), patch.object(spark.monitoring.Monitoring, method) as call:
                spark.main(args+[phase])
            call.assert_called_once_with(*expected)
        with patch.object(spark.Recipe, 'cleanup_key') as cleanup:
            spark.main(args+['cleanup-key'])
        cleanup.assert_called_once_with(18443)
        with patch.object(spark.Recipe, 'import_images') as importer:
            spark.main(args+['import-monitoring-images', '--allow-containerd-import'])
        importer.assert_called_once_with(self.work/'monitoring-arm64-images.tar', True, monitoring_only=True)

    def test_cli_model_is_forwarded_only_with_traffic_verification(self):
        spark.save(self.work/'config.json', self.config)
        args = ['--context', 'monitoring-context', '--work-dir', str(self.work)]
        with patch.object(spark.monitoring.Monitoring, 'verify') as verify:
            spark.main(args+['verify-monitoring', '--verify-traffic', '--model', 'chat-model-b'])
        verify.assert_called_once_with(18443, True, 'chat-model-b')
        for invalid in (['monitoring'], ['verify-monitoring'], ['chat']):
            with self.subTest(phase=invalid), self.assertRaisesRegex(RuntimeError, '--model requires'):
                spark.main(args+invalid+['--model', 'chat-model-b'])

    def test_monitoring_attachment_cannot_be_used_for_deployment_with_a_full_config(self):
        self.config['runtimeClass'] = 'nvidia'
        self.config['nodes'].update(leader='cpu-control', worker='cpu-worker')
        self.config['images'].update(prefix='registry.example/routing', tag='fixture', pullSecrets=[])
        worker = copy.deepcopy(self.nodes[0])
        worker['metadata'].update(name='cpu-worker', uid='node-two')
        self.nodes.append(worker)
        spark.validate(self.config)
        spark.save(self.work/'config.json', self.config)
        args = ['--context', 'monitoring-context', '--work-dir', str(self.work)]
        with patch.object(spark, 'output', side_effect=self.output), redirect_stdout(io.StringIO()):
            spark.main(args+['attach-monitoring'])
        state = (self.work/'state.json').read_bytes()
        config = (self.work/'config.json').read_bytes()
        for phase in ('stack', 'init', 'update', 'recover', 'attach-existing'):
            with self.subTest(phase=phase), patch.object(spark, 'output') as inspect, patch.object(spark, 'run') as mutate, \
                 self.assertRaisesRegex(RuntimeError, 'attached for monitoring'):
                spark.main(args+[phase])
            inspect.assert_not_called()
            mutate.assert_not_called()
            self.assertEqual((self.work/'state.json').read_bytes(), state)
            self.assertEqual((self.work/'config.json').read_bytes(), config)
        with patch.object(spark, 'output') as inspect, redirect_stdout(io.StringIO()):
            spark.main(args+['context'])
            spark.main(args+['paths'])
        inspect.assert_not_called()
        with patch.object(spark.monitoring.Monitoring, 'install') as install:
            spark.main(args+['monitoring'])
        install.assert_called_once()


if __name__ == '__main__':
    unittest.main()
