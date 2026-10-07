# SPDX-License-Identifier: Apache-2.0
import io
import json
import pathlib
import sys
import tempfile
import unittest
import urllib.error
from unittest.mock import MagicMock, patch

from test_recipes import ROOT, runtime
sys.path.insert(0, str(ROOT))
import verify


def stream(model='qwen3.8-27b'):
    chunks = [{'model': model, 'choices': [{'delta': {'content': 'Hello'}}]},
              {'model': model, 'choices': [], 'usage': {'completion_tokens': 1}}]
    return (''.join('data: ' + json.dumps(x) + '\n\n' for x in chunks) + 'data: [DONE]\n\n').encode()


class MemoryGuardTests(unittest.TestCase):
    def test_low_memory_and_swap_stop_owned_runtime_and_latch(self):
        for available, swap in [(runtime.GIB, 0), (10 * runtime.GIB, 4096)]:
            with self.subTest(available=available), tempfile.TemporaryDirectory() as d:
                path = pathlib.Path(d)
                (path / 'memory.swap.current').write_text(str(swap))
                child = MagicMock()
                child.poll.return_value = None
                with patch.object(runtime.subprocess, 'Popen', return_value=child), \
                        patch.object(runtime, 'host_memory', return_value=available), \
                        patch.object(runtime.signal, 'signal'), patch.object(runtime, 'stop') as stop:
                    self.assertEqual(runtime.supervise(['fake'], {'model': {'id': 'test'}}, path, path), 78)
                    self.assertTrue(stop.called)
                    self.assertTrue((path / 'recipe-memory-stop.json').exists())
                with patch.object(runtime.subprocess, 'Popen') as spawn, self.assertRaisesRegex(RuntimeError, 'previously stopped'):
                    runtime.supervise(['fake'], {'model': {'id': 'test'}}, path, path)
                spawn.assert_not_called()

    def test_missing_cgroup_fails_before_process_start(self):
        with tempfile.TemporaryDirectory() as d, patch.object(runtime.subprocess, 'Popen') as spawn:
            with self.assertRaisesRegex(RuntimeError, 'cgroup v2'):
                runtime.supervise(['fake'], {}, pathlib.Path(d), pathlib.Path(d))
            spawn.assert_not_called()


class GatewayTests(unittest.TestCase):
    def test_stream(self):
        self.assertEqual(verify.parse_stream(stream(), 'qwen3.8-27b')['reply'], 'Hello')

    def test_missing_done_wrong_model_error_and_no_usage_fail(self):
        for data in [stream().replace(b'data: [DONE]\n\n', b''), stream('wrong'),
                     b'data: {"error":"bad"}\n\n',
                     b'data: {"model":"qwen3.8-27b","choices":[{"delta":{"content":"Hi"}}]}\n\ndata: [DONE]\n\n']:
            with self.subTest(data=data), self.assertRaises(ValueError):
                verify.parse_stream(data, 'qwen3.8-27b')

    def test_bounds_and_redirect(self):
        with self.assertRaisesRegex(ValueError, 'limit'):
            verify.read_bounded(io.BytesIO(b'x' * (verify.MAX_BYTES + 1)))
        with self.assertRaisesRegex(ValueError, 'redirect'):
            verify.NoRedirect().redirect_request(None, None, None, None, None, None)

    def test_both_models_share_url_and_key(self):
        models = ['qwen3.8-27b', 'qwen3.8-flash-next']
        outputs = [io.BytesIO(json.dumps({'data': [{'id': m} for m in models]}).encode()),
                   io.BytesIO(json.dumps({'models': [{'model': m, 'health': 'Healthy'} for m in models]}).encode())]
        for m in models:
            outputs += [io.BytesIO(json.dumps({'model': m, 'choices': [{'message': {'content': 'Hello'}}],
                                              'usage': {'completion_tokens': 1}}).encode()), io.BytesIO(stream(m))]
        outputs += [urllib.error.HTTPError('url', 401, 'unauthorized', {}, None)]
        opener = MagicMock()
        opener.open.side_effect = outputs
        with patch.object(verify.urllib.request, 'build_opener', return_value=opener), patch.object(verify.ssl, 'create_default_context'):
            result = verify.verify('https://gateway/v1', None, 'test-key', models)
        self.assertTrue(result['passed'])
        calls = opener.open.call_args_list[2:6]
        self.assertEqual(len(calls), 4)
        for call in calls:
            request = call.args[0]
            self.assertEqual(request.full_url, 'https://gateway/v1/chat/completions')
            self.assertEqual(request.get_header('Authorization'), 'Bearer test-key')

    def test_insecure_url_is_rejected(self):
        for url in ['http://gateway/v1', 'https://user:pass@gateway/v1', 'https://gateway/v1?secret=yes']:
            with self.subTest(url=url), self.assertRaises(ValueError):
                verify.verify(url, None, 'key', ['model'])


if __name__ == '__main__':
    unittest.main()
