# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Execute the documented authentication helper with synthetic credentials."""

import http.server
import json
import os
import re
import shutil
import subprocess
import tempfile
import threading
import unittest
from pathlib import Path


GUIDE = Path(__file__).resolve().parents[2] / "release-notes/0.6.1-to-0.6.2-upgrade.md"
FUNCTION_PATH = "functions/synthetic-function/versions/synthetic-version"
ACCOUNT_READ_PATH = "/v2/nvcf/accounts/synthetic-account/" + FUNCTION_PATH

KCP_STUB = r'''
kcp() {
  if [ "$2" = nvcf ]; then
    if [ "$SYNTHETIC_TOKEN_EMPTY" != 1 ]; then
      printf '%s' synthetic-service-account-jwt
    fi
  else
    if [ "$SYNTHETIC_SIGN_FAIL" = 1 ]; then
      return 1
    fi
    while [ "$1" != -- ]; do shift; done
    shift
    "$@"
  fi
}
'''

BAO_STUB = r'''#!/bin/sh
set -eu
case "$*" in
  'write -format=json auth/jwt/login role=nvcf-api-account-bootstrap jwt=-')
    token=$(cat)
    test "$token" = synthetic-service-account-jwt
    printf '%s\n' '{"auth":{"client_token":"synthetic-bao-token"}}'
    ;;
  'write -format=json -f services/nvcf-api/jwt/sign/nvcf-api-admin')
    test "$BAO_TOKEN" = synthetic-bao-token
    printf '%s\n' '{"data":{"token":"synthetic-fresh-jwt"}}'
    ;;
  *) exit 1 ;;
esac
'''


class ProbeHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        self.respond("GET", "Bearer synthetic-fresh-jwt", self.server.read_code)

    def do_POST(self):
        self.respond("POST", "Bearer synthetic-existing-api-key", self.server.invoke_code)

    def respond(self, method, expected, status):
        size = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(size) if size else b""
        expected_path = ACCOUNT_READ_PATH if method == "GET" else "/invoke"
        authorized = (self.headers.get("Authorization") == expected and
                      self.path == expected_path)
        self.server.events.append((method, authorized, body))
        self.send_response(status if authorized else 403)
        self.end_headers()
        self.wfile.write(b'{"result":"synthetic-success"}')


