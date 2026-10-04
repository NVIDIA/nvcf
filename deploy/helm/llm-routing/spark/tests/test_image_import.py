# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import hashlib
import importlib.util
import io
import json
import pathlib
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from contextlib import ExitStack, redirect_stdout
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('image_import_recipe', HERE/'spark.py')
spark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(spark)


class ImageImportTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        self.config = json.loads((HERE/'config.example.json').read_text())
        self.recipe = spark.Recipe(self.config, self.root/'work')
        self.archive = self.root/'images.tar'
        manifest = json.dumps([{'RepoTags': [self.recipe.image('gateway', 'edited')]}]).encode()
        with tarfile.open(self.archive, 'w') as tar:
            info = tarfile.TarInfo('manifest.json')
            info.size = len(manifest)
            tar.addfile(info, io.BytesIO(manifest))
        self.release = self.config['releasePrefix']+'-images'
        self.base = self.release+'-2'
        self.server = self.base+'-server'
        self.nodes = [self.config['nodes']['control']]
        self.names = [self.server]+[self.base+'-'+node for node in self.nodes]
        self.jobs = [{'metadata': {'name': name, 'uid': name+'-uid', 'annotations': {
            'meta.helm.sh/release-name': self.release, 'meta.helm.sh/release-namespace': self.config['namespace']}}}
            for name in self.names]
        self.pod = {'metadata': {'name': self.server+'-pod', 'uid': 'pod-uid',
                               'ownerReferences': [{'kind': 'Job', 'uid': self.server+'-uid'}]},
                    'spec': {'nodeName': self.config['nodes']['control']}}
        self.existing = []
        self.prior_jobs = []
        self.commands = []
        self.helm_calls = []
        self.calls = []
        self.jobs_reads = 0
        self.empty_pod_reads = 0
        self.upload_data = None
        self.replace_after_upload = False
        self.wait_failure = False
        self.destination = self.root/'uploaded.tar'

    def output(self, command, **kwargs):
        self.calls.append(command)
        if command[0] == 'helm':
            action = command[len(self.recipe.hm):]
            if action[0] == 'list':
                return json.dumps(self.existing)
            if action[0] == 'status':
                return '{"version": 2}'
        action = command[len(self.recipe.kc):]
        if action == ['get', 'jobs', '-o', 'json']:
            return json.dumps({'items': self.prior_jobs})
        if action[:2] == ['get', 'jobs']:
            self.jobs_reads += 1
            jobs = copy.deepcopy(self.jobs)
            if self.replace_after_upload and self.jobs_reads > 1:
                jobs[0]['metadata']['uid'] = 'replacement'
            return json.dumps({'items': jobs})
        if action[:2] == ['get', 'pods']:
            if self.empty_pod_reads:
                self.empty_pod_reads -= 1
                return '{"items": []}'
            return json.dumps({'items': [self.pod]})
        if action[:2] == ['get', 'pod']:
            return json.dumps(self.pod)
        self.fail('Unexpected inspection: '+repr(command))

    def execute(self, command, **kwargs):
        self.commands.append(command)
        action = command[len(self.recipe.kc):]
        if action[0] == 'exec':
            self.assertTrue(kwargs['timeout'] > 0)
            self.assertIn('-i', action)
            index = command.index('python3')
            code, expected_hash, expected_size = command[index+2], command[index+4], command[index+5]
            data = kwargs['stdin'].read()
            self.assertEqual(hashlib.sha256(data).hexdigest(), expected_hash)
            self.assertEqual(str(len(data)), expected_size)
            subprocess.run([sys.executable, '-c', code, str(self.destination), expected_hash, expected_size],
                           input=data if self.upload_data is None else self.upload_data,
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
        elif action[0] == 'wait':
            if self.wait_failure and '--for=condition=complete' in action:
                raise RuntimeError('import failed')
        else:
            self.fail('Unexpected command: '+repr(command))

    def invoke(self):
        with ExitStack() as stack:
            stack.enter_context(patch.object(self.recipe, 'bound_cluster'))
            helm = stack.enter_context(patch.object(self.recipe, 'helm_apply', side_effect=lambda *args, **kwargs: self.helm_calls.append((args, kwargs))))
            stack.enter_context(patch.object(spark, 'output', side_effect=self.output))
            stack.enter_context(patch.object(spark, 'run', side_effect=self.execute))
            stack.enter_context(patch.object(spark.time, 'sleep'))
            stack.enter_context(redirect_stdout(io.StringIO()))
            self.recipe.import_images(self.archive, True, 'gateway', 'edited')
            return helm.call_args

    def test_gateway_archive_upload_targets_control_node_without_private_host_path(self):
        self.config['containerd']['archiveDirectory'] = '/unused/private/path'
        call = self.invoke()
        values = call.args[2]
        self.assertEqual(values['nodeNames'], self.nodes)
        self.assertEqual(values['archiveSha256'], hashlib.sha256(self.archive.read_bytes()).hexdigest())
        self.assertNotIn('archiveDirectory', values)
        self.assertFalse(call.kwargs['wait'])
        self.assertEqual(self.destination.read_bytes(), self.archive.read_bytes())
        self.assertEqual(self.destination.stat().st_mode & 0o777, 0o600)
        self.assertFalse(pathlib.Path(str(self.destination)+'.upload').exists())
        self.assertTrue((self.recipe.work/'evidence/image-import.json').exists())
        for command in self.commands+self.calls:
            if command[0] == 'kubectl':
                self.assertEqual(command[1:3], ['--context', self.config['context']])
        self.assertTrue(all(command[0] in ('kubectl', 'helm') for command in self.commands+self.calls))

    @unittest.skipUnless(shutil.which('helm'), 'Helm is required to check command flags')
    def test_release_ownership_query_uses_supported_flags_for_every_status(self):
        self.invoke()
        command = next(cmd for cmd in self.calls if cmd[0] == 'helm' and 'list' in cmd)
        self.assertNotIn('--all', command)
        for flag in ('--deployed', '--failed', '--pending', '--uninstalled', '--superseded', '--uninstalling'):
            self.assertIn(flag, command)
        # --help parses flags without reading the cluster or changing resources.
        subprocess.run(command+['--help'], stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
        subprocess.run(['helm', 'get', 'values', 'test-release', '--all', '--help'],
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)

    def test_socket_settings_do_not_require_archive_directory_or_personal_uid(self):
        self.config['containerd'] = {'socketPath': '/run/k3s/containerd/containerd.sock'}
        call = self.invoke()
        self.assertEqual(call.args[2]['archiveNode'], self.config['nodes']['control'])
        self.assertEqual(call.args[2]['runAsUser'], 1000)

    def test_waits_for_server_pod_creation_before_readiness_and_upload(self):
        self.empty_pod_reads = 2
        self.invoke()
        self.assertEqual(self.empty_pod_reads, 0)
        self.assertIn('pod/'+self.pod['metadata']['name'], self.commands[0])
        self.assertIn('exec', self.commands[1])
        self.assertIn('--for=condition=complete', self.commands[2])

    def test_foreign_release_is_rejected_before_helm(self):
        self.existing = [{'chart': 'unrelated-1.0.0'}]
        with self.assertRaisesRegex(RuntimeError, 'another chart'):
            self.invoke()
        self.assertEqual(self.helm_calls, [])
        self.assertEqual(self.commands, [])

    def test_pending_owned_import_is_rejected_before_upload(self):
        self.prior_jobs = copy.deepcopy(self.jobs)
        with self.assertRaisesRegex(RuntimeError, 'Another image import is active'):
            self.invoke()
        self.assertEqual(self.commands, [])

    def failed_attempt(self):
        return {'failed': True, 'release': self.release, 'revision': 2,
                'jobs': {job['metadata']['name']: job['metadata']['uid'] for job in self.jobs},
                'context': self.config['context'], 'namespace': self.config['namespace']}

    def test_failed_own_attempt_can_retry_without_waiting_for_job_deadline(self):
        self.prior_jobs = copy.deepcopy(self.jobs)
        spark.save(self.recipe.work/'image-import-attempt.json', self.failed_attempt())
        self.invoke()
        self.assertEqual(len(self.helm_calls), 1)
        self.assertFalse((self.recipe.work/'image-import-attempt.json').exists())

    def test_retry_refuses_different_revision_job_uid_context_or_namespace(self):
        self.prior_jobs = copy.deepcopy(self.jobs)
        for field in ('revision', 'jobs', 'context', 'namespace'):
            with self.subTest(field=field):
                previous = self.failed_attempt()
                previous[field] = {} if field == 'jobs' else 'different'
                spark.save(self.recipe.work/'image-import-attempt.json', previous)
                with self.assertRaisesRegex(RuntimeError, 'Another image import|revision changed'):
                    self.invoke()
                self.assertEqual(self.helm_calls, [])
                self.assertEqual(self.commands, [])

    def test_upload_failure_saves_identity_bound_retry_record(self):
        self.upload_data = b'bad data'
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke()
        self.assertEqual(json.loads((self.recipe.work/'image-import-attempt.json').read_text()), self.failed_attempt())

    def test_finished_prior_import_does_not_block_new_upload(self):
        self.prior_jobs = copy.deepcopy(self.jobs)
        for job in self.prior_jobs:
            job['status'] = {'conditions': [{'type': 'Complete', 'status': 'True'}]}
        self.invoke()

    def test_job_with_foreign_helm_owner_rejects_before_upload(self):
        self.jobs[0]['metadata']['annotations']['meta.helm.sh/release-name'] = 'foreign'
        with self.assertRaisesRegex(RuntimeError, 'different Helm ownership'):
            self.invoke()
        self.assertEqual(self.commands, [])
        self.assertFalse(self.destination.exists())

    def test_pod_with_wrong_owner_or_node_cannot_receive_archive(self):
        for kind in ('owner', 'node'):
            with self.subTest(kind=kind):
                original = copy.deepcopy(self.pod)
                if kind == 'owner':
                    self.pod['metadata']['ownerReferences'][0]['uid'] = 'foreign'
                else:
                    self.pod['spec']['nodeName'] = 'another-node'
                with self.assertRaisesRegex(RuntimeError, 'ownership|placement'):
                    self.invoke()
                self.assertFalse(self.destination.exists())
                self.assertFalse(any('exec' in cmd for cmd in self.commands))
                self.pod = original

    def test_corrupted_or_oversize_upload_is_never_published(self):
        for data in (b'corrupted', self.archive.read_bytes()+b'extra'):
            with self.subTest(length=len(data)):
                self.upload_data = data
                with self.assertRaises(subprocess.CalledProcessError):
                    self.invoke()
                self.assertFalse(self.destination.exists())
                self.assertFalse(pathlib.Path(str(self.destination)+'.upload').exists())
                self.assertFalse((self.recipe.work/'evidence/image-import.json').exists())
                self.assertFalse(any('--for=condition=complete' in cmd for cmd in self.commands))
                self.commands.clear()

    def test_archive_limit_is_checked_before_helm_or_upload(self):
        with self.archive.open('wb') as archive:
            archive.truncate(1024**3)
        with self.assertRaisesRegex(RuntimeError, '1 GiB'):
            self.invoke()
        self.assertEqual(self.commands, [])
        self.assertEqual(self.calls, [])

    def test_failed_or_replaced_jobs_do_not_record_success(self):
        self.wait_failure = True
        with self.assertRaisesRegex(RuntimeError, 'import failed'):
            self.invoke()
        self.assertFalse((self.recipe.work/'evidence/image-import.json').exists())
        self.wait_failure = False
        self.replace_after_upload = True
        self.jobs_reads = 0
        with self.assertRaisesRegex(RuntimeError, 'Jobs changed'):
            self.invoke()
        self.assertFalse((self.recipe.work/'evidence/image-import.json').exists())


@unittest.skipUnless(shutil.which('helm'), 'Helm is required to render the image-loader chart')
class ImageLoaderChartTests(unittest.TestCase):
    def test_server_uses_rootless_writable_temporary_storage_and_clients_keep_socket_access(self):
        rendered = subprocess.check_output(['helm', 'template', 'image-test', str(HERE/'charts/image-loader'),
                    '--set', 'enabled=true', '--set', 'nodeNames[0]=node-one', '--set', 'archiveNode=node-one',
                    '--set', 'archiveSha256='+'a'*64, '--set', 'runAsUser=2345',
                    '--set', 'archiveDirectory=/unused/legacy/path'], text=True)
        documents = rendered.split('---')
        server = next(doc for doc in documents if 'name: image-test-1-server\n' in doc)
        client = next(doc for doc in documents if 'name: image-test-1-node-one\n' in doc)
        self.assertNotIn('hostPath:', server)
        self.assertNotIn('/unused/legacy/path', rendered)
        self.assertIn('emptyDir: {sizeLimit: 1Gi}', server)
        self.assertIn('fsGroup: 2345', server)
        self.assertIn('runAsUser: 2345', server)
        self.assertIn('runAsNonRoot: true', server)
        self.assertNotIn('mountPath: /images, readOnly: true', server)
        self.assertIn('type: Socket', client)
        self.assertIn('sha256sum -c -', client)
        self.assertIn('images import --platform=', client)
        self.assertIn('activeDeadlineSeconds:', server)
        self.assertIn('activeDeadlineSeconds:', client)


if __name__ == '__main__':
    unittest.main()
