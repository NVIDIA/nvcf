# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import contextlib
import hashlib
import io
import importlib.util
import json
import os
import pathlib
import shutil
import stat
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('recipe_tool', HERE/'recipe.py')
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)


class RecipeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='recipe-test-')
        self.addCleanup(self.tmp.cleanup)
        self.config = json.loads((HERE/'config.example.json').read_text())
        self.recipe = tool.Recipe(self.config, self.tmp.name)

    def source_repository(self, name='seed'):
        source = pathlib.Path(self.tmp.name).resolve()/name
        source.mkdir()
        subprocess.run(['git', 'init', '--quiet', source], check=True)
        for path in tool.COMPONENTS.values():
            (source/path).mkdir(parents=True, exist_ok=True)
        for name in ('llm-gateway-stack', 'llm-api-gateway', 'llm-request-router'):
            chart = source/'deploy/helm'/name/name
            chart.mkdir(parents=True)
            (chart/'Chart.yaml').write_text('apiVersion: v2\nname: '+name+'\nversion: 0.0.0\n')
        (source/'service.txt').write_text('committed service\n')
        subprocess.run(['git', 'add', '.'], cwd=source, check=True)
        subprocess.run(['git', '-c', 'user.name=Recipe Test', '-c', 'user.email=recipe@example.com',
                        '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null',
                        'commit', '--quiet', '-m', 'Initial source'], cwd=source, check=True)
        revision = tool.output(['git', 'rev-parse', 'HEAD'], cwd=source).strip()
        return source, {'repository': str(source), 'revision': revision}

    def test_prepare_only_builds_dependencies_in_the_selected_checkout(self):
        self.recipe.source, lock = self.source_repository()
        with patch.object(tool, 'run') as run:
            self.recipe.prepare()
            self.assertEqual(self.recipe.source_identity(), {'revision': lock['revision']})
        self.assertEqual(tool.output(['git', 'rev-parse', 'HEAD'], cwd=self.recipe.source).strip(), lock['revision'])
        self.assertEqual((self.recipe.source/'service.txt').read_text(), 'committed service\n')
        self.assertEqual(tool.output(['git', 'status', '--porcelain'], cwd=self.recipe.source), '')
        run.assert_called_once_with(['helm', 'dependency', 'build', '--skip-refresh',
                                    self.recipe.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack'])

    def test_default_source_is_the_recipe_checkout_and_ignores_stale_work_source(self):
        seed, lock = self.source_repository()
        stale = self.recipe.work/'source'
        stale.mkdir()
        (stale/'unrelated.txt').write_text('leave this old copy alone\n')
        recipe_path = seed/'deploy/helm/llm-routing/recipes'
        shutil.copytree(HERE/'glm-5.3', recipe_path/'glm-5.3')
        with patch.object(tool, 'HERE', recipe_path), \
             patch.object(tool, 'run') as run:
            recipe = tool.Recipe(self.config, self.recipe.work)
            recipe.prepare()
            recipe.build_images('gateway', 'developer-change')
        self.assertEqual(recipe.source, seed.resolve())
        self.assertEqual(run.call_args.args[0][-1], str(seed/tool.COMPONENTS['gateway']))
        self.assertEqual((stale/'unrelated.txt').read_text(), 'leave this old copy alone\n')

    def test_prepare_and_build_keep_local_edits_at_the_pinned_head(self):
        self.recipe.source, lock = self.source_repository()
        edited = self.recipe.source/'service.txt'
        edited.write_text('local gateway change\n')
        with patch.object(tool, 'run') as run:
            self.recipe.prepare()
            self.recipe.build_images('gateway', 'edited-build')
        self.assertEqual(edited.read_text(), 'local gateway change\n')
        self.assertEqual(tool.output(['git', 'rev-parse', 'HEAD'], cwd=self.recipe.source).strip(), lock['revision'])
        self.assertEqual([call.args[0][0] for call in run.call_args_list], ['helm', 'docker', 'docker'])
        self.assertIn(self.recipe.image('gateway', 'edited-build'), run.call_args.args[0])

    def test_prepare_and_build_accept_committed_descendants_without_resetting_edits(self):
        self.recipe.source, lock = self.source_repository()
        edited = self.recipe.source/'service.txt'
        edited.write_text('committed developer change\n')
        subprocess.run(['git', 'add', 'service.txt'], cwd=self.recipe.source, check=True)
        subprocess.run(['git', '-c', 'user.name=Recipe Test', '-c', 'user.email=recipe@example.com',
                        '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null',
                        'commit', '--quiet', '-m', 'Developer change'], cwd=self.recipe.source, check=True)
        head = tool.output(['git', 'rev-parse', 'HEAD'], cwd=self.recipe.source).strip()
        edited.write_text('uncommitted follow-up\n')
        with patch.object(tool, 'run') as run:
            self.recipe.prepare()
            self.recipe.build_images('router', 'developer-change')
        self.assertNotEqual(head, lock['revision'])
        self.assertEqual(tool.output(['git', 'rev-parse', 'HEAD'], cwd=self.recipe.source).strip(), head)
        self.assertEqual(edited.read_text(), 'uncommitted follow-up\n')
        self.assertEqual(run.call_args.args[0][-1], str(self.recipe.source/tool.COMPONENTS['router']))
        self.assertEqual([call.args[0][0] for call in run.call_args_list], ['helm', 'docker', 'docker'])

    def test_rewritten_history_with_the_same_sources_is_accepted(self):
        self.recipe.source, lock = self.source_repository()
        subprocess.run(['git', 'checkout', '--quiet', '--orphan', 'unrelated'], cwd=self.recipe.source, check=True)
        subprocess.run(['git', '-c', 'user.name=Recipe Test', '-c', 'user.email=recipe@example.com',
                        '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null',
                        'commit', '--quiet', '-m', 'Unrelated history'], cwd=self.recipe.source, check=True)
        for action in (self.recipe.prepare, self.recipe.build_images):
            with self.subTest(action=action.__name__), \
                 patch.object(tool, 'run') as run:
                action()
            run.assert_called()

    def test_source_must_be_the_checkout_root(self):
        seed, lock = self.source_repository()
        self.recipe.source = seed/'nested'
        self.recipe.source.mkdir()
        with patch.object(tool, 'run') as run, self.assertRaises(RuntimeError):
            self.recipe.prepare()
        run.assert_not_called()

    def test_missing_source_does_not_clone_or_create_a_checkout(self):
        self.recipe.source = self.recipe.work/'missing'
        with patch.object(tool, 'run') as run, self.assertRaises(RuntimeError):
            self.recipe.prepare()
        run.assert_not_called()
        self.assertFalse(self.recipe.source.exists())

    def test_render_prepares_dependencies_before_lint_or_template(self):
        operations = []

        def run(command, **kwargs):
            operations.append(tuple(str(value) for value in command[:3]))

        def output(command, **kwargs):
            operations.append(tuple(str(value) for value in command[:2]))
            return 'kind: List\nitems: []\n'

        with patch.object(self.recipe, 'source_check'), patch.object(tool, 'run', side_effect=run), \
             patch.object(tool, 'output', side_effect=output):
            self.recipe.render()
        self.assertEqual(operations[0], ('helm', 'dependency', 'build'))
        self.assertEqual(sum(operation[:2] == ('helm', 'lint') for operation in operations), 8)
        self.assertEqual(sum(operation == ('helm', 'template') for operation in operations), 8)
        self.assertEqual(len(list((self.recipe.work/'render').glob('*.yaml'))), 8)

    def test_render_dependency_failure_stops_before_rendered_files_or_templates(self):
        with patch.object(self.recipe, 'source_check'), patch.object(tool, 'run', side_effect=RuntimeError('dependency build failed')) as run, \
             patch.object(tool, 'output') as output, self.assertRaisesRegex(RuntimeError, 'dependency build failed'):
            self.recipe.render()
        self.assertEqual(run.call_args.args[0][:3], ['helm', 'dependency', 'build'])
        run.assert_called_once()
        output.assert_not_called()
        self.assertFalse((self.recipe.work/'render').exists())

    def test_build_images_does_not_prepare_chart_dependencies(self):
        with patch.object(self.recipe, 'source_check'), patch.object(self.recipe, 'prepare') as prepare, \
             patch.object(tool, 'run') as run:
            self.recipe.build_images('gateway', 'test-build')
        prepare.assert_not_called()
        self.assertTrue(all(command.args[0][0] == 'docker' for command in run.call_args_list))

    def test_chart_digest_detects_changed_added_and_deleted_templates(self):
        self.recipe.source, _ = self.source_repository()
        original = self.recipe.chart_digest()
        for name in ('llm-gateway-stack', 'llm-api-gateway', 'llm-request-router'):
            template = self.recipe.source/'deploy/helm'/name/name/'templates/deployment.yaml'
            template.parent.mkdir(parents=True)
            template.write_text('original template\n')
            added = self.recipe.chart_digest()
            self.assertNotEqual(original, added)
            template.write_text('changed template\n')
            self.assertNotEqual(added, self.recipe.chart_digest())
            template.unlink()
            self.assertEqual(original, self.recipe.chart_digest())

    def test_chart_digest_ignores_source_history_and_generated_dependencies(self):
        self.recipe.source, _ = self.source_repository()
        before = self.recipe.chart_digest()
        (self.recipe.source/'service.txt').write_text('service edit\n')
        chart = self.recipe.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack'
        (chart/'charts').mkdir()
        (chart/'charts/generated.tgz').write_bytes(b'generated dependency archive')
        (chart/'Chart.lock').write_text('generated timestamp\n')
        self.assertEqual(before, self.recipe.chart_digest())
        self.recipe.source_check()

    def test_missing_sources_or_charts_are_rejected_before_preparation(self):
        self.recipe.source, _ = self.source_repository()
        (self.recipe.source/tool.COMPONENTS['gateway']).rmdir()
        with patch.object(tool, 'run') as run, self.assertRaisesRegex(RuntimeError, 'missing required'):
            self.recipe.prepare()
        run.assert_not_called()
        (self.recipe.source/tool.COMPONENTS['gateway']).mkdir()
        (self.recipe.source/'deploy/helm/llm-api-gateway/llm-api-gateway/Chart.yaml').unlink()
        with patch.object(tool, 'run') as run, self.assertRaisesRegex(RuntimeError, 'Missing routing chart'):
            self.recipe.prepare()
        run.assert_not_called()

    def test_missing_or_unreachable_docker_fails_before_build_with_clear_action(self):
        failures = (FileNotFoundError('docker'), subprocess.CalledProcessError(1, ['docker', 'info']),
                    subprocess.TimeoutExpired(['docker', 'info'], 15))
        for error in failures:
            with self.subTest(error=type(error).__name__), patch.object(self.recipe, 'source_check'), \
                 patch.object(tool, 'run', side_effect=error) as run, self.assertRaises(tool.DockerUnavailableError) as result:
                self.recipe.build_images('gateway')
            self.assertEqual(str(result.exception), 'Start Docker, then rerun build-images.')
            run.assert_called_once_with(['docker', 'info'], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=15)

    def test_docker_probe_uses_existing_environment_and_does_not_hide_build_failures(self):
        failure = subprocess.CalledProcessError(1, ['docker', 'buildx', 'build'])
        with patch.dict(os.environ, {'DOCKER_HOST': 'unix:///custom/docker.sock', 'DOCKER_CONFIG': '/custom/config'}), \
             patch.object(self.recipe, 'source_check'), patch.object(tool, 'run', side_effect=[None, failure]) as run, \
             self.assertRaises(subprocess.CalledProcessError):
            self.recipe.build_images('gateway')
        self.assertEqual(run.call_args_list[0].args[0], ['docker', 'info'])
        self.assertNotIn('env', run.call_args_list[0].kwargs)
        self.assertEqual(run.call_args_list[1].args[0][:3], ['docker', 'buildx', 'build'])
        self.assertNotIn('env', run.call_args_list[1].kwargs)

    def test_operator_build_identifies_actual_checkout_and_local_edits(self):
        self.recipe.source, lock = self.source_repository()
        edited = self.recipe.source/'service.txt'
        edited.write_text('operator change\n')
        subprocess.run(['git', 'add', 'service.txt'], cwd=self.recipe.source, check=True)
        subprocess.run(['git', '-c', 'user.name=Recipe Test', '-c', 'user.email=recipe@example.com',
                        '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null',
                        'commit', '--quiet', '-m', 'Operator change'], cwd=self.recipe.source, check=True)
        head = tool.output(['git', 'rev-parse', 'HEAD'], cwd=self.recipe.source).strip()
        self.assertNotEqual(head, lock['revision'])
        for dirty in (False, True):
            if dirty:
                edited.write_text('uncommitted operator change\n')
            with self.subTest(dirty=dirty), patch.object(tool, 'run') as run:
                self.recipe.build_images('operator')
            expected = head + ('-dirty' if dirty else '')
            self.assertIn('SOURCE_REVISION='+expected, run.call_args.args[0])
            self.assertEqual(run.call_args.args[0][-1], str(self.recipe.source/tool.COMPONENTS['operator']))

    def test_duplicate_model_nodes_and_missing_context_are_rejected(self):
        self.config['nodes']['worker'] = self.config['nodes']['leader']
        with self.assertRaisesRegex(RuntimeError, 'distinct GPU'):
            tool.validate(self.config)
        self.config['nodes']['worker'] = 'another-node'
        self.config['context'] = ''
        with self.assertRaisesRegex(RuntimeError, 'context'):
            tool.validate(self.config)

    def test_work_directory_cannot_put_credentials_in_checkout(self):
        with self.assertRaisesRegex(RuntimeError, 'outside the checkout'):
            tool.Recipe(self.config, HERE/'.work')

    def test_extra_model_configuration_is_rejected_before_any_commands(self):
        for field, value in [('retainedModels', ['legacy-model']), ('testFixture', True)]:
            with self.subTest(field=field):
                config = copy.deepcopy(self.config)
                config[field] = value
                directory = pathlib.Path(self.tmp.name)/'rejected'
                with patch.object(tool, 'run') as run, patch.object(tool, 'output') as output:
                    with self.assertRaisesRegex(RuntimeError, field):
                        tool.Recipe(config, directory)
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
        operations = []
        with patch.object(self.recipe, 'source_check'), \
             patch.object(self.recipe, 'bound_cluster', side_effect=lambda: operations.append('bound')), \
             patch.object(self.recipe, 'helm_apply', side_effect=lambda release, *args: operations.append(release)) as helm, \
             patch.object(tool, 'run', side_effect=lambda *args, **kwargs: operations.append('dependencies')) as run, \
             patch.object(tool, 'output', side_effect=responses):
            self.recipe.deploy_stack()
        self.assertEqual(operations, ['bound', 'dependencies', self.recipe.operator, self.recipe.stack])
        self.assertEqual(run.call_args.args[0][:3], ['helm', 'dependency', 'build'])
        run.assert_called_once()
        self.assertEqual(helm.call_count, 2)
        key_path = pathlib.Path(self.tmp.name)/'api-key'
        key = key_path.read_text().strip()
        self.assertEqual(stat.S_IMODE(key_path.stat().st_mode), 0o600)
        stack_values = helm.call_args_list[1].args[2]
        self.assertNotIn(key, json.dumps(stack_values))
        self.assertEqual(stack_values['apiKeys'][0]['sha256'], hashlib.sha256(key.encode()).hexdigest())
        self.assertEqual(stack_values['recipeSource'], self.recipe.source_identity())
        self.assertEqual(self.recipe.components(), ['gateway', 'router', 'pylon', 'operator'])
        self.assertEqual(self.recipe.state['stack']['apiKeyFile'], str(key_path.resolve()))

    def test_inventory_rejects_all_gpu_allocations_on_model_nodes(self):
        nodes = [{'metadata': {'name': name, 'uid': name, 'labels': {'kubernetes.io/arch': 'arm64'}},
                  'status': {'conditions': [{'type': 'Ready', 'status': 'True'}],
                             'allocatable': {'nvidia.com/gpu': '1'}}} for name in self.config['nodes'].values()]
        allocations = [
            {'containers': [{'resources': {'requests': {'nvidia.com/gpu': '1'}}}]},
            {'containers': [{'resources': {'limits': {'nvidia.com/gpu': '1'}}}]},
            {'initContainers': [{'resources': {'requests': {'nvidia.com/gpu': '1'}}}]},
            {'resourceClaims': [{'name': 'gpu', 'resourceClaimName': 'gpu-claim'}]},
        ]
        for allocation in allocations:
            for phase, node, busy in [('Pending', self.config['nodes']['leader'], True),
                                      ('Running', self.config['nodes']['worker'], True),
                                      ('Succeeded', self.config['nodes']['leader'], False),
                                      ('Failed', self.config['nodes']['leader'], False),
                                      ('Running', 'another-node', False)]:
                pod = {'metadata': {'name': 'gpu-consumer', 'namespace': 'another-namespace'},
                       'spec': {'nodeName': node, **allocation}, 'status': {'phase': phase}}
                responses = [json.dumps({'items': items}) for items in (nodes, [pod], [])]
                with self.subTest(allocation=allocation, phase=phase, node=node), \
                     patch.object(tool, 'output', side_effect=responses), patch.object(tool, 'run') as run:
                    if busy:
                        with self.assertRaisesRegex(RuntimeError, 'GPU is occupied: gpu-consumer'):
                            self.recipe.inventory()
                        run.assert_not_called()
                    else:
                        self.recipe.inventory()

    def test_stack_reuses_ui_key_in_existing_release(self):
        encoded = __import__('base64').b64encode(b'private-cluster-token').decode()
        responses = [json.dumps({'data': {'cluster-token': encoded}}), json.dumps({'data': {'ca.crt': 'public-ca'}})]
        with patch.object(self.recipe, 'prepare'), patch.object(self.recipe, 'bound_cluster'), \
             patch.object(self.recipe, 'helm_apply') as helm, patch.object(tool, 'output', side_effect=responses*2):
            self.recipe.deploy_stack()
            first = copy.deepcopy(helm.call_args.args[2])
            self.recipe.deploy_stack()
        values = helm.call_args.args[2]
        self.assertEqual([call.args[0] for call in helm.call_args_list], [self.recipe.operator, self.recipe.stack]*2)
        self.assertEqual(values['apiKeys'], first['apiKeys'])
        self.assertEqual([key['id'] for key in values['apiKeys']], ['poc-client', 'demo-ui'])
        key = (self.recipe.work/'demo-ui-api-key').read_text().strip()
        self.assertEqual(values['demoUiApiKey'], key)
        self.assertEqual(values['apiKeys'][1]['sha256'], hashlib.sha256(key.encode()).hexdigest())
        self.assertNotEqual(values['apiKeys'][0]['sha256'], values['apiKeys'][1]['sha256'])
        self.assertEqual(stat.S_IMODE((self.recipe.work/'demo-ui-api-key').stat().st_mode), 0o600)
        self.assertNotIn('inferenceWriteTimeout', values['llm-api-gateway']['llmApiGateway'].get('config', {}))

    def test_stack_dependency_failure_stops_before_operator_or_key_creation(self):
        self.recipe.state = {'inventory': {'nodes': {'control': 'node-uid'}}}
        original = copy.deepcopy(self.recipe.state)
        with patch.object(self.recipe, 'source_check'), patch.object(self.recipe, 'bound_cluster') as cluster, \
             patch.object(tool, 'run', side_effect=RuntimeError('dependency build failed')) as run, \
             patch.object(self.recipe, 'helm_apply') as helm, patch.object(tool, 'output') as output, \
             self.assertRaisesRegex(RuntimeError, 'dependency build failed'):
            self.recipe.deploy_stack()
        cluster.assert_called_once()
        self.assertEqual(run.call_args.args[0][:3], ['helm', 'dependency', 'build'])
        run.assert_called_once()
        helm.assert_not_called()
        output.assert_not_called()
        self.assertEqual(self.recipe.state, original)
        self.assertFalse((self.recipe.work/'api-key').exists())

    def test_stack_binding_failure_prevents_dependency_preparation(self):
        with patch.object(self.recipe, 'bound_cluster', side_effect=RuntimeError('node identity changed')), \
             patch.object(self.recipe, 'prepare') as prepare, patch.object(self.recipe, 'helm_apply') as helm, \
             self.assertRaisesRegex(RuntimeError, 'node identity changed'):
            self.recipe.deploy_stack()
        prepare.assert_not_called()
        helm.assert_not_called()

    def test_direct_and_gateway_verification_use_the_glm_client(self):
        self.recipe.state = {'stack': {'apiKeyFile': str(pathlib.Path(self.tmp.name)/'api-key'), 'testFixture': True}}
        for gateway in (False, True):
            with self.subTest(gateway=gateway):
                with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'forward') as forward, \
                     patch.object(self.recipe, 'prepare') as prepare, patch.object(tool, 'run') as run:
                    self.recipe.verify(gateway, 18443)
                prepare.assert_not_called()
                command = [str(value) for value in run.call_args.args[0]]
                self.assertEqual(command[command.index('--mode')+1], 'verify')
                self.assertNotIn('--retained-model', command)
                self.assertEqual('--api-key-file' in command, gateway)
                self.assertEqual('--ca-file' in command, gateway)
                self.assertEqual('--cluster-id' in command, gateway)
                if gateway:
                    self.assertEqual(command[command.index('--cluster-id')+1], self.config['clusterId'])
                self.assertEqual(command[command.index('--url')+1], ('https' if gateway else 'http')+'://127.0.0.1:18443')
                forward.assert_called_once_with(gateway, 18443)
                self.assertTrue(self.recipe.state['gateway' if gateway else 'direct'])

    def test_automatic_key_verification_does_not_pass_until_key_revocation(self):
        self.recipe.state = {'stack': {'apiKeyFile': None}, 'gateway': True}

        @contextlib.contextmanager
        def incomplete_cleanup(*args):
            yield pathlib.Path(self.tmp.name)/'temporary-gateway-key'
            raise RuntimeError('revocation failed')

        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'forward'), \
                patch.object(tool.gateway_access, 'temporary_gateway_key', side_effect=incomplete_cleanup), \
                patch.object(tool, 'run') as run:
            with self.assertRaisesRegex(RuntimeError, 'revocation failed'):
                self.recipe.verify(True, 18443)
        run.assert_called_once()
        self.assertFalse(self.recipe.state['gateway'])
        self.assertFalse(tool.Recipe(self.config, self.tmp.name).state['gateway'])

    def test_failed_verification_invalidates_previous_success(self):
        self.recipe.state = {'stack': {'apiKeyFile': '/unused'}, 'gateway': True, 'direct': True}
        for gateway in (False, True):
            with self.subTest(gateway=gateway), patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'forward'), patch.object(tool, 'run', side_effect=RuntimeError('verification failed')):
                with self.assertRaisesRegex(RuntimeError, 'verification failed'):
                    self.recipe.verify(gateway, 18443)
                resumed = tool.Recipe(self.config, self.tmp.name)
                self.assertFalse(resumed.state['gateway' if gateway else 'direct'])

    def test_attached_installation_requires_glm_verification_before_update(self):
        self.recipe.state = {'attachedExisting': True}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'source_check'), patch.object(tool, 'run') as run, patch.object(tool, 'output') as output:
            with self.assertRaisesRegex(RuntimeError, 'Verify the model'):
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

    def qualification_job(self, suffix, condition='Failed', release=None):
        release = release or self.recipe.backend
        return {'metadata': {'name': self.recipe.backend+suffix, 'uid': suffix,
                             'annotations': {'meta.helm.sh/release-name': release,
                                             'meta.helm.sh/release-namespace': self.config['namespace']}},
                'status': {'conditions': [{'type': condition, 'status': 'True'}]}}

    def qualification_pod(self, name, job, phase='Succeeded'):
        return {'metadata': {'name': name, 'ownerReferences': [{'kind': 'Job', 'name': job['metadata']['name'], 'uid': job['metadata']['uid']}]},
                'status': {'phase': phase}}

    def test_qualification_retry_archives_before_helm_and_persists_new_attempts(self):
        self.recipe.state = {'runtimeSha256': 'a'*64, 'download': True}
        jobs = [self.qualification_job('-qualify-5', 'Complete'),
                self.qualification_job('-chain-3', 'Failed', self.recipe.backend+'-chain')]
        pods = [self.qualification_pod('old-qualification', jobs[0]),
                self.qualification_pod('failed-chain', jobs[1], 'Failed')]
        pods.append({'metadata': {'name': 'unrelated'}, 'status': {'phase': 'Running'}})
        responses = [json.dumps({'items': jobs}), json.dumps({'items': pods}), '{"result":"PASS"}', 'chain failed']

        def check_archive(*args, **kwargs):
            archived = list((self.recipe.work/'evidence').glob('qualification-retry-*'))
            self.assertEqual(len(archived), 1)
            self.assertEqual(json.loads((archived[0]/'jobs.json').read_text())['items'], jobs)
            self.assertEqual(len(json.loads((archived[0]/'pods.json').read_text())['items']), 2)
            self.assertEqual((archived[0]/'failed-chain.log').read_text(), 'chain failed')
            saved = json.loads(self.recipe.state_path.read_text())
            self.assertFalse(saved['qualify'])
            self.assertFalse(saved['download'])

        with patch.object(self.recipe, 'bound_cluster'), patch.object(tool, 'output', side_effect=responses), patch.object(self.recipe, 'helm_apply', side_effect=check_archive) as helm, patch.object(self.recipe, 'logs', return_value=[{'result': 'PASS'}]) as logs:
            self.recipe.backend_phase('qualify', retry=True)
        self.assertEqual(helm.call_args_list[0].args[2]['qualification']['attempt'], 6)
        self.assertEqual(helm.call_args_list[1].args[2]['chain']['attempt'], 4)
        self.assertEqual(logs.call_args_list[0].kwargs['job'], self.recipe.backend+'-qualify-6')
        self.assertEqual(logs.call_args_list[1].kwargs['job'], self.recipe.backend+'-chain-4')
        resumed = tool.Recipe(self.config, self.tmp.name)
        self.assertTrue(resumed.state['qualify'])
        self.assertEqual(resumed.backend_values('qualify')['qualification']['attempt'], 6)
        self.assertEqual(resumed.backend_values('chain')['chain']['attempt'], 4)

    def test_retry_handles_legacy_failed_qualification_and_unavailable_pod_logs(self):
        self.recipe.state = {'runtimeSha256': 'a'*64}
        defaults = self.recipe.backend_values('qualify')
        attempt = defaults['qualification']['attempt']
        job = self.qualification_job('-qualify-'+str(attempt))
        pod = self.qualification_pod('node-interrupted', job, 'Failed')
        responses = [json.dumps({'items': [job]}), json.dumps({'items': [pod]}),
                     tool.subprocess.CalledProcessError(1, ['kubectl', 'logs'], output='container logs unavailable')]
        with patch.object(self.recipe, 'bound_cluster'), patch.object(tool, 'output', side_effect=responses), patch.object(self.recipe, 'helm_apply', side_effect=RuntimeError('Helm interrupted')) as helm:
            with self.assertRaisesRegex(RuntimeError, 'Helm interrupted'):
                self.recipe.backend_phase('qualify', retry=True)
        self.assertEqual(helm.call_args.args[2]['qualification']['attempt'], attempt+1)
        self.assertEqual(helm.call_args.args[2]['chain']['attempt'], defaults['chain']['attempt']+1)
        archived = list((self.recipe.work/'evidence').glob('qualification-retry-*'))[0]
        self.assertEqual((archived/'node-interrupted-log-error.txt').read_text(), 'container logs unavailable')
        resumed = tool.Recipe(self.config, self.tmp.name)
        self.assertEqual(resumed.state['qualificationAttempt'], attempt+1)
        self.assertFalse(resumed.state['qualify'])

    def test_qualification_retry_refuses_active_or_foreign_jobs_before_mutation(self):
        self.recipe.state = {'runtimeSha256': 'a'*64}
        cases = [(self.qualification_job('-qualify-2', 'Running'), 'still active'),
                 (self.qualification_job('-chain-1', release='another-owner'), 'ownership')]
        for job, error in cases:
            with self.subTest(error=error), patch.object(self.recipe, 'bound_cluster'), patch.object(tool, 'output', return_value=json.dumps({'items': [job]})), patch.object(self.recipe, 'helm_apply') as helm:
                with self.assertRaisesRegex(RuntimeError, error):
                    self.recipe.backend_phase('qualify', retry=True)
                helm.assert_not_called()
                self.assertFalse(self.recipe.state_path.exists())

    def test_current_job_acceptance_ignores_stale_and_failed_pod_pass_records(self):
        current = self.qualification_job('-qualify-3', 'Complete')
        old = self.qualification_job('-qualify-2', 'Complete')
        pods = [self.qualification_pod('old-pass', old),
                self.qualification_pod('current-failed', current, 'Failed'),
                self.qualification_pod('current-complete', current)]
        for current_log, expected in [('no PASS record', []), ('{"result":"PASS","current":true}', [{'result': 'PASS', 'current': True}])]:
            with self.subTest(current_log=current_log), patch.object(tool, 'output', side_effect=[json.dumps({'items': pods}), '{"result":"PASS"}', current_log]) as output:
                self.assertEqual(self.recipe.logs('qualification', job=current['metadata']['name']), expected)
                names = [call.args[0][-1] for call in output.call_args_list[1:]]
                self.assertNotIn('old-pass', names)

    def test_qualification_does_not_pass_without_current_chain_evidence(self):
        self.recipe.state = {'runtimeSha256': 'a'*64}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply'), patch.object(self.recipe, 'logs', side_effect=[[{'result': 'PASS'}], []]):
            with self.assertRaisesRegex(RuntimeError, 'chain check did not record PASS'):
                self.recipe.backend_phase('qualify')
        self.assertFalse(self.recipe.state.get('qualify'))

    def test_failed_qualification_invalidates_previous_success_before_helm(self):
        self.recipe.state = {'runtimeSha256': 'a'*64, 'qualify': True, 'download': True}

        def fail_helm(*args, **kwargs):
            saved = json.loads(self.recipe.state_path.read_text())
            self.assertFalse(saved['qualify'])
            self.assertFalse(saved['download'])
            raise RuntimeError('qualification failed')

        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply', side_effect=fail_helm):
            with self.assertRaisesRegex(RuntimeError, 'qualification failed'):
                self.recipe.backend_phase('qualify')
        resumed = tool.Recipe(self.config, self.tmp.name)
        self.assertFalse(resumed.state['qualify'])
        self.assertFalse(resumed.state['download'])
        with patch.object(resumed, 'bound_cluster'), patch.object(resumed, 'helm_apply') as helm:
            for phase, prerequisite in [('download', 'qualify'), ('serve', 'download')]:
                with self.subTest(phase=phase), self.assertRaisesRegex(RuntimeError, 'Missing successful '+prerequisite):
                    resumed.backend_phase(phase)
            helm.assert_not_called()

    def test_retry_option_rejects_other_phases_before_recipe_creation(self):
        for phase in ('load', 'download', 'stack', 'recover'):
            args = ['recipe.py', '--config', '/unused', '--work-dir', '/unused', phase, '--retry']
            with self.subTest(phase=phase), patch.object(tool.sys, 'argv', args), patch.object(tool, 'Recipe') as recipe:
                with self.assertRaisesRegex(RuntimeError, 'only for qualify'):
                    tool.main()
                recipe.assert_not_called()

    def test_retry_does_not_replace_already_successful_qualification(self):
        self.recipe.state = {'runtimeSha256': 'a'*64, 'qualify': True}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(tool, 'output') as output, patch.object(self.recipe, 'helm_apply') as helm:
            with self.assertRaisesRegex(RuntimeError, 'already passed'):
                self.recipe.backend_phase('qualify', retry=True)
            output.assert_not_called()
            helm.assert_not_called()

    def test_image_update_only_changes_selected_tag_and_preserves_other_pods(self):
        values = self.recipe.stack_values('a'*64, 'b'*64)
        values['recipeSource'] = {'revision': 'an-older-build'}
        pods = [{'metadata': {'name': 'llm-api-gateway-old', 'uid': 'g1'}, 'status': {'phase': 'Running'}},
                {'metadata': {'name': 'unrelated-workload', 'uid': 'u1'}, 'status': {'phase': 'Running'}},
                {'metadata': {'name': self.recipe.backend+'-leader', 'uid': 'm1'}, 'status': {'phase': 'Running'}}]
        after = copy.deepcopy(pods)
        after[0]['metadata']['uid'] = 'g2'
        with patch.object(self.recipe, 'source_check') as source, patch.object(self.recipe, 'bound_cluster'), patch.object(tool, 'output', side_effect=[json.dumps(values), json.dumps({'items': pods}), json.dumps({'items': after})]), patch.object(tool, 'run') as run:
            self.recipe.update('gateway', 'next-tag')
        self.assertEqual(source.call_args_list[0].kwargs, {})
        self.assertEqual(source.call_count, 2)
        self.assertEqual(run.call_args_list[0].args[0][:3], ['helm', 'dependency', 'build'])
        self.assertEqual(run.call_count, 2)
        command = run.call_args.args[0]
        self.assertIn('--reuse-values', command)
        self.assertIn('llm-api-gateway.llmApiGateway.image.tag=next-tag', command)
        self.assertIn(self.config['context'], command)
        results = list((pathlib.Path(self.tmp.name)/'evidence').glob('update-*.json'))
        record = json.loads(results[0].read_text())
        self.assertEqual(record['previousTag'], self.config['images']['tag'])
        self.assertEqual(record['backendPodsChanged'], [])
        self.assertEqual(record['chartsSha256'], self.recipe.chart_digest())
        self.assertEqual(record['source'], self.recipe.source_identity())

    def test_update_dependency_failure_stops_before_update_record_or_upgrade(self):
        values = self.recipe.stack_values('a'*64, 'b'*64)
        self.recipe.state = {'attachedExisting': True, 'gateway': True}
        original = copy.deepcopy(self.recipe.state)

        def read(command, **kwargs):
            if command[:len(self.recipe.hm)+3] == self.recipe.hm+['get', 'values', self.recipe.stack]:
                return json.dumps(values)
            self.assertIn('pods', command)
            return '{"items": []}'

        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'source_check'), \
             patch.object(tool, 'output', side_effect=read), \
             patch.object(tool, 'run', side_effect=RuntimeError('dependency build failed')) as run, \
             self.assertRaisesRegex(RuntimeError, 'dependency build failed'):
            self.recipe.update('gateway', 'next-tag')
        self.assertEqual(run.call_args.args[0][:3], ['helm', 'dependency', 'build'])
        run.assert_called_once()
        self.assertFalse((self.recipe.work/'evidence').exists())
        self.assertEqual(self.recipe.state, original)

    def test_update_live_repository_or_tag_mismatch_stops_before_preparation(self):
        for mismatch in ('repository', 'tag'):
            values = self.recipe.stack_values('a'*64, 'b'*64)
            image = values['llm-api-gateway']['llmApiGateway']['image']
            if mismatch == 'repository':
                image['repository'] = 'other/gateway'
            else:
                image['tag'] = 'next-tag'
            with self.subTest(mismatch=mismatch), patch.object(self.recipe, 'bound_cluster'), \
                 patch.object(self.recipe, 'source_check'), patch.object(self.recipe, 'prepare') as prepare, \
                 patch.object(tool, 'output', return_value=json.dumps(values)), patch.object(tool, 'run') as run, \
                 self.assertRaises(RuntimeError):
                self.recipe.update('gateway', 'next-tag')
            prepare.assert_not_called()
            run.assert_not_called()
            self.assertFalse((self.recipe.work/'evidence').exists())

    def test_image_update_rejects_missing_or_changed_charts_before_mutation(self):
        self.recipe.state = {'attachedExisting': True, 'gateway': True}
        for digest in (None, '0'*64):
            values = self.recipe.stack_values('a'*64, 'b'*64)
            values['recipeChartsSha256'] = digest
            with self.subTest(digest=digest), patch.object(self.recipe, 'source_check'), \
                 patch.object(self.recipe, 'bound_cluster'), patch.object(tool, 'output', return_value=json.dumps(values)), \
                 patch.object(tool, 'run') as run:
                with self.assertRaisesRegex(RuntimeError, 'coordinated stack installation'):
                    self.recipe.update('gateway', 'next-tag')
                run.assert_not_called()
                self.assertFalse((self.recipe.work/'evidence').exists())

    def test_image_updates_detect_replacement_of_an_unrelated_running_pod(self):
        values = self.recipe.stack_values('a'*64, 'b'*64)
        pods = [{'metadata': {'name': 'unrelated-workload', 'uid': 'original'}, 'status': {'phase': 'Running'}}]
        after = copy.deepcopy(pods)
        after[0]['metadata']['uid'] = 'replacement'
        for component in ('gateway', 'router'):
            with self.subTest(component=component):
                with patch.object(self.recipe, 'source_check'), patch.object(self.recipe, 'bound_cluster'), patch.object(tool, 'output', side_effect=[json.dumps(values), json.dumps({'items': pods}), json.dumps({'items': after})]), patch.object(tool, 'run'):
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
                  'component': 'router', 'newTag': 'next-tag', 'previousTag': 'old-tag', 'source': {'revision': 'an-older-build'}, 'chartsSha256': self.recipe.chart_digest()}
        path = pathlib.Path(self.tmp.name)/'rollback.json'
        path.write_text(json.dumps(record))
        with patch.object(self.recipe, 'bound_cluster'), patch.object(tool, 'output', return_value=json.dumps(values)), patch.object(self.recipe, 'update') as update:
            self.recipe.rollback(path)
        update.assert_called_once_with('router', 'old-tag')

    def test_rollback_automatically_prepares_before_the_restoring_upgrade(self):
        values = self.recipe.stack_values('a'*64, 'b'*64)
        values['llm-request-router']['llmRequestRouter']['image']['tag'] = 'next-tag'
        record = {'context': self.config['context'], 'namespace': self.config['namespace'], 'release': self.recipe.stack,
                  'component': 'router', 'newTag': 'next-tag', 'previousTag': 'old-tag', 'source': {'revision': 'an-older-build'}, 'chartsSha256': self.recipe.chart_digest()}
        path = self.recipe.work/'rollback.json'
        path.write_text(json.dumps(record))
        reads = [json.dumps(values), json.dumps(values), '{"items": []}', '{"items": []}']
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'source_check'), \
             patch.object(tool, 'output', side_effect=reads), patch.object(tool, 'run') as run:
            self.recipe.rollback(path)
        self.assertEqual(run.call_count, 2)
        self.assertEqual(run.call_args_list[0].args[0][:3], ['helm', 'dependency', 'build'])
        self.assertIn('llm-request-router.llmRequestRouter.image.tag=old-tag', run.call_args_list[1].args[0])

    def test_rollback_refuses_a_subsequent_update(self):
        values = self.recipe.stack_values('a'*64, 'b'*64)
        record = {'context': self.config['context'], 'namespace': self.config['namespace'], 'release': self.recipe.stack,
                  'component': 'gateway', 'newTag': 'different-tag', 'previousTag': 'old-tag', 'source': {'revision': 'an-older-build'}, 'chartsSha256': self.recipe.chart_digest()}
        path = pathlib.Path(self.tmp.name)/'rollback.json'
        path.write_text(json.dumps(record))
        with patch.object(self.recipe, 'bound_cluster'), patch.object(tool, 'output', return_value=json.dumps(values)), patch.object(self.recipe, 'update') as update:
            with self.assertRaisesRegex(RuntimeError, 'Another image update'):
                self.recipe.rollback(path)
            update.assert_not_called()

    def test_rollback_rejects_missing_or_changed_charts_before_reading_release(self):
        record = {'context': self.config['context'], 'namespace': self.config['namespace'], 'release': self.recipe.stack,
                  'component': 'gateway', 'newTag': 'next-tag', 'previousTag': 'old-tag'}
        path = self.recipe.work/'rollback.json'
        for digest in (None, '0'*64):
            record['chartsSha256'] = digest
            path.write_text(json.dumps(record))
            with self.subTest(digest=digest), patch.object(self.recipe, 'bound_cluster'), \
                 patch.object(tool, 'output') as output, patch.object(self.recipe, 'update') as update:
                with self.assertRaisesRegex(RuntimeError, 'chart fingerprint'):
                    self.recipe.rollback(path)
                output.assert_not_called()
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

    def test_image_import_rejects_wrong_tag_before_cluster_mutation(self):
        archive = self.archive('old-tag')
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply') as helm:
            with self.assertRaisesRegex(RuntimeError, 'missing the configured image'):
                self.recipe.import_images(archive, True, 'gateway', 'new-tag')
            helm.assert_not_called()

    def test_existing_attachment_blocks_fresh_stack_and_recovery(self):
        self.recipe.state = {'attachedExisting': True}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'prepare') as prepare, \
             patch.object(self.recipe, 'helm_apply') as helm:
            with self.assertRaisesRegex(RuntimeError, 'fresh stack'):
                self.recipe.deploy_stack()
            with self.assertRaisesRegex(RuntimeError, 'existing backend owner'):
                self.recipe.recovery(True, 18443)
            prepare.assert_not_called()
            helm.assert_not_called()

    def test_explicit_release_and_repository_mapping(self):
        self.config['releases'] = {'stack': 'custom-front', 'operator': 'custom-operator', 'model': 'custom-model'}
        self.config['images']['repositories'] = {'gateway': 'registry.example.com/another/gateway'}
        recipe = tool.Recipe(self.config, self.tmp.name)
        self.assertEqual(recipe.stack, 'custom-front')
        self.assertEqual(recipe.backend, 'custom-model')
        self.assertEqual(recipe.image('gateway', 'new'), 'registry.example.com/another/gateway:new')

    def test_existing_attachment_ownership_failure_makes_no_mutation(self):
        self.config['releases'] = {'stack': 'custom-front', 'operator': 'custom-operator', 'model': 'custom-model'}
        key = pathlib.Path(self.tmp.name)/'key'
        key.write_text('test-only-key')
        self.config['apiKeyFile'] = str(key)
        recipe = tool.Recipe(self.config, self.tmp.name)
        nodes = {'items': [{'metadata': {'name': name, 'uid': name}} for name in self.config['nodes'].values()]}
        foreign = {'metadata': {'annotations': {'meta.helm.sh/release-name': 'another-owner'}}}
        with patch.object(tool, 'output', side_effect=[json.dumps(nodes), json.dumps(foreign)]), patch.object(recipe, 'helm_apply') as helm:
            with self.assertRaisesRegex(RuntimeError, 'ownership'):
                recipe.attach_existing()
            helm.assert_not_called()
        self.assertFalse(recipe.state_path.exists())


if __name__ == '__main__':
    unittest.main()
