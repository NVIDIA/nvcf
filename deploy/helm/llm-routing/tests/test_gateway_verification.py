# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import http.client
import importlib.util
import io
import json
import os
from pathlib import Path
import ssl
import unittest
from unittest.mock import MagicMock, patch

HERE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('gateway_verifier_tested', HERE/'charts/shared-stack/verify-gateway.py')
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)
ORIGIN = verifier.urllib.parse.urlsplit('https://gateway.models.svc:8080')
KEY = 'private-caller-key'


def listing(*models):
    return 200, json.dumps({'object': 'list', 'data': [{'id': model} for model in models]}).encode()


class VerifierTests(unittest.TestCase):
    def setUp(self):
        self.output = self.enterContext(contextlib.redirect_stdout(io.StringIO()))
        self.connection = self.enterContext(patch.object(verifier.http.client, 'HTTPSConnection'))
        self.connection.side_effect = AssertionError('Unexpected network connection')

    def test_empty_and_populated_discovery_check_only_an_unknown_model(self):
        for models in ((), ('first-real-model', 'second-real-model')):
            with self.subTest(models=models), patch.object(verifier, 'request', side_effect=[listing(*models), (401, b''), (404, b'')]) as request:
                self.assertEqual(verifier.check(ORIGIN, object(), KEY), len(models))
            calls = request.call_args_list
            self.assertEqual(calls[0].args[2:], ('/v1/models',))
            for call in calls[1:]:
                self.assertEqual(call.args[2], '/v1/chat/completions')
                self.assertNotIn(call.args[3]['model'], models)
                self.assertTrue(call.args[3]['model'].startswith('verification-uninstalled-'))
            self.assertEqual(calls[1].args[3], calls[2].args[3])
            self.assertNotEqual(calls[1].args[4], KEY)
            self.assertEqual(calls[2].args[4], KEY)

    def test_registered_verification_name_is_rejected_before_auth_requests(self):
        identifier = MagicMock(hex='collision')
        with patch.object(verifier.uuid, 'uuid4', return_value=identifier), \
                patch.object(verifier, 'request', return_value=listing('verification-uninstalled-collision')) as request:
            with self.assertRaisesRegex(verifier.VerificationFailure, 'already exists'):
                verifier.check(ORIGIN, object(), KEY)
        request.assert_called_once()

    def test_invalid_discovery_fails_before_any_chat_request(self):
        responses = [(401, b'private error'), (200, b'not json'), (200, b'\xff'),
                     (200, b'[]'), (200, b'{"data":[]}'), (200, b'{"object":"list","data":{}}'),
                     (200, b'{"object":"list","data":[{"id":" "}]}'),
                     (200, b'{"object":"list","data":[{}]}')]
        for response in responses:
            with self.subTest(response=response), patch.object(verifier, 'request', return_value=response) as request:
                with self.assertRaises(verifier.VerificationFailure):
                    verifier.check(ORIGIN, object(), KEY)
                request.assert_called_once()

    def test_invalid_key_acceptance_and_unexpected_configured_key_status_fail(self):
        for results, label, expected_calls in (([listing(), (404, b'')], 'Invalid caller key', 2),
                                               ([listing(), (401, b''), (403, b'')], 'Configured caller key', 3)):
            with self.subTest(label=label), patch.object(verifier, 'request', side_effect=results) as request:
                with self.assertRaisesRegex(verifier.VerificationFailure, label):
                    verifier.check(ORIGIN, object(), KEY)
                self.assertEqual(request.call_count, expected_calls)

    def test_tls_context_is_verified_against_mounted_ca_and_success_is_sanitized(self):
        context = object()
        with patch.object(verifier.ssl, 'create_default_context', return_value=context) as tls, \
                patch.object(verifier.Path, 'read_text', return_value=KEY+'\n'), \
                patch.object(verifier, 'check', return_value=2) as check:
            self.assertEqual(verifier.verify(ORIGIN.geturl(), '/mounted/ca.crt', '/mounted/key'), 2)
        tls.assert_called_once_with(cafile='/mounted/ca.crt')
        check.assert_called_once_with(ORIGIN, context, KEY)
        result = json.loads(self.output.getvalue())
        self.assertEqual(result['result'], 'PASS')
        self.assertEqual(result['models'], 2)
        self.assertNotIn(KEY, self.output.getvalue())

    def test_transient_transport_retries_then_succeeds(self):
        with patch.object(verifier.ssl, 'create_default_context'), patch.object(verifier.Path, 'read_text', return_value=KEY), \
                patch.object(verifier, 'check', side_effect=[verifier.RetryableFailure('HTTP 503'), 0]) as check, \
                patch.object(verifier.time, 'monotonic', side_effect=[0, 1]), patch.object(verifier.time, 'sleep') as sleep:
            self.assertEqual(verifier.verify(ORIGIN.geturl(), 'ca', 'key', retry_seconds=4), 0)
        self.assertEqual(check.call_count, 2)
        sleep.assert_called_once_with(2)
        self.assertEqual([json.loads(line)['result'] for line in self.output.getvalue().splitlines()], ['RETRY', 'PASS'])

    def test_transient_retries_stop_at_deadline(self):
        with patch.object(verifier.ssl, 'create_default_context'), patch.object(verifier.Path, 'read_text', return_value=KEY), \
                patch.object(verifier, 'check', side_effect=verifier.RetryableFailure('HTTP 503')) as check, \
                patch.object(verifier.time, 'monotonic', side_effect=[0, 3, 4]), patch.object(verifier.time, 'sleep') as sleep:
            with self.assertRaisesRegex(verifier.VerificationFailure, 'timed out'):
                verifier.verify(ORIGIN.geturl(), 'ca', 'key', retry_seconds=4)
        self.assertEqual(check.call_count, 2)
        sleep.assert_called_once_with(1)

    def test_configured_key_401_retries_past_secret_sync_then_succeeds(self):
        responses = [listing(), (401, b''), (401, b''), listing(), (401, b''), (404, b'')]
        with patch.object(verifier.ssl, 'create_default_context'), patch.object(verifier.Path, 'read_text', return_value=KEY), \
                patch.object(verifier, 'request', side_effect=responses) as request, \
                patch.object(verifier.time, 'monotonic', side_effect=[0, 125]), patch.object(verifier.time, 'sleep') as sleep:
            self.assertEqual(verifier.verify(ORIGIN.geturl(), 'ca', 'key'), 0)
        self.assertEqual(request.call_count, 6)
        sleep.assert_called_once_with(2)
        self.assertEqual([json.loads(line)['result'] for line in self.output.getvalue().splitlines()], ['RETRY', 'PASS'])
        self.assertNotIn(KEY, self.output.getvalue())

    def test_configured_key_401_fails_at_reload_deadline(self):
        responses = [listing(), (401, b''), (401, b'')] * 2
        with patch.object(verifier.ssl, 'create_default_context'), patch.object(verifier.Path, 'read_text', return_value=KEY), \
                patch.object(verifier, 'request', side_effect=responses) as request, \
                patch.object(verifier.time, 'monotonic', side_effect=[0, 179, 180]), patch.object(verifier.time, 'sleep') as sleep:
            with self.assertRaisesRegex(verifier.VerificationFailure, 'timed out.*credential reload'):
                verifier.verify(ORIGIN.geturl(), 'ca', 'key')
        self.assertEqual(request.call_count, 6)
        sleep.assert_called_once_with(1)
        self.assertNotIn(KEY, self.output.getvalue())

    def test_invalid_behavior_fails_immediately_without_retry(self):
        with patch.object(verifier.ssl, 'create_default_context'), patch.object(verifier.Path, 'read_text', return_value=KEY), \
                patch.object(verifier, 'check', side_effect=verifier.VerificationFailure('Invalid caller key returned HTTP 404')) as check, \
                patch.object(verifier.time, 'sleep') as sleep:
            with self.assertRaisesRegex(verifier.VerificationFailure, 'Invalid caller key'):
                verifier.verify(ORIGIN.geturl(), 'ca', 'key')
        check.assert_called_once()
        sleep.assert_not_called()

    def test_invalid_origin_or_key_stops_before_network(self):
        for url in ('http://gateway:8080', 'https://user:password@gateway', 'https://gateway/v1/models'):
            with self.subTest(url=url), self.assertRaisesRegex(verifier.VerificationFailure, 'HTTPS origin'):
                verifier.verify(url, 'ca', 'key')
        for key in ('', '  ', 'one\ntwo', 'one\rtwo', 'private-\N{SNOWMAN}'):
            with self.subTest(key=key), patch.object(verifier.ssl, 'create_default_context'), \
                    patch.object(verifier.Path, 'read_text', return_value=key), patch.object(verifier, 'check') as check:
                with self.assertRaises(verifier.VerificationFailure):
                    verifier.verify(ORIGIN.geturl(), 'ca', 'key')
                check.assert_not_called()

    def test_main_failure_has_nonzero_result_without_key_or_response_body(self):
        private_body = b'secret upstream diagnostic'
        with patch.dict(os.environ, {'GATEWAY_URL': ORIGIN.geturl()}), patch.object(verifier.ssl, 'create_default_context'), \
                patch.object(verifier.Path, 'read_text', return_value=KEY), \
                patch.object(verifier, 'request', side_effect=[listing(), (200, private_body)]):
            self.assertEqual(verifier.main(), 1)
        result = json.loads(self.output.getvalue())
        self.assertEqual(result['result'], 'FAIL')
        self.assertIn('Invalid caller key returned HTTP 200', result['reason'])
        self.assertNotIn(KEY, self.output.getvalue())
        self.assertNotIn(private_body.decode(), self.output.getvalue())

    def test_https_request_preserves_verified_context_and_closes_connection(self):
        self.connection.side_effect = None
        connection = self.connection.return_value
        connection.getresponse.return_value.status = 404
        connection.getresponse.return_value.read.return_value = b'{}'
        context = object()
        self.assertEqual(verifier.request(ORIGIN, context, '/v1/chat/completions', {'model': 'unknown'}, KEY), (404, b'{}'))
        self.connection.assert_called_once_with('gateway.models.svc', 8080, context=context, timeout=3)
        self.assertEqual(connection.request.call_args.args[0], 'POST')
        self.assertEqual(connection.request.call_args.args[3]['Authorization'], 'Bearer '+KEY)
        connection.close.assert_called_once()

    def test_only_gateway_unavailability_statuses_are_retryable(self):
        self.connection.side_effect = None
        connection = self.connection.return_value
        connection.getresponse.return_value.read.return_value = b'private diagnostic'
        for status in (502, 503, 504):
            with self.subTest(status=status):
                connection.getresponse.return_value.status = status
                with self.assertRaises(verifier.RetryableFailure):
                    verifier.request(ORIGIN, object(), '/v1/models')
        connection.getresponse.return_value.status = 500
        self.assertEqual(verifier.request(ORIGIN, object(), '/v1/models')[0], 500)
        self.assertEqual(connection.close.call_count, 4)

    def test_certificate_failures_are_terminal_and_transport_failures_retry(self):
        self.connection.side_effect = None
        connection = self.connection.return_value
        for error, expected in ((ssl.SSLCertVerificationError('private detail'), verifier.VerificationFailure),
                                (ConnectionRefusedError('private detail'), verifier.RetryableFailure),
                                (http.client.RemoteDisconnected('private detail'), verifier.RetryableFailure)):
            with self.subTest(error=type(error).__name__):
                connection.request.side_effect = error
                with self.assertRaises(expected) as caught:
                    verifier.request(ORIGIN, object(), '/v1/models')
                self.assertNotIn('private detail', str(caught.exception))
        self.assertEqual(connection.close.call_count, 3)

    def test_oversized_response_fails_and_closes_connection(self):
        self.connection.side_effect = None
        connection = self.connection.return_value
        connection.getresponse.return_value.read.return_value = b'x'*(verifier.MAX_RESPONSE_BYTES+1)
        with self.assertRaisesRegex(verifier.VerificationFailure, 'size limit'):
            verifier.request(ORIGIN, object(), '/v1/models')
        connection.close.assert_called_once()


if __name__ == '__main__':
    unittest.main()
