# SPDX-License-Identifier: Apache-2.0
"""Gateway verification shares the supported Helm connection and client."""
import contextlib
import json
import pathlib
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(HERE))
import verify


class VerifyTests(unittest.TestCase):
    def client(self):
        return Mock(discovery=Mock(return_value={'health': 'Healthy'}),
                    completion=Mock(return_value={'content': 'hello'}),
                    auth=Mock(return_value=[{'status': 401}]))

    def test_each_model_checks_discovery_chat_stream_and_auth(self):
        client = self.client()
        result = verify.verify(client, ['model-a', 'model-b'])
        self.assertTrue(result['passed'])
        self.assertEqual([m['model'] for m in result['models']], ['model-a', 'model-b'])
        self.assertEqual([c.args[0] for c in client.discovery.call_args_list], ['model-a', 'model-b'])
        self.assertEqual([c.args[0] for c in client.auth.call_args_list], ['model-a', 'model-b'])
        self.assertEqual([c.kwargs.get('stream', False) for c in client.completion.call_args_list], [False, True, False, True])

    def test_default_budget_is_unchanged_and_custom_budget_is_recorded(self):
        client = self.client()
        result = verify.verify(client, ['model-a'])
        self.assertNotIn('maxTokens', result)
        self.assertTrue(all('max_tokens' not in c.kwargs for c in client.completion.call_args_list))
        client = self.client()
        result = verify.verify(client, ['model-a'], max_tokens=2048)
        self.assertEqual(result['maxTokens'], 2048)
        self.assertEqual([c.kwargs['max_tokens'] for c in client.completion.call_args_list], [2048, 2048])
        for bad in (0, -1, '5', True):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                verify.verify(self.client(), ['model-a'], max_tokens=bad)

    def test_cli_rejects_a_nonpositive_budget_before_cluster_access(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'evidence.json'
            with patch.object(verify.llm, 'selected_context') as context, \
                 contextlib.redirect_stderr(__import__('io').StringIO()), self.assertRaises(SystemExit):
                verify.main(['--model', 'model-a', '--max-tokens', '0', '--output', str(path)])
            context.assert_not_called()

    def test_empty_duplicate_or_invalid_models_do_not_send_requests(self):
        for models in ([], [''], ['a', 'a'], [1]):
            with self.subTest(models=models), self.assertRaises(ValueError):
                client = self.client()
                verify.verify(client, models)
            client.discovery.assert_not_called()

    def test_client_failure_prevents_success(self):
        client = self.client()
        client.completion.side_effect = RuntimeError('Incomplete SSE')
        with self.assertRaisesRegex(RuntimeError, 'Incomplete SSE'):
            verify.verify(client, ['model-a'])
        client.auth.assert_not_called()

    def test_cli_uses_current_context_and_ephemeral_helm_gateway_access(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'evidence.json'
            client = self.client()
            with patch.object(verify.llm, 'selected_context', return_value='selected') as context, \
                 patch.object(verify.llm, 'gateway', return_value=contextlib.nullcontext(client)) as gateway:
                verify.main(['--namespace', 'demo', '--model', 'model-a', '--output', str(path)])
            context.assert_called_once_with(None)
            gateway.assert_called_once_with('selected', 'demo', None, None)
            self.assertTrue(json.loads(path.read_text())['passed'])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_existing_output_and_duplicate_model_reject_before_cluster_access(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'evidence.json'
            for existing in (False, True):
                if existing:
                    path.write_text('existing evidence')
                args = ['--model', 'model-a', '--output', str(path)]
                if not existing:
                    args += ['--model', 'model-a']
                with self.subTest(existing=existing), patch.object(verify.llm, 'selected_context') as context, \
                     contextlib.redirect_stderr(__import__('io').StringIO()), self.assertRaises(SystemExit):
                    verify.main(args)
                context.assert_not_called()
                if existing:
                    self.assertEqual(path.read_text(), 'existing evidence')
