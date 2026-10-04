# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import base64
import copy
import hashlib
import importlib.util
import json
import pathlib
import stat
import tempfile
import types
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('gateway_access', HERE/'gateway_access.py')
access = importlib.util.module_from_spec(spec)
spec.loader.exec_module(access)


class GatewayKeyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.recipe = types.SimpleNamespace(work=pathlib.Path(self.tmp.name), stack='test-stack',
                    c={'context': 'test', 'namespace': 'demo'},
                    kc=['kubectl', '--context', 'test', '-n', 'demo'],
                    hm=['helm', '--kube-context', 'test', '-n', 'demo'])
        self.url = 'https://127.0.0.1:18443'
        self.metadata = {'namespace': 'demo', 'uid': 'secret-one', 'resourceVersion': '1',
                         'annotations': {'meta.helm.sh/release-name': 'test-stack',
                                         'meta.helm.sh/release-namespace': 'demo'}}
        self.values = {'apiKeysSecret': {'create': True}, 'llm-api-gateway': {'llmApiGateway': {
                      'auth': {'mode': 'staticKeys', 'staticKeys': {'existingSecret': 'caller-keys'}}}}}
        self.original = [{'id': 'teammate', 'sha256': 'a'*64}]
        self.secret = {'metadata': copy.deepcopy(self.metadata), 'data': {'other-file': 'unchanged'}}
        self.set_entries(self.original)
        self.deployment = {'metadata': copy.deepcopy(self.metadata), 'spec': {'replicas': 1,
                           'selector': {'matchLabels': {'app': 'gateway'}}, 'template': {'spec': {
                           'volumes': [{'name': 'auth', 'secret': {'secretName': 'caller-keys'}}],
                           'containers': [{'env': [{'name': 'API_KEYS_PATH', 'value': '/auth/api-keys.json'}],
                                           'volumeMounts': [{'name': 'auth', 'mountPath': '/auth'}]}]}}}}
        self.pods = {'items': [{'metadata': {'uid': 'pod-one'}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}]}
        self.commands, self.statuses = [], []
        self.patch_fail = False
        self.concurrent = None
        self.read_mock = patch.object(access, '_json', side_effect=self.read).start()
        self.run_mock = patch.object(access.subprocess, 'run', side_effect=self.command_run).start()
        test = self
        def status(instance):
            key = instance.key_path.read_text().strip()
            digest = hashlib.sha256(key.encode()).hexdigest()
            result = 400 if any(e['sha256'] == digest for e in test.entries()) else 401
            test.statuses.append(result)
            return result
        patch.object(access.GatewayKey, 'status', status).start()
        patch.object(access.time, 'sleep').start()
        self.addCleanup(patch.stopall)

    def entries(self):
        return json.loads(base64.b64decode(self.secret['data']['api-keys.json']))['keys']

    def set_entries(self, entries):
        self.secret['data']['api-keys.json'] = base64.b64encode(json.dumps({'keys': entries}).encode()).decode()

    def read(self, command):
        self.commands.append(command)
        if command[0] == 'helm':
            return copy.deepcopy(self.values)
        if 'deployment' in command:
            return copy.deepcopy(self.deployment)
        if 'pods' in command:
            return copy.deepcopy(self.pods)
        return copy.deepcopy(self.secret)

    def command_run(self, command, **kwargs):
        self.commands.append(command)
        self.assertIn('--context', command)
        self.assertEqual(command[command.index('--type=json') + 1], '--patch-file')
        path = pathlib.Path(command[command.index('--patch-file') + 1])
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        operations = json.loads(path.read_text())
        if self.concurrent:
            callback, self.concurrent = self.concurrent, None
            callback()
        if self.patch_fail or operations[1]['value'] != self.secret['metadata']['resourceVersion']:
            return types.SimpleNamespace(returncode=1)
        self.assertEqual(operations[0]['value'], self.secret['metadata']['uid'])
        self.secret['data']['api-keys.json'] = operations[2]['value']
        self.secret['metadata']['resourceVersion'] = str(int(self.secret['metadata']['resourceVersion']) + 1)
        return types.SimpleNamespace(returncode=0)

    def test_success_registers_hash_preserves_users_and_removes_credential(self):
        original_data = copy.deepcopy(self.secret['data'])
        with access.temporary_gateway_key(self.recipe, self.url) as path:
            token = path.read_text().strip()
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(len(self.entries()), 2)
            self.assertEqual(self.entries()[0], self.original[0])
            self.assertEqual(self.entries()[1]['sha256'], hashlib.sha256(token.encode()).hexdigest())
            self.assertNotIn(token, json.dumps(self.secret))
        self.assertEqual(self.entries(), self.original)
        self.assertEqual(self.secret['data'], original_data)
        self.assertEqual(self.statuses, [401, 400, 401])
        self.assertFalse(path.exists())
        self.assertFalse((self.recipe.work/'temporary-gateway-key.json').exists())
        self.assertNotIn(token, json.dumps(self.commands))
        self.assertTrue(all('get' in c or 'patch' in c for c in self.commands))

    def test_failed_inference_and_keyboard_interrupt_cleanup(self):
        for exception in (ValueError('request failed'), KeyboardInterrupt()):
            with self.subTest(exception=type(exception).__name__):
                with self.assertRaises(type(exception)):
                    with access.temporary_gateway_key(self.recipe, self.url):
                        raise exception
                self.assertEqual(self.entries(), self.original)

    def test_concurrent_addition_survives_registration_retry_and_cleanup(self):
        teammate = {'id': 'second-user', 'sha256': 'b'*64}
        def concurrent():
            self.set_entries(self.entries() + [teammate])
            self.secret['metadata']['resourceVersion'] = '2'
        self.concurrent = concurrent
        with access.temporary_gateway_key(self.recipe, self.url):
            self.assertEqual(len(self.entries()), 3)
        self.assertEqual(self.entries(), self.original + [teammate])

    def test_concurrent_addition_during_cleanup_survives(self):
        teammate = {'id': 'second-user', 'sha256': 'b'*64}
        with access.temporary_gateway_key(self.recipe, self.url):
            self.set_entries(self.entries() + [teammate])
            self.secret['metadata']['resourceVersion'] = '9'
        self.assertEqual(self.entries(), self.original + [teammate])

    def test_cleanup_failure_retains_journal_for_retry(self):
        with self.assertRaisesRegex(RuntimeError, 'cleanup failed'):
            with access.temporary_gateway_key(self.recipe, self.url):
                self.patch_fail = True
        self.assertTrue((self.recipe.work/'temporary-gateway-key').exists())
        self.assertTrue((self.recipe.work/'temporary-gateway-key.json').exists())
        self.patch_fail = False
        access.cleanup_gateway_key(self.recipe, self.url)
        self.assertEqual(self.entries(), self.original)
        self.assertFalse((self.recipe.work/'temporary-gateway-key.json').exists())

    def test_primary_error_survives_cleanup_error(self):
        primary = ValueError('inference failed')
        with self.assertRaisesRegex(RuntimeError, 'test failed and temporary-key cleanup failed') as caught:
            with access.temporary_gateway_key(self.recipe, self.url):
                self.patch_fail = True
                raise primary
        self.assertIs(caught.exception.__cause__, primary)

    def test_interrupted_previous_run_is_revoked_before_new_key(self):
        abandoned = access.GatewayKey(self.recipe, self.url)
        abandoned.create()
        old_entry = copy.deepcopy(abandoned.record['entry'])
        with access.temporary_gateway_key(self.recipe, self.url):
            self.assertNotIn(old_entry, self.entries())
            self.assertEqual(len(self.entries()), 2)
        self.assertEqual(self.entries(), self.original)

    def test_interrupted_final_file_cleanup_does_not_block_next_test(self):
        key = access.GatewayKey(self.recipe, self.url)
        key.create()
        unlink = pathlib.Path.unlink
        def interrupted_unlink(path, *args, **kwargs):
            if path == key.key_path:
                raise KeyboardInterrupt()
            return unlink(path, *args, **kwargs)
        with patch.object(pathlib.Path, 'unlink', interrupted_unlink):
            with self.assertRaises(KeyboardInterrupt):
                key.recover()
        self.assertEqual(self.entries(), self.original)
        self.assertFalse(key.journal.exists())
        self.assertTrue(key.key_path.exists())
        access.cleanup_gateway_key(self.recipe, self.url)
        self.assertFalse(key.key_path.exists())
        with access.temporary_gateway_key(self.recipe, self.url):
            self.assertEqual(len(self.entries()), 2)
        self.assertEqual(self.entries(), self.original)

    def test_replaced_secret_is_never_modified(self):
        key = access.GatewayKey(self.recipe, self.url)
        key.create()
        self.secret['metadata']['uid'] = 'replacement'
        with self.assertRaisesRegex(RuntimeError, 'was replaced'):
            key.recover()
        self.assertEqual(len(self.entries()), 2)

    def test_same_id_changed_by_other_writer_is_never_removed(self):
        key = access.GatewayKey(self.recipe, self.url)
        key.create()
        entries = self.entries()
        entries[1]['sha256'] = 'c'*64
        self.set_entries(entries)
        with self.assertRaisesRegex(RuntimeError, 'another writer'):
            key.recover()
        self.assertEqual(self.entries(), entries)

    def test_last_key_is_never_removed(self):
        key = access.GatewayKey(self.recipe, self.url)
        key.create()
        self.set_entries([key.record['entry']])
        with self.assertRaisesRegex(RuntimeError, 'final key'):
            key.recover()

    def test_auth_and_ownership_fail_before_registration(self):
        self.values['llm-api-gateway']['llmApiGateway']['auth']['mode'] = 'anonymous'
        with self.assertRaisesRegex(RuntimeError, 'staticKeys'):
            with access.temporary_gateway_key(self.recipe, self.url):
                self.fail('entered')
        self.run_mock.assert_not_called()
        self.values['llm-api-gateway']['llmApiGateway']['auth']['mode'] = 'staticKeys'
        self.secret['metadata']['annotations']['meta.helm.sh/release-name'] = 'other-stack'
        with self.assertRaisesRegex(RuntimeError, 'ownership'):
            with access.temporary_gateway_key(self.recipe, self.url):
                self.fail('entered')
        self.run_mock.assert_not_called()

    def test_subpath_or_multiple_replicas_refused_before_registration(self):
        self.deployment['spec']['template']['spec']['containers'][0]['volumeMounts'][0]['subPath'] = 'api-keys.json'
        with self.assertRaisesRegex(RuntimeError, 'hot-reload'):
            with access.temporary_gateway_key(self.recipe, self.url):
                self.fail('entered')
        self.deployment['spec']['replicas'] = 2
        with self.assertRaisesRegex(RuntimeError, 'one gateway replica'):
            with access.temporary_gateway_key(self.recipe, self.url):
                self.fail('entered')
        self.run_mock.assert_not_called()

    def test_journal_cannot_be_reused_for_another_installation(self):
        key = access.GatewayKey(self.recipe, self.url)
        key.create()
        self.recipe.c['namespace'] = 'other'
        with self.assertRaisesRegex(RuntimeError, 'another installation'):
            access.cleanup_gateway_key(self.recipe, self.url)
        self.assertEqual(len(self.entries()), 2)

    def test_concurrent_use_of_same_work_directory_is_refused(self):
        first = access.GatewayKey(self.recipe, self.url)
        second = access.GatewayKey(self.recipe, self.url)
        with first.locked():
            with self.assertRaisesRegex(RuntimeError, 'Another gateway test'):
                with second.locked():
                    self.fail('entered')

    def test_activation_timeout_removes_unaccepted_key(self):
        with patch.object(access.GatewayKey, 'status', return_value=401), patch.object(access.time, 'monotonic', side_effect=[0, 200, 201]):
            with self.assertRaisesRegex(RuntimeError, 'within the timeout'):
                with access.temporary_gateway_key(self.recipe, self.url):
                    self.fail('entered')
        self.assertEqual(self.entries(), self.original)
        self.assertFalse((self.recipe.work/'temporary-gateway-key.json').exists())

    def test_revocation_wait_failure_retains_journal_even_after_secret_removal(self):
        with self.assertRaisesRegex(RuntimeError, 'cleanup failed'):
            with access.temporary_gateway_key(self.recipe, self.url):
                blocked = patch.object(access.GatewayKey, 'wait', side_effect=RuntimeError('reload delayed'))
                blocked.start()
        blocked.stop()
        self.assertEqual(self.entries(), self.original)
        self.assertTrue((self.recipe.work/'temporary-gateway-key.json').exists())
        access.cleanup_gateway_key(self.recipe, self.url)
        self.assertFalse((self.recipe.work/'temporary-gateway-key.json').exists())

    def test_extra_or_terminating_gateway_pod_refused(self):
        self.pods['items'].append(copy.deepcopy(self.pods['items'][0]))
        with self.assertRaisesRegex(RuntimeError, 'exactly one ready'):
            with access.temporary_gateway_key(self.recipe, self.url):
                self.fail('entered')
        self.pods['items'].pop()
        self.pods['items'][0]['metadata']['deletionTimestamp'] = 'now'
        with self.assertRaisesRegex(RuntimeError, 'exactly one ready'):
            with access.temporary_gateway_key(self.recipe, self.url):
                self.fail('entered')
        self.run_mock.assert_not_called()

    def test_unverified_or_external_gateway_url_is_refused(self):
        for url in ('http://127.0.0.1:18443', 'https://example.com', 'https://user@localhost'):
            with self.subTest(url=url), self.assertRaisesRegex(RuntimeError, 'local HTTPS'):
                access.GatewayKey(self.recipe, url)


if __name__ == '__main__':
    unittest.main()
