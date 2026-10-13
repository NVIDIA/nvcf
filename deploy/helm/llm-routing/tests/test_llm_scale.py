# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Starting and stopping an installed model by scaling its deployments."""
import contextlib
import copy
import importlib.util
import io
import json
from pathlib import Path
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('llm_scale_tested', HERE / 'llm.py')
llm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(llm)
capacity = llm.planning_modules()
scale = llm.scaling_modules()

NAMESPACE = 'llm-stack'


def node(name, gpu='1', memory='121Gi', ready='True', unschedulable=False):
    return {'metadata': {'name': name, 'uid': 'uid-' + name, 'labels': {'nvidia.com/gpu.product': 'NVIDIA-GB10'}},
            'spec': {'unschedulable': unschedulable},
            'status': {'conditions': [{'type': 'Ready', 'status': ready}],
                       'allocatable': {'cpu': '20', 'memory': memory, 'nvidia.com/gpu': gpu, 'ephemeral-storage': '500Gi'}}}


def pod(name, on, release, model=None, gpu='1', memory='100Gi'):
    resources = {'requests': {'cpu': '1', 'memory': memory, **({'nvidia.com/gpu': gpu} if gpu != '0' else {})}}
    return {'metadata': {'name': name, 'namespace': NAMESPACE, 'labels': {'app.kubernetes.io/instance': release}},
            'spec': {'nodeName': on, 'containers': [{'name': 'main', 'resources': resources}]},
            'status': {'phase': 'Running'}}


def endpoint(release, model):
    return {'metadata': {'name': release, 'namespace': NAMESPACE, 'annotations': {'meta.helm.sh/release-name': release}},
            'spec': {'modelName': model}, 'status': {'conditions': []}}


def deployment(name, release, on, replicas=0, gpu='1', memory='100Gi', labelled=True, affinity=False):
    template = {'containers': [{'name': 'main', 'resources': {'requests': {'cpu': '1', 'memory': memory,
                                                                           **({'nvidia.com/gpu': gpu} if gpu != '0' else {})}}}]}
    if affinity:
        template['affinity'] = {'nodeAffinity': {'requiredDuringSchedulingIgnoredDuringExecution': {'nodeSelectorTerms': [
            {'matchFields': [{'key': 'metadata.name', 'operator': 'In', 'values': [on]}]}]}}}
    elif on is not None:
        template['nodeSelector'] = {'kubernetes.io/hostname': on}
    return {'metadata': {'name': name, 'namespace': NAMESPACE, **({'labels': {'app.kubernetes.io/instance': release}} if labelled else {})},
            'spec': {'replicas': replicas, 'selector': {'matchLabels': {'app.kubernetes.io/instance': release}},
                     'template': {'spec': template}}}


def transport(release):
    return {'metadata': {'name': 'pylon-' + release, 'namespace': NAMESPACE},
            'spec': {'replicas': 1, 'selector': {'matchLabels': {'app.kubernetes.io/instance': release}},
                     'template': {'spec': {'containers': [{'name': 'pylon', 'resources': {'requests': {'cpu': '100m', 'memory': '128Mi'}}}]}}}}


def snapshot(nodes, pods=(), endpoints=()):
    return {'context': 'test-cluster', 'nodes': nodes, 'pods': list(pods), 'endpoints': list(endpoints),
            'runtimeClasses': [], 'storageClasses': []}


def sglang(release, nodes, replicas=0, **kwargs):
    return [deployment(f'{release}-serve-{rank}', release, name, replicas, **kwargs) for rank, name in enumerate(nodes)]


