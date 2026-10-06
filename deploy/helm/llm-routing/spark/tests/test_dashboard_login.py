# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import http.client
import http.server
import io
import json
import pathlib
import sys
import threading
import unittest
import urllib.parse
from types import SimpleNamespace
from unittest.mock import Mock, patch

HERE = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(HERE))
import dashboard_login


class DashboardLoginTests(unittest.TestCase):
    credentials = ('test-admin', 'private-admin-password')
    cookies = (
        'grafana_session=private-session; Path=/; HttpOnly; SameSite=Lax',
        'grafana_session_expiry=1234567890; Path=/; SameSite=Lax',
    )

    def request(self, url, path=None, host=None):
        parsed = urllib.parse.urlsplit(url)
        with contextlib.closing(http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=2)) as connection:
            headers = {} if host is None else {'Host': host}
            connection.request('GET', parsed.path if path is None else path, headers=headers)
            response = connection.getresponse()
            return SimpleNamespace(status=response.status, headers=response.getheaders(), body=response.read())

    def run_login(self, action=None, *, status=200, cookies=None, browser_result=True,
                  browser_error=False, timeout=2, process=None):
        cookies = self.cookies if cookies is None else cookies
        result = SimpleNamespace(url=None, requests=[], error=None, output='', port=None)
        threads, failures = [], []

        class Grafana(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                body = self.rfile.read(int(self.headers['Content-Length']))
                result.requests.append((self.path, self.headers['Content-Type'], json.loads(body)))
                payload = b'private-upstream-error'
                self.send_response(status)
                for cookie in cookies:
                    self.send_header('Set-Cookie', cookie)
                self.send_header('Content-Length', str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        def visit(url):
            try:
                action(url)
            except BaseException as error:
                failures.append(error)

        def browser(url):
            result.url = url
            if action is not None:
                thread = threading.Thread(target=visit, args=(url,), daemon=True)
                threads.append(thread)
                thread.start()
            if browser_error:
                raise dashboard_login.webbrowser.Error('private-browser-error')
            return browser_result

        with http.server.HTTPServer(('127.0.0.1', 0), Grafana) as grafana:
            result.port = grafana.server_port
            worker = threading.Thread(target=grafana.serve_forever, kwargs={'poll_interval': 0.01}, daemon=True)
            worker.start()
            output = io.StringIO()
            try:
                with patch.object(dashboard_login.webbrowser, 'open', side_effect=browser), \
                        contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                    try:
                        dashboard_login.open_dashboard(result.port, self.credentials,
                                                       process or Mock(poll=Mock(return_value=None)), timeout=timeout)
                    except RuntimeError as error:
                        result.error = error
                for thread in threads:
                    thread.join(timeout=3)
                    self.assertFalse(thread.is_alive(), 'Browser request did not finish')
            finally:
                grafana.shutdown()
                worker.join(timeout=3)
            result.output = output.getvalue()
        if failures:
            raise failures[0]
        return result

    def assert_closed(self, url):
        with self.assertRaises(OSError):
            self.request(url)

    def assert_private(self, result):
        text = result.output + str(result.error)
        for secret in (self.credentials[1], 'private-session', 'private-upstream-error', 'private-browser-error'):
            self.assertNotIn(secret, text)

    def test_browser_receives_session_cookies_and_dashboard_redirect_once(self):
        responses = []
        result = self.run_login(lambda url: responses.append(self.request(url)))
        self.assertIsNone(result.error)
        self.assertEqual(result.requests, [('/login', 'application/json', {
            'user': self.credentials[0], 'password': self.credentials[1],
        })])
        response, = responses
        self.assertEqual(response.status, 303)
        self.assertEqual([value for name, value in response.headers if name == 'Set-Cookie'], list(self.cookies))
        headers = dict(response.headers)
        self.assertEqual(headers['Location'], 'http://127.0.0.1:' + str(result.port) + '/d/llm-demo')
        self.assertEqual(headers['Cache-Control'], 'no-store')
        self.assertEqual(headers['Referrer-Policy'], 'no-referrer')
        self.assertEqual(response.body, b'')
        self.assertEqual(result.output, '')
        self.assert_closed(result.url)
        self.assert_private(result)

    def test_invalid_host_path_and_query_cannot_consume_sign_in(self):
        def visit(url):
            parsed = urllib.parse.urlsplit(url)
            for arguments in ({'host': 'other.example'}, {'path': '/'}, {'path': parsed.path + '?extra=1'}):
                response = self.request(url, **arguments)
                self.assertEqual(response.status, 404)
                self.assertNotIn('Set-Cookie', dict(response.headers))
            self.assertEqual(self.request(url).status, 303)

        result = self.run_login(visit)
        self.assertIsNone(result.error)
        self.assertEqual(len(result.requests), 1)
        self.assert_closed(result.url)
        self.assert_private(result)

    def test_login_failures_never_forward_cookies_or_upstream_errors(self):
        for status, cookies in ((401, self.cookies), (302, self.cookies), (200, ()),
                                (200, ('grafana_session=; Path=/; HttpOnly',))):
            with self.subTest(status=status, cookies=cookies):
                responses = []
                result = self.run_login(lambda url: responses.append(self.request(url)), status=status, cookies=cookies)
                self.assertIsInstance(result.error, RuntimeError)
                self.assertIn('Grafana admin sign-in failed', str(result.error))
                response, = responses
                self.assertEqual(response.status, 502)
                self.assertNotIn('Set-Cookie', dict(response.headers))
                self.assertNotIn(b'private-upstream-error', response.body)
                self.assertNotIn(self.credentials[1].encode(), response.body)
                self.assert_closed(result.url)
                self.assert_private(result)

    def test_browser_failure_provides_only_a_temporary_manual_link(self):
        for raises in (False, True):
            with self.subTest(browser_error=raises):
                responses = []
                result = self.run_login(lambda url: responses.append(self.request(url)),
                                        browser_result=False, browser_error=raises)
                self.assertIsNone(result.error)
                self.assertEqual(responses[0].status, 303)
                self.assertIn(result.url, result.output)
                self.assertIn('one-time', result.output)
                self.assert_private(result)
                self.assert_closed(result.url)

    def test_timeout_and_disconnected_tunnel_close_unused_callback(self):
        for timeout, process, message in ((0, Mock(poll=Mock(return_value=None)), 'timed out'),
                                          (2, Mock(poll=Mock(return_value=1)), 'disconnected')):
            with self.subTest(message=message):
                result = self.run_login(timeout=timeout, process=process)
                self.assertIsInstance(result.error, RuntimeError)
                self.assertIn(message, str(result.error))
                self.assertEqual(result.requests, [])
                self.assert_closed(result.url)
                self.assert_private(result)


if __name__ == '__main__':
    unittest.main()
