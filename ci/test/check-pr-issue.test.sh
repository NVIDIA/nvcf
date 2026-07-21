#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Focused tests for ci/check-pr-issue.sh.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="${here}/check-pr-issue.sh"
fails=0
tmpdir="$(mktemp -d)"
server_pid=""

cleanup() {
  if [ -n "${server_pid}" ]; then
    kill "${server_pid}" >/dev/null 2>&1 || true
  fi
  rm -rf "${tmpdir}"
}
trap cleanup EXIT

issue_body() {
  printf '## Issues\n%s\n\n## Checklist\n' "$1"
}

run_check() {
  local body="$1"
  local skip="${2-1}"
  local api_url="${3:-}"

  PR_BODY="${body}" \
  GITHUB_TOKEN="test-token" \
  GITHUB_REPOSITORY="NVIDIA/nvcf" \
  SKIP_GITHUB_ISSUE_EXISTS_CHECK="${skip}" \
  GITHUB_API_URL="${api_url}" \
    bash "${script}" >/dev/null 2>&1
}

pass_case() {
  local name="$1"
  local body="$2"

  if run_check "${body}"; then
    echo "ok    (accept) ${name}"
  else
    echo "FAIL  (should accept) ${name}"; fails=$((fails+1))
  fi
}

fail_case() {
  local name="$1"
  local body="$2"

  if run_check "${body}"; then
    echo "FAIL  (should reject) ${name}"; fails=$((fails+1))
  else
    echo "ok    (reject) ${name}"
  fi
}

start_api() {
  local server_py="${tmpdir}/mock_github_api.py"
  cat > "${server_py}" <<'PY'
import http.server
import json
import socketserver

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/repos/NVIDIA/nvcf/issues/286":
            self.send_json(200, {"number": 286})
        elif self.path == "/repos/NVIDIA/nvcf/issues/287":
            self.send_json(200, {"number": 287, "pull_request": {}})
        elif self.path == "/repos/NVIDIA/nvcf/issues/404":
            self.send_json(404, {"message": "Not Found"})
        else:
            self.send_json(500, {"message": f"unexpected path {self.path}"})

    def send_json(self, status, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        return

with socketserver.TCPServer(("127.0.0.1", 0), Handler) as httpd:
    print(httpd.server_address[1], flush=True)
    httpd.serve_forever()
PY

  python3 "${server_py}" > "${tmpdir}/api.port" 2> "${tmpdir}/api.log" &
  server_pid=$!
  for _ in {1..50}; do
    if [ -s "${tmpdir}/api.port" ]; then
      return 0
    fi
    sleep 0.1
  done
  echo "FAIL  mock GitHub API did not start"; fails=$((fails+1))
  return 1
}

pass_case_api() {
  local name="$1"
  local body="$2"
  local api_url="$3"

  if run_check "${body}" "" "${api_url}"; then
    echo "ok    (api accept) ${name}"
  else
    echo "FAIL  (api should accept) ${name}"; fails=$((fails+1))
  fi
}

fail_case_api() {
  local name="$1"
  local body="$2"
  local api_url="$3"

  if run_check "${body}" "" "${api_url}"; then
    echo "FAIL  (api should reject) ${name}"; fails=$((fails+1))
  else
    echo "ok    (api reject) ${name}"
  fi
}

pass_case "action keyword short ref" "$(issue_body 'Closes #286')"
pass_case "action keyword repo ref" "$(issue_body 'Fixes NVIDIA/nvcf#286')"
pass_case "NO-REF last resort" "$(issue_body 'NO-REF')"

fail_case "missing Issues section" $'## TL;DR\nCloses #286\n'
fail_case "reference outside Issues" $'## TL;DR\nCloses #286\n\n## Issues\n\n## Checklist\n'
fail_case "HTML comment is hidden" "$(issue_body '<!-- Closes #286 -->')"
fail_case "unterminated HTML comment is hidden" $'## Issues\n<!-- Closes #286\n\n## Checklist\n'
fail_case "standalone repo ref" "$(issue_body 'NVIDIA/nvcf#286')"
fail_case "other repo ref" "$(issue_body 'Fixes other/repo#286')"
fail_case "tracker key" "$(issue_body 'NVCF-1234')"
fail_case "URL in Issues section" "$(issue_body $'Closes #286\nhttps://example.invalid/private/context')"

if start_api; then
  IFS= read -r api_port < "${tmpdir}/api.port"
  api_url="http://127.0.0.1:${api_port}"
  pass_case_api "existing issue" "$(issue_body 'Closes #286')" "${api_url}"
  fail_case_api "pull request number" "$(issue_body 'Closes #287')" "${api_url}"
  fail_case_api "missing issue" "$(issue_body 'Closes #404')" "${api_url}"
fi

echo
if [ "${fails}" -eq 0 ]; then
  echo "all check-pr-issue tests passed"
else
  echo "${fails} test(s) failed"; exit 1
fi
