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
spec = importlib.util.spec_from_file_location('cli_recipe', HERE/'recipe.py')
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)


class CliTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name).resolve()
        self.home = self.root/'home'
        self.home.mkdir()
        self.env = patch.dict(os.environ, {'HOME': str(self.home), 'LLM_ROUTING_CONTEXT': 'team-context', 'XDG_STATE_HOME': ''})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.config = json.loads((HERE/'config.example.json').read_text())
        self.config['context'] = 'team-context'
        self.config['releases'] = {'stack': 'test-stack', 'operator': 'test-operator', 'model': 'test-glm'}
        self.work = tool.default_work_dir('team-context')

    def write_config(self, config=None, path=None):
        path = path or self.work/'config.json'
        tool.save(path, config or self.config)
        return path

    def test_default_state_is_private_path_outside_checkout(self):
        digest = hashlib.sha256(b'team-context').hexdigest()[:20]
        self.assertEqual(self.work, self.home/'.local/state/nvcf/llm-routing'/digest)
        self.assertFalse(self.work.exists())
        self.assertFalse(self.work.is_relative_to(HERE.parents[3]))

    def test_context_names_cannot_escape_storage_and_contexts_are_isolated(self):
        other = tool.default_work_dir('../../another context')
        self.assertNotEqual(other, self.work)
        self.assertEqual(other.parent, self.work.parent)
        self.assertRegex(other.name, r'^[0-9a-f]{20}$')

    def test_absolute_xdg_state_home_is_used(self):
        with patch.dict(os.environ, {'XDG_STATE_HOME': str(self.root/'state')}):
            result = tool.default_work_dir('team-context')
        self.assertEqual(result.parent, self.root/'state/nvcf/llm-routing')

    def test_relative_xdg_is_rejected(self):
        with patch.dict(os.environ, {'XDG_STATE_HOME': 'relative'}), self.assertRaisesRegex(RuntimeError, 'absolute'):
            tool.main(['paths'])
        self.assertFalse(self.work.exists())

    def test_empty_kubeconfig_fails_with_actionable_error_without_traceback(self):
        stream = io.StringIO()
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': ''}), patch.object(tool, 'output', return_value='{}') as output, \
             redirect_stderr(stream), self.assertRaises(SystemExit) as result:
            tool.main(['attach-existing'])
        self.assertEqual(result.exception.code, 2)
        self.assertEqual(output.call_args.args[0], ['kubectl', 'config', 'view', '-o', 'json'])
        self.assertIn('No Kubernetes context found. Set KUBECONFIG', stream.getvalue())
        self.assertNotIn('Traceback', stream.getvalue())
        self.assertFalse(self.work.exists())

    def test_sole_kubeconfig_context_supports_attach_and_verify_without_environment_context(self):
        local = {'contexts': [{'name': 'team-context'}], 'current-context': 'unrelated'}
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': '', 'KUBECONFIG': str(self.root/'team-kubeconfig')}), \
             patch.object(tool, 'output', return_value=json.dumps(local)) as output, \
             patch.object(tool, 'discover_config', return_value=self.config) as discover, \
             patch.object(tool.Recipe, 'attach_existing'), patch.object(tool.Recipe, 'verify') as verify, \
             redirect_stdout(io.StringIO()):
            tool.main(['attach-existing'])
            tool.main(['verify-gateway'])
        discover.assert_called_once_with('team-context', None)
        verify.assert_called_once_with(True, 18443)
        self.assertEqual(output.call_count, 2)
        for call in output.call_args_list:
            self.assertEqual(call.args[0], ['kubectl', 'config', 'view', '-o', 'json'])
        self.assertEqual(json.loads((self.work/'config.json').read_text())['context'], 'team-context')

    def test_multiple_contexts_never_choose_current_context_even_with_explicit_kubeconfig(self):
        local = {'contexts': [{'name': 'team-context'}, {'name': 'unrelated'}], 'current-context': 'unrelated'}
        for kubeconfig in ('', str(self.root/'team-kubeconfig')):
            with self.subTest(kubeconfig=kubeconfig), patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': '', 'KUBECONFIG': kubeconfig}), \
                 patch.object(tool, 'output', return_value=json.dumps(local)), patch.object(tool, 'discover_config') as discover, \
                 redirect_stderr(io.StringIO()) as errors, self.assertRaises(SystemExit) as result:
                tool.main(['attach-existing'])
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
            with self.subTest(arguments=arguments), patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': context}), \
                 patch.object(tool, 'output') as output, redirect_stdout(io.StringIO()):
                tool.main(arguments)
            output.assert_not_called()

    def test_context_discovery_does_not_print_credentials(self):
        local = {'contexts': [{'name': 'team-context'}], 'users': [{'name': 'user', 'user': {'token': 'sensitive-fixture'}}]}
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': ''}), patch.object(tool, 'output', return_value=json.dumps(local)) as output, \
             redirect_stdout(io.StringIO()) as stream:
            tool.main(['paths'])
        self.assertNotIn('sensitive-fixture', stream.getvalue())
        self.assertNotIn('--raw', output.call_args.args[0])
        self.assertFalse(self.work.exists())

    def test_unreadable_kubeconfig_reports_only_setup_error(self):
        for failure in (FileNotFoundError('kubectl missing'), ValueError('sensitive parse text')):
            with self.subTest(failure=failure), patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': ''}), \
                 patch.object(tool, 'output', side_effect=failure), redirect_stderr(io.StringIO()) as stream, \
                 self.assertRaises(SystemExit) as result:
                tool.main(['paths'])
            self.assertEqual(result.exception.code, 2)
            self.assertEqual(stream.getvalue(), 'error: Could not read kubeconfig. Set KUBECONFIG or pass --context NAME.\n')

    def test_discovered_context_still_rejects_saved_identity_mismatch(self):
        self.write_config(dict(self.config, context='other-context'))
        local = {'contexts': [{'name': 'team-context'}]}
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': ''}), patch.object(tool, 'output', return_value=json.dumps(local)), \
             patch.object(tool.Recipe, 'verify') as verify, self.assertRaisesRegex(RuntimeError, 'Selected context differs'):
            tool.main(['verify-gateway'])
        verify.assert_not_called()

    def test_context_prints_only_selected_name_without_writing_state(self):
        with redirect_stdout(io.StringIO()) as result, patch.object(tool, 'Recipe') as recipe, patch.object(tool, 'output') as output:
            tool.main(['context'])
        self.assertEqual(result.getvalue(), 'team-context\n')
        recipe.assert_not_called()
        output.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_context_can_resolve_sole_kubeconfig_name_without_export(self):
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': ''}), \
             patch.object(tool, 'output', return_value='{"contexts": [{"name": "team-context"}]}'), \
             redirect_stdout(io.StringIO()) as result:
            tool.main(['context'])
        self.assertEqual(result.getvalue(), 'team-context\n')
        self.assertFalse(self.work.exists())

    def test_paths_is_read_only_and_has_no_credentials(self):
        stream = io.StringIO()
        with redirect_stdout(stream), patch.object(tool, 'Recipe') as recipe, patch.object(tool, 'output') as output:
            tool.main(['paths'])
        paths = json.loads(stream.getvalue())
        self.assertEqual(paths, {'workDir': str(self.work), 'config': str(self.work/'config.json'), 'source': str(HERE.parents[3])})
        recipe.assert_not_called()
        output.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_explicit_source_directory_overrides_the_recipe_checkout(self):
        source = self.root/'another-checkout'
        with redirect_stdout(io.StringIO()) as stream, patch.object(tool, 'output') as output:
            tool.main(['paths', '--source-dir', str(source)])
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
        with patch.object(tool, 'discover_config', return_value=self.config) as discover, \
             patch.object(tool.Recipe, 'attach_existing', attach), patch.object(tool.Recipe, 'verify', verify), \
             patch.object(tool.Recipe, 'prepare') as prepare, redirect_stdout(io.StringIO()):
            tool.main(['attach-existing'])
            tool.main(['verify-gateway'])
        discover.assert_called_once_with('team-context', None)
        prepare.assert_not_called()
        self.assertEqual(seen, [('attach', self.work), ('verify', self.work, True, 18443)])
        self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)
        self.assertEqual((self.work/'config.json').stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.work.stat().st_mode & 0o777, 0o700)

    def test_repeated_attach_reuses_config_instead_of_rediscovery(self):
        self.write_config()
        with patch.object(tool, 'discover_config') as discover, patch.object(tool.Recipe, 'attach_existing') as attach, redirect_stdout(io.StringIO()):
            tool.main(['attach-existing'])
        discover.assert_not_called()
        attach.assert_called_once()

    def monitoring_config(self):
        config = copy.deepcopy(self.config)
        config.pop('runtimeClass')
        config['nodes'] = {'control': config['nodes']['control']}
        config['images'] = {'pullPolicy': 'IfNotPresent'}
        config['releases'].pop('model')
        return config

    def bind_monitoring(self, recipe):
        recipe.stamp('attachedMonitoring')
        recipe.stamp('inventory', {'nodes': {recipe.c['nodes']['control']: 'control-uid'}})
        recipe.stamp('stack', {'apiKeyFile': None})

    def test_monitoring_discovers_attaches_installs_and_opens_dashboard(self):
        config = self.monitoring_config()
        calls = []
        def attach(recipe):
            self.assertFalse((self.work/'config.json').exists())
            self.bind_monitoring(recipe)
            calls.append('attach')
        def install(monitor):
            self.assertEqual(json.loads((self.work/'config.json').read_text()), config)
            self.assertTrue(monitor.recipe.state['inventory'])
            self.assertIsNone(monitor.recipe.backend)
            calls.append('install')
        def dashboard(monitor, port):
            self.assertEqual(port, 13000)
            self.assertEqual(json.loads((self.work/'config.json').read_text()), config)
            calls.append('dashboard')
        with patch.object(tool.monitoring_setup, 'discover_config', return_value=config) as discover, \
             patch.object(tool.Recipe, 'attach_monitoring', attach), \
             patch.object(tool.monitoring.Monitoring, 'install', autospec=True, side_effect=install), \
             patch.object(tool.monitoring.Monitoring, 'dashboard', autospec=True, side_effect=dashboard), \
             patch.object(tool, 'discover_config') as model_discover, \
             patch.object(tool.Recipe, 'prepare') as prepare, redirect_stdout(io.StringIO()):
            tool.main(['monitoring'])
        self.assertEqual(calls, ['attach', 'install', 'dashboard'])
        discover.assert_called_once_with('team-context', None, tool.output)
        model_discover.assert_not_called()
        prepare.assert_not_called()
        self.assertEqual((self.work/'config.json').stat().st_mode & 0o777, 0o600)

    def test_dashboard_discovers_and_attaches_without_installing(self):
        path = self.root/'dashboard'/'config.json'
        with patch.object(tool.monitoring_setup, 'discover_config', return_value=self.monitoring_config()) as discover, \
             patch.object(tool.Recipe, 'attach_monitoring', autospec=True, side_effect=self.bind_monitoring), \
             patch.object(tool.monitoring.Monitoring, 'dashboard') as dashboard, \
             patch.object(tool.monitoring.Monitoring, 'install') as install, redirect_stdout(io.StringIO()):
            tool.main(['--config', str(path), 'dashboard'])
        discover.assert_called_once_with('team-context', None, tool.output)
        dashboard.assert_called_once_with(13000, admin=False)
        install.assert_not_called()
        self.assertEqual(json.loads(path.read_text()), self.monitoring_config())

    def test_monitoring_reuses_owner_state_and_enables_only_monitoring_settings(self):
        self.config['monitoring'].update(enabled=False, retentionPeriod='7d')
        self.write_config()
        recipe = tool.Recipe(self.config, self.work)
        original = {'identity': recipe.identity, 'inventory': {'nodes': {'control': 'uid'}},
                    'stack': {'apiKeyFile': 'owner-key'}, 'serve': {'release': 'test-glm'},
                    'artifacts': {'ready': True}, 'qualification': {'passed': True}}
        tool.save(self.work/'state.json', original)
        tool.save(self.work/'api-key', 'existing-owner-key')
        def install(monitor):
            self.assertEqual(monitor.recipe.state, original)
            self.assertTrue(monitor.recipe.c['monitoring']['enabled'])
            self.assertFalse(json.loads((self.work/'config.json').read_text())['monitoring']['enabled'])
        with patch.object(tool.monitoring_setup, 'discover_config') as discover, \
             patch.object(tool.Recipe, 'attach_monitoring') as attach, \
             patch.object(tool.monitoring.Monitoring, 'install', autospec=True, side_effect=install), \
             patch.object(tool.monitoring.Monitoring, 'dashboard') as dashboard:
            tool.main(['monitoring', '--port', '13001'])
        dashboard.assert_called_once_with(13001)
        discover.assert_not_called()
        attach.assert_not_called()
        expected = copy.deepcopy(self.config)
        expected['monitoring']['enabled'] = True
        self.assertEqual(json.loads((self.work/'config.json').read_text()), expected)
        self.assertEqual(json.loads((self.work/'state.json').read_text()), original)
        self.assertEqual((self.work/'api-key').read_text(), 'existing-owner-key')

    def test_existing_config_without_monitoring_or_state_attaches_before_install(self):
        self.config.pop('monitoring')
        self.write_config()
        with patch.object(tool.monitoring_setup, 'discover_config') as discover, \
             patch.object(tool.Recipe, 'attach_monitoring', autospec=True, side_effect=self.bind_monitoring), \
             patch.object(tool.monitoring.Monitoring, 'install') as install, \
             patch.object(tool.monitoring.Monitoring, 'dashboard') as dashboard:
            tool.main(['monitoring'])
        discover.assert_not_called()
        install.assert_called_once()
        dashboard.assert_called_once_with(13000)
        self.assertEqual(json.loads((self.work/'config.json').read_text()), dict(self.config, monitoring={'enabled': True}))

    def test_failed_monitoring_install_preserves_saved_disabled_setting(self):
        self.config['monitoring']['enabled'] = False
        self.write_config()
        before = (self.work/'config.json').read_bytes()
        with patch.object(tool.Recipe, 'attach_monitoring', autospec=True, side_effect=self.bind_monitoring), \
             patch.object(tool.monitoring.Monitoring, 'install', side_effect=RuntimeError('Wrong Helm owner')), \
             patch.object(tool.monitoring.Monitoring, 'dashboard') as dashboard, \
             self.assertRaisesRegex(RuntimeError, 'Wrong Helm owner'):
            tool.main(['monitoring'])
        dashboard.assert_not_called()
        self.assertEqual((self.work/'config.json').read_bytes(), before)

    def test_failed_dashboard_keeps_completed_monitoring_installation(self):
        self.config['monitoring']['enabled'] = False
        self.write_config()
        def install(monitor):
            monitor.recipe.stamp('monitoring', {'release': monitor.release})
        def dashboard(monitor, port):
            self.assertTrue(json.loads((self.work/'config.json').read_text())['monitoring']['enabled'])
            self.assertEqual(monitor.recipe.state['monitoring']['release'], monitor.release)
            raise OSError('Address already in use')
        with patch.object(tool.Recipe, 'attach_monitoring', autospec=True, side_effect=self.bind_monitoring), \
             patch.object(tool.monitoring.Monitoring, 'install', autospec=True, side_effect=install) as install_call, \
             patch.object(tool.monitoring.Monitoring, 'dashboard', autospec=True, side_effect=dashboard), \
             self.assertRaisesRegex(OSError, 'Address already in use'):
            tool.main(['monitoring'])
        install_call.assert_called_once()
        self.assertIn('monitoring', json.loads((self.work/'state.json').read_text()))

    def test_failed_fresh_attachment_does_not_save_configuration_or_install(self):
        with patch.object(tool.monitoring_setup, 'discover_config', return_value=self.monitoring_config()), \
             patch.object(tool.Recipe, 'attach_monitoring', side_effect=RuntimeError('Foreign routing release')), \
             patch.object(tool.monitoring.Monitoring, 'install') as install, \
             self.assertRaisesRegex(RuntimeError, 'Foreign routing release'):
            tool.main(['monitoring'])
        install.assert_not_called()
        self.assertFalse((self.work/'config.json').exists())
        self.assertFalse((self.work/'state.json').exists())

    def test_dashboard_reuses_bound_state_and_supports_optional_admin_and_port(self):
        self.config['monitoring']['enabled'] = False
        self.write_config()
        recipe = tool.Recipe(self.config, self.work)
        self.bind_monitoring(recipe)
        before = (self.work/'config.json').read_bytes(), (self.work/'state.json').read_bytes()
        for arguments, port, admin in [(['dashboard'], 13000, False),
                                       (['dashboard', '--admin'], 13000, True),
                                       (['dashboard', '--admin', '--port', '13001'], 13001, True)]:
            with self.subTest(arguments=arguments), patch.object(tool.Recipe, 'attach_monitoring') as attach, \
                 patch.object(tool.monitoring.Monitoring, 'dashboard') as dashboard, \
                 patch.object(tool.monitoring.Monitoring, 'install') as install:
                tool.main(arguments)
            attach.assert_not_called()
            install.assert_not_called()
            dashboard.assert_called_once_with(port, admin=admin)
        self.assertEqual(((self.work/'config.json').read_bytes(), (self.work/'state.json').read_bytes()), before)

    def test_uninstall_monitoring_uses_saved_configuration_without_attachment_or_dashboard(self):
        self.config['monitoring']['enabled'] = False
        self.write_config()
        with patch.object(tool.monitoring_setup, 'discover_config') as discover, \
             patch.object(tool.Recipe, 'attach_monitoring') as attach, \
             patch.object(tool.monitoring, 'Monitoring') as monitor:
            tool.main(['uninstall-monitoring'])
        monitor.return_value.uninstall.assert_called_once_with()
        monitor.return_value.install.assert_not_called()
        monitor.return_value.dashboard.assert_not_called()
        discover.assert_not_called()
        attach.assert_not_called()
        self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)

    def test_uninstall_requires_saved_configuration_before_discovery(self):
        with patch.object(tool.monitoring_setup, 'discover_config') as discover, \
             patch.object(tool.monitoring, 'Monitoring') as monitor, \
             self.assertRaises(RuntimeError):
            tool.main(['uninstall-monitoring'])
        discover.assert_not_called()
        monitor.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_admin_flag_is_rejected_before_file_or_context_discovery(self):
        for phase in ('paths', 'monitoring', 'attach-monitoring', 'verify-gateway'):
            with self.subTest(phase=phase), patch.dict(os.environ, {'SPARK_CONTEXT': ''}), \
                 patch.object(tool, 'cli_settings') as settings, patch.object(tool, 'output') as output, \
                 self.assertRaisesRegex(RuntimeError, '--admin is supported only for dashboard'):
                tool.main([phase, '--admin'])
            settings.assert_not_called()
            output.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_monitoring_commands_reject_orphan_state_before_discovery(self):
        tool.save(self.work/'state.json', {'identity': 'original'})
        for phase in ('monitoring', 'dashboard'):
            with self.subTest(phase=phase), patch.object(tool.monitoring_setup, 'discover_config') as discover, \
                 self.assertRaisesRegex(RuntimeError, 'missing its configuration'):
                tool.main([phase])
            discover.assert_not_called()
        self.assertEqual(json.loads((self.work/'state.json').read_text()), {'identity': 'original'})

    def test_monitoring_preserves_concurrent_configuration_change(self):
        self.config['monitoring']['enabled'] = False
        self.write_config()
        edited = dict(self.config, apiKeyFile='updated-key-path')
        with patch.object(tool.Recipe, 'attach_monitoring', autospec=True, side_effect=self.bind_monitoring), \
             patch.object(tool.monitoring.Monitoring, 'install', side_effect=lambda: self.write_config(edited)), \
             patch.object(tool.monitoring.Monitoring, 'dashboard') as dashboard, \
             self.assertRaisesRegex(RuntimeError, 'Configuration changed during monitoring installation'):
            tool.main(['monitoring'])
        dashboard.assert_not_called()
        self.assertEqual(json.loads((self.work/'config.json').read_text()), edited)

    def test_unavailable_docker_reports_one_actionable_line_without_traceback(self):
        self.write_config()
        with patch.object(tool.Recipe, 'source_check'), \
             patch.object(tool, 'run', side_effect=FileNotFoundError('missing Docker socket')) as run, \
             redirect_stderr(io.StringIO()) as errors, self.assertRaises(SystemExit) as result:
            tool.main(['build-images', '--component', 'gateway'])
        self.assertEqual(result.exception.code, 2)
        self.assertEqual(errors.getvalue(), 'error: Start Docker, then retry the image build.\n')
        self.assertEqual(run.call_count, 1)

    def test_prepare_uses_default_saved_config(self):
        self.write_config()
        with patch.object(tool.Recipe, 'prepare') as prepare:
            tool.main(['prepare'])
        prepare.assert_called_once()

    def test_missing_default_config_does_not_silently_attach_for_verification(self):
        with patch.object(tool, 'discover_config') as discover, self.assertRaisesRegex(RuntimeError, 'attach-existing first'):
            tool.main(['verify-gateway'])
        discover.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_selected_context_must_match_saved_config(self):
        other = dict(self.config, context='other')
        self.write_config(other)
        with patch.object(tool.Recipe, 'verify') as verify, self.assertRaisesRegex(RuntimeError, 'Selected context differs'):
            tool.main(['verify-gateway'])
        verify.assert_not_called()

    def test_namespace_mismatch_cannot_retarget_saved_installation(self):
        self.write_config()
        with patch.object(tool, 'discover_config') as discover, self.assertRaisesRegex(RuntimeError, 'Selected namespace differs'):
            tool.main(['attach-existing', '--namespace', 'another'])
        discover.assert_not_called()
        self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)

    def test_legacy_config_and_work_flags_need_no_context_environment(self):
        path = self.write_config(path=self.root/'legacy.json')
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': ''}), patch.object(tool.Recipe, 'prepare') as prepare:
            tool.main(['--config', str(path), '--work-dir', str(self.root/'legacy-work'), 'prepare'])
        prepare.assert_called_once()

    def test_explicit_work_reuses_its_saved_context_when_no_context_env(self):
        self.write_config()
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': ''}), patch.object(tool.Recipe, 'verify') as verify:
            tool.main(['--work-dir', str(self.work), 'verify-gateway'])
        verify.assert_called_once_with(True, 18443)

    def test_explicit_context_overrides_environment_but_must_match_config(self):
        path = self.write_config(path=self.root/'explicit.json')
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': 'another'}), patch.object(tool.Recipe, 'prepare') as prepare:
            tool.main(['--context', 'team-context', '--config', str(path), 'prepare'])
        prepare.assert_called_once()
        with self.assertRaisesRegex(RuntimeError, 'Selected context differs'):
            tool.main(['--context', 'another', '--config', str(path), 'prepare'])

    def test_missing_config_with_existing_state_is_not_overwritten(self):
        tool.save(self.work/'state.json', {'identity': 'original'})
        with patch.object(tool, 'discover_config') as discover, self.assertRaisesRegex(RuntimeError, 'missing its configuration'):
            tool.main(['attach-existing'])
        discover.assert_not_called()
        self.assertEqual(json.loads((self.work/'state.json').read_text()), {'identity': 'original'})

    def test_export_uses_configured_repository_and_atomic_private_archive(self):
        self.config['images']['repositories'] = {'gateway': 'registry.example.com/own/gateway'}
        recipe = tool.Recipe(self.config, self.work)
        def save_image(command):
            self.assertEqual(command[:3], ['docker', 'save', '--output'])
            self.assertEqual(command[-1], 'registry.example.com/own/gateway:edited')
            pathlib.Path(command[3]).write_text('image archive')
        with patch.object(recipe, 'source_check') as source, patch.object(tool, 'run', side_effect=save_image), redirect_stdout(io.StringIO()):
            recipe.export_images('gateway', 'edited')
        source.assert_called_once()
        self.assertEqual((self.work/'arm64-images.tar').read_text(), 'image archive')
        self.assertEqual((self.work/'arm64-images.tar').stat().st_mode & 0o777, 0o600)
        self.assertEqual(list(self.work.glob('.arm64-images-*')), [])

    def test_failed_export_preserves_previous_archive(self):
        recipe = tool.Recipe(self.config, self.work)
        (self.work/'arm64-images.tar').write_text('original')
        with patch.object(recipe, 'source_check'), patch.object(tool, 'run', side_effect=RuntimeError('docker failed')):
            with self.assertRaisesRegex(RuntimeError, 'docker failed'):
                recipe.export_images('router', 'edited')
        self.assertEqual((self.work/'arm64-images.tar').read_text(), 'original')
        self.assertEqual(list(self.work.glob('.arm64-images-*')), [])

    def test_import_defaults_to_work_archive_and_keeps_permission_gate(self):
        self.write_config()
        with patch.object(tool.Recipe, 'import_images') as importer:
            tool.main(['import-images', '--component', 'gateway', '--tag', 'edited'])
        importer.assert_called_once_with(self.work/'arm64-images.tar', False, 'gateway', 'edited')
        explicit = self.root/'custom.tar'
        with patch.object(tool.Recipe, 'import_images') as importer:
            tool.main(['import-images', '--archive', str(explicit), '--allow-containerd-import'])
        importer.assert_called_once_with(explicit, True, None, None)

    def test_chat_uses_saved_config_with_either_stream_option_order(self):
        self.write_config()
        for arguments in (['chat', 'Name eight planets.', '--stream'], ['chat', '--stream', 'Name eight planets.']):
            with self.subTest(arguments=arguments), patch.object(tool.Recipe, 'chat') as chat:
                tool.main(arguments)
                chat.assert_called_once_with('Name eight planets.', True, 18443, model=None)
        with patch.object(tool.Recipe, 'chat') as chat:
            tool.main(['chat'])
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
                with self.subTest(arguments=arguments), patch.object(tool.Recipe, 'chat') as chat:
                    tool.main(arguments)
                chat.assert_called_once_with('What is 17 multiplied by 19?', streaming, 18443, model='qwen3.8-27b')
        with patch.object(tool.Recipe, 'chat') as chat:
            tool.main(['chat', '--model', 'GLM-5.3-UD-IQ2_M'])
        chat.assert_called_once_with(None, False, 18443, model='GLM-5.3-UD-IQ2_M')

    def test_monitoring_traffic_accepts_model_without_requiring_model_recipe(self):
        config = self.monitoring_config()
        for field in ('recipe', 'gpu', 'runtimeImage'):
            config.pop(field, None)
        self.write_config(config)
        for arguments in (
            ['verify-monitoring', '--verify-traffic', '--model', 'qwen3.8-27b'],
            ['--model', 'qwen3.8-27b', '--verify-traffic', 'verify-monitoring'],
        ):
            with self.subTest(arguments=arguments), patch.object(tool.monitoring, 'Monitoring') as monitor:
                tool.main(arguments)
            monitor.return_value.verify.assert_called_once_with(18443, True, 'qwen3.8-27b')
            self.assertIsNone(monitor.call_args.args[0].backend)

    def test_monitoring_model_requires_traffic_and_traffic_requires_monitoring(self):
        for arguments, message in (
            (['verify-monitoring', '--model', 'qwen3.8-27b'], 'verify-monitoring --verify-traffic'),
            (['chat', 'Hello', '--verify-traffic'], '--verify-traffic requires verify-monitoring'),
        ):
            with self.subTest(arguments=arguments), patch.object(tool, 'Recipe') as recipe, \
                 self.assertRaisesRegex(RuntimeError, message):
                tool.main(arguments)
            recipe.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_chat_options_cannot_be_silently_ignored_by_other_phases(self):
        for arguments in (['verify-gateway', '--stream'], ['paths', 'accidental prompt'],
                          ['verify-gateway', '--model', 'qwen3.8-27b'], ['paths', '--model', 'qwen3.8-27b']):
            with self.subTest(arguments=arguments), patch.object(tool, 'Recipe') as recipe, self.assertRaisesRegex(RuntimeError, 'only for chat'):
                tool.main(arguments)
            recipe.assert_not_called()

    def test_init_creates_private_discovered_configuration(self):
        with redirect_stdout(io.StringIO()), patch.object(tool, 'Recipe') as recipe, \
             patch.object(tool.cluster_setup, 'discover_config', return_value=self.config) as discover:
            tool.main(['init'])
        recipe.assert_not_called()
        discover.assert_called_once_with('team-context', None, tool.load_recipe('glm-5.3'))
        expected = self.config
        path = self.work/'config.json'
        self.assertEqual(json.loads(path.read_text()), expected)
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.work.stat().st_mode & 0o777, 0o700)
        self.assertFalse((self.work/'state.json').exists())
        with patch.object(tool.Recipe, 'prepare') as prepare:
            tool.main(['prepare'])
        prepare.assert_called_once()

    def test_init_refuses_state_without_configuration(self):
        tool.save(self.work/'state.json', {'identity': 'original'})
        with self.assertRaisesRegex(RuntimeError, 'does not overwrite'):
            tool.main(['init'])
        self.assertFalse((self.work/'config.json').exists())
        self.assertEqual(json.loads((self.work/'state.json').read_text()), {'identity': 'original'})

    def test_init_archives_stale_progress_and_preserves_config_and_credentials(self):
        self.write_config()
        recipe = tool.Recipe(self.config, self.work)
        original = {'identity': recipe.identity, 'serve': True, 'stack': True, 'attachedExisting': True}
        tool.save(self.work/'state.json', original)
        tool.save(self.work/'api-key', 'keep-me')
        before = (self.work/'config.json').read_bytes()
        with patch.object(tool.cluster_setup, 'validate_reinitialization') as inspect, redirect_stdout(io.StringIO()):
            tool.main(['init'])
        inspect.assert_called_once_with(self.config, self.config['releases']['model'])
        self.assertEqual((self.work/'config.json').read_bytes(), before)
        self.assertEqual((self.work/'api-key').read_text(), 'keep-me')
        self.assertFalse((self.work/'state.json').exists())
        archive, = self.work.glob('before-reinit-*')
        self.assertEqual(json.loads((archive/'state.json').read_text()), original)
        self.assertEqual(json.loads((archive/'config.json').read_text()), self.config)
        self.assertEqual(archive.stat().st_mode & 0o777, 0o700)
        self.assertEqual(tool.Recipe(self.config, self.work).state, {})

    def test_init_reuses_config_when_progress_was_already_archived(self):
        self.write_config()
        with patch.object(tool.cluster_setup, 'validate_reinitialization') as inspect, redirect_stdout(io.StringIO()):
            tool.main(['--context', 'team-context', 'init'])
        inspect.assert_called_once_with(self.config, self.config['releases']['model'])
        self.assertFalse((self.work/'state.json').exists())
        self.assertEqual(list(self.work.glob('before-reinit-*')), [])

    def test_rejected_reinit_preserves_progress(self):
        self.write_config()
        recipe = tool.Recipe(self.config, self.work)
        original = {'identity': recipe.identity, 'serve': True}
        tool.save(self.work/'state.json', original)
        with patch.object(tool.cluster_setup, 'validate_reinitialization',
                          side_effect=tool.cluster_setup.ClusterSetupError('Live installation')), \
             redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            tool.main(['init'])
        self.assertEqual(json.loads((self.work/'state.json').read_text()), original)
        self.assertEqual(list(self.work.glob('before-reinit-*')), [])

    def test_init_supports_new_explicit_config_path_and_namespace(self):
        path = self.root/'custom'/'config.json'
        with redirect_stdout(io.StringIO()), patch.object(tool.cluster_setup, 'discover_config', \
                return_value=dict(self.config, namespace='my-stack')) as discover:
            tool.main(['--config', str(path), '--namespace', 'my-stack', 'init'])
        discover.assert_called_once_with('team-context', 'my-stack', tool.load_recipe('glm-5.3'))
        config = json.loads(path.read_text())
        self.assertEqual(config['context'], 'team-context')
        self.assertEqual(config['namespace'], 'my-stack')
        self.assertFalse((self.work/'config.json').exists())

    def test_repeated_attach_checks_existing_node_bindings_before_refresh(self):
        recipe = tool.Recipe(self.config, self.work)
        recipe.state = {'identity': recipe.identity, 'attachedExisting': True,
                        'inventory': {'nodes': {name: name+'-old' for name in tool.all_nodes(self.config)}}}
        original = copy.deepcopy(recipe.state)
        live = {'items': [{'metadata': {'name': name, 'uid': name+'-new'}} for name in tool.all_nodes(self.config)]}
        with patch.object(tool, 'output', return_value=json.dumps(live)) as output, \
             self.assertRaisesRegex(RuntimeError, 'node identities changed'):
            recipe.attach_existing()
        self.assertEqual(output.call_count, 1)
        self.assertEqual(recipe.state, original)


if __name__ == '__main__':
    unittest.main()
