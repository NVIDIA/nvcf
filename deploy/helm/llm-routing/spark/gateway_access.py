# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Register a caller key for one test and revoke it without changing Helm values."""
import base64
import contextlib
import fcntl
import hashlib
import http.client
import json
import os
import pathlib
import secrets
import ssl
import subprocess
import time
import tempfile
import urllib.parse


def _require(condition, message):
    if not condition:
        raise RuntimeError(message)


def _save(path, value):
    fd, temporary = tempfile.mkstemp(prefix='.' + path.name, dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write(value if isinstance(value, str) else json.dumps(value) + '\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        pathlib.Path(temporary).unlink(missing_ok=True)


def _json(command):
    result = subprocess.run(command, text=True, capture_output=True)
    _require(result.returncode == 0, 'Cannot inspect gateway credentials with this Kubernetes identity.')
    return json.loads(result.stdout)


class GatewayKey:
    def __init__(self, recipe, url, ca_file=None, timeout=180):
        self.recipe = recipe
        self.work = pathlib.Path(recipe.work)
        self.journal = self.work/'temporary-gateway-key.json'
        self.key_path = self.work/'temporary-gateway-key'
        self.patch_path = self.work/'temporary-gateway-key-patch.json'
        self.url = urllib.parse.urlsplit(url)
        _require(self.url.scheme == 'https' and self.url.hostname in ('localhost', '127.0.0.1', '::1')
                 and self.url.path in ('', '/') and not self.url.username,
                 'Temporary credentials require the verified local HTTPS gateway tunnel.')
        self.ca_file = ca_file or self.work/'ca.crt'
        self.timeout = timeout
        self.binding = {'context': recipe.c['context'], 'namespace': recipe.c['namespace'], 'release': recipe.stack}
        self.record = None

    @contextlib.contextmanager
    def locked(self):
        fd = os.open(self.work/'temporary-gateway-key.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise RuntimeError('Another gateway test is using this work directory.') from None
            yield
        finally:
            os.close(fd)

    def owned(self, obj):
        meta = obj['metadata']
        annotations = meta.get('annotations', {})
        _require(meta.get('namespace') == self.binding['namespace']
                 and annotations.get('meta.helm.sh/release-name') == self.binding['release']
                 and annotations.get('meta.helm.sh/release-namespace') == self.binding['namespace'],
                 'The gateway credential resource has unexpected Helm ownership.')

    def gateway(self):
        deployment = _json(self.recipe.kc + ['get', 'deployment', 'llm-api-gateway', '-o', 'json'])
        self.owned(deployment)
        _require(deployment['spec'].get('replicas', 1) == 1,
                 'Temporary key checks currently require one gateway replica.')
        selector = ','.join(k + '=' + v for k, v in sorted(deployment['spec']['selector']['matchLabels'].items()))
        pods = _json(self.recipe.kc + ['get', 'pods', '-l', selector, '-o', 'json'])['items']
        _require(len(pods) == 1 and not pods[0]['metadata'].get('deletionTimestamp')
                 and any(c['type'] == 'Ready' and c['status'] == 'True' for c in pods[0].get('status', {}).get('conditions', [])),
                 'Wait for exactly one ready gateway pod before testing temporary credentials.')
        return deployment

    def discover(self):
        values = _json(self.recipe.hm + ['get', 'values', self.recipe.stack, '--all', '-o', 'json'])
        auth = values.get('llm-api-gateway', {}).get('llmApiGateway', {}).get('auth', {})
        _require(auth.get('mode') == 'staticKeys', 'Temporary caller keys require staticKeys gateway authentication.')
        name = auth.get('staticKeys', {}).get('existingSecret')
        _require(name and values.get('apiKeysSecret', {}).get('create'),
                 'Temporary caller keys require the stack-managed API key Secret.')
        spec = self.gateway()['spec']['template']['spec']
        mounts = {v['name'] for v in spec.get('volumes', []) if v.get('secret', {}).get('secretName') == name}
        _require(any(e.get('name') == 'API_KEYS_PATH'
                     and any(m.get('name') in mounts and not m.get('subPath')
                             and e.get('value') == m.get('mountPath', '').rstrip('/') + '/api-keys.json'
                             for m in c.get('volumeMounts', []))
                     for c in spec['containers'] for e in c.get('env', [])),
                 'Gateway does not hot-reload the expected mounted API key Secret.')
        secret = _json(self.recipe.kc + ['get', 'secret', name, '-o', 'json'])
        self.owned(secret)
        self.decode(secret)
        return name, secret['metadata']['uid'], secret['data']['api-keys.json']

    def decode(self, secret):
        try:
            document = json.loads(base64.b64decode(secret['data']['api-keys.json'], validate=True))
            entries = document['keys']
        except (ValueError, KeyError, TypeError):
            raise RuntimeError('Gateway key Secret does not contain the expected JSON key list.') from None
        _require(isinstance(entries, list) and bool(entries), 'Refusing to empty the gateway caller key set.')
        _require(all(isinstance(e, dict) and isinstance(e.get('id'), str) and isinstance(e.get('sha256'), str)
                     for e in entries), 'Gateway caller key entries are invalid.')
        return document

    def secret(self):
        secret = _json(self.recipe.kc + ['get', 'secret', self.record['secret'], '-o', 'json'])
        self.owned(secret)
        _require(secret['metadata']['uid'] == self.record['secretUid'],
                 'Gateway key Secret was replaced. Refusing to modify a different resource.')
        return secret

    def change(self, adding):
        # These changes exist only during the test. CAS avoids overwriting a
        # teammate's concurrent key change; persistent settings stay in Helm.
        for _ in range(8):
            secret = self.secret()
            document = self.decode(secret)
            entries = document['keys']
            entry = self.record['entry']
            same_id = [e for e in entries if e['id'] == entry['id']]
            _require(not same_id or same_id == [entry], 'Temporary key identifier was changed by another writer.')
            if adding:
                if same_id:
                    return
                _require(all(e['sha256'] != entry['sha256'] for e in entries), 'Temporary key digest is already registered.')
                document['keys'] = entries + [entry]
            else:
                if not same_id:
                    return
                document['keys'] = [e for e in entries if e != entry]
                _require(bool(document['keys']), 'Refusing to remove the gateway\'s final key.')
            encoded = base64.b64encode(json.dumps(document).encode()).decode()
            original = self.record.get('originalData')
            if not adding and original and document == json.loads(base64.b64decode(original)):
                encoded = original
            patch = [{'op': 'test', 'path': '/metadata/uid', 'value': secret['metadata']['uid']},
                     {'op': 'test', 'path': '/metadata/resourceVersion', 'value': secret['metadata']['resourceVersion']},
                     {'op': 'replace', 'path': '/data/api-keys.json', 'value': encoded}]
            _save(self.patch_path, patch)
            try:
                result = subprocess.run(self.recipe.kc + ['patch', 'secret', self.record['secret'], '--type=json',
                                        '--patch-file', str(self.patch_path), '-o', 'name'],
                                        text=True, capture_output=True)
            finally:
                self.patch_path.unlink(missing_ok=True)
            if result.returncode == 0:
                return
            latest = self.secret()
            _require(latest['metadata']['resourceVersion'] != secret['metadata']['resourceVersion'],
                     'Cannot update the temporary caller key. Check permission to patch the gateway Secret.')
            time.sleep(0.2)
        raise RuntimeError('Gateway credentials changed repeatedly. Retry the test after other changes finish.')

    def status(self):
        token = self.key_path.read_text().strip()
        _require(hashlib.sha256(token.encode()).hexdigest() == self.record['entry']['sha256'],
                 'Temporary caller key file does not match its cleanup journal.')
        connection = http.client.HTTPSConnection(self.url.hostname, self.url.port or 443,
                          context=ssl.create_default_context(cafile=self.ca_file), timeout=10)
        try:
            # An empty chat request reaches auth first, then returns 400 without
            # consuming model capacity. Public discovery cannot prove key reload.
            connection.request('POST', '/v1/chat/completions', '{}',
                               {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
            response = connection.getresponse()
            response.read()
            return response.status
        finally:
            connection.close()

    def wait(self, expected):
        deadline = time.monotonic() + self.timeout
        while True:
            status = self.status()
            if status == expected:
                return
            _require(status in (400, 401), 'Gateway credential check returned unexpected HTTP ' + str(status) + '.')
            _require(time.monotonic() < deadline, 'Gateway has not reloaded the temporary caller key within the timeout.')
            time.sleep(2)

    def recover(self):
        if not self.journal.exists():
            self.key_path.unlink(missing_ok=True)
            return
        self.record = json.loads(self.journal.read_text())
        _require(self.record.get('binding') == self.binding, 'Temporary key cleanup belongs to another installation.')
        self.change(False)
        self.gateway()
        # Keep the journal if either removal or live rejection cannot be proven.
        self.wait(401)
        self.journal.unlink()
        self.key_path.unlink(missing_ok=True)
        self.record = None

    def create(self):
        name, uid, original = self.discover()
        token = secrets.token_urlsafe(48)
        self.record = {'binding': self.binding, 'secret': name, 'secretUid': uid, 'originalData': original,
                       'entry': {'id': 'spark-test-' + secrets.token_hex(16),
                                 'sha256': hashlib.sha256(token.encode()).hexdigest()},
                       'createdAt': int(time.time())}
        _save(self.key_path, token + '\n')
        _save(self.journal, self.record)
        _require(self.status() == 401, 'Gateway did not reject the unregistered test key.')
        self.change(True)
        self.wait(400)


@contextlib.contextmanager
def temporary_gateway_key(recipe, url, ca_file=None, timeout=180):
    """Yield a private key path inside the caller's active gateway port-forward."""
    access = GatewayKey(recipe, url, ca_file, timeout)
    with access.locked():
        access.recover()
        try:
            access.create()
            yield access.key_path
        except BaseException as error:
            try:
                access.recover()
            except BaseException as cleanup_error:
                raise RuntimeError('Gateway test failed and temporary-key cleanup failed. '
                                   'Retry cleanup-key with this work directory: ' + str(cleanup_error)) from error
            raise
        else:
            try:
                access.recover()
            except BaseException as error:
                raise RuntimeError('Temporary-key cleanup failed. Retry cleanup-key with this work directory: ' + str(error)) from error


def cleanup_gateway_key(recipe, url, ca_file=None, timeout=180):
    """Resume cleanup after an interrupted test without issuing another key."""
    access = GatewayKey(recipe, url, ca_file, timeout)
    with access.locked():
        access.recover()
