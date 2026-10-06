# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('recipe_load_resume', HERE/'recipe.py')
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)


class LoadResumeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='recipe-resume-')
        self.addCleanup(self.tmp.cleanup)
        config = json.loads((HERE/'config.example.json').read_text())
        self.recipe = tool.Recipe(config, self.tmp.name)
        self.recipe.state = {'download': True, 'runtimeSha256': 'a'*64}
        self.values = self.recipe.backend_values('serve')
        self.release = {'name': self.recipe.backend, 'namespace': config['namespace'],
                        'version': 6, 'info': {'status': 'deployed'}}
        self.resources = []
        for suffix, role, container, component in [('', 'leader', 'llama', 'model-server'),
                ('-rpc-worker', 'worker', 'rpc', 'rpc-worker'), ('-artifacts', 'leader', 'artifacts', 'artifacts')]:
            name = self.recipe.backend+suffix
            labels = {'app.kubernetes.io/instance': self.recipe.backend, 'app.kubernetes.io/component': component}
            self.resources.append({'kind': 'Deployment', 'metadata': self.metadata(name, generation=2),
                'spec': {'replicas': 1, 'selector': {'matchLabels': labels}, 'template': {
                    'metadata': {'labels': labels, 'annotations': {'checksum/runtime': self.recipe.state['runtimeSha256']}},
                    'spec': {'runtimeClassName': config['runtimeClass'],
                             'nodeSelector': {'kubernetes.io/hostname': config['nodes'][role]},
                             'containers': [{'name': container, 'image': config['runtimeImage'],
                                'resources': {'requests': {'nvidia.com/gpu': '1'}, 'limits': {'nvidia.com/gpu': '1'}}}]}}},
                'status': {'observedGeneration': 2, 'replicas': 1, 'updatedReplicas': 1,
                           'readyReplicas': 1, 'availableReplicas': 1}})
        for resource in self.resources:
            pod = resource['spec']['template']['spec']
            volume = 'rpc-cache' if resource['metadata']['name'].endswith('-rpc-worker') else 'artifacts'
            pod['volumes'] = [{'name': volume, 'persistentVolumeClaim': {'claimName': self.recipe.backend+'-'+volume}}]
            pod['containers'][0]['volumeMounts'] = [{'name': volume, 'mountPath': '/'+volume}]
        env = {'FIRST_SHARD': self.values['model']['firstShard'], 'SERVED_MODEL': self.values['model']['servedName'],
               'RPC_ENDPOINT': self.recipe.backend+'-rpc-worker:50052', 'SERVER_ARGS': json.dumps(self.values['model']['args'])}
        self.resources[0]['spec']['template']['spec']['containers'][0]['env'] = [
            {'name': key, 'value': value} for key, value in env.items()]
        for suffix in ('-artifacts', '-rpc-cache'):
            name = self.recipe.backend+suffix
            self.resources.append({'kind': 'PersistentVolumeClaim', 'metadata': self.metadata(name),
                'spec': {'volumeName': name+'-volume', 'storageClassName': config['storageClass']},
                'status': {'phase': 'Bound'}})
        self.commands = []
        self.status_calls = 0
        self.resource_calls = 0
        self.next_release = None
        self.next_resources = None
        self.existing = ''

    def metadata(self, name, **extra):
        return {'name': name, 'namespace': self.recipe.c['namespace'], 'uid': name+'-uid',
                'annotations': {'meta.helm.sh/release-name': self.recipe.backend,
                                'meta.helm.sh/release-namespace': self.recipe.c['namespace']},
                'labels': {'app.kubernetes.io/managed-by': 'Helm'}, **extra}

    def command_output(self, command, **kwargs):
        self.commands.append(command)
        if 'exec' not in command:
            self.assertEqual(kwargs.get('timeout'), 45)
        if command == self.recipe.hm+['status', self.recipe.backend, '-o', 'json']:
            self.status_calls += 1
            return json.dumps(self.next_release if self.status_calls > 1 and self.next_release else self.release)
        if command == self.recipe.hm+['get', 'values', self.recipe.backend, '--revision', '6', '-o', 'json']:
            return json.dumps(self.values)
        if command == self.recipe.kc+['get', 'deployment', self.recipe.backend, '--ignore-not-found', '-o', 'json']:
            return self.existing
        if command[:len(self.recipe.kc)+1] == self.recipe.kc+['get']:
            self.resource_calls += 1
            resources = self.next_resources if self.resource_calls > 1 and self.next_resources else self.resources
            return json.dumps({'items': resources})
        if command[:len(self.recipe.kc)+1] == self.recipe.kc+['exec']:
            return 'MemAvailable: 120000000 kB\n'
        raise AssertionError('Unexpected command: '+str(command))

    def attempt(self, error=None):
        with patch.object(self.recipe, 'bound_cluster') as bound, \
             patch.object(self.recipe, 'helm_apply') as helm, patch.object(tool, 'run') as run, \
             patch.object(tool, 'output', side_effect=self.command_output):
            if error:
                with self.assertRaisesRegex(RuntimeError, error):
                    self.recipe.backend_phase('serve')
                self.assertNotIn('serve', self.recipe.state)
                self.assertFalse(self.recipe.state_path.exists())
            else:
                self.recipe.backend_phase('serve')
            bound.assert_called_once()
            run.assert_not_called()
            return helm

    def test_completed_load_recovers_checkpoint_without_helm_or_preload_memory_checks(self):
        self.attempt().assert_not_called()
        self.assertTrue(json.loads(self.recipe.state_path.read_text())['serve'])
        self.assertNotIn('direct', self.recipe.state)
        self.assertNotIn('gateway', self.recipe.state)
        self.assertFalse(any('exec' in command for command in self.commands))
        evidence = json.loads((self.recipe.work/'evidence/load-resume.json').read_text())
        self.assertEqual(evidence['revision'], self.release['version'])
        self.assertEqual(len(evidence['resources']), 5)

    def test_first_load_keeps_memory_preflight_and_helm_apply(self):
        self.values = self.recipe.backend_values('download')
        self.attempt().assert_called_once_with(self.recipe.backend, HERE/'charts/gguf-backend',
                                    self.recipe.backend_values('serve'), '70m', jobs=False)
        memory = [command for command in self.commands if 'exec' in command]
        self.assertEqual(len(memory), 2)
        self.assertTrue(any('deploy/'+self.recipe.backend+'-rpc-leader' in command for command in memory))
        self.assertTrue(self.recipe.state['serve'])

    def test_pending_and_failed_release_never_adopts_ready_resources(self):
        for status in ('pending-upgrade', 'pending-install', 'pending-rollback', 'failed', 'uninstalling'):
            with self.subTest(status=status):
                self.release['info']['status'] = status
                self.attempt('Resolve the Helm operation').assert_not_called()
        self.assertEqual(self.resource_calls, 0)
        self.assertFalse(any('exec' in command for command in self.commands))

    def test_changed_values_and_unexpected_phase_are_rejected(self):
        for field, value in [('phase', 'qualify'), ('model', {'register': True}), ('image', 'another/image:tag')]:
            with self.subTest(field=field):
                self.values = self.recipe.backend_values('serve')
                self.values[field] = value
                self.attempt('differ|not at').assert_not_called()

    def test_download_cannot_overwrite_an_existing_model(self):
        self.values = self.recipe.backend_values('download')
        self.existing = json.dumps(self.resources[0])
        self.attempt('already exists').assert_not_called()

    def test_missing_foreign_terminating_or_unready_resources_are_rejected(self):
        original = copy.deepcopy(self.resources)
        mutations = [
            lambda items: items.pop(),
            lambda items: items[0]['metadata']['annotations'].update({'meta.helm.sh/release-name': 'another-release'}),
            lambda items: items[1]['metadata'].update(deletionTimestamp='2026-10-02T00:00:00Z'),
            lambda items: items[0]['status'].update(observedGeneration=1),
            lambda items: items[1]['status'].update(updatedReplicas=0),
            lambda items: items[2]['status'].update(readyReplicas=0),
            lambda items: items[0]['spec']['template']['spec']['volumes'][0]['persistentVolumeClaim'].update(claimName='another-artifacts'),
            lambda items: items[1]['spec']['template']['spec']['volumes'][0]['persistentVolumeClaim'].update(claimName='another-cache'),
            lambda items: items[0]['spec']['template']['spec']['containers'][0]['env'][2].update(value='another-rpc-worker:50052'),
            lambda items: items[0]['spec']['template']['spec']['containers'][0]['env'][0].update(value='another-model.gguf'),
            lambda items: items[3]['status'].update(phase='Pending'),
            lambda items: items[4]['spec'].update(storageClassName='another-class'),
            lambda items: items[0]['spec']['template']['spec']['nodeSelector'].update({'kubernetes.io/hostname': 'another-node'}),
            lambda items: items[1]['spec']['template']['spec']['containers'][0].update(image='another/image:tag'),
            lambda items: items[0]['spec']['template']['metadata']['annotations'].update({'checksum/runtime': 'b'*64}),
            lambda items: items[1]['spec']['template']['spec']['containers'][0]['resources']['limits'].update({'nvidia.com/gpu': '0'}),
        ]
        for index, mutate in enumerate(mutations):
            with self.subTest(index=index):
                self.resources = copy.deepcopy(original)
                mutate(self.resources)
                self.attempt('[Mm]odel|Missing').assert_not_called()

    def test_revision_or_resource_replacement_during_check_is_rejected(self):
        self.next_release = copy.deepcopy(self.release)
        self.next_release['version'] += 1
        self.attempt('changed while resuming').assert_not_called()
        self.status_calls = self.resource_calls = 0
        self.next_release = None
        self.next_resources = copy.deepcopy(self.resources)
        self.next_resources[0]['metadata']['uid'] = 'replacement'
        self.attempt('changed while resuming').assert_not_called()

    def test_read_timeout_preserves_missing_checkpoint(self):
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply') as helm, \
             patch.object(tool, 'output', side_effect=subprocess.TimeoutExpired(['helm', 'status'], 45)):
            with self.assertRaises(subprocess.TimeoutExpired):
                self.recipe.backend_phase('serve')
            self.assertNotIn('serve', self.recipe.state)
            self.assertFalse(self.recipe.state_path.exists())
            helm.assert_not_called()

    def test_connection_failure_preserves_missing_checkpoint(self):
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'helm_apply') as helm, \
             patch.object(tool, 'output', side_effect=OSError('connection lost')):
            with self.assertRaisesRegex(OSError, 'connection lost'):
                self.recipe.backend_phase('serve')
            self.assertNotIn('serve', self.recipe.state)
            self.assertFalse(self.recipe.state_path.exists())
            helm.assert_not_called()


if __name__ == '__main__':
    unittest.main()
