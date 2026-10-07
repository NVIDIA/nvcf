# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import argparse
import contextlib
import copy
import io
import json
import pathlib
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(HERE))
import deploy_models
import recipes
from test_recipes import snapshot

RUN_COMMAND = deploy_models.Deployment.run


class DeployModelsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = pathlib.Path(self.tmp.name).resolve()
        self.key = self.base / 'api-key'
        self.key.write_text('test-caller-key')
        self.connection = {'context': 'recipe-test', 'namespace': 'model-test', 'stackRelease': 'shared-stack',
                           'operatorRelease': 'shared-operator', 'apiKeyFile': str(self.key)}
        self.stack = Mock()
        self.stack.load_connection.return_value = self.connection
        self.stack.inspect_connection.return_value = {'ca': 'test-ca'}
        self.stack.forward_connection.side_effect = lambda *args: contextlib.nullcontext('https://127.0.0.1:1234/v1')
        self.args = argparse.Namespace(work_dir=self.base / 'deployment', stack_connection=self.base / 'connection.json',
            model=['qwen3.8-27b', 'qwen3.8-27b-nvfp4'], storage_class='local-path', runtime_class='nvidia',
            capabilities=None, requirements=None, context_length=8192, concurrency=1,
            no_nvme_offload=False, preference='fewest-nodes')
        self.inventory = snapshot(2)
        self.commands = []
        self.values = {}
        self.phase = None
        self.blocked_release = False
        self.existing_resources = []
        self.failed_qualification = False
        self.bad_marker = False
        self.bad_download = False
        self.missing_rank = False
        for target, value in [('stack_api', self.stack), ('inventory', self.inventory), ('check_plan', {'passed': True})]:
            patcher = patch.object(recipes, target, return_value=value)
            patcher.start()
            self.addCleanup(patcher.stop)
        patcher = patch.object(deploy_models.verify, 'verify', return_value={'passed': True, 'models': []})
        self.verifier = patcher.start()
        self.addCleanup(patcher.stop)
        patcher = patch.object(deploy_models.Deployment, 'run', autospec=True, side_effect=self.fake_run)
        patcher.start()
        self.addCleanup(patcher.stop)

    def fake_run(self, deployment, command, label, timeout=90):
        command = [str(arg) for arg in command]
        self.commands.append(command)
        if command[0] == 'helm':
            action = command[5]
            if action == 'list':
                return json.dumps([{'name': 'qwen3-8-27b'}] if self.blocked_release else [])
            if action == 'template':
                return '# rendered\n'
            if action in ('install', 'upgrade'):
                name = command[6]
                self.phase = command[command.index('--set') + 1].split('=')[1]
                self.values[name] = dict(json.loads(pathlib.Path(command[command.index('--values') + 1]).read_text()), phase=self.phase)
                return 'Applied model release\n'
            if action == 'get':
                return json.dumps(self.values[command[7]])
        if command[0] == 'kubectl':
            if command[5] == 'get':
                items = copy.deepcopy(self.existing_resources)
                if self.phase in ('qualify', 'download'):
                    for release in deployment.result['releases']:
                        for rank in range(len(release['nodes'])):
                            status = {'succeeded': 1}
                            if self.failed_qualification and self.phase == 'qualify':
                                status = {'conditions': [{'type': 'Failed', 'status': 'True'}]}
                            items.append({'kind': 'Job', 'metadata': {'name': release['name'] + '-' + self.phase + '-' + str(rank)}, 'status': status})
                if self.phase == 'serve':
                    for release in deployment.result['releases']:
                        items.append({'kind': 'InferenceEndpoint', 'metadata': {'name': release['name']},
                            'spec': {'modelName': release['model']}, 'status': {'conditions': [
                            {'type': condition, 'status': 'True'} for condition in ('Ready', 'TransportReady', 'Registered')]}})
                        for rank in range(len(release['nodes'])):
                            if self.missing_rank:
                                continue
                            items.append({'kind': 'Deployment', 'metadata': {'name': release['name'] + '-serve-' + str(rank), 'generation': 1},
                                          'status': {'observedGeneration': 1, 'readyReplicas': 1}})
                return json.dumps({'items': items})
            if command[5] == 'logs':
                job = command[6].removeprefix('job/')
                release = next(r for r in deployment.result['releases'] if job.startswith(r['name'] + '-' + self.phase))
                return json.dumps({'event': 'invalid' if self.bad_marker else ('qualification_pass' if self.phase == 'qualify' else 'download_pass'),
                                   'model': release['model'], 'revision': 'wrong' if self.bad_download else release['values']['model']['revision'],
                                   'rank': int(job.rsplit('-', 1)[1]), 'nodes': len(release['nodes'])})
        raise AssertionError('Unexpected command: ' + str(command))

    def deploy(self):
        with contextlib.redirect_stdout(io.StringIO()):
            return deploy_models.deploy(self.args)

    def mutations(self):
        return [command for command in self.commands if command[0] == 'helm' and command[5] in ('install', 'upgrade')]

    def test_two_models_complete_and_verify_together_with_one_connection(self):
        self.assertTrue(self.deploy()['passed'])
        self.assertEqual([c[c.index('--set') + 1] for c in self.mutations()], ['phase=qualify'] * 2 + ['phase=download'] * 2 + ['phase=serve'] * 2)
        self.verifier.assert_called_once_with('https://127.0.0.1:1234/v1', self.args.work_dir / 'ca.crt', 'test-caller-key', self.args.model)
        self.assertEqual(self.args.work_dir.stat().st_mode & 0o777, 0o700)
        self.assertTrue((self.args.work_dir / 'verification.json').exists())
        for command in self.commands:
            flag = '--context' if command[0] == 'kubectl' else '--kube-context'
            self.assertEqual(command[command.index(flag) + 1], self.connection['context'])
            self.assertEqual(command[command.index('-n') + 1], self.connection['namespace'])
        for command in self.mutations():
            self.assertIn(command[6], ('qwen3-8-27b', 'qwen3-8-27b-nvfp4'))
            self.assertEqual(command[7], str(HERE / 'charts/sglang'))
            self.assertNotIn('--install', command)
        self.assertTrue(all(c[5] in ('get', 'logs') for c in self.commands if c[0] == 'kubectl'))

    def test_single_model_is_verified(self):
        self.args.model = self.args.model[:1]
        self.deploy()
        self.assertEqual(self.verifier.call_args.args[-1], self.args.model)

    def test_failed_qualification_blocks_download_and_serving(self):
        self.failed_qualification = True
        with self.assertRaisesRegex(ValueError, 'Job failed'):
            self.deploy()
        self.assertEqual([c[c.index('--set') + 1] for c in self.mutations()], ['phase=qualify'] * 2)
        self.verifier.assert_not_called()
        self.assertTrue((self.args.work_dir / 'failure.json').exists())

    def test_completed_job_without_runtime_proof_blocks_next_phase(self):
        self.bad_marker = True
        with self.assertRaisesRegex(ValueError, 'qualification_pass'):
            self.deploy()
        self.assertTrue(all('phase=qualify' in c for c in self.mutations()))
        self.verifier.assert_not_called()

    def test_existing_work_directory_is_not_overwritten(self):
        self.args.work_dir.mkdir()
        marker = self.args.work_dir / 'saved'
        marker.write_text('preserved')
        with self.assertRaisesRegex(ValueError, 'new deployment work directory'):
            self.deploy()
        self.assertEqual(marker.read_text(), 'preserved')
        self.assertEqual(self.commands, [])

    def test_existing_release_blocks_all_mutations(self):
        self.blocked_release = True
        with self.assertRaisesRegex(ValueError, 'already exists'):
            self.deploy()
        self.assertEqual(self.mutations(), [])

    def test_retained_cache_blocks_all_mutations(self):
        self.existing_resources = [{'kind': 'PersistentVolumeClaim', 'metadata': {'name': 'qwen3-8-27b-cache-0'}}]
        with self.assertRaisesRegex(ValueError, 'retained caches'):
            self.deploy()
        self.assertEqual(self.mutations(), [])

    def test_common_name_prefix_does_not_confuse_another_precision(self):
        self.args.model = self.args.model[:1]
        self.existing_resources = [{'kind': 'PersistentVolumeClaim', 'metadata': {'name': 'qwen3-8-27b-nvfp4-cache-0'}}]
        self.deploy()

    def test_failed_binding_blocks_mutations(self):
        self.stack.inspect_connection.side_effect = ValueError('Shared resource replaced')
        with self.assertRaisesRegex(ValueError, 'Shared resource replaced'):
            self.deploy()
        self.assertEqual(self.mutations(), [])

    def test_changed_capacity_blocks_mutations(self):
        recipes.check_plan.side_effect = ValueError('GPU occupied')
        with self.assertRaisesRegex(ValueError, 'GPU occupied'):
            self.deploy()
        self.assertEqual(self.mutations(), [])

    def test_external_values_change_blocks_upgrade(self):
        original = self.fake_run
        def changed(deployment, command, label, timeout=90):
            if 'get' in command and 'values' in command:
                return '{}'
            return original(deployment, command, label, timeout)
        deploy_models.Deployment.run.side_effect = changed
        with self.assertRaisesRegex(ValueError, 'values changed'):
            self.deploy()
        self.assertTrue(all(c[5] == 'install' for c in self.mutations()))

    def test_missing_rank_readiness_blocks_gateway_verification(self):
        deployment = deploy_models.Deployment(self.args)
        with contextlib.redirect_stdout(io.StringIO()):
            deployment.prepare()
        self.phase = 'serve'
        self.missing_rank = True
        with patch.dict(deploy_models.PHASE_TIMEOUTS, {'serve': 2}), \
                patch.object(deploy_models.time, 'monotonic', side_effect=[0, 0, 1, 2, 3]), \
                patch.object(deploy_models.time, 'sleep'):
            with self.assertRaisesRegex(ValueError, 'did not become'):
                deployment.wait_serving()
        self.verifier.assert_not_called()

    def test_wrong_download_revision_blocks_serving(self):
        self.bad_download = True
        with self.assertRaisesRegex(ValueError, 'download_pass'):
            self.deploy()
        self.assertFalse(any('phase=serve' in command for command in self.mutations()))
        self.verifier.assert_not_called()

    def test_cli_dispatch_preserves_requested_models_and_options(self):
        args = ['recipes.py', 'deploy', '--stack-connection', 'connection.json', '--work-dir', 'new-work',
                '--model', 'qwen3.8-27b', '--model', 'qwen3.8-27b-nvfp4',
                '--storage-class', 'local-path', '--runtime-class', 'nvidia']
        with patch.object(sys, 'argv', args), patch.object(deploy_models, 'deploy') as deploy:
            recipes.main()
        selected = deploy.call_args.args[0]
        self.assertEqual(selected.model, self.args.model)
        self.assertEqual(selected.stack_connection, pathlib.Path('connection.json'))
        self.assertEqual(selected.context_length, 8192)

    def test_command_failure_keeps_private_log_without_printing_output(self):
        deployment = deploy_models.Deployment(self.args)
        deployment.work.mkdir()
        result = Mock(returncode=1, stdout='diagnostic details')
        with patch.object(deploy_models.subprocess, 'run', return_value=result):
            with self.assertRaisesRegex(ValueError, 'Command failed. Read') as error:
                RUN_COMMAND(deployment, ['helm', 'template'], 'render')
        self.assertNotIn('diagnostic details', str(error.exception))
        log = deployment.work / 'logs/0001-render.log'
        self.assertEqual(log.read_text(), 'diagnostic details')
        self.assertEqual(log.stat().st_mode & 0o777, 0o600)


if __name__ == '__main__':
    unittest.main()
