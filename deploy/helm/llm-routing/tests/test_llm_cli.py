# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import base64
import contextlib
import importlib.util
import io
import json
import os
import signal
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import MagicMock, patch

HERE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('llm_cli_tested', HERE / 'llm.py')
llm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(llm)


class IsolatedTest(unittest.TestCase):
    def setUp(self):
        self.enterContext(patch.object(llm.subprocess, 'run', side_effect=AssertionError('Unexpected process')))
        self.enterContext(patch.object(llm.subprocess, 'Popen', side_effect=AssertionError('Unexpected port forward')))
        self.output = self.enterContext(contextlib.redirect_stdout(io.StringIO()))
        self.errors = self.enterContext(contextlib.redirect_stderr(io.StringIO()))


class ContextTests(IsolatedTest):
    def test_explicit_context_skips_current_context_read(self):
        with patch.object(llm, 'run') as command:
            self.assertEqual(llm.selected_context('selected-context'), 'selected-context')
        command.assert_not_called()

    def test_default_captures_current_context_and_ignores_stale_environment(self):
        with patch.dict(os.environ, {'LLM_ROUTING_CONTEXT': 'stale-context'}), \
                patch.object(llm, 'run', return_value='current-context\n') as command:
            self.assertEqual(llm.selected_context(None), 'current-context')
        command.assert_called_once_with(['kubectl', 'config', 'current-context'])

    def test_main_resolves_context_once_before_gateway_access(self):
        client = MagicMock()
        client.public_json.return_value = {'object': 'list', 'data': []}
        with patch.object(llm, 'run', side_effect=['captured-context\n', 'changed-context\n']) as command, \
                patch.object(llm, 'gateway') as gateway:
            gateway.return_value.__enter__.return_value = client
            llm.main(['models'])
        command.assert_called_once_with(['kubectl', 'config', 'current-context'])
        gateway.assert_called_once_with('captured-context', 'llm-stack', None, None)

    def test_missing_or_failed_context_stops_before_gateway(self):
        for result in ('\n', RuntimeError('kubectl context failed')):
            with self.subTest(result=result), patch.object(llm, 'run') as command, \
                    patch.object(llm, 'gateway') as gateway:
                if isinstance(result, Exception):
                    command.side_effect = result
                else:
                    command.return_value = result
                with self.assertRaisesRegex((ValueError, RuntimeError), '[Cc]ontext'):
                    llm.main(['models'])
                gateway.assert_not_called()


class CommandTests(IsolatedTest):
    def test_chat_passes_model_and_prompt_as_literals_and_streams(self):
        model = 'provider/model;$(example)'
        prompt = 'Say hello; do not interpret shell syntax.'
        with patch.object(llm, 'gateway') as gateway:
            client = gateway.return_value.__enter__.return_value
            client.completion.return_value = {'content': 'hello'}
            result = llm.main(['--context', 'target', 'chat', '--model', model, '--stream', prompt])
        gateway.assert_called_once_with('target', 'llm-stack', None, None)
        client.completion.assert_called_once_with(model, prompt, stream=True, display=True)
        self.assertEqual(result, {'content': 'hello'})

    def test_empty_chat_input_and_invalid_namespace_fail_before_cluster_access(self):
        for arguments in (['chat', '--model', ' ', 'hello'], ['chat', '--model', 'real', ' '],
                          ['--namespace', 'Invalid_Namespace', 'models']):
            with self.subTest(arguments=arguments), patch.object(llm, 'selected_context') as context, \
                    self.assertRaises(SystemExit):
                llm.main(arguments)
            context.assert_not_called()

    def test_models_outputs_only_discovery_json(self):
        listing = {'object': 'list', 'data': [{'id': 'first'}, {'id': 'second'}]}
        with patch.object(llm, 'gateway') as gateway:
            client = gateway.return_value.__enter__.return_value
            client.key = 'sensitive-caller-value'
            client.public_json.return_value = listing
            self.assertEqual(llm.main(['--context', 'target', 'models']), listing)
        self.assertEqual(json.loads(self.output.getvalue()), listing)
        self.assertNotIn(client.key, self.output.getvalue() + self.errors.getvalue())

    def test_model_list_rejects_invalid_response(self):
        for listing in ({'data': []}, {'object': 'list', 'data': {}}, {'object': 'other', 'data': []}):
            client = MagicMock()
            client.public_json.return_value = listing
            with self.subTest(listing=listing), self.assertRaisesRegex(RuntimeError, 'invalid model list'):
                llm.model_list(client)


