#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# fetch-verified against a local server that rate-limits first: the download
# must survive a burst of 429s, still reject a bad checksum, and give up on a
# permanent failure without leaving a file behind.
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work_dir=$(mktemp -d)
server_pid=""
cleanup() {
    [ -n "${server_pid}" ] && kill "${server_pid}" 2>/dev/null || true
    rm -rf "${work_dir}"
}
trap cleanup EXIT HUP INT TERM

fail() {
    echo "fetch-verified-test: $*" >&2
    exit 1
}

printf 'pinned artifact body\n' > "${work_dir}/artifact.jar"
good_sha=$(sha256sum "${work_dir}/artifact.jar" | cut -d' ' -f1)
bad_sha=0000000000000000000000000000000000000000000000000000000000000000

# The server answers 429 to the first N requests for a path, then 200. N is
# the leading path segment, so one server covers every case.
cat > "${work_dir}/server.py" <<'PY'
import http.server, os, sys, collections
body = open(sys.argv[2], "rb").read()
seen = collections.Counter()
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        limit = int(self.path.split("/")[1])
        seen[self.path] += 1
        if self.path.endswith("/missing"):
            self.send_response(404); self.end_headers(); return
        if self.path.endswith("/stall"):
            # Headers, one byte, then silence: a transfer that never ends.
            self.send_response(200); self.send_header("Content-Length", str(len(body))); self.end_headers()
            self.wfile.write(body[:1]); self.wfile.flush()
            import time; time.sleep(30); return
        if seen[self.path] <= limit:
            self.send_response(429); self.send_header("Retry-After", "0"); self.end_headers(); return
        self.send_response(200); self.send_header("Content-Length", str(len(body))); self.end_headers()
        self.wfile.write(body)
srv = http.server.HTTPServer(("127.0.0.1", 0), H)
open(sys.argv[1], "w").write(str(srv.server_address[1]))
srv.serve_forever()
PY
python3 "${work_dir}/server.py" "${work_dir}/port" "${work_dir}/artifact.jar" &
server_pid=$!
for _ in $(seq 1 50); do [ -s "${work_dir}/port" ] && break; sleep 0.1; done
[ -s "${work_dir}/port" ] || fail "test server did not start"
base="http://127.0.0.1:$(cat "${work_dir}/port")"

export FETCH_RETRIES=4 FETCH_RETRY_DELAY=1 FETCH_RETRY_MAX_TIME=30
fetch=${script_dir}/fetch-verified.sh

# 1. Two 429s, then 200: succeeds and the file matches.
"${fetch}" "${good_sha}" "${base}/2/ok.jar" "${work_dir}/out1.jar" ||
    fail "download did not survive two 429 responses"
cmp -s "${work_dir}/out1.jar" "${work_dir}/artifact.jar" || fail "downloaded content differs"

# 2. Same server behaviour, wrong checksum: fails and leaves nothing behind.
if "${fetch}" "${bad_sha}" "${base}/1/bad.jar" "${work_dir}/out2.jar" 2>/dev/null; then
    fail "checksum mismatch was accepted"
fi
[ ! -e "${work_dir}/out2.jar" ] || fail "mismatched download was left on disk"

# 3. More 429s than retries: fails, bounded, nothing left behind.
if "${fetch}" "${good_sha}" "${base}/40/slow.jar" "${work_dir}/out3.jar" 2>/dev/null; then
    fail "an endless 429 stream should exhaust the retries"
fi
[ ! -e "${work_dir}/out3.jar" ] || fail "failed download was left on disk"

# 3b. FETCH_RETRIES is the request budget, first request included: two 429s
#     need a third request, so a budget of two fails and a budget of three
#     succeeds against the same server behaviour.
if FETCH_RETRIES=2 "${fetch}" "${good_sha}" "${base}/2/budget-two.jar" "${work_dir}/out3b.jar" 2>/dev/null; then
    fail "a budget of two requests must not absorb two 429s"
fi
FETCH_RETRIES=3 "${fetch}" "${good_sha}" "${base}/2/budget-three.jar" "${work_dir}/out3c.jar" ||
    fail "a budget of three requests must absorb two 429s"
if FETCH_RETRIES=0 "${fetch}" "${good_sha}" "${base}/0/zero.jar" "${work_dir}/out3d.jar" 2>/dev/null; then
    fail "FETCH_RETRIES=0 must be rejected"
fi

# 4. Permanent 404: not transient, so it fails at once and leaves nothing behind.
start=$(date +%s)
if "${fetch}" "${good_sha}" "${base}/0/missing" "${work_dir}/out4.jar" 2>/dev/null; then
    fail "a 404 should fail"
fi
[ ! -e "${work_dir}/out4.jar" ] || fail "404 download was left on disk"
[ $(( $(date +%s) - start )) -lt 3 ] || fail "a 404 was retried; only transient responses should be"

# 5. A transfer that stalls after the headers is cut off by the per-attempt
#    cap, retried, and finally fails, without hanging the build and without
#    leaving the partial file behind.
start=$(date +%s)
if FETCH_MAX_TIME=1 FETCH_RETRIES=1 "${fetch}" "${good_sha}" "${base}/0/stall" "${work_dir}/out5.jar" 2>/dev/null; then
    fail "a stalled transfer should not succeed"
fi
[ ! -e "${work_dir}/out5.jar" ] || fail "stalled download was left on disk"
[ $(( $(date +%s) - start )) -lt 10 ] || fail "a stalled transfer was not cut off by FETCH_MAX_TIME"

echo "fetch-verified-test: all checks passed"
