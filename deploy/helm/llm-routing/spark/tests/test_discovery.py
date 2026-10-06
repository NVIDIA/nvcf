# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
import pathlib
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('discovery_recipe', HERE/'spark.py')
spark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(spark)


class DiscoveryTests(unittest.TestCase):
    def setUp(self):
        def owned(name, release, node=None):
            obj = {'metadata': {'name': name, 'namespace': 'demo', 'annotations': {
                'meta.helm.sh/release-name': release, 'meta.helm.sh/release-namespace': 'demo'}}}
            if node:
                obj['spec'] = {'template': {'spec': {'nodeSelector': {'kubernetes.io/hostname': node}, 'containers': []}}}
            return obj
        self.gateway = owned('llm-api-gateway', 'team-stack', 'control')
        self.router = owned('llm-request-router', 'team-stack', 'control')
        self.gateway['spec']['template']['spec']['containers'] = [{'image': 'registry.example.com/gateway:current'}]
        self.router['spec']['template']['spec']['containers'] = [{'image': 'other.example.com/router:previous'}]
        endpoint = owned('glm53-iq2', 'team-glm')
        endpoint['spec'] = {'service': {'name': 'team-glm'}, 'modelName': 'GLM-5.3-UD-IQ2_M'}
        self.resources = {('deployment', 'llm-api-gateway'): self.gateway,
                          ('deployment', 'llm-request-router'): self.router,
                          ('deployment', 'team-glm'): owned('team-glm', 'team-glm', 'leader'),
                          ('deployment', 'team-glm-rpc-worker'): owned('team-glm-rpc-worker', 'team-glm', 'worker'),
                          ('deployment', 'operator'): owned('operator', 'operator', 'control'),
                          ('service', 'team-glm'): owned('team-glm', 'team-glm'),
                          ('inferenceendpoint', 'glm53-iq2'): endpoint}
        self.deployments = [self.gateway]
        self.values = {
            'team-stack': {'sparkRecipeSource': {'revision': 'old-build'},
                           'clusterId': 'demo', 'apiKeys': [{'id': 'owner', 'sha256': 'DO-NOT-COPY-HASH'}],
                           'tls': {'privateKey': 'DO-NOT-COPY-TLS'},
                           'llm-api-gateway': {'llmApiGateway': {'image': {'registry': 'registry.example.com', 'repository': 'gateway', 'tag': 'current', 'pullPolicy': 'Never'},
                               'auth': {'mode': 'staticKeys', 'staticKeys': {'existingSecret': 'caller-keys'}}}},
                           'llm-request-router': {'llmRequestRouter': {'image': {'registry': 'other.example.com', 'repository': 'router', 'tag': 'previous'}}}},
            'team-glm': {'targets': [{'id': 'leader', 'node': 'leader'}, {'id': 'worker', 'node': 'worker'}],
                         'artifacts': {'storageClassName': 'storage'}, 'runtimeClassName': 'gpu', 'image': 'cuda/runtime:pinned'},
            'operator': {'clusterId': 'demo', 'watchNamespaces': ['demo'], 'router': {'grpcAddress': 'http://llm-request-router.demo.svc.cluster.local:50071'},
                         'fullnameOverride': 'operator', 'trustBundle': {'configMap': 'public-ca'},
                         'image': {'repository': 'operator.example.com/operator'}, 'pylon': {'image': {'repository': 'worker.example.com/pylon'}}},
            'shared-images': {'archiveNode': 'control', 'nodeNames': ['control'], 'runAsUser': 4567, 'socketPath': '/run/custom/containerd.sock'}}
        self.releases = [{'name': 'operator', 'chart': 'pylon-operator-1.0'}, {'name': 'shared-images', 'chart': 'pylon-image-loader-1.0'}]
        self.commands = []

    def output(self, command):
        self.commands.append(command)
        if command[0] == 'helm':
            self.assertIn('--kube-context', command)
            if 'list' in command:
                return json.dumps(self.releases)
            self.assertIn('--all', command)
            return json.dumps(self.values[command[command.index('values')+1]])
        self.assertIn('--context', command)
        if 'deployments' in command:
            if '--all-namespaces' in command:
                self.assertLess(command.index('get'), command.index('--all-namespaces'))
            return json.dumps({'items': self.deployments})
        offset = command.index('get')
        return json.dumps(self.resources[tuple(command[offset+1:offset+3])])

    def discover(self, namespace=None):
        with patch.object(spark, 'output', side_effect=self.output), patch.object(spark, 'run') as mutate:
            result = spark.discover_config('my-context', namespace)
            mutate.assert_not_called()
            return result

    def test_discovers_distinct_repositories_import_settings_and_omits_credentials(self):
        config = self.discover()
        self.assertEqual(config['context'], 'my-context')
        self.assertEqual(config['releases'], {'stack': 'team-stack', 'operator': 'operator', 'glm': 'team-glm'})
        self.assertEqual(config['images']['repositories'], {'gateway': 'registry.example.com/gateway', 'router': 'other.example.com/router',
                                                         'operator': 'operator.example.com/operator', 'pylon': 'worker.example.com/pylon'})
        self.assertEqual(config['containerd']['runAsUser'], 4567)
        self.assertEqual(config['containerd']['socketPath'], '/run/custom/containerd.sock')
        self.assertNotIn('archiveDirectory', config['containerd'])
        self.assertEqual(config['releasePrefix'], 'shared')
        self.assertIsNone(config['apiKeyFile'])
        self.assertNotIn('DO-NOT-COPY', json.dumps(config))

    def test_legacy_archive_directory_is_not_carried_into_new_configuration(self):
        self.values['shared-images']['archiveDirectory'] = '/unused/old-user-directory'
        config = self.discover()
        self.assertNotIn('archiveDirectory', config['containerd'])
        self.assertEqual(config['containerd']['archiveNode'], 'control')
        self.assertEqual(config['containerd']['nodeNames'], ['control'])

    def test_ambiguous_installations_need_namespace(self):
        another = copy.deepcopy(self.gateway)
        another['metadata']['namespace'] = 'other'
        self.deployments.append(another)
        with self.assertRaisesRegex(RuntimeError, 'Multiple.*--namespace'):
            self.discover()
        self.assertEqual(len(self.commands), 1)

    def test_namespace_scoped_discovery_does_not_list_cluster_deployments(self):
        self.discover('demo')
        self.assertNotIn('--all-namespaces', self.commands[0])
        self.assertEqual(self.commands[0][self.commands[0].index('-n')+1], 'demo')

    def test_source_metadata_is_informational_for_discovery(self):
        for source in (None, {'revision': 'another-build'}, {'repository': 'old-fork', 'revision': 'old-pin'}):
            self.values['team-stack']['sparkRecipeSource'] = source
            self.discover()

    def test_model_service_foreign_ownership_is_rejected(self):
        self.resources['service', 'team-glm']['metadata']['annotations']['meta.helm.sh/release-name'] = 'someone-else'
        with self.assertRaisesRegex(RuntimeError, 'GLM resource ownership'):
            self.discover()

    def test_unrelated_operator_is_ignored(self):
        self.releases.insert(0, {'name': 'unrelated-operator', 'chart': 'pylon-operator-1.0'})
        self.values['unrelated-operator'] = {'clusterId': 'different'}
        self.assertEqual(self.discover()['releases']['operator'], 'operator')

    def test_no_importer_does_not_invent_privileged_paths(self):
        self.releases = self.releases[:1]
        self.assertIsNone(self.discover()['containerd'])

    def test_live_image_drift_is_rejected(self):
        self.router['spec']['template']['spec']['containers'][0]['image'] = 'different/image:tag'
        with self.assertRaisesRegex(RuntimeError, 'Live router image differs'):
            self.discover()

    def test_missing_context_never_inspects_default_cluster(self):
        with patch.object(spark, 'output') as output, self.assertRaisesRegex(RuntimeError, '--context'):
            spark.discover_config(None)
        output.assert_not_called()


if __name__ == '__main__':
    unittest.main()
