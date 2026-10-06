# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import json
import unittest
from unittest.mock import patch
from test_cluster_setup import setup, node, gpu_pod, HERE


class ReinitializationTests(unittest.TestCase):
    def setUp(self):
        self.config = json.loads((HERE/'config.example.json').read_text())
        self.namespace = self.config['namespace']
        self.prefix = self.config['releasePrefix']
        self.resources = {
            'namespaces': [{'metadata': {'name': self.namespace}}],
            'nodes': [node(name) for name in self.config['nodes'].values()],
            'pods': [], 'inferenceendpoints.pylon.nvidia.com': [],
            'deployments,statefulsets,daemonsets,jobs,cronjobs,pods,services,ingresses': [],
            'persistentvolumeclaims': [], 'persistentvolumes': [], 'secrets': [],
            'crds': [{'metadata': self.metadata('inferenceendpoints.pylon.nvidia.com', self.prefix+'-operator'),
                      'spec': {'group': 'pylon.nvidia.com', 'scope': 'Namespaced', 'names': {'kind': 'InferenceEndpoint'},
                               'versions': [{'name': 'v1alpha1', 'served': True, 'storage': True}]}}],
        }
        for suffix, role in [('artifacts', 'leader'), ('rpc-cache', 'worker')]:
            name = self.prefix+'-glm-'+suffix
            self.resources['persistentvolumeclaims'].append({
                'metadata': self.metadata(name, self.prefix+'-glm'), 'status': {'phase': 'Bound'},
                'spec': {'volumeName': name, 'storageClassName': self.config['storageClass'],
                         'accessModes': ['ReadWriteOnce']}})
            self.resources['persistentvolumes'].append({'metadata': {'name': name}, 'spec': {
                'claimRef': {'uid': name, 'name': name, 'namespace': self.namespace},
                'nodeAffinity': {'required': {'nodeSelectorTerms': [{'matchExpressions': [
                    {'key': 'kubernetes.io/hostname', 'operator': 'In', 'values': [self.config['nodes'][role]]}]}]}}}})

    def metadata(self, name, release):
        return {'name': name, 'uid': name, 'annotations': {'meta.helm.sh/release-name': release,
                'meta.helm.sh/release-namespace': self.namespace, 'helm.sh/resource-policy': 'keep'}}

    def validate(self, releases=None):
        before = copy.deepcopy(self.config)
        with patch.object(setup, 'items', side_effect=lambda ctx, resource, **kw: copy.deepcopy(self.resources[resource])), \
             patch.object(setup.subprocess, 'check_output', return_value=json.dumps(releases or [])) as helm:
            setup.validate_reinitialization(self.config, self.config['releasePrefix'] + '-glm')
        command = helm.call_args.args[0]
        self.assertEqual(command[:5], ['helm', '--kube-context', self.config['context'], '-n', self.namespace])
        self.assertIn('--pending', command)
        self.assertIn('--uninstalling', command)
        self.assertEqual(self.config, before)

    def test_uninstalled_demo_reuses_placement_and_storage(self):
        self.validate()

    def test_saved_fresh_config_can_be_reused_without_namespace(self):
        self.resources['namespaces'] = []
        self.resources['crds'] = []
        self.validate()

    def test_any_release_status_blocks_reset(self):
        for status in ['deployed', 'pending-install', 'pending-upgrade', 'failed', 'uninstalling', 'uninstalled']:
            with self.subTest(status=status), self.assertRaisesRegex(setup.ClusterSetupError, 'Helm releases still exist'):
                self.validate([{'name': self.prefix+'-stack', 'status': status}])

    def test_workloads_and_endpoints_block_reset(self):
        for resource in ['deployments,statefulsets,daemonsets,jobs,cronjobs,pods,services,ingresses',
                         'inferenceendpoints.pylon.nvidia.com']:
            with self.subTest(resource=resource):
                self.resources[resource] = [{'metadata': {'name': 'running'}}]
                with self.assertRaisesRegex(setup.ClusterSetupError, 'still exist'):
                    self.validate()
                self.resources[resource] = []

    def test_foreign_owner_or_missing_keep_blocks_reuse(self):
        for resource in ['crds', 'persistentvolumeclaims']:
            for key, value in [('meta.helm.sh/release-name', 'foreign'),
                               ('meta.helm.sh/release-namespace', 'foreign'), ('helm.sh/resource-policy', '')]:
                with self.subTest(resource=resource, key=key):
                    annotations = self.resources[resource][0]['metadata']['annotations']
                    original = annotations[key]
                    annotations[key] = value
                    with self.assertRaisesRegex(setup.ClusterSetupError, 'another owner'):
                        self.validate()
                    annotations[key] = original

    def test_changed_storage_binding_or_placement_is_rejected(self):
        claim = self.resources['persistentvolumeclaims'][0]
        for key, value in [('storageClassName', 'foreign'), ('volumeName', 'missing'), ('accessModes', ['ReadWriteMany'])]:
            with self.subTest(key=key):
                old = claim['spec'][key]
                claim['spec'][key] = value
                with self.assertRaises(setup.ClusterSetupError):
                    self.validate()
                claim['spec'][key] = old
        self.resources['persistentvolumes'][0]['spec']['nodeAffinity']['required']['nodeSelectorTerms'][0]['matchExpressions'][0]['values'] = ['other-node']
        with self.assertRaisesRegex(setup.ClusterSetupError, 'affinity'):
            self.validate()

    def test_foreign_secret_and_incompatible_crd_are_rejected(self):
        self.resources['secrets'] = [{'metadata': {'name': 'foreign'}}]
        with self.assertRaisesRegex(setup.ClusterSetupError, 'Secret'):
            self.validate()
        self.resources['secrets'] = []
        self.resources['crds'][0]['spec']['versions'][0]['name'] = 'v2'
        with self.assertRaisesRegex(setup.ClusterSetupError, 'incompatible'):
            self.validate()

    def test_busy_gpu_and_terminating_namespace_are_rejected(self):
        self.resources['pods'] = [gpu_pod(self.config['nodes']['leader'])]
        with self.assertRaisesRegex(setup.ClusterSetupError, 'occupied'):
            self.validate()
        self.resources['pods'] = []
        self.resources['namespaces'][0]['metadata']['deletionTimestamp'] = 'now'
        with self.assertRaisesRegex(setup.ClusterSetupError, 'terminating'):
            self.validate()

    def test_foreign_namespace_without_retained_resources_is_rejected(self):
        self.resources['crds'] = []
        self.resources['persistentvolumeclaims'] = []
        with self.assertRaisesRegex(setup.ClusterSetupError, 'ownership evidence'):
            self.validate()


if __name__ == '__main__':
    unittest.main()
