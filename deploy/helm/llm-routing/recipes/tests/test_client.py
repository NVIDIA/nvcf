# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import copy
import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('recipe_client', HERE/'client.py')
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)
MODEL = json.loads((HERE/'backend.defaults.json').read_text())['model']['servedName']


def discovery_documents(model=MODEL):
    return [{'object': 'list', 'data': [{'id': model, 'object': 'model'}]},
            {'id': model, 'object': 'model'},
            {'generatedAt': '2026-10-01T00:00:00Z', 'models': [
                {'model': model, 'health': 'Healthy', 'clusters': [
                    {'clusterId': 'expected-cluster', 'registeredServers': 2, 'healthyServers': 1}]}]}]


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

    def test_auth_checks_all_inference_routes_and_accepts_real_chat_with_valid_key(self):
        c = client.Client('https://localhost:18443')
        c.key = 'test-only-key'
        responses = [(Mock(), Mock(status=401)) for _ in range(6)]
        with patch.object(c, 'request', side_effect=responses) as request, patch.object(c, 'completion', return_value={'status': 200}) as completion:
            results = c.auth(MODEL)
        self.assertEqual([r['status'] for r in results], [401]*6+[200])
        self.assertEqual([r['path'] for r in results[:6]],
                         [path for path in ('/v1/chat/completions', '/v1/responses', '/v1/embeddings') for _ in range(2)])
        self.assertEqual(request.call_args_list[0].args[-1], None)
        self.assertEqual([r['credential'] for r in results], ['missing', 'invalid']*3+['valid'])
        completion.assert_called_once_with(MODEL, 'Reply with the word ready.')
        for connection, _ in responses:
            connection.close.assert_called_once()
        self.assertNotIn(c.key, json.dumps(results))

    def test_auth_rejects_an_unprotected_inference_route(self):
        c = client.Client('https://localhost:18443')
        c.key = 'test-only-key'
        for accepted_index in range(6):
            responses = [(Mock(), Mock(status=200 if i == accepted_index else 401)) for i in range(6)]
            with self.subTest(accepted_index=accepted_index), patch.object(c, 'request', side_effect=responses), patch.object(c, 'completion') as completion:
                with self.assertRaisesRegex(RuntimeError, 'Unexpected auth result'):
                    c.auth(MODEL)
                completion.assert_not_called()
                responses[accepted_index][0].close.assert_called_once()

    def test_empty_key_file_cannot_silently_skip_gateway_auth_verification(self):
        with tempfile.TemporaryDirectory() as directory:
            key = pathlib.Path(directory)/'api-key'
            key.write_text('\n')
            with self.assertRaisesRegex(ValueError, 'empty'):
                client.Client('https://localhost:18443', api_key_file=key)

    def test_public_discovery_never_sends_the_configured_key(self):
        c = client.Client('https://localhost:18443')
        c.key = 'test-only-key'
        connection = Mock()
        response = Mock(status=200)
        response.read.return_value = b'{"models":[]}'
        connection.getresponse.return_value = response
        with patch.object(c, 'connect', return_value=connection):
            self.assertEqual(c.public_json('/v1/registry'), {'models': []})
        connection.request.assert_called_once_with('GET', '/v1/registry', None, {})
        connection.close.assert_called_once()

    def test_discovery_checks_model_retrieval_and_healthy_expected_cluster(self):
        c = client.Client('https://localhost:18443')
        documents = discovery_documents('owner/model-name')
        with patch.object(c, 'public_json', side_effect=documents) as public_json:
            result = c.discovery('owner/model-name', 'expected-cluster')
        self.assertEqual(public_json.call_args_list[1].args[0], '/v1/models/owner%2Fmodel-name')
        self.assertEqual(public_json.call_args_list[2].args[0], '/v1/registry?model=owner%2Fmodel-name')
        self.assertEqual(result['registry'], documents[2])

    def test_discovery_rejects_missing_models_legacy_shape_wrong_cluster_and_bad_counts(self):
        cases = []
        documents = discovery_documents()
        documents[0]['data'] = []
        cases.append(('missing model', documents))
        documents = discovery_documents()
        documents[1]['id'] = 'another-model'
        cases.append(('wrong retrieval', documents))
        documents = discovery_documents()
        documents[2] = {'object': 'list', 'data': [{'model': MODEL, 'inferenceServers': 1}]}
        cases.append(('legacy registry', documents))
        for name, update in [('wrong cluster', {'clusterId': 'another-cluster'}),
                             ('no healthy server', {'healthyServers': 0}),
                             ('negative count', {'healthyServers': -1}),
                             ('more healthy than registered', {'healthyServers': 3}),
                             ('boolean count', {'healthyServers': True}),
                             ('no registered server', {'registeredServers': 0})]:
            documents = discovery_documents()
            documents[2]['models'][0]['clusters'][0].update(update)
            cases.append((name, documents))
        documents = discovery_documents()
        documents[2]['models'][0]['health'] = 'Unhealthy'
        cases.append(('unhealthy model', documents))
        for name, documents in cases:
            with self.subTest(name=name), patch.object(client.Client, 'public_json', side_effect=copy.deepcopy(documents)):
                with self.assertRaises((RuntimeError, ValueError)):
                    client.Client('https://localhost:18443').discovery(MODEL, 'expected-cluster')

    def test_discovery_checks_public_http_status(self):
        c = client.Client('https://localhost:18443')
        connection = Mock()
        with patch.object(c, 'request', return_value=(connection, Mock(status=401))):
            with self.assertRaisesRegex(RuntimeError, 'Discovery failed with HTTP 401'):
                c.public_json('/v1/models')
        connection.close.assert_called_once()

    def test_verify_cli_checks_glm_chat_streaming_discovery_and_auth(self):
        instance = Mock(key='test-only-key')
        instance.completion.side_effect = [
            {'model': MODEL, 'content': answer}
            for answer in ('323', '2, 5, 9, 14', 'Red', 'A GPU performs parallel computation.')
        ]
        instance.auth.return_value = [{'status': status} for status in [401]*6+[200]]
        instance.discovery.return_value = {'registry': discovery_documents()[2]}
        stdout = io.StringIO()
        with patch('sys.argv', ['client.py', '--mode', 'verify', '--cluster-id', 'expected-cluster']), patch.object(client, 'Client', return_value=instance), contextlib.redirect_stdout(stdout):
            client.main()
        calls = instance.completion.call_args_list
        self.assertEqual([call.args[0] for call in calls], [MODEL]*4)
        self.assertEqual([call.kwargs.get('stream', False) for call in calls], [False, False, False, True])
        instance.auth.assert_called_once_with(MODEL)
        instance.discovery.assert_called_once_with(MODEL, 'expected-cluster')
        report = json.loads(stdout.getvalue())
        self.assertEqual(report['result'], 'PASS')
        self.assertEqual(len(report['requests']), 4)
        self.assertEqual(report['discovery'], instance.discovery.return_value)
        self.assertNotIn(instance.key, stdout.getvalue())

    def test_verify_cli_rejects_an_incorrect_glm_answer(self):
        instance = Mock(key=None)
        instance.completion.return_value = {'model': MODEL, 'content': '5'}
        with patch('sys.argv', ['client.py', '--mode', 'verify']), patch.object(client, 'Client', return_value=instance):
            with self.assertRaisesRegex(RuntimeError, 'Incorrect answer'):
                client.main()
        instance.completion.assert_called_once()
        instance.auth.assert_not_called()
        instance.discovery.assert_not_called()

    def test_removed_retained_model_option_fails_before_client_creation(self):
        with patch('sys.argv', ['client.py', '--mode', 'verify', '--retained-model', 'legacy-model']), patch.object(client, 'Client') as create, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                client.main()
        self.assertEqual(error.exception.code, 2)
        create.assert_not_called()


if __name__ == '__main__':
    unittest.main()
