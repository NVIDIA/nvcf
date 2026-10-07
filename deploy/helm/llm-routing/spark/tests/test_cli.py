# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('cli_recipe', HERE/'spark.py')
spark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(spark)


class CliTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name).resolve()
        self.home = self.root/'home'
        self.home.mkdir()
        self.env = patch.dict(os.environ, {'HOME': str(self.home), 'SPARK_CONTEXT': 'team-context', 'XDG_STATE_HOME': ''})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.config = json.loads((HERE/'config.example.json').read_text())
        self.config['context'] = 'team-context'
        self.config['releases'] = {'stack': 'test-stack', 'operator': 'test-operator', 'glm': 'test-glm'}
        self.work = spark.default_work_dir('team-context')

    def write_config(self, config=None, path=None):
        path = path or self.work/'config.json'
        spark.save(path, config or self.config)
        return path

    def test_default_state_is_private_path_outside_checkout(self):
        digest = hashlib.sha256(b'team-context').hexdigest()[:20]
        self.assertEqual(self.work, self.home/'.local/state/nvcf/llm-routing'/digest)
        self.assertFalse(self.work.exists())
        self.assertFalse(self.work.is_relative_to(HERE.parents[3]))

    def test_context_names_cannot_escape_storage_and_contexts_are_isolated(self):
        other = spark.default_work_dir('../../another context')
        self.assertNotEqual(other, self.work)
        self.assertEqual(other.parent, self.work.parent)
        self.assertRegex(other.name, r'^[0-9a-f]{20}$')

    def test_absolute_xdg_state_home_is_used(self):
        with patch.dict(os.environ, {'XDG_STATE_HOME': str(self.root/'state')}):
            result = spark.default_work_dir('team-context')
        self.assertEqual(result.parent, self.root/'state/nvcf/llm-routing')

    def test_relative_xdg_is_rejected(self):
        with patch.dict(os.environ, {'XDG_STATE_HOME': 'relative'}), self.assertRaisesRegex(RuntimeError, 'absolute'):
            spark.main(['paths'])
        self.assertFalse(self.work.exists())

    def test_empty_kubeconfig_fails_with_actionable_error_without_traceback(self):
        stream = io.StringIO()
        with patch.dict(os.environ, {'SPARK_CONTEXT': ''}), patch.object(spark, 'output', return_value='{}') as output, \
             redirect_stderr(stream), self.assertRaises(SystemExit) as result:
            spark.main(['attach-existing'])
        self.assertEqual(result.exception.code, 2)
        self.assertEqual(output.call_args.args[0], ['kubectl', 'config', 'view', '-o', 'json'])
        self.assertIn('No Kubernetes context found. Set KUBECONFIG', stream.getvalue())
        self.assertNotIn('Traceback', stream.getvalue())
        self.assertFalse(self.work.exists())

    def test_sole_kubeconfig_context_supports_attach_and_verify_without_environment_context(self):
        local = {'contexts': [{'name': 'team-context'}], 'current-context': 'unrelated'}
        with patch.dict(os.environ, {'SPARK_CONTEXT': '', 'KUBECONFIG': str(self.root/'team-kubeconfig')}), \
             patch.object(spark, 'output', return_value=json.dumps(local)) as output, \
             patch.object(spark, 'discover_config', return_value=self.config) as discover, \
             patch.object(spark.Recipe, 'attach_existing'), patch.object(spark.Recipe, 'verify') as verify, \
             redirect_stdout(io.StringIO()):
            spark.main(['attach-existing'])
            spark.main(['verify-gateway'])
        discover.assert_called_once_with('team-context', None)
        verify.assert_called_once_with(True, 18443)
        self.assertEqual(output.call_count, 2)
        for call in output.call_args_list:
            self.assertEqual(call.args[0], ['kubectl', 'config', 'view', '-o', 'json'])
        self.assertEqual(json.loads((self.work/'config.json').read_text())['context'], 'team-context')

    def test_multiple_contexts_never_choose_current_context_even_with_explicit_kubeconfig(self):
        local = {'contexts': [{'name': 'team-context'}, {'name': 'unrelated'}], 'current-context': 'unrelated'}
        for kubeconfig in ('', str(self.root/'team-kubeconfig')):
            with self.subTest(kubeconfig=kubeconfig), patch.dict(os.environ, {'SPARK_CONTEXT': '', 'KUBECONFIG': kubeconfig}), \
                 patch.object(spark, 'output', return_value=json.dumps(local)), patch.object(spark, 'discover_config') as discover, \
                 redirect_stderr(io.StringIO()) as errors, self.assertRaises(SystemExit) as result:
                spark.main(['attach-existing'])
            self.assertEqual(result.exception.code, 2)
            self.assertIn('Multiple Kubernetes contexts found. Pass --context NAME', errors.getvalue())
            self.assertNotIn('Traceback', errors.getvalue())
            discover.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_explicit_context_sources_precede_kubeconfig_discovery(self):
        config_path = self.write_config(path=self.root/'explicit.json')
        self.write_config()
        scenarios = [
            ('other-env', ['--context', 'team-context', 'paths']),
            ('team-context', ['paths']),
            ('', ['--config', str(config_path), 'paths']),
            ('', ['--work-dir', str(self.work), 'paths']),
        ]
        for context, arguments in scenarios:
            with self.subTest(arguments=arguments), patch.dict(os.environ, {'SPARK_CONTEXT': context}), \
                 patch.object(spark, 'output') as output, redirect_stdout(io.StringIO()):
                spark.main(arguments)
            output.assert_not_called()

    def test_context_discovery_does_not_print_credentials(self):
        local = {'contexts': [{'name': 'team-context'}], 'users': [{'name': 'user', 'user': {'token': 'sensitive-fixture'}}]}
        with patch.dict(os.environ, {'SPARK_CONTEXT': ''}), patch.object(spark, 'output', return_value=json.dumps(local)) as output, \
             redirect_stdout(io.StringIO()) as stream:
            spark.main(['paths'])
        self.assertNotIn('sensitive-fixture', stream.getvalue())
        self.assertNotIn('--raw', output.call_args.args[0])
        self.assertFalse(self.work.exists())

    def test_unreadable_kubeconfig_reports_only_setup_error(self):
        for failure in (FileNotFoundError('kubectl missing'), ValueError('sensitive parse text')):
            with self.subTest(failure=failure), patch.dict(os.environ, {'SPARK_CONTEXT': ''}), \
                 patch.object(spark, 'output', side_effect=failure), redirect_stderr(io.StringIO()) as stream, \
                 self.assertRaises(SystemExit) as result:
                spark.main(['paths'])
            self.assertEqual(result.exception.code, 2)
            self.assertEqual(stream.getvalue(), 'error: Could not read kubeconfig. Set KUBECONFIG or pass --context NAME.\n')

    def test_discovered_context_still_rejects_saved_identity_mismatch(self):
        self.write_config(dict(self.config, context='other-context'))
        local = {'contexts': [{'name': 'team-context'}]}
        with patch.dict(os.environ, {'SPARK_CONTEXT': ''}), patch.object(spark, 'output', return_value=json.dumps(local)), \
             patch.object(spark.Recipe, 'verify') as verify, self.assertRaisesRegex(RuntimeError, 'Selected context differs'):
            spark.main(['verify-gateway'])
        verify.assert_not_called()

    def test_context_prints_only_selected_name_without_writing_state(self):
        with redirect_stdout(io.StringIO()) as result, patch.object(spark, 'Recipe') as recipe, patch.object(spark, 'output') as output:
            spark.main(['context'])
        self.assertEqual(result.getvalue(), 'team-context\n')
        recipe.assert_not_called()
        output.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_context_can_resolve_sole_kubeconfig_name_without_export(self):
        with patch.dict(os.environ, {'SPARK_CONTEXT': ''}), \
             patch.object(spark, 'output', return_value='{"contexts": [{"name": "team-context"}]}'), \
             redirect_stdout(io.StringIO()) as result:
            spark.main(['context'])
        self.assertEqual(result.getvalue(), 'team-context\n')
        self.assertFalse(self.work.exists())

    def test_paths_is_read_only_and_has_no_credentials(self):
        stream = io.StringIO()
        with redirect_stdout(stream), patch.object(spark, 'Recipe') as recipe, patch.object(spark, 'output') as output:
            spark.main(['paths'])
        paths = json.loads(stream.getvalue())
        self.assertEqual(paths, {'workDir': str(self.work), 'config': str(self.work/'config.json'), 'source': str(HERE.parents[3])})
        recipe.assert_not_called()
        output.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_explicit_source_directory_overrides_the_recipe_checkout(self):
        source = self.root/'another-checkout'
        with redirect_stdout(io.StringIO()) as stream, patch.object(spark, 'output') as output:
            spark.main(['paths', '--source-dir', str(source)])
        self.assertEqual(json.loads(stream.getvalue())['source'], str(source))
        output.assert_not_called()
        self.assertFalse(self.work.exists())
        self.assertFalse(source.exists())

    def test_attach_then_verify_uses_generated_config_without_flags_or_prepare(self):
        seen = []
        def attach(recipe):
            seen.append(('attach', recipe.work))
            recipe.stamp('attachedExisting')
        def verify(recipe, gateway, port):
            seen.append(('verify', recipe.work, gateway, port))
        with patch.object(spark, 'discover_config', return_value=self.config) as discover, \
             patch.object(spark.Recipe, 'attach_existing', attach), patch.object(spark.Recipe, 'verify', verify), \
             patch.object(spark.Recipe, 'prepare') as prepare, redirect_stdout(io.StringIO()):
            spark.main(['attach-existing'])
            spark.main(['verify-gateway'])
        discover.assert_called_once_with('team-context', None)
        prepare.assert_not_called()
        self.assertEqual(seen, [('attach', self.work), ('verify', self.work, True, 18443)])
        self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)
        self.assertEqual((self.work/'config.json').stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.work.stat().st_mode & 0o777, 0o700)

    def test_repeated_attach_reuses_config_instead_of_rediscovery(self):
        self.write_config()
        with patch.object(spark, 'discover_config') as discover, patch.object(spark.Recipe, 'attach_existing') as attach, redirect_stdout(io.StringIO()):
            spark.main(['attach-existing'])
        discover.assert_not_called()
        attach.assert_called_once()

    def test_unavailable_docker_reports_one_actionable_line_without_traceback(self):
        self.write_config()
        with patch.object(spark.Recipe, 'source_check'), \
             patch.object(spark, 'run', side_effect=FileNotFoundError('missing Docker socket')) as run, \
             redirect_stderr(io.StringIO()) as errors, self.assertRaises(SystemExit) as result:
            spark.main(['build-images', '--component', 'gateway'])
        self.assertEqual(result.exception.code, 2)
        self.assertEqual(errors.getvalue(), 'error: Start Docker, then rerun build-images.\n')
        self.assertEqual(run.call_count, 1)

    def test_prepare_uses_default_saved_config(self):
        self.write_config()
        with patch.object(spark.Recipe, 'prepare') as prepare:
            spark.main(['prepare'])
        prepare.assert_called_once()

    def test_missing_default_config_does_not_silently_attach_for_verification(self):
        with patch.object(spark, 'discover_config') as discover, self.assertRaisesRegex(RuntimeError, 'attach-existing first'):
            spark.main(['verify-gateway'])
        discover.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_selected_context_must_match_saved_config(self):
        other = dict(self.config, context='other')
        self.write_config(other)
        with patch.object(spark.Recipe, 'verify') as verify, self.assertRaisesRegex(RuntimeError, 'Selected context differs'):
            spark.main(['verify-gateway'])
        verify.assert_not_called()

    def test_namespace_mismatch_cannot_retarget_saved_installation(self):
        self.write_config()
        with patch.object(spark, 'discover_config') as discover, self.assertRaisesRegex(RuntimeError, 'Selected namespace differs'):
            spark.main(['attach-existing', '--namespace', 'another'])
        discover.assert_not_called()
        self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)

    def test_legacy_config_and_work_flags_need_no_context_environment(self):
        path = self.write_config(path=self.root/'legacy.json')
        with patch.dict(os.environ, {'SPARK_CONTEXT': ''}), patch.object(spark.Recipe, 'prepare') as prepare:
            spark.main(['--config', str(path), '--work-dir', str(self.root/'legacy-work'), 'prepare'])
        prepare.assert_called_once()

    def test_explicit_work_reuses_its_saved_context_when_no_context_env(self):
        self.write_config()
        with patch.dict(os.environ, {'SPARK_CONTEXT': ''}), patch.object(spark.Recipe, 'verify') as verify:
            spark.main(['--work-dir', str(self.work), 'verify-gateway'])
        verify.assert_called_once_with(True, 18443)

    def test_explicit_context_overrides_environment_but_must_match_config(self):
        path = self.write_config(path=self.root/'explicit.json')
        with patch.dict(os.environ, {'SPARK_CONTEXT': 'another'}), patch.object(spark.Recipe, 'prepare') as prepare:
            spark.main(['--context', 'team-context', '--config', str(path), 'prepare'])
        prepare.assert_called_once()
        with self.assertRaisesRegex(RuntimeError, 'Selected context differs'):
            spark.main(['--context', 'another', '--config', str(path), 'prepare'])

    def test_missing_config_with_existing_state_is_not_overwritten(self):
        spark.save(self.work/'state.json', {'identity': 'original'})
        with patch.object(spark, 'discover_config') as discover, self.assertRaisesRegex(RuntimeError, 'missing its configuration'):
            spark.main(['attach-existing'])
        discover.assert_not_called()
        self.assertEqual(json.loads((self.work/'state.json').read_text()), {'identity': 'original'})

    def test_export_uses_configured_repository_and_atomic_private_archive(self):
        self.config['images']['repositories'] = {'gateway': 'registry.example.com/own/gateway'}
        recipe = spark.Recipe(self.config, self.work)
        def save_image(command):
            self.assertEqual(command[:3], ['docker', 'save', '--output'])
            self.assertEqual(command[-1], 'registry.example.com/own/gateway:edited')
            pathlib.Path(command[3]).write_text('image archive')
        with patch.object(recipe, 'source_check') as source, patch.object(spark, 'run', side_effect=save_image), redirect_stdout(io.StringIO()):
            recipe.export_images('gateway', 'edited')
        source.assert_called_once()
        self.assertEqual((self.work/'arm64-images.tar').read_text(), 'image archive')
        self.assertEqual((self.work/'arm64-images.tar').stat().st_mode & 0o777, 0o600)
        self.assertEqual(list(self.work.glob('.arm64-images-*')), [])

    def test_failed_export_preserves_previous_archive(self):
        recipe = spark.Recipe(self.config, self.work)
        (self.work/'arm64-images.tar').write_text('original')
        with patch.object(recipe, 'source_check'), patch.object(spark, 'run', side_effect=RuntimeError('docker failed')):
            with self.assertRaisesRegex(RuntimeError, 'docker failed'):
                recipe.export_images('router', 'edited')
        self.assertEqual((self.work/'arm64-images.tar').read_text(), 'original')
        self.assertEqual(list(self.work.glob('.arm64-images-*')), [])

    def test_import_defaults_to_work_archive_and_keeps_permission_gate(self):
        self.write_config()
        with patch.object(spark.Recipe, 'import_images') as importer:
            spark.main(['import-images', '--component', 'gateway', '--tag', 'edited'])
        importer.assert_called_once_with(self.work/'arm64-images.tar', False, 'gateway', 'edited')
        explicit = self.root/'custom.tar'
        with patch.object(spark.Recipe, 'import_images') as importer:
            spark.main(['import-images', '--archive', str(explicit), '--allow-containerd-import'])
        importer.assert_called_once_with(explicit, True, None, None)

    def test_chat_uses_saved_config_with_either_stream_option_order(self):
        self.write_config()
        for arguments in (['chat', 'Name eight planets.', '--stream'], ['chat', '--stream', 'Name eight planets.']):
            with self.subTest(arguments=arguments), patch.object(spark.Recipe, 'chat') as chat:
                spark.main(arguments)
                chat.assert_called_once_with('Name eight planets.', True, 18443, model=None)
        with patch.object(spark.Recipe, 'chat') as chat:
            spark.main(['chat'])
        chat.assert_called_once_with(None, False, 18443, model=None)

    def test_chat_accepts_model_selection_in_either_option_order(self):
        self.write_config()
        for streaming in (False, True):
            for arguments in (
                ['chat', 'What is 17 multiplied by 19?', '--model', 'qwen3.8-27b'],
                ['chat', '--model', 'qwen3.8-27b', 'What is 17 multiplied by 19?'],
                ['--model', 'qwen3.8-27b', 'chat', 'What is 17 multiplied by 19?'],
            ):
                if streaming:
                    arguments = arguments + ['--stream']
                with self.subTest(arguments=arguments), patch.object(spark.Recipe, 'chat') as chat:
                    spark.main(arguments)
                chat.assert_called_once_with('What is 17 multiplied by 19?', streaming, 18443, model='qwen3.8-27b')
        with patch.object(spark.Recipe, 'chat') as chat:
            spark.main(['chat', '--model', 'GLM-5.3-UD-IQ2_M'])
        chat.assert_called_once_with(None, False, 18443, model='GLM-5.3-UD-IQ2_M')

    def test_chat_options_cannot_be_silently_ignored_by_other_phases(self):
        for arguments in (['verify-gateway', '--stream'], ['paths', 'accidental prompt'],
                          ['verify-gateway', '--model', 'qwen3.8-27b'], ['paths', '--model', 'qwen3.8-27b']):
            with self.subTest(arguments=arguments), patch.object(spark, 'Recipe') as recipe, self.assertRaisesRegex(RuntimeError, 'only for chat'):
                spark.main(arguments)
            recipe.assert_not_called()

    def test_init_creates_private_discovered_configuration(self):
        with redirect_stdout(io.StringIO()), patch.object(spark, 'Recipe') as recipe, \
             patch.object(spark.cluster_setup, 'discover_config', return_value=self.config) as discover:
            spark.main(['init'])
        recipe.assert_not_called()
        discover.assert_called_once_with('team-context', None)
        expected = self.config
        path = self.work/'config.json'
        self.assertEqual(json.loads(path.read_text()), expected)
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.work.stat().st_mode & 0o777, 0o700)
        self.assertFalse((self.work/'state.json').exists())
        with patch.object(spark.Recipe, 'prepare') as prepare:
            spark.main(['prepare'])
        prepare.assert_called_once()

    def test_init_refuses_state_without_configuration(self):
        spark.save(self.work/'state.json', {'identity': 'original'})
        with self.assertRaisesRegex(RuntimeError, 'does not overwrite'):
            spark.main(['init'])
        self.assertFalse((self.work/'config.json').exists())
        self.assertEqual(json.loads((self.work/'state.json').read_text()), {'identity': 'original'})

    def test_init_archives_stale_progress_and_preserves_config_and_credentials(self):
        self.write_config()
        recipe = spark.Recipe(self.config, self.work)
        original = {'identity': recipe.identity, 'serve': True, 'stack': True, 'attachedExisting': True}
        spark.save(self.work/'state.json', original)
        spark.save(self.work/'api-key', 'keep-me')
        before = (self.work/'config.json').read_bytes()
        with patch.object(spark.cluster_setup, 'validate_reinitialization') as inspect, redirect_stdout(io.StringIO()):
            spark.main(['init'])
        inspect.assert_called_once_with(self.config)
        self.assertEqual((self.work/'config.json').read_bytes(), before)
        self.assertEqual((self.work/'api-key').read_text(), 'keep-me')
        self.assertFalse((self.work/'state.json').exists())
        archive, = self.work.glob('before-reinit-*')
        self.assertEqual(json.loads((archive/'state.json').read_text()), original)
        self.assertEqual(json.loads((archive/'config.json').read_text()), self.config)
        self.assertEqual(archive.stat().st_mode & 0o777, 0o700)
        self.assertEqual(spark.Recipe(self.config, self.work).state, {})

    def test_init_reuses_config_when_progress_was_already_archived(self):
        self.write_config()
        with patch.object(spark.cluster_setup, 'validate_reinitialization') as inspect, redirect_stdout(io.StringIO()):
            spark.main(['--context', 'team-context', 'init'])
        inspect.assert_called_once_with(self.config)
        self.assertFalse((self.work/'state.json').exists())
        self.assertEqual(list(self.work.glob('before-reinit-*')), [])

    def test_rejected_reinit_preserves_progress(self):
        self.write_config()
        recipe = spark.Recipe(self.config, self.work)
        original = {'identity': recipe.identity, 'serve': True}
        spark.save(self.work/'state.json', original)
        with patch.object(spark.cluster_setup, 'validate_reinitialization',
                          side_effect=spark.cluster_setup.ClusterSetupError('Live installation')), \
             redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            spark.main(['init'])
        self.assertEqual(json.loads((self.work/'state.json').read_text()), original)
        self.assertEqual(list(self.work.glob('before-reinit-*')), [])

    def test_init_supports_new_explicit_config_path_and_namespace(self):
        path = self.root/'custom'/'config.json'
        with redirect_stdout(io.StringIO()), patch.object(spark.cluster_setup, 'discover_config', \
                return_value=dict(self.config, namespace='my-stack')) as discover:
            spark.main(['--config', str(path), '--namespace', 'my-stack', 'init'])
        discover.assert_called_once_with('team-context', 'my-stack')
        config = json.loads(path.read_text())
        self.assertEqual(config['context'], 'team-context')
        self.assertEqual(config['namespace'], 'my-stack')
        self.assertFalse((self.work/'config.json').exists())

    def test_repeated_attach_checks_existing_node_bindings_before_refresh(self):
        recipe = spark.Recipe(self.config, self.work)
        recipe.state = {'identity': recipe.identity, 'attachedExisting': True,
                        'inventory': {'nodes': {name: name+'-old' for name in self.config['nodes'].values()}}}
        original = copy.deepcopy(recipe.state)
        live = {'items': [{'metadata': {'name': name, 'uid': name+'-new'}} for name in self.config['nodes'].values()]}
        with patch.object(spark, 'output', return_value=json.dumps(live)) as output, \
             self.assertRaisesRegex(RuntimeError, 'node identities changed'):
            recipe.attach_existing()
        self.assertEqual(output.call_count, 1)
        self.assertEqual(recipe.state, original)


if __name__ == '__main__':
    unittest.main()