class AccessMaterialTests(IsolatedTest):
    def setUp(self):
        super().setUp()
        self.secret = 'sensitive-caller-value'
        self.ca = 'public-ca-certificate'
        self.values = {'operator': {'trustBundle': {'configMap': 'configured-ca'}},
                       'callerKey': {'existingSecret': 'external-caller', 'secretName': 'unused-default'}}
        self.service = {'metadata': {'annotations': {'meta.helm.sh/release-name': 'installed-stack',
                                                    'meta.helm.sh/release-namespace': 'model-space'}}}

    def resource(self, context, namespace, kind, name):
        self.assertEqual((context, namespace), ('target', 'model-space'))
        if (kind, name) == ('service', 'llm-api-gateway'):
            return self.service
        if kind == 'configmap' and name in ('configured-ca', 'override-ca'):
            return {'data': {'ca.crt': self.ca}}
        if (kind, name) == ('secret', 'external-caller'):
            return {'data': {'api-key': base64.b64encode(self.secret.encode()).decode()}}
        raise AssertionError('Unexpected credential resource: ' + kind + '/' + name)

    def test_installed_helm_values_resolve_ca_and_existing_caller_secret(self):
        with patch.object(llm, 'read_json', side_effect=self.resource) as resource, \
                patch.object(llm, 'run', return_value=json.dumps(self.values)) as run:
            self.assertEqual(llm.access_material('target', 'model-space'), (self.ca, self.secret))
        self.assertEqual(run.call_args.args[0], ['helm', '--kube-context', 'target', '--namespace', 'model-space',
                                               'get', 'values', 'installed-stack', '--all', '--output', 'json'])
        resource.assert_any_call('target', 'model-space', 'configmap', 'configured-ca')
        resource.assert_any_call('target', 'model-space', 'secret', 'external-caller')
        self.assertNotIn(self.secret, self.output.getvalue() + self.errors.getvalue())
        self.assertNotIn(self.secret, str(run.call_args))

    def test_explicit_ca_and_key_file_override_legacy_missing_values(self):
        with tempfile.TemporaryDirectory() as directory:
            key = Path(directory) / 'key'
            key.write_text(self.secret + '\n')
            with patch.object(llm, 'read_json', side_effect=self.resource) as resource, \
                    patch.object(llm, 'run', return_value='{}'):
                self.assertEqual(llm.access_material('target', 'model-space', 'override-ca', key), (self.ca, self.secret))
            self.assertEqual(key.read_text(), self.secret + '\n')
        self.assertFalse(any(call.args[2] == 'secret' for call in resource.call_args_list))
        resource.assert_any_call('target', 'model-space', 'configmap', 'override-ca')

    def test_service_owner_must_match_selected_namespace(self):
        for annotations in ({}, {'meta.helm.sh/release-name': 'foreign', 'meta.helm.sh/release-namespace': 'other'}):
            self.service = {'metadata': {'annotations': annotations}}
            with self.subTest(annotations=annotations), patch.object(llm, 'read_json', side_effect=self.resource), \
                    patch.object(llm, 'run') as run, self.assertRaisesRegex(ValueError, 'selected namespace'):
                llm.access_material('target', 'model-space')
            run.assert_not_called()

    def test_legacy_missing_credentials_has_actionable_override_error(self):
        for values, expected in (({}, '--ca-configmap'),
                                 ({'operator': self.values['operator']}, '--api-key-file')):
            with self.subTest(expected=expected), patch.object(llm, 'read_json', side_effect=self.resource), \
                    patch.object(llm, 'run', return_value=json.dumps(values)), self.assertRaisesRegex(ValueError, expected):
                llm.access_material('target', 'model-space')


