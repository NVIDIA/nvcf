#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Verify the container-cache.conf registry-auth probe shape:
#
#   - The probe is per-resource: HEAD against the same URI the client is
#     requesting, with the client's Authorization header. A weaker scheme
#     (e.g. probing the registry root or /v2/) would let a caller with a
#     bearer scoped to repoA read a cached blob that originally came from
#     repoB -- which neutralises the registry's authorization model.
#   - Anonymous requests (no Authorization header) DO NOT enable the cache.
#     There are no credentials to validate, so we cannot prove the caller
#     has rights to anything cached. The request falls through to upstream
#     and the registry's own auth policy applies.
#   - Probe responses 200-399 are accepted as "auth ok". 3xx covers Docker
#     Hub redirecting blob fetches to S3 / Cloudflare -- the redirect
#     itself proves the bearer was accepted.
#   - Both blob-by-digest and manifest-by-digest locations have a
#     content-type guard that refuses to write text/html responses to disk
#     (defends against rate-limit HTML pages poisoning the cache).
#   - `proxy_no_cache $disable_cache` is wired alongside `proxy_cache_bypass`
#     so a probe failure or HTML response actually blocks cache writes (not
#     just reads).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT_FILE="$(mktemp)"
trap 'rm -f "${OUT_FILE}"' EXIT

cd "${ROOT_DIR}"

helm template c ./deploy --set-string 'targetHost=docker.io\,nvcr.io' > "${OUT_FILE}"

assert_count() {
  local needle="$1" want="$2"
  local got
  got=$(grep -F -c -- "${needle}" "${OUT_FILE}" || true)
  if [[ "${got}" -lt "${want}" ]]; then
    echo "FAILED: expected at least ${want} occurrences of '${needle}', got ${got}" >&2
    exit 1
  fi
}

assert_not_has() {
  local needle="$1"
  if grep -F -q -- "${needle}" "${OUT_FILE}"; then
    echo "FAILED: unexpected pattern found: ${needle}" >&2
    exit 1
  fi
}

echo "Checking probe is per-resource (uses ngx.var.request_uri)..."
# Old probe (broken): hits the registry root or /v2/ with the client's auth
#                     -- doesn't validate that the bearer covers THIS resource.
# New probe (correct): hits the same URI the client is asking for.
assert_count 'auth_host .. ngx.var.request_uri' 2
assert_not_has 'request_uri("https://" .. auth_host, {'
assert_not_has 'request_uri("https://" .. auth_host .. "/v2/"'

echo "Checking probe accepts 2xx and 3xx (registry redirects)..."
# Authenticated 200 = "you can read this".
# Authenticated 3xx = "you can read this; here's where it lives" (Docker
# Hub redirects blobs to S3 / Cloudflare).
assert_count 'res.status >= 200 and res.status < 400' 2

echo "Checking anonymous requests bypass cache (no false validation)..."
assert_count 'Anonymous request; cache bypassed' 2
# Anonymous path must NOT set disable_cache=0; it returns early leaving
# the location-level default ("1") in place.
assert_not_has 'Anonymous pull; skipping registry auth probe.'

echo "Checking proxy_no_cache is wired..."
# Both blob-by-digest and manifest-by-digest paths must gate cache *writes*
# on $disable_cache, not just reads.
assert_count 'proxy_no_cache $disable_cache' 2

echo "Checking text/html content-type guard..."
# Both digest paths defensively refuse to cache HTML responses (e.g.
# Docker Hub rate-limit pages).
assert_count 'string.find(string.lower(ct), "text/html"' 2
assert_count 'Upstream returned text/html on a registry' 2

echo "Container-registry auth checks passed."
