# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import copy
import importlib.util
import io
import json
from pathlib import Path
import shlex
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('llm_plan_tested', HERE / 'llm.py')
llm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(llm)
recipes, capacity = llm.planning_modules()
import model_storage


def snapshot(count=1):
    return {'context': 'test-cluster', 'pods': [], 'endpoints': [],
            'runtimeClasses': [{'metadata': {'name': 'nvidia'}}],
            'storageClasses': [{'metadata': {'name': 'local-path'}, 'volumeBindingMode': 'WaitForFirstConsumer'}],
            'nodes': [{'metadata': {'name': f'available-{index}', 'uid': f'uid-{index}', 'labels': {
                'kubernetes.io/os': 'linux', 'kubernetes.io/arch': 'arm64', 'nvidia.com/gpu.product': 'NVIDIA-GB10'}},
                'spec': {}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True'}],
                'allocatable': {'cpu': '20', 'memory': '130Gi', 'nvidia.com/gpu': '1', 'ephemeral-storage': '500Gi'}}}
                for index in range(count)]}


class PlanTests(unittest.TestCase):
    def setUp(self):
        self.enterContext(patch.object(model_storage, 'collect', return_value={'caches': [], 'warnings': []}))
        self.output = self.enterContext(contextlib.redirect_stdout(io.StringIO()))
        self.errors = self.enterContext(contextlib.redirect_stderr(io.StringIO()))
        self.enterContext(patch.object(llm, 'gateway', side_effect=AssertionError('Plan must not access gateway')))
        self.enterContext(patch.object(llm, 'access_material', side_effect=AssertionError('Plan must not read credentials')))
        self.enterContext(patch.object(llm.subprocess, 'run', side_effect=AssertionError('Unexpected process')))
        self.enterContext(patch.object(llm.subprocess, 'Popen', side_effect=AssertionError('Unexpected process')))

    def plan(self, data=None, *args, model='qwen3.8-27b', namespace='llm-stack'):
        data = data if data is not None else snapshot()
        before = copy.deepcopy(data)
        with patch.object(recipes, 'inventory', return_value=data) as inventory:
            report = llm.main(['--context', 'test-cluster', '--namespace', namespace, 'plan', '--model', model, *args])
        inventory.assert_called_once_with('test-cluster')
        self.assertEqual(data, before)
        return report

    def test_fit_uses_bundled_defaults_actual_node_and_matching_profile(self):
        report = self.plan(None, '--verbose')
        self.assertEqual(report['status'], 'fits')
        command = shlex.split(report['deployment']['command'])
        self.assertEqual(command[:3], ['helm', 'install', 'qwen-fp8'])
        self.assertNotIn('--values', command)
        self.assertIn('recipe=qwen3.8-27b', command)
        self.assertIn('nodes[0]=available-0', command)
        self.assertIn('profileName=spark-fp8', command)
        self.assertNotIn('gpu-node-1', report['deployment']['command'])
        self.assertIn('No changes made', self.output.getvalue())
        self.assertIn('104.0 GiB RAM', self.output.getvalue())

    def test_current_context_resolved_once_and_every_inventory_call_is_read_only(self):
        data = snapshot()
        resource_keys = {'nodes': 'nodes', 'pods': 'pods', 'inferenceendpoints.pylon.nvidia.com': 'endpoints',
                         'storageclasses': 'storageClasses', 'runtimeclasses': 'runtimeClasses'}
        calls = []
        def inventory_command(command):
            calls.append(command)
            self.assertEqual(command[:3], ['kubectl', '--context', 'captured-cluster'])
            self.assertEqual(command[3], 'get')
            resource = command[4]
            self.assertIn(resource, resource_keys)
            if resource in ('pods', 'inferenceendpoints.pylon.nvidia.com'):
                self.assertIn('--all-namespaces', command)
            return {'items': data[resource_keys[resource]]}
        with patch.object(llm, 'run', return_value='captured-cluster\n') as current, \
                patch.object(recipes, 'command_json', side_effect=inventory_command):
            llm.main(['plan', '--model', 'qwen3.8-27b'])
        current.assert_called_once_with(['kubectl', 'config', 'current-context'])
        self.assertEqual(len(calls), 5)

    def test_blocked_report_shows_owner_and_allocations_without_command(self):
        data = snapshot()
        data['pods'] = [{'metadata': {'name': 'existing-server', 'namespace': 'another-team',
                                    'labels': {'app.kubernetes.io/instance': 'existing-model'}},
                         'spec': {'nodeName': 'available-0', 'containers': [{'resources': {
                             'requests': {'nvidia.com/gpu': 1, 'memory': '104Gi', 'cpu': '4'}}}]},
                         'status': {'phase': 'Running'}}]
        report = self.plan(data, '--verbose')
        self.assertEqual(report['status'], 'blocked')
        self.assertIsNone(report['deployment'])
        self.assertIn('another-team/existing-model', self.output.getvalue())
        self.assertIn('GPU 1 / 1 / 0', self.output.getvalue())
        self.assertIn('Insufficient GPU', self.output.getvalue())
        self.assertIn('Stop keeps downloads', self.output.getvalue())

    def test_existing_unready_endpoint_does_not_suggest_fresh_install(self):
        data = snapshot()
        data['endpoints'] = [{'metadata': {'name': 'qwen-fp8', 'namespace': 'llm-stack', 'annotations': {
            'meta.helm.sh/release-name': 'qwen-fp8'}}, 'spec': {'modelName': 'qwen3.8-27b'},
            'status': {'conditions': [{'type': 'Ready', 'status': 'False'}, {'type': 'Registered', 'status': 'False'}]}}]
        report = self.plan(data, '--verbose')
        self.assertEqual(report['status'], 'existing')
        self.assertIsNone(report['deployment'])
        self.assertIn('Ready=False, Registered=False', self.output.getvalue())
        self.assertIn('helm status qwen-fp8', self.output.getvalue())
        self.assertNotIn('helm install', self.output.getvalue())

    def test_pending_pod_shows_missing_node_affinity_and_scheduler_reason(self):
        data = snapshot()
        data['pods'] = [{'metadata': {'name': 'pending-fp8', 'namespace': 'llm-stack'},
                         'spec': {'containers': [{'resources': {'requests': {'nvidia.com/gpu': 1}}}],
                                  'affinity': {'nodeAffinity': {'requiredDuringSchedulingIgnoredDuringExecution': {
                                      'nodeSelectorTerms': [{'matchFields': [{'key': 'metadata.name',
                                          'operator': 'In', 'values': ['placeholder-node']}]}]}}}},
                         'status': {'phase': 'Pending', 'conditions': [{'type': 'PodScheduled', 'status': 'False',
                             'reason': 'Unschedulable', 'message': 'No nodes match node affinity.'}]}}]
        report = self.plan(data, '--verbose')
        self.assertEqual(report['status'], 'blocked')
        self.assertIn('Requested nodes: placeholder-node', self.output.getvalue())
        self.assertIn('No nodes match node affinity.', self.output.getvalue())

    def test_planned_recipe_reports_unsupported_without_install_command(self):
        report = self.plan(None, '--verbose', model='deepseek-v4-flash')
        self.assertEqual(report['status'], 'unsupported')
        self.assertIsNone(report['deployment'])
        self.assertIn('NO DEPLOYABLE RECIPE', self.output.getvalue())

    def test_endpoint_identity_is_namespace_scoped(self):
        data = snapshot()
        data['endpoints'] = [{'metadata': {'name': 'same-model', 'namespace': 'elsewhere'},
                              'spec': {'modelName': 'qwen3.8-27b'}}]
        self.assertEqual(self.plan(data)['status'], 'fits')

    def test_site_prerequisites_block_commands(self):
        for field in ('runtimeClasses', 'storageClasses'):
            data = snapshot()
            data[field] = []
            with self.subTest(field=field):
                report = self.plan(data, '--verbose')
                self.assertEqual(report['status'], 'blocked')
                self.assertIsNone(report['deployment'])

    def test_flash_command_qualifies_and_overrides_all_target_addresses(self):
        facts = {'nodes': {f'available-{index}': {'fabric': 'pair', 'gbps': 200, 'address': f'192.0.2.{index+1}',
                                                'interface': 'enp1s0'} for index in range(2)}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'capabilities.json'
            path.write_text(json.dumps(facts))
            report = self.plan(snapshot(2), '--verbose', '--profile', 'spark-nvfp4-tp2', '--capabilities', str(path),
                               '--context-length', '32768', model='qwen3.8-flash-next')
        self.assertEqual(report['status'], 'fits')
        command = shlex.split(report['deployment']['command'])
        self.assertIn('recipes/values/qwen3.8-flash-next-tp2.yaml', command)
        self.assertIn('targets[1].node=available-1', command)
        self.assertIn('targets[1].address=192.0.2.2', command)
        self.assertIn('targets[1].interface=enp1s0', command)
        self.assertIn('phase=qualify', command)
        self.assertIn('--wait-for-jobs', command)
        self.assertIn('contextLength=32768', command)
        self.assertIn('qualification only', self.output.getvalue())

    def test_glm_workload_overrides_are_rejected_instead_of_ignored(self):
        with patch.object(recipes, 'inventory') as inventory, self.assertRaisesRegex(ValueError, 'fixed recipe tuning'):
            llm.main(['--context', 'test-cluster', 'plan', '--model', 'glm-5.3', '--context-length', '1024'])
        inventory.assert_not_called()

    def test_glm_uses_two_actual_nodes_and_gguf_chart(self):
        report = self.plan(snapshot(2), model='glm-5.3')
        self.assertEqual(report['status'], 'fits')
        command = shlex.split(report['deployment']['command'])
        self.assertNotIn('--values', command)
        self.assertIn('recipe=glm-5.3', command)
        self.assertTrue(any('pylon-gguf-backend-' in value for value in command))
        self.assertIn('nodes[1]=available-1', command)

    def test_json_is_machine_readable_and_unknown_gpu_is_printable(self):
        report = self.plan(snapshot(), '--json')
        self.assertEqual(json.loads(self.output.getvalue()), report)
        self.output.truncate(0)
        self.output.seek(0)
        data = snapshot()
        del data['nodes'][0]['status']['allocatable']['nvidia.com/gpu']
        self.assertEqual(self.plan(data)['status'], 'blocked')
        self.assertIn('unknown', self.output.getvalue())

    def test_default_is_two_tables_without_deployment_diagnostics(self):
        data = snapshot()
        data['endpoints'] = [{'metadata': {'name': 'old-model', 'namespace': 'llm-stack'},
                             'spec': {'modelName': 'qwen3.8-27b'}}]
        caches = {'caches': [{'claim': 'old-cache', 'namespace': 'another-namespace',
                             'nodes': ['available-0'], 'bytesUsed': 29 * 1024**3,
                             'usageStatus': 'measured'}], 'warnings': []}
        with patch.object(model_storage, 'collect', return_value=caches):
            report = self.plan(data)
        output = self.output.getvalue()
        self.assertEqual(report['status'], 'existing')
        self.assertIsNone(report['deployment'])
        for expected in ('GPU allocation', 'Model files and caches', 'old-cache', '29.0 GiB', 'another-namespace'):
            self.assertIn(expected, output)
        for hidden in ('EXISTING DEPLOYMENT', 'Endpoint ', 'helm install', 'helm status', 'No changes made', 'Rank 0: Insufficient'):
            self.assertNotIn(hidden, output)

    def test_json_includes_cache_bytes_and_unmeasured_status_without_table_text(self):
        storage = {'caches': [
            {'claim': 'mounted', 'namespace': 'old-namespace', 'nodes': ['available-0'],
             'bytesUsed': 123456, 'usageStatus': 'measured'},
            {'claim': 'retained', 'namespace': 'old-namespace', 'nodes': ['available-0'],
             'bytesUsed': None, 'usageStatus': 'not-mounted'}], 'warnings': []}
        with patch.object(model_storage, 'collect', return_value=storage):
            report = self.plan(None, '--json')
        parsed = json.loads(self.output.getvalue())
        self.assertEqual(parsed, report)
        self.assertEqual(parsed['storage']['caches'][0]['bytesUsed'], 123456)
        self.assertIsNone(parsed['storage']['caches'][1]['bytesUsed'])
        self.assertEqual(parsed['storage']['caches'][1]['usageStatus'], 'not-mounted')

    def test_storage_inventory_failure_is_not_presented_as_no_caches(self):
        with patch.object(model_storage, 'collect', return_value={'caches': [], 'warnings': ['Forbidden']}):
            self.plan()
        self.assertIn('Storage inventory incomplete', self.output.getvalue())
        self.assertNotIn('No model cache claims found', self.output.getvalue())

    def test_invalid_inputs_stop_before_inventory(self):
        for args in (['--concurrency', '0'], ['--release', 'bad,name'], ['--storage-class', 'other,inject=true']):
            with self.subTest(args=args), patch.object(recipes, 'inventory') as inventory, self.assertRaises(SystemExit):
                llm.main(['--context', 'test-cluster', 'plan', '--model', 'qwen3.8-27b', *args])
            inventory.assert_not_called()

    def test_generated_command_shell_quotes_context(self):
        context = 'cluster; echo unwanted'
        with patch.object(recipes, 'inventory', return_value=snapshot()):
            report = llm.main(['--context', context, 'plan', '--model', 'qwen3.8-27b'])
        command = shlex.split(report['deployment']['command'])
        self.assertEqual(command[command.index('--kube-context')+1], context)


if __name__ == '__main__':
    unittest.main()
