# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import hashlib
import io
import importlib.util
import json
import os
import pathlib
import stat
import tarfile
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('spark_recipe', HERE/'spark.py')
spark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(spark)


class RecipeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='spark-recipe-test-')
        self.addCleanup(self.tmp.cleanup)
        self.config = json.loads((HERE/'config.example.json').read_text())
        self.recipe = spark.Recipe(self.config, self.tmp.name)

    def test_duplicate_model_nodes_and_missing_context_are_rejected(self):
        self.config['nodes']['worker'] = self.config['nodes']['leader']
        with self.assertRaisesRegex(RuntimeError, 'distinct GPU'):
            spark.validate(self.config)
        self.config['nodes']['worker'] = 'another-node'
        self.config['context'] = ''
        with self.assertRaisesRegex(RuntimeError, 'context'):
            spark.validate(self.config)

    def test_work_directory_cannot_put_credentials_in_checkout(self):
        with self.assertRaisesRegex(RuntimeError, 'outside the checkout'):
            spark.Recipe(self.config, HERE/'.work')

    def test_extra_model_configuration_is_rejected_before_any_commands(self):
        for field, value in [('retainedModels', ['legacy-model']), ('testFixture', True)]:
            with self.subTest(field=field):
                config = copy.deepcopy(self.config)
                config[field] = value
                directory = pathlib.Path(self.tmp.name)/'rejected'
                with patch.object(spark, 'run') as run, patch.object(spark, 'output') as output:
                    with self.assertRaisesRegex(RuntimeError, field):
                        spark.Recipe(config, directory)
                run.assert_not_called()
                output.assert_not_called()
                self.assertFalse(directory.exists())

    def test_preparation_cannot_unload_a_running_model(self):
        self.recipe.state = {'serve': True}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply') as helm:
            with self.assertRaisesRegex(RuntimeError, 'already been loaded'):
                self.recipe.backend_phase('preflight')
            helm.assert_not_called()

    def test_registration_requires_real_direct_verification(self):
        self.recipe.state = {'serve': True, 'stack': True}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply') as helm:
            with self.assertRaisesRegex(RuntimeError, 'directly verify'):
                self.recipe.register()
            helm.assert_not_called()

    def test_stack_generates_private_key_hash_and_only_glm_stack_components(self):
        encoded = __import__('base64').b64encode(b'private-cluster-token').decode()
        responses = [json.dumps({'data': {'cluster-token': encoded}}), json.dumps({'data': {'ca.crt': 'public-ca'}})]
        with patch.object(self.recipe, 'source_check'), patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply') as helm, patch.object(spark, 'output', side_effect=responses):
            self.recipe.deploy_stack()
        self.assertEqual(helm.call_count, 2)
        key_path = pathlib.Path(self.tmp.name)/'api-key'
        key = key_path.read_text().strip()
        self.assertEqual(stat.S_IMODE(key_path.stat().st_mode), 0o600)
        stack_values = helm.call_args_list[1].args[2]
        self.assertNotIn(key, json.dumps(stack_values))
        self.assertEqual(stack_values['apiKeys'][0]['sha256'], hashlib.sha256(key.encode()).hexdigest())
        self.assertEqual(self.recipe.components(), ['gateway', 'router', 'pylon', 'operator'])
        self.assertEqual(self.recipe.state['stack']['apiKeyFile'], str(key_path.resolve()))

    def test_direct_and_gateway_verification_use_the_glm_client(self):
        self.recipe.state = {'stack': {'apiKeyFile': str(pathlib.Path(self.tmp.name)/'api-key'), 'testFixture': True}}
        for gateway in (False, True):
            with self.subTest(gateway=gateway):
                with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'forward') as forward, patch.object(spark, 'run') as run:
                    self.recipe.verify(gateway, 18443)
                command = [str(value) for value in run.call_args.args[0]]
                self.assertEqual(command[command.index('--mode')+1], 'verify')
                self.assertNotIn('--retained-model', command)
                self.assertEqual('--api-key-file' in command, gateway)
                self.assertEqual('--ca-file' in command, gateway)
                self.assertEqual(command[command.index('--url')+1], ('https' if gateway else 'http')+'://127.0.0.1:18443')
                forward.assert_called_once_with(gateway, 18443)
                self.assertTrue(self.recipe.state['gateway' if gateway else 'direct'])

    def test_attached_installation_requires_glm_verification_before_update(self):
        self.recipe.state = {'attachedExisting': True}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'source_check'), patch.object(spark, 'run') as run, patch.object(spark, 'output') as output:
            with self.assertRaisesRegex(RuntimeError, 'Verify GLM'):
                self.recipe.update('gateway', 'next-tag')
        run.assert_not_called()
        output.assert_not_called()

    def test_model_config_keeps_two_gpus_and_scoped_canary(self):
        values = self.recipe.backend_values(register=True, render=True)
        self.assertEqual([t['id'] for t in values['targets']], ['leader', 'worker'])
        self.assertEqual(values['model']['canary'], {'timeoutSeconds': 180, 'intervalSeconds': 60})
        self.assertEqual(values['model']['args'][values['model']['args'].index('--parallel')+1], '1')
        self.assertEqual(values['model']['args'][values['model']['args'].index('--ctx-size')+1], '2048')
        self.assertEqual(len(values['model']['lock']['files']), 6)

    def test_image_update_only_changes_selected_tag_and_preserves_other_pods(self):
        values = self.recipe.stack_values('a'*64, 'b'*64)
        pods = [{'metadata': {'name': 'llm-api-gateway-old', 'uid': 'g1'}, 'status': {'phase': 'Running'}},
                {'metadata': {'name': 'unrelated-workload', 'uid': 'u1'}, 'status': {'phase': 'Running'}},
                {'metadata': {'name': self.recipe.glm+'-leader', 'uid': 'm1'}, 'status': {'phase': 'Running'}}]
        after = copy.deepcopy(pods)
        after[0]['metadata']['uid'] = 'g2'
        with patch.object(self.recipe, 'source_check'), patch.object(self.recipe, 'bound_cluster'), patch.object(spark, 'output', side_effect=[json.dumps(values), json.dumps({'items': pods}), json.dumps({'items': after})]), patch.object(spark, 'run') as run:
            self.recipe.update('gateway', 'next-tag')
        command = run.call_args.args[0]
        self.assertIn('--reuse-values', command)
        self.assertIn('llm-api-gateway.llmApiGateway.image.tag=next-tag', command)
        self.assertIn(self.config['context'], command)
        results = list((pathlib.Path(self.tmp.name)/'evidence').glob('update-*.json'))
        record = json.loads(results[0].read_text())
        self.assertEqual(record['previousTag'], self.config['images']['tag'])
        self.assertEqual(record['backendPodsChanged'], [])

    def test_image_updates_detect_replacement_of_an_unrelated_running_pod(self):
        values = self.recipe.stack_values('a'*64, 'b'*64)
        pods = [{'metadata': {'name': 'unrelated-workload', 'uid': 'original'}, 'status': {'phase': 'Running'}}]
        after = copy.deepcopy(pods)
        after[0]['metadata']['uid'] = 'replacement'
        for component in ('gateway', 'router'):
            with self.subTest(component=component):
                with patch.object(self.recipe, 'source_check'), patch.object(self.recipe, 'bound_cluster'), patch.object(spark, 'output', side_effect=[json.dumps(values), json.dumps({'items': pods}), json.dumps({'items': after})]), patch.object(spark, 'run'):
                    with self.assertRaisesRegex(RuntimeError, 'Backend pods changed'):
                        self.recipe.update(component, 'next-tag')
                records = list((pathlib.Path(self.tmp.name)/'evidence').glob('update-*.json'))
                record = json.loads(max(records, key=lambda path: path.stat().st_mtime_ns).read_text())
                self.assertEqual(record['component'], component)
                self.assertEqual(record['backendPodsChanged'], ['unrelated-workload'])

    def test_rollback_restores_the_recorded_component_tag(self):
        values = self.recipe.stack_values('a'*64, 'b'*64)
        values['llm-request-router']['llmRequestRouter']['image']['tag'] = 'next-tag'
        record = {'context': self.config['context'], 'namespace': self.config['namespace'], 'release': self.recipe.stack,
                  'component': 'router', 'newTag': 'next-tag', 'previousTag': 'old-tag'}
        path = pathlib.Path(self.tmp.name)/'rollback.json'
        path.write_text(json.dumps(record))
        with patch.object(self.recipe, 'bound_cluster'), patch.object(spark, 'output', return_value=json.dumps(values)), patch.object(self.recipe, 'update') as update:
            self.recipe.rollback(path)
        update.assert_called_once_with('router', 'old-tag')

    def test_rollback_refuses_a_subsequent_update(self):
        values = self.recipe.stack_values('a'*64, 'b'*64)
        record = {'context': self.config['context'], 'namespace': self.config['namespace'], 'release': self.recipe.stack,
                  'component': 'gateway', 'newTag': 'different-tag', 'previousTag': 'old-tag'}
        path = pathlib.Path(self.tmp.name)/'rollback.json'
        path.write_text(json.dumps(record))
        with patch.object(self.recipe, 'bound_cluster'), patch.object(spark, 'output', return_value=json.dumps(values)), patch.object(self.recipe, 'update') as update:
            with self.assertRaisesRegex(RuntimeError, 'Another image update'):
                self.recipe.rollback(path)
            update.assert_not_called()

    def test_recovery_restores_worker_even_when_down_operation_fails(self):
        self.recipe.state = {'registered': True, 'runtimeSha256': 'a'*64}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'verify'), patch.object(self.recipe, 'helm_apply', side_effect=[RuntimeError('down failed'), None]) as helm:
            with self.assertRaisesRegex(RuntimeError, 'down failed'):
                self.recipe.recovery(True, 18443)
        self.assertEqual(helm.call_count, 2)
        self.assertEqual(helm.call_args_list[0].args[2]['rpc']['replicas'], 0)
        self.assertEqual(helm.call_args_list[1].args[2].get('rpc', {}).get('replicas', 1), 1)

    def test_import_cannot_touch_runtime_sockets_without_opt_in(self):
        with patch.object(self.recipe, 'bound_cluster') as cluster:
            with self.assertRaisesRegex(RuntimeError, 'allow-containerd-import'):
                self.recipe.import_images('/not-used', False)
            cluster.assert_not_called()

    def archive(self, tag):
        path = pathlib.Path(self.tmp.name)/'images.tar'
        data = json.dumps([{'RepoTags': [self.recipe.image('gateway', tag)]}]).encode()
        with tarfile.open(path, 'w') as tar:
            info = tarfile.TarInfo('manifest.json')
            info.size = len(data)
            tar.addfile(info, io.BytesIO(data))
        return path

    def test_gateway_image_import_targets_only_control_node(self):
        archive = self.archive('new-tag')
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply') as helm:
            self.recipe.import_images(archive, True, 'gateway', 'new-tag')
        self.assertEqual(helm.call_args.args[2]['nodeNames'], [self.config['nodes']['control']])
        self.assertEqual(helm.call_args.args[2]['archiveSha256'], hashlib.sha256(archive.read_bytes()).hexdigest())

    def test_image_import_rejects_wrong_tag_before_cluster_mutation(self):
        archive = self.archive('old-tag')
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply') as helm:
            with self.assertRaisesRegex(RuntimeError, 'missing the configured image'):
                self.recipe.import_images(archive, True, 'gateway', 'new-tag')
            helm.assert_not_called()

    def test_existing_attachment_blocks_fresh_stack_and_recovery(self):
        self.recipe.state = {'attachedExisting': True}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply') as helm:
            with self.assertRaisesRegex(RuntimeError, 'fresh stack'):
                self.recipe.deploy_stack()
            with self.assertRaisesRegex(RuntimeError, 'existing backend owner'):
                self.recipe.recovery(True, 18443)
            helm.assert_not_called()

    def test_explicit_release_and_repository_mapping(self):
        self.config['releases'] = {'stack': 'custom-front', 'operator': 'custom-operator', 'glm': 'custom-model'}
        self.config['images']['repositories'] = {'gateway': 'registry.example.com/another/gateway'}
        recipe = spark.Recipe(self.config, self.tmp.name)
        self.assertEqual(recipe.stack, 'custom-front')
        self.assertEqual(recipe.glm, 'custom-model')
        self.assertEqual(recipe.image('gateway', 'new'), 'registry.example.com/another/gateway:new')

    def test_existing_attachment_ownership_failure_makes_no_mutation(self):
        self.config['releases'] = {'stack': 'custom-front', 'operator': 'custom-operator', 'glm': 'custom-model'}
        key = pathlib.Path(self.tmp.name)/'key'
        key.write_text('test-only-key')
        self.config['apiKeyFile'] = str(key)
        recipe = spark.Recipe(self.config, self.tmp.name)
        nodes = {'items': [{'metadata': {'name': name, 'uid': name}} for name in self.config['nodes'].values()]}
        foreign = {'metadata': {'annotations': {'meta.helm.sh/release-name': 'another-owner'}}}
        with patch.object(spark, 'output', side_effect=[json.dumps(nodes), json.dumps(foreign)]), patch.object(recipe, 'helm_apply') as helm:
            with self.assertRaisesRegex(RuntimeError, 'ownership'):
                recipe.attach_existing()
            helm.assert_not_called()
        self.assertFalse(recipe.state_path.exists())


if __name__ == '__main__':
    unittest.main()