class ScalingLogicTests(unittest.TestCase):
    def test_a_release_owns_its_deployments_including_unlabelled_ones_and_not_transport_pods(self):
        deployments = [*sglang('deepseek-v4', ['n1', 'n2']), transport('deepseek-v4'),
                       deployment('glm', 'glm', 'n0', labelled=False), deployment('glm-artifacts', 'glm', 'n0', gpu='0', memory='128Mi', labelled=False),
                       transport('glm'), *sglang('other', ['n3'])]
        owned = scale.release_deployments(deployments, 'deepseek-v4', NAMESPACE)
        self.assertEqual([item['deployment'] for item in owned], ['deepseek-v4-serve-0', 'deepseek-v4-serve-1'])
        self.assertEqual([item['node'] for item in owned], ['n1', 'n2'])
        self.assertEqual(owned[0]['gpu'], 1)
        self.assertEqual(owned[0]['memoryBytes'], 100 * scale.GIB)
        glm = scale.release_deployments(deployments, 'glm', NAMESPACE)
        self.assertEqual([item['deployment'] for item in glm], ['glm', 'glm-artifacts'])
        self.assertEqual(scale.release_deployments(deployments, 'deepseek-v4', 'another-namespace'), [])

    def test_node_comes_from_a_selector_or_a_single_node_affinity(self):
        self.assertEqual(scale.deployment_node(deployment('a', 'r', 'n1')), 'n1')
        self.assertEqual(scale.deployment_node(deployment('a', 'r', 'n2', affinity=True)), 'n2')
        self.assertIsNone(scale.deployment_node(deployment('a', 'r', None)))

    def test_a_served_model_resolves_to_its_release_and_ambiguity_is_refused(self):
        endpoints = [endpoint('qwen-fp8', 'qwen3.8-27b'), endpoint('qwen-nvfp4', 'qwen3.8-27b-nvfp4')]
        self.assertEqual(scale.resolve_release(endpoints, NAMESPACE, model='qwen3.8-27b'), 'qwen-fp8')
        self.assertEqual(scale.resolve_release(endpoints, NAMESPACE, model='anything', release='given'), 'given')
        with self.assertRaisesRegex(ValueError, 'No installed release serves nope'):
            scale.resolve_release(endpoints, NAMESPACE, model='nope')
        endpoints.append(endpoint('second', 'qwen3.8-27b'))
        with self.assertRaisesRegex(ValueError, 'several releases'):
            scale.resolve_release(endpoints, NAMESPACE, model='qwen3.8-27b')
        self.assertEqual(scale.release_model(endpoints, NAMESPACE, 'qwen-fp8'), 'qwen3.8-27b')

    def test_start_is_ready_when_every_node_has_room(self):
        report = scale.plan_start(snapshot([node('n1'), node('n2')]), sglang('deepseek-v4', ['n1', 'n2']), 'deepseek-v4', NAMESPACE)
        self.assertEqual(report['status'], 'ready')
        self.assertEqual([item['deployment'] for item in report['toStart']], ['deepseek-v4-serve-0', 'deepseek-v4-serve-1'])
        self.assertEqual(report['blockers'], [])

    def test_a_held_gpu_blocks_the_start_and_names_the_model_that_holds_it(self):
        data = snapshot([node('n1'), node('n2')], pods=[pod('qwen-fp8-serve-0-x', 'n2', 'qwen-fp8')],
                        endpoints=[endpoint('qwen-fp8', 'qwen3.8-27b')])
        report = scale.plan_start(data, sglang('deepseek-v4', ['n1', 'n2']) + sglang('qwen-fp8', ['n2'], replicas=1), 'deepseek-v4', NAMESPACE)
        self.assertEqual(report['status'], 'blocked')
        self.assertEqual(len(report['blockers']), 1)
        self.assertIn('Node n2', report['blockers'][0])
        self.assertIn('GPU: needs 1, 0 free', report['blockers'][0])
        self.assertIn('In use by qwen3.8-27b', report['blockers'][0])
        self.assertEqual([(entry['release'], entry['model']) for entry in report['holders']], [('qwen-fp8', 'qwen3.8-27b')])
        self.assertEqual(scale.blocker_stop_hint(report), ['llm.py stop --release qwen-fp8'])

    def test_the_stop_hint_uses_the_release_and_falls_back_to_the_served_model(self):
        data = snapshot([node('n1')], pods=[pod('held', 'n1', 'qwen-fp8')])
        report = scale.plan_start(data, sglang('deepseek-v4', ['n1']) + sglang('qwen-fp8', ['n1'], replicas=1), 'deepseek-v4', NAMESPACE)
        report['holders'][0]['model'] = 'qwen3.8-27b'
        self.assertEqual(scale.blocker_stop_hint(report), ['llm.py stop --release qwen-fp8'])
        report['holders'][0]['release'] = None
        self.assertEqual(scale.blocker_stop_hint(report), ['llm.py stop --model qwen3.8-27b'])

    def test_system_and_monitoring_pods_are_never_offered_as_holders(self):
        crowd = [pod('qwen-fp8-serve-0-x', 'n1', 'qwen-fp8'), pod('operator-x', 'n1', 'gpu-operator', gpu='0', memory='5Gi'),
                 pod('ui-x', 'n1', 'demo-ui', gpu='0', memory='1Gi'), pod('pylon-qwen-fp8-x', 'n1', 'qwen-fp8', gpu='0', memory='1Gi')]
        data = snapshot([node('n1')], pods=crowd)
        report = scale.plan_start(data, sglang('deepseek-v4', ['n1']) + sglang('qwen-fp8', ['n1'], replicas=1) + [transport('qwen-fp8')],
                                  'deepseek-v4', NAMESPACE)
        self.assertEqual([entry['release'] for entry in report['holders']], ['qwen-fp8'])
        self.assertEqual(scale.blocker_stop_hint(report), ['llm.py stop --release qwen-fp8'])
        self.assertNotIn('gpu-operator', report['blockers'][0])
        self.assertNotIn('demo-ui', report['blockers'][0])

    def test_memory_alone_can_block_a_start(self):
        data = snapshot([node('n1', memory='121Gi')], pods=[pod('tiny-gpu-free', 'n1', 'other', gpu='0', memory='60Gi')])
        report = scale.plan_start(data, sglang('big', ['n1'], memory='100Gi'), 'big', NAMESPACE)
        self.assertEqual(report['status'], 'blocked')
        self.assertIn('memory: needs 100.0 GiB, 61.0 GiB free', report['blockers'][0])
        self.assertNotIn('GPU:', report['blockers'][0])

    def test_a_multi_rank_start_is_blocked_if_any_rank_cannot_fit(self):
        data = snapshot([node('n1'), node('n2')], pods=[pod('held', 'n2', 'other')])
        report = scale.plan_start(data, sglang('tp2', ['n1', 'n2']), 'tp2', NAMESPACE)
        self.assertEqual(report['status'], 'blocked')
        self.assertEqual(len(report['toStart']), 2)

    def test_demand_on_one_node_is_added_up_across_deployments(self):
        deployments = [deployment('glm', 'glm', 'n0', gpu='1', memory='70Gi'), deployment('glm-artifacts', 'glm', 'n0', gpu='0', memory='128Mi')]
        self.assertEqual(scale.plan_start(snapshot([node('n0')]), deployments, 'glm', NAMESPACE)['status'], 'ready')
        deployments.append(deployment('glm-second-gpu', 'glm', 'n0', gpu='1', memory='1Gi'))
        report = scale.plan_start(snapshot([node('n0')]), deployments, 'glm', NAMESPACE)
        self.assertEqual(report['status'], 'blocked')
        self.assertIn('GPU: needs 2, 1 free', report['blockers'][0])

    def test_running_and_partly_running_releases_are_handled(self):
        data = snapshot([node('n1'), node('n2')])
        running = sglang('tp2', ['n1', 'n2'], replicas=1)
        self.assertEqual(scale.plan_start(data, running, 'tp2', NAMESPACE)['status'], 'running')
        partly = [running[0], copy.deepcopy(running[1])]
        partly[1]['spec']['replicas'] = 0
        data = snapshot([node('n1'), node('n2')], pods=[pod('tp2-serve-0-x', 'n1', 'tp2')])
        report = scale.plan_start(data, partly, 'tp2', NAMESPACE)
        self.assertEqual(report['status'], 'ready')
        self.assertEqual([item['deployment'] for item in report['toStart']], ['tp2-serve-1'])
        self.assertEqual(report['running'], ['tp2-serve-0'])

    def test_unhealthy_nodes_and_unpinned_deployments_block_a_start(self):
        for bad, text in ((node('n1', ready='False'), 'not Ready'), (node('n1', unschedulable=True), 'cordoned')):
            with self.subTest(text=text):
                report = scale.plan_start(snapshot([bad]), sglang('r', ['n1']), 'r', NAMESPACE)
                self.assertEqual(report['status'], 'blocked')
                self.assertTrue(any(text in blocker for blocker in report['blockers']))
        report = scale.plan_start(snapshot([node('n1')]), [deployment('floating', 'r', None)], 'r', NAMESPACE)
        self.assertEqual(report['status'], 'blocked')
        self.assertIn('not pinned to one node', report['blockers'][0])
        report = scale.plan_start(snapshot([]), sglang('r', ['gone']), 'r', NAMESPACE)
        self.assertIn('not in the cluster', report['blockers'][0])

    def test_unknown_releases_are_an_error_and_stop_lists_only_running_deployments(self):
        with self.assertRaisesRegex(ValueError, 'no model deployments'):
            scale.plan_start(snapshot([node('n1')]), [], 'missing', NAMESPACE)
        deployments = [deployment('a', 'r', 'n1', replicas=1), deployment('b', 'r', 'n2', replicas=0)]
        report = scale.plan_stop(deployments, 'r', NAMESPACE)
        self.assertEqual((report['status'], [item['deployment'] for item in report['toStop']], report['stopped']), ('running', ['a'], ['b']))
        self.assertEqual(scale.plan_stop([deployment('a', 'r', 'n1')], 'r', NAMESPACE)['status'], 'stopped')


