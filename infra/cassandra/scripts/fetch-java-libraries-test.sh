#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# fetch-java-libraries against a local repository: the base is swapped while
# the artifact path and checksum are kept, and an entry outside the public
# base is refused.
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work_dir=$(mktemp -d)
server_pid=""
cleanup() {
    [ -n "${server_pid}" ] && kill "${server_pid}" 2>/dev/null || true
    rm -rf "${work_dir}"
}
trap cleanup EXIT HUP INT TERM
fail() { echo "fetch-java-libraries-test: $*" >&2; exit 1; }

# A local "repository manager" serving the same artifact paths Maven Central would.
repo=${work_dir}/repo
mkdir -p "${repo}/com/example/one/1.0/" "${repo}/com/example/two/2.0/"
printf 'one\n' > "${repo}/com/example/one/1.0/one-1.0.jar"
printf 'two\n' > "${repo}/com/example/two/2.0/two-2.0.jar"
sha_one=$(sha256sum "${repo}/com/example/one/1.0/one-1.0.jar" | cut -d' ' -f1)
sha_two=$(sha256sum "${repo}/com/example/two/2.0/two-2.0.jar" | cut -d' ' -f1)
python3 -u -m http.server --bind 127.0.0.1 --directory "${repo}" 0 > "${work_dir}/server.log" 2>&1 &
server_pid=$!
port=""
for _ in $(seq 1 50); do
    port=$(sed -n 's/.*port \([0-9]*\).*/\1/p' "${work_dir}/server.log" | head -n1)
    [ -n "${port}" ] && break
    sleep 0.1
done
[ -n "${port}" ] || fail "test server did not start"
base="http://127.0.0.1:${port}"

cat > "${work_dir}/good.lock" <<LOCK
# comment line
${sha_one}  https://repo.maven.apache.org/maven2/com/example/one/1.0/one-1.0.jar
${sha_two}  https://repo.maven.apache.org/maven2/com/example/two/2.0/two-2.0.jar
LOCK
export FETCH_RETRIES=2 FETCH_RETRY_DELAY=1

# 1. Base override: both artifacts come from the local repository, verified.
MAVEN_REPOSITORY_BASE="${base}/" "${script_dir}/fetch-java-libraries.sh" "${work_dir}/good.lock" "${work_dir}/out" ||
    fail "download through the overridden base failed"
cmp -s "${work_dir}/out/one-1.0.jar" "${repo}/com/example/one/1.0/one-1.0.jar" || fail "one-1.0.jar differs"
cmp -s "${work_dir}/out/two-2.0.jar" "${repo}/com/example/two/2.0/two-2.0.jar" || fail "two-2.0.jar differs"

# 2. An entry outside the public base is refused before any request is made.
cat > "${work_dir}/foreign.lock" <<LOCK
${sha_one}  https://example.invalid/maven2/com/example/one/1.0/one-1.0.jar
LOCK
if MAVEN_REPOSITORY_BASE="${base}" "${script_dir}/fetch-java-libraries.sh" "${work_dir}/foreign.lock" "${work_dir}/out2" 2>"${work_dir}/err"; then
    fail "an entry outside the public base was fetched"
fi
grep -q 'not under https://repo.maven.apache.org/maven2' "${work_dir}/err" || fail "refusal did not name the public base"
[ ! -e "${work_dir}/out2/one-1.0.jar" ] || fail "refused entry was downloaded"

# 3. A checksum mismatch still fails through the base override.
cat > "${work_dir}/bad.lock" <<LOCK
${sha_two}  https://repo.maven.apache.org/maven2/com/example/one/1.0/one-1.0.jar
LOCK
if MAVEN_REPOSITORY_BASE="${base}" "${script_dir}/fetch-java-libraries.sh" "${work_dir}/bad.lock" "${work_dir}/out3" 2>/dev/null; then
    fail "checksum mismatch was accepted"
fi
[ ! -e "${work_dir}/out3/one-1.0.jar" ] || fail "mismatched download was left on disk"

echo "fetch-java-libraries-test: all checks passed"
