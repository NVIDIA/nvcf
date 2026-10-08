# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Transfer a Grafana login to the browser through a one-use loopback callback."""
import contextlib
import http.client
import http.cookies
import http.server
import json
import secrets
import time
import webbrowser


def open_dashboard(port, credentials, process, timeout=120, prefix=''):
    path = '/' + secrets.token_urlsafe(32)
    destination = 'http://127.0.0.1:' + str(port) + prefix + '/d/llm-demo'
    deadline = time.monotonic() + timeout

    class LoginHandler(http.server.BaseHTTPRequestHandler):
        def setup(self):
            self.request.settimeout(5)
            super().setup()

        def log_message(self, *args):
            pass

        def do_GET(self):
            host = '127.0.0.1:' + str(self.server.server_port)
            if self.path != path or self.headers.get('Host') != host or self.server.used or time.monotonic() >= deadline:
                self.send_error(404)
                return
            self.server.used = True
            try:
                with contextlib.closing(http.client.HTTPConnection('127.0.0.1', port, timeout=15)) as connection:
                    body = json.dumps({'user': credentials[0], 'password': credentials[1]})
                    connection.request('POST', prefix + '/login', body, {'Content-Type': 'application/json'})
                    response = connection.getresponse()
                    if response.status != 200:
                        raise RuntimeError('Login failed')
                    cookies = [value for name, value in response.getheaders() if name.lower() == 'set-cookie']
                    parsed = http.cookies.SimpleCookie()
                    for cookie in cookies:
                        parsed.load(cookie)
                    if not parsed.get('grafana_session') or not parsed['grafana_session'].value:
                        raise RuntimeError('Login cookie missing')
            except (OSError, http.client.HTTPException, http.cookies.CookieError, RuntimeError):
                self.server.error = 'Grafana admin sign-in failed. Check Grafana readiness and its admin Secret.'
                self.send_error(502, self.server.error)
                return
            self.send_response(303)
            for cookie in cookies:
                self.send_header('Set-Cookie', cookie)
            self.send_header('Location', destination)
            self.send_header('Cache-Control', 'no-store')
            self.send_header('Referrer-Policy', 'no-referrer')
            self.send_header('Content-Length', '0')
            self.end_headers()

    with http.server.HTTPServer(('127.0.0.1', 0), LoginHandler) as server:
        server.timeout = 0.5
        server.used = False
        server.error = None
        url = 'http://127.0.0.1:' + str(server.server_port) + path
        try:
            opened = webbrowser.open(url)
        except webbrowser.Error:
            opened = False
        if not opened:
            print('Open this one-time admin sign-in link within two minutes:', url, flush=True)
        while not server.used:
            if process.poll() is not None:
                raise RuntimeError('Grafana tunnel disconnected. Rerun dashboard after restoring access.')
            if time.monotonic() >= deadline:
                raise RuntimeError('Browser sign-in timed out. Rerun dashboard --admin.')
            server.handle_request()
        if server.error:
            raise RuntimeError(server.error)