class OpenBaoProbeChecks(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        missing = [tool for tool in ("bash", "curl", "jq") if not shutil.which(tool)]
        if missing:
            raise RuntimeError("Install the guide prerequisites: " + ", ".join(missing))
        guide = GUIDE.read_text(encoding="utf-8")
        helper = re.search(r"^fresh_auth\(\) \{\n.*?^\}", guide, re.M | re.S)
        read_url = re.search(r"^export FUNCTION_READ_URL=.*$", guide, re.M)
        if helper is None or read_url is None:
            raise AssertionError("The guide must define fresh_auth and FUNCTION_READ_URL")
        cls.helper = helper.group()
        cls.read_url = read_url.group()
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), ProbeHandler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.addClassCleanup(cls.close_server)
        cls.base_url = f"http://127.0.0.1:{cls.server.server_port}"

    @classmethod
    def close_server(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()

    def run_probe(self, *, read_code=200, invoke_code=200, sign_fail=False,
                  shared_jwt=False, missing_header=False, success=False,
                  methods=(), wrong_route=False, old_read=False, empty_token=False):
        self.server.events = []
        self.server.read_code = read_code
        self.server.invoke_code = invoke_code
        with tempfile.TemporaryDirectory(prefix="openbao-probe-") as temp:
            directory = Path(temp)
            bao = directory / "bao"
            bao.write_text(BAO_STUB, encoding="utf-8")
            bao.chmod(0o700)
            request = directory / "request.json"
            request.write_text('{"input":"synthetic"}', encoding="utf-8")
            header = directory / "invocation-header"
            if not missing_header:
                header.write_text("Authorization: Bearer synthetic-existing-api-key\n", encoding="utf-8")
                header.chmod(0o600)

            body = self.helper
            if shared_jwt:
                self.assertIn('@"$INVOCATION_AUTH_HEADER"', body)
                body = body.replace('@"$INVOCATION_AUTH_HEADER"', '@"$directory/auth-header"')
            if old_read:
                fixed_read = 'IFS= read -r service_jwt || test -n "$service_jwt"'
                self.assertIn(fixed_read, body)
                body = body.replace(fixed_read, "IFS= read -r service_jwt")
            script = "set -euo pipefail\nset +x\numask 077\n" + self.read_url + "\n"
            if wrong_route:
                script += 'FUNCTION_READ_URL="${NVCF_API_URL%/}/v2/nvcf/' + FUNCTION_PATH + '"\n'
            script += KCP_STUB + body + '\nfresh_auth synthetic-pod "$RESULT_DIR"\n'
            env = dict(os.environ, NVCF_API_URL=self.base_url + "/",
                       NCA_ID="synthetic-account", FUNCTION_ID="synthetic-function",
                       FUNCTION_VERSION_ID="synthetic-version",
                       FUNCTION_INVOKE_URL=self.base_url + "/invoke",
                       INVOCATION_REQUEST=str(request), INVOCATION_AUTH_HEADER=str(header),
                       RESULT_DIR=str(directory), SYNTHETIC_SIGN_FAIL=str(int(sign_fail)),
                       SYNTHETIC_TOKEN_EMPTY=str(int(empty_token)),
                       PATH=str(directory) + os.pathsep + os.environ["PATH"],
                       CURL_HOME=str(directory), NO_PROXY="127.0.0.1")
            result = subprocess.run(["bash"], input=script, text=True, env=env,
                                    capture_output=True, timeout=10)
            self.assertEqual(result.returncode == 0, success, result.stderr)
            self.assertEqual((directory / "fresh-auth-passed.txt").exists(), success)
            self.assertEqual([event[0] for event in self.server.events], list(methods))
            self.assertEqual(result.stdout, "")
            if success:
                self.assertTrue(all(event[1] for event in self.server.events))
                self.assertEqual(self.server.events[-1][2], request.read_bytes())
                for name in ("auth-header", "function.json", "invocation.json",
                             "fresh-auth-passed.txt"):
                    self.assertEqual((directory / name).stat().st_mode & 0o777, 0o600, name)
                response = json.loads((directory / "invocation.json").read_text(encoding="utf-8"))
                self.assertEqual(response["result"], "synthetic-success")
            if shared_jwt:
                self.assertEqual(self.server.events,
                                 [("GET", True, b""), ("POST", False, request.read_bytes())])

    def test_separate_credentials_and_account_route_accept_newline_free_token(self):
        self.run_probe(success=True, methods=("GET", "POST"))

    def test_read_failure_prevents_invocation(self):
        self.run_probe(read_code=403, methods=("GET",))

    def test_invocation_failure_prevents_success_marker(self):
        self.run_probe(invoke_code=403, methods=("GET", "POST"))

    def test_async_invocation_is_not_synchronous_success(self):
        self.run_probe(invoke_code=202, methods=("GET", "POST"))

    def test_signing_pipeline_failure_prevents_requests(self):
        self.run_probe(sign_fail=True)

    def test_missing_api_key_header_fails(self):
        self.run_probe(missing_header=True, methods=("GET",))

    def test_shared_jwt_regression_fails_invocation(self):
        self.run_probe(shared_jwt=True, methods=("GET", "POST"))

    def test_ordinary_read_route_rejects_admin_jwt(self):
        self.run_probe(wrong_route=True, methods=("GET",))

    def test_original_read_regression_fails_without_trailing_newline(self):
        self.run_probe(old_read=True)

    def test_empty_service_account_token_prevents_requests(self):
        self.run_probe(empty_token=True)


class OpenBaoRenderChecks(unittest.TestCase):
    def run_render(self, *, as_list=False, statefulsets=1):
        blocks = re.findall(r"^```bash\n(.*?)^```", GUIDE.read_text(encoding="utf-8"), re.M | re.S)
        block = next(b for b in blocks if b.startswith('TARGET_STATE="$BACKUP_DIR/target"'))
        tags = re.findall(r'endswith\("(:[^"]+)"\)', block)
        self.assertEqual(len(tags), 2)
        migration = {"kind": "Job", "metadata": {"name": "openbao-server-migrations"},
                     "spec": {"template": {"spec": {"containers": [
                         {"name": "migration", "image": "registry.example/migration" + tags[0]}]}}}}
        server = {"kind": "StatefulSet", "metadata": {"name": "openbao-server"},
                  "spec": {"template": {"spec": {"containers": [
                      {"name": "openbao", "image": "registry.example/openbao" + tags[1]}]}}}}
        resources = [{"kind": "Service", "metadata": {"name": "unrelated"}},
                     migration] + [server] * statefulsets
        with tempfile.TemporaryDirectory(prefix="openbao-render-") as temp:
            root = Path(temp)
            baseline = root / "source"
            baseline.mkdir()
            (baseline / "decision.json").write_text('{"architecture":"arm64"}', encoding="utf-8")
            rendered = root / "synthetic-render.json"
            if as_list:
                rendered.write_text(json.dumps({"kind": "List", "items": resources}), encoding="utf-8")
            else:
                rendered.write_text("\n".join(json.dumps(item) for item in resources), encoding="utf-8")
            script = 'set -euo pipefail\nkcp() { cat "$SYNTHETIC_RENDER"; }\n' + block
            env = dict(os.environ, BACKUP_DIR=str(root), BASELINE=str(baseline),
                       SYNTHETIC_RENDER=str(rendered))
            result = subprocess.run(["bash"], input=script, text=True, env=env,
                                    capture_output=True, timeout=10)
            self.assertEqual(result.returncode == 0, statefulsets == 1, result.stderr)
            if statefulsets == 1:
                selected = json.loads((root / "target/rendered-statefulset.json").read_text(encoding="utf-8"))
                self.assertEqual(selected, server)
                normalized = json.loads((root / "target/rendered.json").read_text(encoding="utf-8"))
                self.assertIsInstance(normalized, list)

    def test_accepts_kubectl_json_stream(self):
        self.run_render()

    def test_accepts_kubectl_list_object(self):
        self.run_render(as_list=True)

    def test_rejects_missing_statefulset(self):
        self.run_render(statefulsets=0)

    def test_rejects_duplicate_statefulsets(self):
        self.run_render(statefulsets=2)


if __name__ == "__main__":
    unittest.main()