class ForwardTests(IsolatedTest):
    def process(self, output='Forwarding from 127.0.0.1:45678 -> 8080\n'):
        process = MagicMock()
        process.poll.return_value = None

        def start(command, **kwargs):
            self.assertEqual(command, ['kubectl', '--context', 'target', '--namespace', 'model-space', 'port-forward',
                                       'svc/llm-api-gateway', ':8080', '--address', '127.0.0.1'])
            kwargs['stdout'].write(output)
            kwargs['stdout'].flush()
            return process

        return process, start

    def test_dynamic_forward_terminates_on_normal_and_exceptional_exit(self):
        for fail in (False, True):
            with self.subTest(fail=fail), tempfile.TemporaryDirectory() as directory:
                process, start = self.process()
                with patch.object(llm.subprocess, 'Popen', side_effect=start):
                    try:
                        with llm.forward('target', 'model-space', Path(directory)) as url:
                            self.assertEqual(url, 'https://127.0.0.1:45678')
                            if fail:
                                raise RuntimeError('request failed')
                    except RuntimeError as error:
                        self.assertTrue(fail)
                        self.assertEqual(str(error), 'request failed')
                process.terminate.assert_called_once()
                process.wait.assert_called_once_with(timeout=5)

    def test_forward_timeout_terminates_process(self):
        process, start = self.process('')
        with tempfile.TemporaryDirectory() as directory, patch.object(llm.subprocess, 'Popen', side_effect=start), \
                patch.object(llm.time, 'monotonic', side_effect=[0, 0, 31]), patch.object(llm.time, 'sleep'), \
                self.assertRaisesRegex(RuntimeError, 'Timed out'):
            with llm.forward('target', 'model-space', Path(directory)):
                self.fail('A timed out forward must not yield')
        process.terminate.assert_called_once()
        process.wait.assert_called_once_with(timeout=5)

    def test_stuck_forward_is_killed_during_cleanup(self):
        process, start = self.process()
        process.wait.side_effect = [subprocess.TimeoutExpired('kubectl', 5), 0]
        with tempfile.TemporaryDirectory() as directory, patch.object(llm.subprocess, 'Popen', side_effect=start):
            with llm.forward('target', 'model-space', Path(directory)):
                pass
        process.terminate.assert_called_once()
        process.kill.assert_called_once()
        self.assertEqual(process.wait.call_count, 2)

    def test_sigterm_cleans_forward_and_private_credentials_before_exit(self):
        process, start = self.process()
        paths = []

        def client(url, ca, key):
            paths.extend((ca, key))
            return MagicMock()

        with patch.object(llm, 'access_material', return_value=('public-ca', 'private-caller')), \
                patch.object(llm.subprocess, 'Popen', side_effect=start), patch.object(llm, 'Client', side_effect=client), \
                self.assertRaises(SystemExit) as stopped:
            with llm.gateway('target', 'model-space'):
                llm.terminate(signal.SIGTERM, None)
        self.assertEqual(stopped.exception.code, 143)
        self.assertEqual(len(paths), 2)
        self.assertFalse(any(path.exists() or path.parent.exists() for path in paths))
        process.terminate.assert_called_once()
        process.wait.assert_called_once_with(timeout=5)

    def test_gateway_uses_private_temp_credentials_and_removes_them_after_error(self):
        process, start = self.process()
        paths = []
        secret = 'sensitive-caller-value'

        def client(url, ca, key):
            self.assertEqual(url, 'https://127.0.0.1:45678')
            self.assertEqual(ca.read_text(), 'public-ca')
            self.assertEqual(key.read_text(), secret)
            for path in (ca, key):
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                paths.append(path)
            return MagicMock()

        with patch.object(llm, 'access_material', return_value=('public-ca', secret)), \
                patch.object(llm.subprocess, 'Popen', side_effect=start) as popen, patch.object(llm, 'Client', side_effect=client), \
                self.assertRaisesRegex(RuntimeError, 'request failed'):
            with llm.gateway('target', 'model-space'):
                raise RuntimeError('request failed')
        self.assertEqual(len(paths), 2)
        self.assertFalse(any(path.exists() or path.parent.exists() for path in paths))
        self.assertNotIn(secret, str(popen.call_args) + self.output.getvalue() + self.errors.getvalue())
        process.terminate.assert_called_once()


if __name__ == '__main__':
    unittest.main()
