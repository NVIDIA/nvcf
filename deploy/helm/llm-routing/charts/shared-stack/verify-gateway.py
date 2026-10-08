# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Verify shared gateway TLS, discovery and caller authentication without inference."""
import http.client
import json
import os
from pathlib import Path
import ssl
import sys
import time
import urllib.parse
import uuid

MAX_RESPONSE_BYTES = 1024 * 1024


class VerificationFailure(Exception):
    pass


class RetryableFailure(Exception):
    pass


def request(origin, context, path, payload=None, key=None):
    connection = http.client.HTTPSConnection(origin.hostname, origin.port or 443, context=context, timeout=3)
    headers = {'Content-Type': 'application/json'} if payload is not None else {}
    if key:
        headers['Authorization'] = 'Bearer ' + key
    try:
        connection.request('POST' if payload is not None else 'GET', path,
                           json.dumps(payload) if payload is not None else None, headers)
        response = connection.getresponse()
        body = response.read(MAX_RESPONSE_BYTES + 1)
        if len(body) > MAX_RESPONSE_BYTES:
            raise VerificationFailure('Gateway response exceeded the verification size limit')
        if response.status in (502, 503, 504):
            raise RetryableFailure('Gateway temporarily unavailable: HTTP ' + str(response.status))
        return response.status, body
    except ssl.SSLCertVerificationError as error:
        raise VerificationFailure('Gateway TLS certificate verification failed') from error
    except (OSError, http.client.HTTPException) as error:
        raise RetryableFailure('Gateway transport unavailable: ' + type(error).__name__) from error
    finally:
        connection.close()


def check(origin, context, key):
    status, body = request(origin, context, '/v1/models')
    if status != 200:
        raise VerificationFailure('Model discovery returned HTTP ' + str(status))
    try:
        listing = json.loads(body)
    except (ValueError, UnicodeError) as error:
        raise VerificationFailure('Model discovery returned invalid JSON') from error
    if (not isinstance(listing, dict) or listing.get('object') != 'list'
            or not isinstance(listing.get('data'), list)
            or any(not isinstance(item, dict) or not isinstance(item.get('id'), str) or not item['id'].strip()
                   for item in listing['data'])):
        raise VerificationFailure('Model discovery returned an invalid model list')
    unknown = 'verification-uninstalled-' + uuid.uuid4().hex
    if any(item['id'] == unknown for item in listing['data']):
        raise VerificationFailure('Verification model name already exists')
    payload = {'model': unknown, 'messages': [{'role': 'user', 'content': 'Check gateway access.'}], 'max_tokens': 1}
    for credential, expected, label in [('invalid-verification-key', 401, 'Invalid caller key'), (key, 404, 'Configured caller key')]:
        status, _ = request(origin, context, '/v1/chat/completions', payload, credential)
        if label == 'Configured caller key' and status == 401:
            raise RetryableFailure('Configured caller key is waiting for the gateway credential reload: HTTP 401')
        if status != expected:
            raise VerificationFailure(label + ' returned HTTP ' + str(status) + ', expected ' + str(expected))
    return len(listing['data'])


def verify(url, ca_file, key_file, retry_seconds=180):
    origin = urllib.parse.urlsplit(url)
    if origin.scheme != 'https' or not origin.hostname or origin.username or origin.password or origin.path not in ('', '/'):
        raise VerificationFailure('Verification requires a gateway HTTPS origin')
    try:
        context = ssl.create_default_context(cafile=str(ca_file))
        key = Path(key_file).read_text().strip()
        key.encode('ascii')
    except (OSError, ValueError) as error:
        raise VerificationFailure('Could not load the mounted gateway CA or caller key') from error
    if not key or '\n' in key or '\r' in key:
        raise VerificationFailure('The mounted caller key must be nonempty and single-line')
    deadline = time.monotonic() + retry_seconds
    attempt = 0
    while True:
        attempt += 1
        try:
            count = check(origin, context, key)
            print(json.dumps({'result': 'PASS', 'models': count, 'checks': ['tls', 'models', 'invalid-key-401', 'caller-key-404']}), flush=True)
            return count
        except RetryableFailure as error:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise VerificationFailure('Gateway verification timed out: ' + str(error)) from error
            print(json.dumps({'result': 'RETRY', 'attempt': attempt, 'reason': str(error)}), flush=True)
            time.sleep(min(2, remaining))


def main():
    try:
        verify(os.environ['GATEWAY_URL'], '/access/ca/ca.crt', '/access/caller/api-key')
    except (VerificationFailure, KeyError) as error:
        print(json.dumps({'result': 'FAIL', 'reason': str(error)}), flush=True)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
