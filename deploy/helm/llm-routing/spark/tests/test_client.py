# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('spark_client', HERE/'client.py')
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)


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
        with patch.object(c, 'request', return_value=(connection, stream())):
            result = c.completion('real-model', 'calculate', True)
        self.assertEqual(result['content'], '323')
        self.assertEqual(result['reasoningCharacters'], 5)
        self.assertTrue(result['done'])
        self.assertTrue(result['usage'])
        connection.close.assert_called_once()

    def test_truncated_sse_is_not_success(self):
        c = client.Client('http://127.0.0.1:18000')
        with patch.object(c, 'request', return_value=(Mock(), stream(False))):
            with self.assertRaisesRegex(RuntimeError, 'Incomplete SSE'):
                c.completion('real-model', 'calculate', True)

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
            results = c.auth('real-model')
        self.assertEqual([r['status'] for r in results], [401, 401, 403])
        self.assertEqual(request.call_args_list[0].args[-1], None)
        self.assertNotIn(c.key, json.dumps(results))


if __name__ == '__main__':
    unittest.main()