class ScalingCommandTests(unittest.TestCase):
    def setUp(self):
        self.output = self.enterContext(contextlib.redirect_stdout(io.StringIO()))
        self.errors = self.enterContext(contextlib.redirect_stderr(io.StringIO()))
        self.commands = []
        self.deployments = []
        self.cluster = snapshot([node('n1'), node('n2')])
        self.enterContext(patch.object(llm, 'selected_context', return_value='test-cluster'))
        self.enterContext(patch.object(capacity, 'inventory', side_effect=lambda context: copy.deepcopy(self.cluster)))
        self.enterContext(patch.object(llm, 'run', side_effect=self.fake_run))
        self.enterContext(patch.object(llm, 'gateway', side_effect=AssertionError('Scaling must not use the gateway')))

    def fake_run(self, command, timeout=60):
        command = [str(item) for item in command]
        if command[-3:] == ['get', 'deployments', '-o'] or ('get' in command and 'deployments' in command):
            return json.dumps({'items': copy.deepcopy(self.deployments)})
        self.commands.append(command)
        return ''

    def scaled(self):
        return [item for item in self.commands if 'scale' in item]

    def call(self, *args):
        return llm.main(['--context', 'test-cluster', *args])

    def install(self, release='deepseek-v4', model='deepseek-v4-flash', nodes=('n1', 'n2'), replicas=0):
        self.deployments.extend(sglang(release, list(nodes), replicas))
        self.cluster['endpoints'].append(endpoint(release, model))

    def test_dry_run_checks_and_reports_without_scaling(self):
        self.install()
        report = self.call('start', '--model', 'deepseek-v4-flash', '--dry-run')
        self.assertEqual(report['status'], 'ready')
        self.assertEqual(self.scaled(), [])
        self.assertIn('Would start 2 deployment(s) of deepseek-v4', self.output.getvalue())

    def test_start_scales_every_stopped_deployment_to_one_after_the_check(self):
        self.install()
        report = self.call('start', '--model', 'deepseek-v4-flash')
        self.assertEqual(report['started'], ['deepseek-v4-serve-0', 'deepseek-v4-serve-1'])
        self.assertEqual([item[-2:] for item in self.scaled()],
                         [['deployment/deepseek-v4-serve-0', '--replicas=1'], ['deployment/deepseek-v4-serve-1', '--replicas=1']])
        self.assertTrue(all(item[:3] == ['kubectl', '--context', 'test-cluster'] for item in self.scaled()))
        self.assertIn('Started deepseek-v4', self.output.getvalue())

    def test_wait_asks_kubectl_to_wait_for_the_endpoint(self):
        self.install()
        self.call('start', '--model', 'deepseek-v4-flash', '--wait', '--timeout', '90m')
        waits = [item for item in self.commands if 'wait' in item]
        self.assertEqual(len(waits), 1)
        self.assertIn('inferenceendpoint/deepseek-v4', waits[0])
        self.assertIn('--timeout=90m', waits[0])

    def test_a_blocked_start_changes_nothing_and_says_who_to_stop(self):
        self.install()
        self.deployments.extend(sglang('qwen-fp8', ['n2'], replicas=1))
        self.cluster['pods'].append(pod('qwen-fp8-serve-0-x', 'n2', 'qwen-fp8'))
        self.cluster['endpoints'].append(endpoint('qwen-fp8', 'qwen3.8-27b'))
        report = self.call('start', '--model', 'deepseek-v4-flash')
        self.assertEqual(report['status'], 'blocked')
        self.assertEqual(self.scaled(), [])
        text = self.output.getvalue()
        self.assertIn('Cannot start deepseek-v4', text)
        self.assertIn('Node n2', text)
        self.assertIn('llm.py stop --release qwen-fp8', text)
        self.assertIn('--stop-blockers', text)

    def test_stop_blockers_stops_the_holder_first_and_then_starts(self):
        self.install()
        self.deployments.extend(sglang('qwen-fp8', ['n2'], replicas=1))
        self.cluster['pods'].append(pod('qwen-fp8-serve-0-x', 'n2', 'qwen-fp8'))
        real_inventory = capacity.inventory

        def freed(context):
            data = copy.deepcopy(self.cluster)
            if any('--replicas=0' in item for item in self.scaled()):
                data['pods'] = [item for item in data['pods'] if item['metadata']['name'] != 'qwen-fp8-serve-0-x']
            return data
        with patch.object(capacity, 'inventory', side_effect=freed):
            report = self.call('start', '--release', 'deepseek-v4', '--stop-blockers')
        self.assertEqual(report['status'], 'ready')
        self.assertEqual(report['stoppedBlockers'], ['qwen-fp8'])
        order = [item[-2:] for item in self.scaled()]
        self.assertEqual(order[0], ['deployment/qwen-fp8-serve-0', '--replicas=0'])
        self.assertEqual({item[1] for item in order[1:]}, {'--replicas=1'})
        self.assertEqual(len(order), 3)

    def test_stop_blockers_never_stops_anything_in_a_dry_run(self):
        self.install()
        self.deployments.extend(sglang('qwen-fp8', ['n2'], replicas=1))
        self.cluster['pods'].append(pod('qwen-fp8-serve-0-x', 'n2', 'qwen-fp8'))
        report = self.call('start', '--release', 'deepseek-v4', '--stop-blockers', '--dry-run')
        self.assertEqual(report['status'], 'blocked')
        self.assertEqual(self.scaled(), [])

    def test_start_of_a_running_model_is_a_no_op(self):
        self.install(replicas=1)
        report = self.call('start', '--model', 'deepseek-v4-flash')
        self.assertEqual(report['status'], 'running')
        self.assertEqual(self.scaled(), [])
        self.assertIn('already running', self.output.getvalue())

    def test_stop_scales_the_running_deployments_to_zero_and_waits_for_the_pods(self):
        self.install(replicas=1)
        report = self.call('stop', '--model', 'deepseek-v4-flash')
        self.assertEqual(report['stoppedNow'], ['deepseek-v4-serve-0', 'deepseek-v4-serve-1'])
        self.assertEqual({item[-1] for item in self.scaled()}, {'--replicas=0'})
        waits = [item for item in self.commands if 'wait' in item]
        self.assertEqual(len(waits), 1)
        self.assertIn('--for=delete', waits[0])
        self.assertIn('app.kubernetes.io/instance=deepseek-v4', waits[0])
        self.assertIn('Deployed, unhealthy', self.output.getvalue())

    def test_stop_dry_run_and_already_stopped_do_not_scale(self):
        self.install(replicas=1)
        self.call('stop', '--model', 'deepseek-v4-flash', '--dry-run')
        self.assertEqual(self.scaled(), [])
        self.deployments.clear()
        self.deployments.extend(sglang('deepseek-v4', ['n1', 'n2'], replicas=0))
        report = self.call('stop', '--model', 'deepseek-v4-flash')
        self.assertEqual(report['status'], 'stopped')
        self.assertEqual(self.scaled(), [])

    def test_a_model_without_an_endpoint_is_found_through_helm(self):
        self.deployments.extend(sglang('qwen-nvfp4', ['n1'], replicas=0))
        found = [{'release': 'qwen-nvfp4', 'status': 'deployed', 'suspended': True, 'nodes': ['n1']}]
        with patch.object(llm, 'model_releases', return_value=(found, {'qwen-nvfp4'})) as lookup:
            report = self.call('start', '--model', 'qwen3.8-27b-nvfp4', '--dry-run')
        self.assertEqual(report['release'], 'qwen-nvfp4')
        lookup.assert_called_once()

    def test_an_unknown_model_is_a_clear_error(self):
        with self.assertRaisesRegex(ValueError, 'No installed release serves'):
            self.call('start', '--model', 'not-a-model')
        self.assertEqual(self.scaled(), [])

    def test_json_output_is_one_complete_report(self):
        self.install()
        self.call('start', '--model', 'deepseek-v4-flash', '--dry-run', '--json')
        report = json.loads(self.output.getvalue())
        self.assertEqual((report['action'], report['status'], report['dryRun']), ('start', 'ready', True))

    def test_arguments_are_validated_before_any_cluster_access(self):
        for args in (['start'], ['start', '--model', 'a', '--release', 'b'], ['start', '--model', 'a', '--timeout', 'soon'],
                     ['stop', '--release', 'llm-stack'], ['start', '--model', '-bad']):
            with self.subTest(args=args), self.assertRaises(SystemExit):
                self.call(*args)
        self.assertEqual(self.commands, [])


if __name__ == '__main__':
    unittest.main()
