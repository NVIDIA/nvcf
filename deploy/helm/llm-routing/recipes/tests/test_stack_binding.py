# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import hashlib
import importlib.util
import io
import json
import pathlib
import re
import tempfile
import unittest
from contextlib import redirect_stdout
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('bound_glm_recipe', HERE/'recipe.py')
spark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(spark)


class StackBindingTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name).resolve()
        self.work = self.root/'glm'
        self.key = self.root/'caller-key'
        self.key.write_text('test-caller-key\n')
        self.model = json.loads((HERE/'glm.config.example.json').read_text())
        self.connection = {'schemaVersion': 1, 'kind': 'llm-stack-connection', 'context': 'shared-context',
                           'namespace': 'shared-models', 'clusterId': 'shared-cluster', 'stackRelease': 'shared-stack',
                           'operatorRelease': 'shared-operator', 'controlNode': 'spark-control',
                           'controlNodeUID': 'spark-control-uid', 'caConfigMap': 'shared-ca',
                           'apiKeyFile': str(self.key), 'caSHA256': hashlib.sha256(b'test-public-ca').hexdigest(),
                           'apiKeySHA256': hashlib.sha256(b'test-caller-key').hexdigest()}
        self.shared = spark.stack_binding.stack_module()
        self.connection['resources'] = {key: key.replace('/', '-')+'-uid'
                                        for key in self.shared.connection_resources(self.connection)}
        self.config = spark.stack_binding.overlay(self.model, self.connection)
        self.recipe = spark.Recipe(self.config, self.work)
        self.nodes = [{'metadata': {'name': name, 'uid': name+'-uid', 'labels': {'kubernetes.io/arch': 'arm64'}},
                       'status': {'conditions': [{'type': 'Ready', 'status': 'True'}], 'allocatable': {'nvidia.com/gpu': '1'}}}
                      for name in spark.all_nodes(self.config)]
        self.inspection = {'ca': 'test-public-ca', 'nodes': {node['metadata']['name']: node['metadata']['uid'] for node in self.nodes},
                           'resources': self.connection['resources']}
        self.resources, self.releases, self.endpoints, self.pods = [], [], [], []
        self.commands = []

    def output(self, command):
        self.commands.append(command)
        if command[0] == 'helm':
            self.assertIn('--kube-context', command)
            self.assertIn('list', command)
            return json.dumps(self.releases)
        self.assertIn('--context', command)
        self.assertIn('get', command)
        resource = command[command.index('get')+1]
        records = {'nodes': self.nodes, 'pods': self.pods, 'inferenceendpoints': self.endpoints,
                   'deployments,statefulsets,jobs,services,configmaps,persistentvolumeclaims': self.resources,
                   'crds': [{'metadata': {'name': 'inferenceendpoints.pylon.nvidia.com', 'annotations': {
                       'meta.helm.sh/release-name': 'original-crd-owner', 'meta.helm.sh/release-namespace': 'another-namespace'}}}]}
        return json.dumps({'items': records[resource]})

    def attach(self):
        with patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection), \
             patch.object(spark, 'output', side_effect=self.output), patch.object(spark, 'run') as run, redirect_stdout(io.StringIO()):
            self.recipe.attach_stack(self.work/'config.json')
        run.assert_not_called()

    def test_minimal_config_uses_verified_connection_without_infrastructure_images(self):
        spark.validate(self.config)
        self.assertNotIn('images', self.config)
        self.assertNotIn('tls', self.config)
        self.assertEqual(self.config['releases'], {'stack': 'shared-stack', 'operator': 'shared-operator'})
        self.assertEqual(self.config['nodes']['control'], 'spark-control')
        self.assertNotIn('externalStack', self.model)
        self.assertEqual(self.recipe.identity['externalStack'], self.connection)
        self.assertEqual(self.recipe.identity['recipe'], self.model['recipe'])
        self.assertEqual(self.recipe.identity['gpu'], self.model['gpu'])

    def test_external_config_checks_resource_and_tuning_overrides_before_cluster_access(self):
        changes = [({'resources': {'model': {'limits': {'nvidia.com/gpu': '2'}}}}, 'cannot set nvidia.com/gpu'),
                   ({'resources': {'rpc': {'requests': {'memory': 'invalid'}}}}, 'quantity string'),
                   ({'resources': {'model': {'requests': {'cpu': '13'}}}}, 'exceeds limit'),
                   ({'tuning': {'slots': 0}}, 'positive integer')]
        for override, message in changes:
            with self.subTest(override=override), patch.object(spark, 'run') as run, patch.object(spark, 'output') as output, \
                 self.assertRaisesRegex(RuntimeError, message):
                spark.Recipe(dict(self.config, **override), self.work)
            run.assert_not_called()
            output.assert_not_called()

    def test_external_retune_preserves_binding_and_updates_only_the_model_release(self):
        self.attach()
        previous = self.recipe.backend_values(register=True)
        config = copy.deepcopy(self.config)
        config['tuning'] = {'contextPerSlot': 4096, 'slots': 2}
        config['resources'] = {'model': {'requests': {'cpu': '5'}}, 'rpc': {'limits': {'cpu': '9'}}}
        recipe = spark.Recipe(config, self.work)
        recipe.state.update(serve=True, registered=True, direct=True, gateway=True, runtimeSha256='a'*64)
        binding = copy.deepcopy(recipe.state['stack'])
        def inspect(command):
            if command[0] != 'helm':
                return self.output(command)
            self.assertIn(recipe.backend, command)
            return json.dumps({'info': {'status': 'deployed'}} if 'status' in command else previous)
        with patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection) as shared, \
             patch.object(spark, 'output', side_effect=inspect), patch.object(spark, 'run') as run, redirect_stdout(io.StringIO()):
            recipe.retune()
        shared.assert_called_once_with(self.connection)
        helm = [call.args[0] for call in run.call_args_list if call.args[0][0] == 'helm']
        self.assertEqual(len(helm), 1)
        self.assertEqual(helm[0][helm[0].index('--install')+1], recipe.backend)
        values = json.loads((self.work/(recipe.backend+'-values.json')).read_text())
        args = values['model']['args']
        self.assertEqual(args[args.index('--ctx-size')+1], '8192')
        self.assertEqual(args[args.index('--parallel')+1], '2')
        self.assertEqual(values['model']['resources']['requests']['cpu'], '5')
        self.assertEqual(values['rpc']['resources']['limits']['cpu'], '9')
        for role in ('model', 'rpc'):
            for kind in ('requests', 'limits'):
                self.assertEqual(values[role]['resources'][kind]['nvidia.com/gpu'], 1)
        self.assertEqual(recipe.state['stack'], binding)
        self.assertEqual(recipe.identity['externalStack'], self.connection)
        self.assertFalse(recipe.state['direct'])
        self.assertFalse(recipe.state['gateway'])
        self.assertTrue(values['model']['register'])

    def test_imported_stack_connection_schema_is_enforced_for_saved_model_config(self):
        for field in ('caSHA256', 'apiKeySHA256'):
            config = copy.deepcopy(self.config)
            del config['externalStack'][field]
            with self.subTest(field=field), self.assertRaisesRegex(RuntimeError, 'fingerprint'):
                spark.validate(config)
        for resources in ({'deployment/llm-api-gateway': 'gateway-uid'},
                          dict(self.connection['resources'], unexpected='other-uid')):
            config = copy.deepcopy(self.config)
            config['externalStack']['resources'] = resources
            with self.subTest(resources=resources), self.assertRaisesRegex(RuntimeError, 'every shared resource UID'):
                spark.validate(config)

    def test_connection_load_errors_are_actionable_without_recipe_creation(self):
        path = self.root/'connection.json'
        for document, expected in (('{invalid', 'Cannot load shared stack connection'),
                                   (json.dumps(dict(self.connection, caSHA256='bad')), 'fingerprint')):
            path.write_text(document)
            with self.subTest(document=document), self.assertRaisesRegex(RuntimeError, expected):
                spark.stack_binding.load_connection(path)
        path.unlink()
        with self.assertRaisesRegex(RuntimeError, 'Cannot load shared stack connection'):
            spark.stack_binding.load_connection(path)
        self.assertEqual(list(self.work.iterdir()), [])

    def shared_resources(self):
        resources = {}
        for key, release in self.shared.connection_resources(self.connection).items():
            kind, name = key.split('/', 1)
            metadata = {'name': name, 'uid': self.connection['resources'][key], 'namespace': self.connection['namespace'], 'generation': 1}
            if release:
                metadata['annotations'] = {'meta.helm.sh/release-name': release,
                                           'meta.helm.sh/release-namespace': self.connection['namespace']}
            resources[(kind, name)] = {'metadata': metadata}
            if kind == 'deployment':
                resources[(kind, name)].update(spec={'template': {'spec': {'containers': [{'args': []}]}}},
                                               status={'observedGeneration': 1, 'availableReplicas': 1})
        resources[('node', self.connection['controlNode'])] = next(node for node in self.nodes if node['metadata']['name'] == self.connection['controlNode'])
        resources[('customresourcedefinition', self.shared.CRD)]['spec'] = {
            'group': 'pylon.nvidia.com', 'scope': 'Namespaced', 'names': {'kind': 'InferenceEndpoint'},
            'versions': [{'name': 'v1alpha1', 'served': True, 'schema': {'openAPIV3Schema': {'properties': {'spec': {'properties': {
                name: {} for name in ('modelName', 'service', 'inferenceAPIFormat', 'health', 'maxEngineConcurrency')}}}}}}]}
        resources[('deployment', self.connection['operatorRelease'])]['spec']['template']['spec']['containers'][0]['args'] = [
            '--cluster-id=' + self.connection['clusterId'], '--watch-namespaces=' + self.connection['namespace'],
            '--router-grpc-address=http://llm-request-router.' + self.connection['namespace'] + '.svc.cluster.local:50071',
            '--trust-bundle-configmap=' + self.connection['caConfigMap']]
        resources[('configmap', self.connection['caConfigMap'])]['data'] = {'ca.crt': 'test-public-ca'}
        return resources

    def test_attachment_uses_real_shared_connection_inspection_contract(self):
        path = self.root/'connection.json'
        path.write_text(json.dumps(self.connection))
        self.assertEqual(spark.stack_binding.load_connection(path), self.connection)
        resources = self.shared_resources()
        def get(connection, kind, name):
            self.assertEqual(connection, self.connection)
            return resources[(kind, name)]
        with patch.object(self.shared, 'get', side_effect=get), \
             patch.object(self.shared, 'run', return_value='{"items": []}') as shared_run, \
             patch.object(spark, 'output', side_effect=self.output), patch.object(spark, 'run') as run, redirect_stdout(io.StringIO()):
            self.recipe.attach_stack(self.work/'config.json')
        run.assert_not_called()
        shared_run.assert_called_once()
        self.assertEqual(shared_run.call_args.args[0], ['kubectl', '--context', 'shared-context', '-n', 'shared-models', 'get', 'deployments', '-A', '-o', 'json'])
        self.assertEqual(self.recipe.state['identity']['externalStack'], self.connection)

    def test_runtime_connection_failure_does_not_save_model_attachment(self):
        resources = self.shared_resources()
        resources[('deployment', 'llm-api-gateway')]['metadata']['uid'] = 'replacement-gateway'
        with patch.object(self.shared, 'get', side_effect=lambda connection, kind, name: resources[(kind, name)]), \
             patch.object(spark, 'output') as output, self.assertRaisesRegex(RuntimeError, 'Shared resource was replaced'):
            self.recipe.attach_stack(self.work/'config.json')
        output.assert_not_called()
        self.assertEqual(list(self.work.iterdir()), [])

    def test_overlay_rejects_conflicting_shared_identity(self):
        changes = ({'namespace': 'other'}, {'context': 'other'}, {'clusterId': 'other'}, {'apiKeyFile': '/other'},
                   {'nodes': dict(self.model['nodes'], control='other')}, {'releases': {'stack': 'other'}},
                   {'externalStack': dict(self.connection, stackRelease='other')})
        for change in changes:
            with self.subTest(change=change), self.assertRaisesRegex(RuntimeError, 'differs|already bound'):
                spark.stack_binding.overlay(dict(self.model, **change), self.connection)

    def test_release_collision_lookup_covers_all_statuses_with_helm3_and_helm4_flags(self):
        with patch.object(spark, 'output', side_effect=self.output):
            self.recipe.check_model_absent()
        command = next(command for command in self.commands if command[0] == 'helm')
        self.assertNotIn('--all', command)
        self.assertTrue({'--deployed', '--failed', '--pending', '--uninstalled', '--superseded', '--uninstalling'} <= set(command))
        pattern = command[command.index('--filter')+1]
        self.assertIsNotNone(re.fullmatch(pattern, self.recipe.backend))
        self.assertIsNotNone(re.fullmatch(pattern, self.recipe.backend+'-chain'))
        self.assertIsNone(re.fullmatch(pattern, self.recipe.backend+'-unrelated'))

    def test_attachment_saves_only_binding_and_never_marks_model_or_gpu_inventory_ready(self):
        self.pods = [{'metadata': {'name': 'busy-model', 'namespace': 'unrelated'}, 'spec': {'nodeName': self.model['nodes']['model'][0]}}]
        self.attach()
        self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)
        self.assertEqual((self.work/'ca.crt').read_text(), 'test-public-ca')
        self.assertTrue(self.recipe.state['attachedStack'])
        for phase in ('serve', 'registered', 'direct', 'gateway', 'inventory', 'attachedExisting'):
            self.assertNotIn(phase, self.recipe.state)
        self.assertEqual(self.recipe.state['stack'], {'apiKeyFile': str(self.key)})
        self.assertEqual(self.key.read_text(), 'test-caller-key\n')
        self.assertEqual((self.work/'config.json').stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.work/'state.json').stat().st_mode & 0o777, 0o600)

    def test_failed_shared_inspection_does_not_save_config_ca_or_state(self):
        with patch.object(spark.stack_binding, 'inspect_connection', side_effect=RuntimeError('shared identity changed')), \
             patch.object(spark, 'output') as output, self.assertRaisesRegex(RuntimeError, 'shared identity changed'):
            self.recipe.attach_stack(self.work/'config.json')
        output.assert_not_called()
        self.assertEqual(list(self.work.iterdir()), [])

    def test_attachment_rejects_missing_key_before_saving_any_binding(self):
        self.key.unlink()
        with patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection), \
             patch.object(spark, 'output', side_effect=self.output), self.assertRaisesRegex(RuntimeError, 'API-key file'):
            self.recipe.attach_stack(self.work/'config.json')
        self.assertEqual(list(self.work.iterdir()), [])

    def test_existing_model_release_resources_or_endpoint_cannot_be_adopted(self):
        scenarios = [('releases', [{'name': self.recipe.backend}]),
                     ('resources', [{'metadata': {'name': self.recipe.backend+'-artifacts'}}]),
                     ('endpoints', [{'metadata': {'name': 'glm53-iq2'}}]),
                     ('endpoints', [{'metadata': {'name': 'other-glm'}, 'spec': {'modelName': 'GLM-5.3-UD-IQ2_M'}}])]
        for field, records in scenarios:
            with self.subTest(field=field), patch.object(self, field, records), \
                 patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection), \
                 patch.object(spark, 'output', side_effect=self.output), self.assertRaisesRegex(RuntimeError, 'already exist|already registered'):
                self.recipe.attach_stack(self.work/'config.json')
            self.assertEqual(list(self.work.iterdir()), [])

    def test_inventory_accepts_verified_shared_namespace_and_existing_crd(self):
        self.attach()
        self.pods = [{'metadata': {'name': 'shared-router', 'namespace': self.config['namespace']},
                      'spec': {'nodeName': self.config['nodes']['control'], 'containers': [{'resources': {'requests': {'cpu': '1'}}}]}}]
        with patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection), \
             patch.object(spark, 'output', side_effect=self.output), patch.object(spark, 'run') as run, redirect_stdout(io.StringIO()):
            self.recipe.inventory()
        self.assertIn('inventory', self.recipe.state)
        self.assertEqual(run.call_count, 2)
        for call in run.call_args_list:
            self.assertIn('get', call.args[0])

    def test_inventory_rejects_busy_gpu_after_successful_attachment(self):
        self.attach()
        self.pods = [{'metadata': {'name': 'busy-model', 'namespace': 'unrelated'}, 'status': {'phase': 'Running'},
                      'spec': {'nodeName': self.config['nodes']['model'][0], 'containers': [{'resources': {'requests': {'nvidia.com/gpu': '1'}}}]}}]
        with patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection), \
             patch.object(spark, 'output', side_effect=self.output), patch.object(spark, 'run') as run, \
             self.assertRaisesRegex(RuntimeError, 'GPU is occupied'):
            self.recipe.inventory()
        run.assert_not_called()
        self.assertNotIn('inventory', self.recipe.state)

    def test_preflight_cannot_bypass_required_gpu_inventory(self):
        self.attach()
        with patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection), \
             patch.object(spark, 'output', side_effect=self.output), patch.object(self.recipe, 'helm_apply') as helm, \
             self.assertRaisesRegex(RuntimeError, 'successful inventory'):
            self.recipe.backend_phase('preflight')
        helm.assert_not_called()

    def test_bound_cluster_rechecks_shared_resources_and_model_node_uids(self):
        self.attach()
        self.nodes[0]['metadata']['uid'] = 'replacement-node'
        with patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection) as inspect, \
             patch.object(spark, 'output', side_effect=self.output), self.assertRaisesRegex(RuntimeError, 'identities changed'):
            self.recipe.bound_cluster()
        inspect.assert_called_once_with(self.connection)

    def test_saved_binding_cannot_switch_stack_identity(self):
        self.attach()
        changed = copy.deepcopy(self.config)
        changed['externalStack']['resources']['deployment/llm-api-gateway'] = 'replaced'
        with self.assertRaisesRegex(RuntimeError, 'different installation'):
            spark.Recipe(changed, self.work)

    def test_saved_external_recipe_cannot_change_gpu_sizing(self):
        self.attach()
        changed = copy.deepcopy(self.config)
        changed['gpu']['memoryGiB'] += 64
        with self.assertRaisesRegex(RuntimeError, 'different installation'):
            spark.Recipe(changed, self.work)

    def test_external_attachment_supports_one_model_node(self):
        self.model['nodes']['model'] = self.model['nodes']['model'][:1]
        self.model['gpu']['memoryGiB'] = 512
        config = spark.stack_binding.overlay(self.model, self.connection)
        recipe = spark.Recipe(config, self.root/'single-model')
        with patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection), \
             patch.object(spark, 'output', side_effect=self.output), redirect_stdout(io.StringIO()):
            recipe.attach_stack(recipe.work/'config.json')
        self.assertEqual(len(recipe.targets), 1)
        self.assertEqual(recipe.workers, [])
        self.assertTrue(recipe.state['attachedStack'])
        self.assertNotIn('inventory', recipe.state)

    def test_shared_credentials_cannot_fall_back_to_temporary_mutation(self):
        self.attach()
        self.recipe.state['stack']['apiKeyFile'] = None
        with patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection), \
             patch.object(spark.gateway_access, 'temporary_gateway_key') as key, self.assertRaisesRegex(RuntimeError, 'caller-key binding'):
            self.recipe.chat('Hello', False, 18443)
        key.assert_not_called()

    def test_infrastructure_mutations_are_rejected_before_commands(self):
        actions = [lambda: self.recipe.deploy_stack(), lambda: self.recipe.update('gateway', 'next'),
                   lambda: self.recipe.rollback(self.root/'result.json'), lambda: self.recipe.reinitialize(self.work/'config.json'),
                   lambda: self.recipe.attach_existing(), lambda: self.recipe.build_images(), lambda: self.recipe.export_images(),
                   lambda: self.recipe.import_images(self.root/'archive.tar', True), lambda: self.recipe.cleanup_key(18443),
                   lambda: self.recipe.attach_monitoring(),
                   lambda: self.recipe.import_images(self.root/'archive.tar', True, monitoring_only=True)]
        for action in actions:
            with self.subTest(action=action), patch.object(spark, 'run') as run, patch.object(spark, 'output') as output, \
                 self.assertRaisesRegex(RuntimeError, 'externally managed infrastructure'):
                action()
            run.assert_not_called()
            output.assert_not_called()

    def test_helm_apply_rejects_shared_release_or_chart_before_writing_values(self):
        for release, chart in ((self.recipe.stack, HERE/'charts/gguf-backend'), (self.recipe.backend, HERE.parent)):
            with self.subTest(release=release, chart=chart), patch.object(spark, 'run') as run, \
                 self.assertRaisesRegex(RuntimeError, 'own backend releases'):
                self.recipe.helm_apply(release, chart, {})
            run.assert_not_called()
        self.assertEqual(list(self.work.iterdir()), [])

    def test_backend_render_does_not_require_or_render_infrastructure(self):
        with patch.object(self.recipe, 'source_check') as source, patch.object(spark, 'run') as run, \
             patch.object(spark, 'output', return_value='kind: List\nitems: []\n') as output, redirect_stdout(io.StringIO()):
            self.recipe.render()
        source.assert_not_called()
        self.assertEqual(run.call_count, 6)
        self.assertEqual(output.call_count, 6)
        for call in run.call_args_list:
            self.assertEqual(call.args[0][:3], ['helm', 'lint', HERE/'charts/gguf-backend'])
        self.assertEqual(len(list((self.work/'render').glob('glm-*.yaml'))), 6)

    def test_enabled_monitoring_does_not_expand_external_model_render_scope(self):
        self.recipe.c['monitoring'] = {'enabled': True}
        with patch.object(spark.monitoring, 'chart_values') as monitor, patch.object(spark, 'run') as run, \
             patch.object(spark, 'output', return_value='kind: List\nitems: []\n'), redirect_stdout(io.StringIO()):
            self.recipe.render()
        monitor.assert_not_called()
        self.assertEqual(run.call_count, 6)
        for call in run.call_args_list:
            self.assertEqual(call.args[0][:3], ['helm', 'lint', HERE/'charts/gguf-backend'])
        self.assertFalse((self.work/'render/monitoring-values.json').exists())

    def test_monitoring_constructor_rejects_external_model_before_creating_workdir(self):
        work = self.root/'new-monitor'
        with patch.object(spark.monitoring_setup, 'validate') as validate, \
             self.assertRaisesRegex(RuntimeError, 'separate monitoring work directory'):
            spark.Recipe(self.config, work, monitoring_only=True)
        validate.assert_not_called()
        self.assertFalse(work.exists())

    def test_monitoring_commands_preserve_external_model_state_and_config(self):
        self.attach()
        before = {path.name: path.read_bytes() for path in self.work.iterdir()}
        for phase in ('attach-monitoring', 'monitoring', 'dashboard', 'verify-monitoring', 'uninstall-monitoring',
                      'monitoring-images', 'export-monitoring-images', 'import-monitoring-images', 'cleanup-key'):
            with self.subTest(phase=phase), patch.object(spark.monitoring_setup, 'attach') as attach, \
                 patch.object(spark.monitoring, 'Monitoring') as monitor, \
                 patch.object(spark, 'run') as run, patch.object(spark, 'output') as output, \
                 self.assertRaisesRegex(RuntimeError, 'externally managed infrastructure'):
                spark.main(['--work-dir', str(self.work), phase])
            attach.assert_not_called()
            monitor.assert_not_called()
            run.assert_not_called()
            output.assert_not_called()
            self.assertEqual({path.name: path.read_bytes() for path in self.work.iterdir()}, before)

    def test_independent_registration_owns_only_model_release(self):
        self.recipe.state = {'serve': True, 'direct': True, 'stack': {'apiKeyFile': str(self.key)}}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply') as helm, patch.object(spark, 'run') as run:
            self.recipe.register()
        helm.assert_called_once()
        self.assertEqual(helm.call_args.args[:2], (self.recipe.backend, HERE/'charts/gguf-backend'))
        self.assertTrue(helm.call_args.args[2]['model']['register'])
        self.assertEqual(run.call_count, 3)

    def test_cli_overlays_before_validation_and_keeps_input_model_config(self):
        path = self.root/'input-glm.json'
        path.write_text(json.dumps(self.model))
        before = path.read_bytes()
        with patch.object(spark.stack_binding, 'load_connection', return_value=self.connection), \
             patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection), \
             patch.object(spark, 'output', side_effect=self.output), patch.object(spark, 'run') as run, redirect_stdout(io.StringIO()):
            spark.main(['--config', str(path), '--work-dir', str(self.work), 'attach-stack', '--stack-connection', str(self.root/'connection.json')])
        run.assert_not_called()
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)
        with patch.object(spark.Recipe, 'prepare') as prepare:
            spark.main(['--work-dir', str(self.work), 'prepare'])
        prepare.assert_called_once()

    def test_cli_cannot_ignore_stack_connection_or_modify_shared_images(self):
        with self.assertRaisesRegex(RuntimeError, 'only for attach-stack'):
            spark.main(['paths', '--stack-connection', '/unused'])
        self.attach()
        with patch.object(spark, 'run') as run, self.assertRaisesRegex(RuntimeError, 'externally managed infrastructure'):
            spark.main(['--work-dir', str(self.work), 'push-images'])
        run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
