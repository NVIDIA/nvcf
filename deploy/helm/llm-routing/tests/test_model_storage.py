# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import json
from pathlib import Path
import subprocess
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'recipes'))
import model_storage


def claim(name='old-model-cache-0', namespace='old-namespace', release='old-model', bound=True):
    return {'metadata': {'name': name, 'namespace': namespace, 'annotations': {
        'meta.helm.sh/release-name': release, 'volume.kubernetes.io/selected-node': 'node-a'}},
        'spec': {'accessModes': ['ReadWriteOnce'], 'resources': {'requests': {'storage': '80Gi'}},
                 **({'volumeName': 'pv-a'} if bound else {})},
        'status': {'phase': 'Bound' if bound else 'Pending', 'capacity': {'storage': '80Gi'} if bound else {}}}


def pod(name='server', cache='old-model-cache-0', namespace='old-namespace'):
    return {'metadata': {'name': name, 'namespace': namespace},
            'spec': {'nodeName': 'node-a', 'volumes': [{'name': 'weights', 'persistentVolumeClaim': {'claimName': cache}}],
                     'containers': [{'name': 'model', 'resources': {'requests': {'nvidia.com/gpu': '1'}},
                                     'volumeMounts': [{'name': 'weights', 'mountPath': '/cache'}]}]},
            'status': {'phase': 'Running', 'containerStatuses': [{'name': 'model', 'state': {'running': {}}}]}}


