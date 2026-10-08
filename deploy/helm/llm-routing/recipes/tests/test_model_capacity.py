# SPDX-License-Identifier: Apache-2.0
import copy
import json
import pathlib
import sys
import unittest
from unittest.mock import patch

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import model_capacity

GIB = 1024 ** 3
CATALOG = json.loads((ROOT / 'index.json').read_text())


def node(name, **allocatable):
    resources = {'cpu': '20', 'memory': '120Gi', 'nvidia.com/gpu': '1', 'ephemeral-storage': '500Gi'}
    resources.update(allocatable)
    return {'metadata': {'name': name, 'uid': 'uid-' + name, 'labels': {
        'kubernetes.io/arch': 'arm64', 'kubernetes.io/os': 'linux', 'nvidia.com/gpu.product': 'NVIDIA-GB10'}},
        'spec': {}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True'}], 'allocatable': resources}}


def inventory(count=3):
    return {'context': 'capacity-test', 'nodes': [node('node-' + str(i)) for i in range(count)], 'pods': []}


def pod(name, node_name='node-0', namespace='other-team', **resources):
    spec = {'containers': [{'name': 'worker', 'resources': {'requests': resources}}]}
    if node_name:
        spec['nodeName'] = node_name
    return {'metadata': {'name': name, 'namespace': namespace}, 'spec': spec, 'status': {'phase': 'Running'}}


def check(snapshot=None, model='qwen3.8-27b', **kwargs):
    return model_capacity.analyze(snapshot if snapshot is not None else inventory(), CATALOG, model, **kwargs)


class ModelCapacityTests(unittest.TestCase):
    def test_fits_single_node_precisions_and_is_json_safe_without_mutation(self):
        snapshot = inventory(1)
        before = copy.deepcopy(snapshot)
        for model in ('qwen3.8-27b', 'qwen3.8-27b-nvfp4'):
            result = check(snapshot, model)
            self.assertEqual(result['status'], 'fits')
            self.assertEqual(result['chosenNodes'], ['node-0'])
            self.assertEqual(result['nodes'][0]['gpu'], {'allocatable': 1, 'allocated': 0, 'free': 1})
            self.assertEqual(result, json.loads(json.dumps(result)))
            self.assertTrue(any('reservation' in note for note in result['limitations']))
        self.assertEqual(snapshot, before)

    def test_busy_gpu_workloads_in_other_namespaces_are_explained(self):
        snapshot = inventory(1)
        snapshot['pods'] = [pod('existing-model', namespace='another-namespace', **{'nvidia.com/gpu': '1'})]
        result = check(snapshot)
        self.assertEqual(result['status'], 'blocked')
        self.assertEqual(result['chosenNodes'], [])
        self.assertEqual(result['nodes'][0]['gpu']['free'], 0)
        self.assertEqual(result['nodes'][0]['workloads'][0]['namespace'], 'another-namespace')
        self.assertEqual(result['nodes'][0]['workloads'][0]['pod'], 'existing-model')
        self.assertIn('Insufficient GPU', str(result['profiles'][0]['nodeBlockers']))

    def test_unscheduled_gpu_pod_blocks_even_when_another_node_is_free(self):
        snapshot = inventory()
        snapshot['pods'] = [pod('waiting-model', node_name=None, **{'nvidia.com/gpu': '1'})]
        result = check(snapshot)
        self.assertEqual(result['status'], 'blocked')
        self.assertIn('other-team/waiting-model', ' '.join(result['blockers']))
        self.assertEqual(result['pendingWorkloads'][0]['pod'], 'waiting-model')

    def test_pending_workload_reports_release_model_and_scheduling_targets(self):
        snapshot = inventory(1)
        waiting = pod('waiting', node_name=None, **{'nvidia.com/gpu': '1'})
        waiting['metadata']['labels'] = {'app.kubernetes.io/instance': 'fp8'}
        waiting['spec']['affinity'] = {'nodeAffinity': {'requiredDuringSchedulingIgnoredDuringExecution': {
            'nodeSelectorTerms': [{'matchFields': [{'key': 'metadata.name', 'operator': 'In', 'values': ['placeholder-node']}]}]}}}
        waiting['status'].update(phase='Pending', conditions=[{'type': 'PodScheduled', 'status': 'False',
                                  'reason': 'Unschedulable', 'message': 'No nodes match node affinity.'}])
        snapshot['pods'] = [waiting]
        snapshot['endpoints'] = [{'metadata': {'namespace': 'other-team', 'name': 'fp8'},
                                 'spec': {'modelName': 'qwen3.8-27b'}}]
        pending = check(snapshot)['pendingWorkloads'][0]
        self.assertEqual(pending['release'], 'fp8')
        self.assertEqual(pending['model'], 'qwen3.8-27b')
        self.assertEqual(pending['targetNodes'], ['placeholder-node'])
        self.assertEqual(pending['scheduling']['reason'], 'Unschedulable')
        self.assertEqual(pending['scheduling']['message'], 'No nodes match node affinity.')

    def test_completed_pods_are_ignored_and_terminating_pods_count(self):
        snapshot = inventory(1)
        done = pod('done', **{'nvidia.com/gpu': '1'})
        done['status']['phase'] = 'Succeeded'
        snapshot['pods'] = [done]
        self.assertEqual(check(snapshot)['status'], 'fits')
        terminating = pod('stopping', **{'nvidia.com/gpu': '1'})
        terminating['metadata']['deletionTimestamp'] = '2026-10-07T00:00:00Z'
        snapshot['pods'].append(terminating)
        self.assertEqual(check(snapshot)['status'], 'blocked')

    def test_init_sidecar_overhead_and_limits_fallback_are_reserved(self):
        snapshot = inventory(1)
        workload = pod('complex')
        workload['spec'].update(containers=[{'resources': {'limits': {'cpu': '2', 'memory': '3Gi'}}}],
            initContainers=[{'restartPolicy': 'Always', 'resources': {'requests': {'cpu': '1', 'memory': '1Gi'}}},
                            {'resources': {'requests': {'cpu': '8', 'memory': '20Gi'}}}],
            overhead={'cpu': '250m', 'memory': '512Mi'})
        snapshot['pods'] = [workload]
        result = check(snapshot)
        row = result['nodes'][0]
        self.assertEqual(row['cpu']['allocatedMillicores'], 9250)
        self.assertEqual(row['memory']['allocatedBytes'], 21 * GIB + GIB // 2)
        self.assertEqual(result['status'], 'blocked')
        self.assertIn('Insufficient memory', str(result['profiles'][0]['nodeBlockers']))

    def test_unknown_required_capacity_never_means_free_capacity(self):
        for resource in ('nvidia.com/gpu', 'cpu', 'memory'):
            with self.subTest(resource=resource):
                snapshot = inventory(1)
                del snapshot['nodes'][0]['status']['allocatable'][resource]
                result = check(snapshot)
                self.assertEqual(result['status'], 'blocked')
                self.assertIn('unknown', str(result['profiles'][0]['nodeBlockers']))
        snapshot = inventory(1)
        del snapshot['nodes'][0]['status']['allocatable']['ephemeral-storage']
        self.assertEqual(check(snapshot)['status'], 'fits')

    def test_glm_accounts_for_preparation_and_artifact_pod_and_role_order(self):
        snapshot = inventory(2)
        snapshot['nodes'][0]['status']['allocatable']['cpu'] = '4'
        snapshot['nodes'][1]['status']['allocatable']['cpu'] = '9'
        result = check(snapshot, 'glm-5.3')
        self.assertEqual(result['status'], 'fits')
        self.assertEqual(result['chosenNodes'], ['node-1', 'node-0'])
        leader, worker = result['requirements'][0]['perNode']
        self.assertEqual(leader['cpuRequestMillicores'], 8100)
        self.assertEqual(leader['memoryRequestBytes'], 110 * GIB + 128 * 1024 ** 2)
        self.assertEqual(worker['cpuRequestMillicores'], 2000)
        snapshot['nodes'][1]['status']['allocatable']['cpu'] = '8'
        self.assertEqual(check(snapshot, 'glm-5.3')['status'], 'blocked')
        self.assertEqual(check(inventory(1), 'glm-5.3')['status'], 'blocked')

    def test_glm_host_memory_hardware_minimum_is_distinct_from_scheduling_memory(self):
        snapshot = inventory(2)
        snapshot['nodes'][0]['status']['capacity'] = {'memory': '119Gi'}
        result = check(snapshot, 'glm-5.3')
        self.assertEqual(result['status'], 'blocked')
        self.assertIn('Host memory capacity', str(result['profiles'][0]['nodeBlockers']))

    def test_flash_nvme_requires_explicit_capability_and_ephemeral_capacity(self):
        snapshot = inventory(1)
        kwargs = {'model': 'qwen3.8-flash-next', 'profile_id': 'spark-nvfp4-nvme'}
        self.assertEqual(check(snapshot, **kwargs)['status'], 'blocked')
        facts = {'nodes': {'node-0': {'localNvme': True}}}
        self.assertEqual(check(snapshot, capabilities=facts, **kwargs)['status'], 'fits')
        snapshot['nodes'][0]['status']['allocatable']['ephemeral-storage'] = '55Gi'
        result = check(snapshot, capabilities=facts, **kwargs)
        self.assertEqual(result['status'], 'blocked')
        self.assertIn('ephemeral storage', str(result['profiles'][0]['nodeBlockers']))

    def test_flash_tp2_requires_compatible_verified_fabric_and_distinct_addresses(self):
        snapshot = inventory(2)
        kwargs = {'model': 'qwen3.8-flash-next', 'profile_id': 'spark-nvfp4-tp2'}
        facts = {'nodes': {'node-' + str(i): {'fabric': 'pair', 'gbps': 200, 'interface': 'eth1',
                                             'address': '192.0.2.' + str(i + 1)} for i in range(2)}}
        self.assertEqual(check(snapshot, **kwargs)['status'], 'blocked')
        self.assertEqual(check(snapshot, capabilities=facts, **kwargs)['status'], 'fits')
        for field, value in [('fabric', 'different'), ('address', '192.0.2.1'), ('gbps', 100), ('interface', '')]:
            changed = copy.deepcopy(facts)
            changed['nodes']['node-1'][field] = value
            with self.subTest(field=field):
                self.assertEqual(check(snapshot, capabilities=changed, **kwargs)['status'], 'blocked')

    def test_safety_and_hardware_mismatches_are_blocked(self):
        changes = [lambda n: n['spec'].update(unschedulable=True),
                   lambda n: n['spec'].update(taints=[{'key': 'busy', 'effect': 'NoSchedule'}]),
                   lambda n: n['status'].update(conditions=[{'type': 'Ready', 'status': 'False'}]),
                   lambda n: n['metadata']['labels'].update({'kubernetes.io/arch': 'amd64'}),
                   lambda n: n['metadata']['labels'].update({'nvidia.com/gpu.product': 'other-gpu'}),
                   lambda n: n['metadata']['labels'].update({'nvidia.com/gpu.sharing-strategy': 'time-slicing'}),
                   lambda n: n['status']['allocatable'].update({'nvidia.com/mig-1g.5gb': '1'})]
        for change in changes:
            snapshot = inventory(1)
            change(snapshot['nodes'][0])
            self.assertEqual(check(snapshot)['status'], 'blocked')

    def test_mig_strategy_policy_does_not_imply_an_active_partition(self):
        snapshot = inventory(1)
        snapshot['nodes'][0]['metadata']['labels']['nvidia.com/mig.strategy'] = 'mixed'
        self.assertEqual(check(snapshot)['status'], 'fits')
        snapshot['nodes'][0]['status']['allocatable']['nvidia.com/mig-1g.5gb'] = '0'
        self.assertEqual(check(snapshot)['status'], 'fits')
        snapshot['nodes'][0]['status']['allocatable']['nvidia.com/mig-1g.5gb'] = '1'
        result = check(snapshot)
        self.assertEqual(result['status'], 'blocked')
        self.assertIn('MIG is enabled', str(result['profiles'][0]['nodeBlockers']))

    def test_envelopes_unavailable_models_and_invalid_inputs(self):
        self.assertEqual(check(context_length=10000)['status'], 'blocked')
        self.assertEqual(check(concurrency=2)['status'], 'blocked')
        for recipe in CATALOG['recipes']:
            if not recipe['availability']['deployable']:
                result = check(model=recipe['id'])
                self.assertEqual(result['status'], 'unsupported')
                self.assertEqual(result['blockers'], [recipe['availability']['reason']])
        for kwargs in ({'model': 'unknown'}, {'profile_id': 'unknown'}, {'context_length': 0}, {'concurrency': True}):
            with self.assertRaises(ValueError):
                check(**kwargs)
        with self.assertRaises(ValueError):
            check({'nodes': []})


class CapacityPrimitiveTests(unittest.TestCase):
    def test_kubernetes_quantities_are_exact_and_invalid_values_fail(self):
        for value, expected in [('1000m', 1), ('1.5Gi', 1610612736), ('128G', 128000000000)]:
            self.assertEqual(model_capacity.quantity(value), expected)
        for value in ('bad', 'nan', '-1Gi'):
            with self.assertRaises(ValueError):
                model_capacity.quantity(value)

    def test_read_only_inventory_uses_explicit_context_and_all_namespace_workloads(self):
        with patch.object(model_capacity, 'command_json', return_value={'items': []}) as run:
            result = model_capacity.inventory('explicit-context')
            for call in run.call_args_list:
                command = call.args[0]
                self.assertEqual(command[:4], ['kubectl', '--context', 'explicit-context', 'get'])
                if command[4] in ('pods', 'inferenceendpoints.pylon.nvidia.com'):
                    self.assertIn('--all-namespaces', command)
            self.assertEqual(result['context'], 'explicit-context')
        for invalid in ('', None, '-argument'):
            with patch.object(model_capacity, 'command_json') as run, self.assertRaises(ValueError):
                model_capacity.inventory(invalid)
            run.assert_not_called()

    def test_alternate_discrete_hardware_requires_explicit_device_memory(self):
        catalog = copy.deepcopy(CATALOG)
        profile = next(recipe for recipe in catalog['recipes'] if recipe['id'] == 'qwen3.8-27b')['profiles'][0]
        profile['hardware'] = {'os': 'linux', 'architecture': 'amd64', 'gpuCount': 1,
                               'gpuProducts': ['NVIDIA-Test-GPU'], 'memoryMode': 'discrete', 'minDeviceMemoryGiB': 80}
        snapshot = inventory(1)
        snapshot['nodes'][0]['metadata']['labels'].update({
            'kubernetes.io/arch': 'amd64', 'nvidia.com/gpu.product': 'NVIDIA Test GPU'})
        for measured, expected in [(None, 'blocked'), (79, 'blocked'), (80, 'fits'), (True, 'blocked')]:
            with self.subTest(measured=measured):
                report = model_capacity.analyze(snapshot, catalog, 'qwen3.8-27b',
                    capabilities={'nodes': {'node-0': {'cudaTotalMemoryGiB': measured}}})
                self.assertEqual(report['status'], expected)


if __name__ == '__main__':
    unittest.main()
