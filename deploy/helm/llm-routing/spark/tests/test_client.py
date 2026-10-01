# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('spark_client', HERE/'client.py')
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)
MODEL = json.loads((HERE/'backend.defaults.json').read_text())['model']['servedName']


def stream(done=True):
    records = [{'choices': [{'delta': {'reasoning_content': 'think'}}]},
               {'choices': [{'delta': {'content': '323'}}]},
               {'choices': [{'delta': {}, 'finish_reason': 'stop'}], 'usage': {'completion_tokens': 3}}]
    lines = [('data: '+json.dumps(record)+'\n').encode() for record in records]
    if done:
        lines.append(b'data: [DONE]\n')
    response = Mock(status=200)
    response.__iter__ = Mock(return_value=iter(lines))
    return response


class ClientTests(unittest.TestCase):
    def test_valid_sse_preserves_reasoning_usage_and_done(self):
        c = client.Client('http://127.0.0.1:18000')
        connection = Mock()
        with patch.object(c, 'request', return_value=(connection, stream())) as request:
            result = c.completion(MODEL, 'calculate', True)
        payload = request.call_args.args[1]
        self.assertEqual(payload['model'], MODEL)
        self.assertEqual(payload['reasoning_effort'], 'low')
        self.assertEqual(payload['chat_template_kwargs'], {'clear_thinking': True})
        self.assertEqual(payload['max_tokens'], 512)
        self.assertEqual(payload['stream_options'], {'include_usage': True})
        self.assertEqual(result['content'], '323')
        self.assertEqual(result['reasoningCharacters'], 5)
        self.assertTrue(result['done'])
        self.assertTrue(result['usage'])
        connection.close.assert_called_once()

    def test_truncated_sse_is_not_success(self):
        c = client.Client('http://127.0.0.1:18000')
        with patch.object(c, 'request', return_value=(Mock(), stream(False))):
            with self.assertRaisesRegex(RuntimeError, 'Incomplete SSE'):
                c.completion(MODEL, 'calculate', True)

    def test_fixture_content_cannot_pass_as_a_glm_response(self):
        c = client.Client('http://127.0.0.1:18000')
        response = Mock(status=200)
        response.read.return_value = json.dumps({
            'model': MODEL, 'usage': {'completion_tokens': 1},
            'choices': [{'finish_reason': 'stop', 'message': {'content': 'xxxx'}}],
        }).encode()
        connection = Mock()
        with patch.object(c, 'request', return_value=(connection, response)):
            with self.assertRaisesRegex(RuntimeError, 'fixture response'):
                c.completion(MODEL, 'calculate')
        connection.close.assert_called_once()

    def test_caller_key_cannot_be_sent_over_plaintext(self):
        with tempfile.TemporaryDirectory() as directory:
            key = pathlib.Path(directory)/'api-key'
            key.write_text('secret-for-test')
            with self.assertRaisesRegex(ValueError, 'verified HTTPS'):
                client.Client('http://127.0.0.1:18000', api_key_file=key)

    def test_negative_auth_is_checked_against_each_expected_status(self):
        c = client.Client('https://localhost:18443')
        c.key = 'test-only-key'
        responses = [(Mock(), Mock(status=status)) for status in (401, 401, 403)]
        with patch.object(c, 'request', side_effect=responses) as request:
            results = c.auth(MODEL)
        self.assertEqual([r['status'] for r in results], [401, 401, 403])
        self.assertEqual(request.call_args_list[0].args[-1], None)
        self.assertNotIn(c.key, json.dumps(results))

    def test_verify_cli_checks_only_glm_chat_streaming_and_auth(self):
        instance = Mock(key='test-only-key')
        instance.completion.side_effect = [
            {'model': MODEL, 'content': answer}
            for answer in ('323', '2, 5, 9, 14', 'Red', 'A GPU performs parallel computation.')
        ]
        instance.auth.return_value = [{'status': status} for status in (401, 401, 403)]
        stdout = io.StringIO()
        with patch('sys.argv', ['client.py', '--mode', 'verify']), patch.object(client, 'Client', return_value=instance), contextlib.redirect_stdout(stdout):
            client.main()
        calls = instance.completion.call_args_list
        self.assertEqual([call.args[0] for call in calls], [MODEL]*4)
        self.assertEqual([call.kwargs.get('stream', False) for call in calls], [False, False, False, True])
        instance.auth.assert_called_once_with(MODEL)
        report = json.loads(stdout.getvalue())
        self.assertEqual(report['result'], 'PASS')
        self.assertEqual(len(report['requests']), 4)
        self.assertNotIn(instance.key, stdout.getvalue())

    def test_verify_cli_rejects_an_incorrect_glm_answer(self):
        instance = Mock(key=None)
        instance.completion.return_value = {'model': MODEL, 'content': '5'}
        with patch('sys.argv', ['client.py', '--mode', 'verify']), patch.object(client, 'Client', return_value=instance):
            with self.assertRaisesRegex(RuntimeError, 'Incorrect answer'):
                client.main()
        instance.completion.assert_called_once()
        instance.auth.assert_not_called()

    def test_removed_retained_model_option_fails_before_client_creation(self):
        with patch('sys.argv', ['client.py', '--mode', 'verify', '--retained-model', 'legacy-model']), patch.object(client, 'Client') as create, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                client.main()
        self.assertEqual(error.exception.code, 2)
        create.assert_not_called()


if __name__ == '__main__':
    unittest.main()