class StorageTests(unittest.TestCase):
    def collect(self, claims=None, pods=None, usage='30448560\t/cache\n', failures=None, volumes=None, nodes=None):
        calls = []
        claims = [claim()] if claims is None else claims
        failures = failures or {}
        def run(command, timeout):
            calls.append(command)
            self.assertEqual(command[:3], ['kubectl', '--context', 'test-context'])
            if command[3] == 'get':
                kind = command[4]
                if kind in failures:
                    raise failures[kind]
                if kind == 'persistentvolumeclaims':
                    self.assertIn('--all-namespaces', command)
                    return json.dumps({'items': claims})
                self.assertEqual(kind, 'persistentvolumes')
                return json.dumps({'items': volumes or []})
            self.assertEqual(command[3:6], ['--namespace', 'old-namespace', 'exec'])
            self.assertEqual(command[-4:], ['--', 'du', '-sk', '/cache'])
            self.assertEqual(timeout, 10)
            if isinstance(usage, Exception):
                raise usage
            return usage
        report = model_storage.collect('test-context', {'pods': pods or [], 'nodes': nodes or []}, run)
        return report, calls

    def test_retained_claims_discovered_without_endpoints_or_pods(self):
        report, calls = self.collect()
        self.assertEqual(report['warnings'], [])
        self.assertEqual(len(report['caches']), 1)
        row = report['caches'][0]
        self.assertEqual((row['namespace'], row['nodes']), ('old-namespace', ['node-a']))
        self.assertEqual(row['usageStatus'], 'not-mounted')
        self.assertIsNone(row['bytesUsed'])
        self.assertEqual(len(calls), 2)

    def test_claim_inventory_keeps_ownership_for_foreign_name_conflicts(self):
        pvc = claim('qwen-fp8-cache-0', namespace='llm-stack', release='different-release')
        pvc['metadata']['annotations']['meta.helm.sh/release-namespace'] = 'llm-stack'
        pvc['spec']['storageClassName'] = 'local-path'
        report, _ = self.collect(claims=[pvc])
        self.assertEqual(report['caches'], [])
        self.assertEqual(report['claims'][0]['release'], 'different-release')
        self.assertEqual(report['claims'][0]['releaseNamespace'], 'llm-stack')
        self.assertEqual(report['claims'][0]['storageClass'], 'local-path')
        self.assertEqual(report['claims'][0]['nodes'], ['node-a'])

    def test_claim_inventory_preserves_actual_bound_capacity_and_access(self):
        pvc = claim()
        pvc['status']['capacity']['storage'] = '10Gi'
        report, _ = self.collect(claims=[pvc])
        row = report['claims'][0]
        self.assertEqual(row['capacityBytes'], 10 * 1024**3)
        self.assertEqual(row['phase'], 'Bound')
        self.assertEqual(row['accessModes'], ['ReadWriteOnce'])
        self.assertEqual(row['volumeMode'], 'Filesystem')
        self.assertFalse(row['deleting'])
        pvc['metadata']['deletionTimestamp'] = '2026-10-08T00:00:00Z'
        report, _ = self.collect(claims=[pvc])
        self.assertTrue(report['claims'][0]['deleting'])

    def test_pv_hostname_affinity_resolves_node_names(self):
        pvc = claim()
        del pvc['metadata']['annotations']['volume.kubernetes.io/selected-node']
        nodes = [{'metadata': {'name': 'available-0', 'labels': {'kubernetes.io/hostname': 'worker-a'}}}]
        selector = {'key': 'kubernetes.io/hostname', 'operator': 'In', 'values': ['worker-a']}
        volume = {'metadata': {'name': 'pv-a'}, 'spec': {'nodeAffinity': {'required': {
            'nodeSelectorTerms': [{'matchExpressions': [selector]}]}}}}
        report, _ = self.collect(claims=[pvc], volumes=[volume], nodes=nodes)
        self.assertEqual(report['claims'][0]['nodes'], ['available-0'])
        self.assertEqual(report['caches'][0]['nodes'], ['available-0'])
        nodes.append({'metadata': {'name': 'available-1', 'labels': {'kubernetes.io/hostname': 'worker-a'}}})
        report, _ = self.collect(claims=[pvc], volumes=[volume], nodes=nodes)
        self.assertEqual(report['claims'][0]['nodes'], ['available-0', 'available-1'])
        report, _ = self.collect(claims=[pvc], volumes=[volume], nodes=[])
        self.assertEqual(report['claims'][0]['nodes'], [])
        volume['spec']['nodeAffinity']['required']['nodeSelectorTerms'] = [
            {'matchFields': [{'key': 'metadata.name', 'operator': 'In', 'values': ['available-1']}]}]
        report, _ = self.collect(claims=[pvc], volumes=[volume], nodes=nodes)
        self.assertEqual(report['claims'][0]['nodes'], ['available-1'])

    def test_disk_measurement_uses_blocks_not_claim_request(self):
        report, calls = self.collect(pods=[pod()])
        row = report['caches'][0]
        self.assertEqual(row['bytesUsed'], 30448560 * 1024)
        self.assertNotEqual(row['bytesUsed'], 80 * 1024**3)
        self.assertEqual(row['usageStatus'], 'measured')
        self.assertEqual(len(calls), 3)

    def test_recipe_artifacts_and_rpc_cache_included_monitoring_excluded(self):
        claims = [claim('glm-artifacts', release='glm'), claim('glm-rpc-cache', release='glm'), claim('glm-rpc-cache-n1', release='glm'),
                  claim('monitoring-metrics', release='monitoring')]
        report, _ = self.collect(claims=claims)
        self.assertEqual([r['claim'] for r in report['caches']], ['glm-artifacts', 'glm-rpc-cache', 'glm-rpc-cache-n1'])

    def test_gpu_pod_identifies_external_claim_without_helm_ownership(self):
        report, _ = self.collect(claims=[claim('shared-weights', release='')], pods=[pod(cache='shared-weights')])
        self.assertEqual(report['caches'][0]['claim'], 'shared-weights')
        self.assertEqual(report['caches'][0]['usageStatus'], 'measured')

    def test_unprovisioned_claim_is_not_zero_disk_usage(self):
        report, _ = self.collect(claims=[claim(bound=False)])
        self.assertIsNone(report['caches'][0]['bytesUsed'])
        self.assertEqual(report['caches'][0]['usageStatus'], 'not-provisioned')

    def test_permission_errors_are_not_reported_as_empty_inventory(self):
        report, calls = self.collect(failures={'persistentvolumeclaims': RuntimeError('Forbidden')})
        self.assertEqual(report['caches'], [])
        self.assertIn('Forbidden', report['warnings'][0])
        self.assertEqual(len(calls), 1)

    def test_missing_pv_access_keeps_claim_and_selected_node(self):
        report, _ = self.collect(failures={'persistentvolumes': RuntimeError('Forbidden')})
        self.assertEqual(len(report['caches']), 1)
        self.assertEqual(report['caches'][0]['nodes'], ['node-a'])
        self.assertIn('Forbidden', report['warnings'][0])

    def test_failed_or_partial_measurements_remain_unavailable(self):
        for usage in [RuntimeError('permission denied'), subprocess.TimeoutExpired('du', 10),
                      '12\t/cache\n4\t/other\n', 'not a size']:
            with self.subTest(usage=usage):
                report, _ = self.collect(pods=[pod()], usage=usage)
                row = report['caches'][0]
                self.assertIsNone(row['bytesUsed'])
                self.assertEqual(row['usageStatus'], 'unavailable')
                self.assertTrue(row['measurementError'])

    def test_finished_or_partial_mounts_are_not_executed(self):
        for mode in ['Succeeded', 'subPath', 'subPathExpr', 'waiting']:
            p = pod()
            if mode == 'Succeeded':
                p['status']['phase'] = mode
            elif mode == 'waiting':
                p['status']['containerStatuses'][0]['state'] = {'waiting': {}}
            else:
                p['spec']['containers'][0]['volumeMounts'][0][mode] = 'subset'
            with self.subTest(mode=mode):
                report, calls = self.collect(pods=[p])
                self.assertEqual(len(calls), 2)
                self.assertIsNone(report['caches'][0]['bytesUsed'])

    def test_shared_claim_is_measured_once(self):
        report, calls = self.collect(pods=[pod(), pod(name='second-server')])
        self.assertEqual(len(calls), 3)
        self.assertEqual(len(report['caches']), 1)


if __name__ == '__main__':
    unittest.main()
