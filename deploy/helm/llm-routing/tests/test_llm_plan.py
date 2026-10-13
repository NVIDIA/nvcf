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


def catalog_chart(recipe_id):
    catalog = json.loads((HERE / 'recipes/index.json').read_text())
    recipe = next(item for item in catalog['recipes'] if item['id'] == recipe_id)
    return recipe['profiles'][0]['deployment']['chart']
spec = importlib.util.spec_from_file_location('llm_plan_tested', HERE / 'llm.py')
llm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(llm)
capacity = llm.planning_modules()
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


def retained_claim(name='qwen-fp8-cache-0', release='qwen-fp8', node='available-1'):
    return {'namespace': 'llm-stack', 'claim': name, 'release': release,
            'releaseNamespace': 'llm-stack', 'storageClass': 'local-path', 'nodes': [node],
            'volume': 'retained-pv', 'phase': 'Bound', 'accessModes': ['ReadWriteOnce'],
            'volumeMode': 'Filesystem', 'capacityBytes': 80 * 1024**3}


class PlanTests(unittest.TestCase):
    def setUp(self):
        self.storage = self.enterContext(patch.object(model_storage, 'collect', return_value={'caches': [], 'claims': [], 'warnings': []}))
        self.original_model_releases = llm.model_releases
        self.releases = self.enterContext(patch.object(llm, 'model_releases', return_value=([], set())))
        self.output = self.enterContext(contextlib.redirect_stdout(io.StringIO()))
        self.errors = self.enterContext(contextlib.redirect_stderr(io.StringIO()))
        self.enterContext(patch.object(llm, 'gateway', side_effect=AssertionError('Plan must not access gateway')))
        self.enterContext(patch.object(llm, 'access_material', side_effect=AssertionError('Plan must not read credentials')))
        self.enterContext(patch.object(llm.subprocess, 'run', side_effect=AssertionError('Unexpected process')))
        self.enterContext(patch.object(llm.subprocess, 'Popen', side_effect=AssertionError('Unexpected process')))

    def plan(self, data=None, *args, model='qwen3.8-27b', namespace='llm-stack'):
        data = data if data is not None else snapshot()
        before = copy.deepcopy(data)
        with patch.object(capacity, 'inventory', return_value=data) as inventory:
            report = llm.main(['--context', 'test-cluster', '--namespace', namespace, 'plan', '--model', model, *args])
        inventory.assert_called_once_with('test-cluster')
        self.assertEqual(data, before)
        return report

    def test_fit_uses_bundled_defaults_actual_node_and_matching_profile(self):
        report = self.plan(None, '--verbose')
        self.assertEqual(report['status'], 'fits')
        command = shlex.split(report['deployment']['command'])
        self.assertEqual(command[:3], ['helm', 'install', 'qwen-fp8'])
        chart = catalog_chart('qwen3.8-27b')
        self.assertEqual(command[3:6], [chart['repository'], '--version', chart['version']])
        self.assertNotIn('--values', command)
        self.assertIn('recipe=qwen3.8-27b', command)
        self.assertIn('nodes[0]=available-0', command)
        self.assertIn('profileName=spark-fp8', command)
        self.assertNotIn('gpu-node-1', report['deployment']['command'])
        self.assertIn('No changes made', self.output.getvalue())
        self.assertIn('104.0 GiB RAM', self.output.getvalue())

    def test_suspended_existing_model_suppresses_duplicate_even_with_release_override(self):
        entry = {'release': 'original-model', 'status': 'deployed', 'suspended': True, 'nodes': ['available-0'],
                 'inspectCommand': 'helm get values original-model', 'releaseCommand': 'helm status original-model'}
        self.releases.return_value = ([entry], {'original-model'})
        report = self.plan(snapshot(2), '--release', 'duplicate', '--verbose')
        self.assertEqual(report['status'], 'existing')
        self.assertIsNone(report['deployment'])
        self.assertIn('suspended=True', self.output.getvalue())
        self.assertIn('resume its existing release', self.output.getvalue())

    def test_station_discovers_memory_and_selects_free_node_without_capabilities(self):
        data = snapshot(2)
        for node in data['nodes']:
            node['metadata']['labels'].update({
                'nvidia.com/gpu.product': 'NVIDIA-GB300', 'nvidia.com/gpu.memory': '256703'})
        data['pods'] = [{'metadata': {'name': 'busy', 'namespace': 'other'}, 'spec': {
            'nodeName': 'available-0', 'containers': [{'resources': {'requests': {'nvidia.com/gpu': '1'}}}]}}]
        report = self.plan(data, '--verbose')
        self.assertEqual(report['status'], 'fits')
        self.assertEqual(report['chosenProfile'], 'station-fp8')
        self.assertEqual(report['chosenNodes'], ['available-1'])
        command = shlex.split(report['deployment']['command'])
        self.assertIn('profileName=station-fp8', command)
        self.assertIn('nodes[0]=available-1', command)

    def test_retained_owned_cache_selects_original_node_and_blocks_busy_or_unknown_placement(self):
        claim = retained_claim()
        self.storage.return_value['claims'] = [claim]
        report = self.plan(snapshot(2))
        self.assertEqual(report['chosenNodes'], ['available-1'])
        self.assertIn('nodes[0]=available-1', report['deployment']['command'])
        data = snapshot(2)
        data['pods'] = [{'metadata': {'name': 'busy', 'namespace': 'other'}, 'spec': {
            'nodeName': 'available-1', 'containers': [{'resources': {'requests': {'nvidia.com/gpu': 1}}}]}}]
        self.assertEqual(self.plan(data)['status'], 'blocked')
        for change in ({'nodes': []}, {'nodes': ['available-0', 'available-1']}, {'release': 'foreign'},
                       {'releaseNamespace': None}, {'storageClass': 'other'}, {'phase': 'Pending'},
                       {'phase': 'Lost'}, {'deleting': True}, {'volume': None}, {'accessModes': ['ReadWriteMany']},
                       {'volumeMode': 'Block'}, {'capacityBytes': 10 * 1024**3}, {'capacityBytes': 0}):
            with self.subTest(change=change):
                self.storage.return_value['claims'] = [dict(claim, **change)]
                report = self.plan(snapshot(2))
                self.assertEqual(report['status'], 'blocked')
                self.assertIsNone(report['deployment'])

    def test_helm_inventory_reads_model_values_and_detects_suspended_alias(self):
        self.storage.return_value.update(
            claims=[retained_claim('alias-cache-0', 'alias', 'available-0')],
            caches=[{'namespace': 'llm-stack', 'claim': 'alias-cache-0', 'nodes': ['available-0'],
                     'bytesUsed': None, 'usageStatus': 'not-mounted'}])
        def run(command):
            self.assertEqual(command[:5], ['helm', '--kube-context', 'test-cluster', '--namespace', 'llm-stack'])
            if command[5] == 'list':
                self.assertNotIn('--all', command)
                return json.dumps([{'name': 'alias', 'chart': 'pylon-sglang-recipe-0.2.0', 'status': 'deployed'},
                                   {'name': 'llm-stack', 'chart': 'llm-shared-stack-0.1.0', 'status': 'deployed'}])
            self.assertEqual(command[5:], ['get', 'values', 'alias', '--all', '--output', 'json'])
            return json.dumps({'recipe': 'qwen3.8-27b', 'profileName': 'spark-fp8',
                               'suspended': True, 'nodes': ['available-0'], 'unrelated': 'do-not-export'})
        with patch.object(llm, 'model_releases', self.original_model_releases), patch.object(llm, 'run', side_effect=run) as calls:
            report = self.plan(snapshot(2), '--release', 'duplicate')
        self.assertEqual(report['status'], 'existing')
        self.assertEqual(report['existingDeployments'][0]['release'], 'alias')
        self.assertEqual(calls.call_count, 2)
        self.assertIn('EXISTING DEPLOYMENT: alias', self.output.getvalue())
        cache = report['storage']['caches'][0]
        self.assertEqual((cache['release'], cache['recipe'], cache['profile']),
                         ('alias', 'qwen3.8-27b', 'spark-fp8'))
        self.assertEqual(cache['usageStatus'], 'not-mounted')
        self.assertIsNone(cache['bytesUsed'])
        self.assertNotIn('do-not-export', json.dumps(report))

    def test_helm_release_name_used_by_another_recipe_does_not_generate_install(self):
        responses = [json.dumps([{'name': 'qwen-fp8', 'chart': 'pylon-sglang-recipe-0.2.0', 'status': 'deployed'}]),
                     json.dumps({'recipe': 'qwen3.8-27b-nvfp4'})]
        with patch.object(llm, 'model_releases', self.original_model_releases), patch.object(llm, 'run', side_effect=responses):
            report = self.plan()
        self.assertEqual(report['status'], 'blocked')
        self.assertIsNone(report['deployment'])

    def test_retained_extra_rank_cannot_be_installed_as_a_single_node_profile(self):
        self.storage.return_value['claims'] = [retained_claim('qwen-fp8-cache-1')]
        report = self.plan(snapshot(2))
        self.assertEqual(report['status'], 'blocked')
        self.assertIn('Retained cache ranks', str(report['blockers']))
        self.assertIsNone(report['deployment'])

    def test_helm_inventory_includes_suspended_releases_after_the_first_page(self):
        pages = [[{'name': 'other-' + str(i), 'chart': 'unrelated-1.0.0'} for i in range(256)],
                 [{'name': 'alias', 'chart': 'pylon-sglang-recipe-0.2.0', 'status': 'deployed'}]]
        offsets = []
        def run(command):
            if command[5] == 'list':
                offsets.append(command[command.index('--offset') + 1])
                self.assertEqual(command[command.index('--max') + 1], '256')
                return json.dumps(pages.pop(0))
            return json.dumps({'recipe': 'qwen3.8-27b', 'suspended': True})
        with patch.object(llm, 'model_releases', self.original_model_releases), patch.object(llm, 'run', side_effect=run):
            report = self.plan()
        self.assertEqual(offsets, ['0', '256'])
        self.assertEqual(report['status'], 'existing')
        self.assertIsNone(report['deployment'])
        self.assertEqual(report['existingDeployments'][0]['release'], 'alias')

    def test_duplicate_or_noncanonical_cache_ranks_block_install(self):
        claim = retained_claim()
        self.storage.return_value['claims'] = [claim, dict(claim, nodes=['available-0'])]
        report = self.plan(snapshot(2))
        self.assertEqual(report['status'], 'blocked')
        self.assertIn('Multiple retained cache claims', str(report['blockers']))
        catalog = json.loads((HERE / 'recipes/index.json').read_text())
        model = next(item for item in catalog['recipes'] if item['id'] == 'glm-5.3')
        caches = {'claims': [retained_claim('glm-artifacts', 'glm', 'available-0'),
                             retained_claim('glm-rpc-cache-n0', 'glm', 'available-1')]}
        nodes, _, blockers = llm.retained_cache_nodes(caches, 'llm-stack', 'glm', model, 'local-path')
        self.assertEqual(nodes, {0: 'available-0'})
        self.assertIn('explicit inspection', str(blockers))

    def test_shared_release_name_is_reserved(self):
        with self.assertRaises(SystemExit):
            self.plan(None, '--release', 'llm-stack')
        self.assertIn('reserved for shared infrastructure', self.errors.getvalue())

    def test_inventory_errors_and_taken_release_names_suppress_installs(self):
        self.releases.return_value = ([], {'qwen-fp8'})
        report = self.plan()
        self.assertEqual(report['status'], 'blocked')
        self.assertIn('already in use', str(report['blockers']))
        self.releases.side_effect = RuntimeError('Forbidden')
        report = self.plan()
        self.assertEqual(report['status'], 'blocked')
        self.assertIn('Helm release inventory failed', str(report['blockers']))
        self.releases.side_effect = None
        self.releases.return_value = ([], set())
        self.storage.return_value['warnings'] = ['PVC inventory forbidden']
        self.assertEqual(self.plan()['status'], 'blocked')

    def test_default_release_comes_from_catalog_deployment_metadata(self):
        catalog = json.loads((HERE/'recipes/index.json').read_text())
        model = next(item for item in catalog['recipes'] if item['id'] == 'qwen3.8-27b')
        model['profiles'][0]['deployment']['releaseName'] = 'catalog-name'
        original = Path.read_text
        with patch.object(Path, 'read_text', lambda path, *a, **kw: json.dumps(catalog) if path == HERE/'recipes/index.json' else original(path, *a, **kw)):
            report = self.plan()
            override = self.plan(None, '--release', 'requested-release')
        self.assertEqual(report['deployment']['release'], 'catalog-name')
        self.assertEqual(override['deployment']['release'], 'requested-release')

    def test_published_chart_is_pinned_to_the_catalog_version(self):
        chart = catalog_chart('qwen3.8-27b')
        self.assertEqual(chart['publication'], 'oci')
        command = shlex.split(self.plan()['deployment']['command'])
        self.assertEqual(command[3:6], [chart['repository'], '--version', chart['version']])

    def test_unpublished_chart_is_resolved_from_catalog_when_run_outside_checkout(self):
        catalog = json.loads((HERE/'recipes/index.json').read_text())
        for recipe in catalog['recipes']:
            for profile in recipe['profiles']:
                profile['deployment']['chart'].update(repository=None, publication='local')
        original = Path.read_text
        with patch.object(Path, 'read_text', lambda path, *a, **kw: json.dumps(catalog) if path == HERE/'recipes/index.json' else original(path, *a, **kw)):
            with tempfile.TemporaryDirectory() as directory, contextlib.chdir(directory):
                report = self.plan()
        command = shlex.split(report['deployment']['command'])
        chart = Path(command[3])
        self.assertEqual(chart, HERE / 'recipes/charts/sglang')
        self.assertTrue((chart / 'Chart.yaml').is_file())
        self.assertNotIn('--version', command)

    def test_chart_source_preserves_exact_helm_reference_as_one_argument(self):
        for chart in ('./packages/model chart.tgz', 'repository/model-chart',
                      'oci://registry.example.com/charts/model', 'https://example.com/model.tgz'):
            with self.subTest(chart=chart):
                report = self.plan(None, '--chart-source', chart)
                command = shlex.split(report['deployment']['command'])
                self.assertEqual(command[3], chart)
                self.assertNotIn('--version', command)
                self.assertIn('profileName=spark-fp8', command)
                self.assertIn('nodes[0]=available-0', command)

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
                patch.object(capacity, 'command_json', side_effect=inventory_command):
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

    def test_planned_and_unavailable_recipes_never_suggest_installation(self):
        catalog = json.loads((HERE / 'recipes/index.json').read_text())
        recipe = copy.deepcopy(catalog['recipes'][0])
        recipe.update(id='future-model', profiles=[], servedModelId=None, runtime=None)
        for status in ('planned', 'unavailable'):
            recipe['availability'] = {'status': status, 'deployable': False, 'reason': 'Test fixture'}
            with self.subTest(status=status), patch.object(Path, 'read_text', return_value=json.dumps({'recipes': [recipe]})):
                self.output.truncate(0)
                self.output.seek(0)
                report = self.plan(None, '--verbose', model=recipe['id'])
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

    def test_flash_tp2_command_deploys_automatically_with_verified_fabric(self):
        facts = {'nodes': {f'available-{index}': {'fabric': 'pair', 'gbps': 200, 'address': f'192.0.2.{index+1}',
                                                'interface': 'enp1s0'} for index in range(2)}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'capabilities.json'
            path.write_text(json.dumps(facts))
            report = self.plan(snapshot(2), '--verbose', '--profile', 'spark-nvfp4-tp2', '--capabilities', str(path),
                               '--context-length', '32768', model='qwen3.8-flash-next')
        self.assertEqual(report['status'], 'fits')
        command = shlex.split(report['deployment']['command'])
        self.assertEqual(command[:3], ['helm', 'install', 'qwen-flash'])
        self.assertNotIn('--values', command)
        self.assertIn('recipe=qwen3.8-flash-next', command)
        self.assertIn('profileName=spark-nvfp4-tp2', command)
        self.assertIn('nodes[0]=available-0', command)
        self.assertIn('nodes[1]=available-1', command)
        option = command[command.index('--set-json') + 1]
        name, value = option.split('=', 1)
        self.assertEqual(name, 'nodeCapabilities')
        self.assertEqual(json.loads(value), {node: {'fabric': entry['fabric'], 'address': entry['address'],
            'interface': entry['interface'], 'linkGbps': entry['gbps']} for node, entry in facts['nodes'].items()})
        self.assertNotIn('phase=qualify', command)
        self.assertNotIn('--wait-for-jobs', command)
        self.assertIn('contextLength=32768', command)
        self.assertNotIn('qualification only', self.output.getvalue())

    def test_flash_nvme_command_includes_only_selected_verified_capability(self):
        facts = {'available-0': {'localNvme': True, 'unrelated': 'ignore'},
                 'unused-node': {'localNvme': True}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'capabilities.json'
            path.write_text(json.dumps(facts))
            report = self.plan(None, '--verbose', '--profile', 'spark-nvfp4-nvme', '--capabilities', str(path),
                               model='qwen3.8-flash-next')
        self.assertEqual(report['status'], 'fits')
        command = shlex.split(report['deployment']['command'])
        self.assertNotIn('--values', command)
        self.assertIn('nodes[0]=available-0', command)
        option = command[command.index('--set-json') + 1]
        self.assertEqual(json.loads(option.split('=', 1)[1]), {'available-0': {'localNvme': True}})

    def test_flash_requires_verified_nvme_or_fabric_before_suggesting_installation(self):
        for profile in ('spark-nvfp4-nvme', 'spark-nvfp4-tp2'):
            with self.subTest(profile=profile):
                report = self.plan(snapshot(2), '--profile', profile, model='qwen3.8-flash-next')
                self.assertEqual(report['status'], 'blocked')
                self.assertIsNone(report['deployment'])

    def test_glm_workload_overrides_are_rejected_instead_of_ignored(self):
        with patch.object(capacity, 'inventory') as inventory, self.assertRaisesRegex(ValueError, 'fixed recipe tuning'):
            llm.main(['--context', 'test-cluster', 'plan', '--model', 'glm-5.3', '--context-length', '1024'])
        inventory.assert_not_called()

    def test_glm_uses_two_actual_nodes_and_gguf_chart(self):
        report = self.plan(snapshot(2), model='glm-5.3')
        self.assertEqual(report['status'], 'fits')
        command = shlex.split(report['deployment']['command'])
        self.assertNotIn('--values', command)
        self.assertIn('recipe=glm-5.3', command)
        chart = catalog_chart('glm-5.3')
        self.assertEqual(command[3:6], [chart['repository'], '--version', chart['version']])
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

    def test_default_rejects_incompatible_hardware_without_advertising_its_requirements(self):
        data = snapshot(2)
        for node in data['nodes']:
            node['metadata']['labels']['nvidia.com/gpu.product'] = 'NVIDIA-GB300'
        report = self.plan(data, '--profile', 'spark-fp8')
        output = self.output.getvalue()
        self.assertEqual(report['status'], 'blocked')
        self.assertIsNone(report['chosenProfile'])
        self.assertIsNone(report['deployment'])
        self.assertIn('DOES NOT FIT current requirements.', output)
        self.assertEqual(output.count('spark-fp8: GPU product does not match this profile.'), 1)
        for reason in report['blockers']:
            self.assertEqual(output.count(reason), 1)
        for hidden in ('Needs (', '104.0 GiB RAM', 'Selected nodes:', 'helm install'):
            self.assertNotIn(hidden, output)
        for expected in ('available-0', 'available-1', 'GPU allocation', 'No model cache claims found.'):
            self.assertIn(expected, output)
        self.output.truncate(0)
        self.output.seek(0)
        verbose_report = self.plan(data, '--profile', 'spark-fp8', '--verbose')
        self.assertEqual(verbose_report, report)
        verbose = self.output.getvalue()
        self.assertIn('DOES NOT FIT', verbose)
        self.assertIn('Evaluated profile: spark-fp8', verbose)
        self.assertIn('Required GPU: NVIDIA-GB10\n', verbose)
        self.assertIn('Result: incompatible with detected NVIDIA-GB300 GPUs', verbose)
        self.assertEqual(verbose.count('GPU product does not match this profile.'), 2)

    def test_verbose_does_not_infer_gpu_mismatch_for_mixed_or_unknown_products(self):
        for product in ('NVIDIA-GB10', ''):
            with self.subTest(product=product):
                self.output.truncate(0)
                self.output.seek(0)
                data = snapshot(3)
                data['nodes'][0]['metadata']['labels']['nvidia.com/gpu.product'] = 'NVIDIA-GB300'
                data['nodes'][1]['metadata']['labels']['nvidia.com/gpu.product'] = product
                data['nodes'][2]['status']['allocatable']['nvidia.com/gpu'] = '0'
                data['nodes'][2]['metadata']['labels']['nvidia.com/gpu.product'] = 'ignored-cpu-node'
                self.plan(data, '--verbose')
                output = self.output.getvalue()
                self.assertIn('Detected GPUs: ' + ', '.join(sorted(['NVIDIA-GB300', product or 'unknown'])), output)
                self.assertIn('Result: ' + ('fits' if product else 'blocked'), output)
                self.assertNotIn('Result: incompatible', output)
                self.assertNotIn('ignored-cpu-node', output)

    def test_verbose_warns_about_failed_checks_after_successful_smoke_test(self):
        report = self.plan(None, '--profile', 'spark-fp8')
        self.assertEqual(report['status'], 'fits')
        report['profiles'][0]['validation'] = {
            'automaticHelmStatus': 'smoke-tested',
            'automaticHelmWorkload': {'failedChecks': ['context-boundary'],
                                      'reason': 'Short prompts passed. Long-context request returned HTTP 502.'}}
        self.output.truncate(0)
        self.output.seek(0)
        llm.print_capacity_plan(report, verbose=True)
        output = self.output.getvalue()
        self.assertIn('Automatic startup validation: smoke-tested (failed: context-boundary).', output)
        self.assertIn('Validation notes: Short prompts passed. Long-context request returned HTTP 502.', output)
        self.assertIn('FITS current scheduling allocations.', output)

    def test_busy_matching_gpu_is_not_reported_as_missing_profile(self):
        data = snapshot(2)
        data['nodes'][0]['metadata']['labels']['nvidia.com/gpu.product'] = 'NVIDIA-GB300'
        data['pods'] = [{'metadata': {'name': 'busy', 'namespace': 'other'}, 'spec': {
            'nodeName': 'available-1', 'containers': [{'resources': {'requests': {'nvidia.com/gpu': 1}}}]}}]
        report = self.plan(data)
        self.assertEqual(report['status'], 'blocked')
        output = self.output.getvalue()
        self.assertIn('DOES NOT FIT', output)
        self.assertIn('Insufficient GPU', output)
        self.assertNotIn('No compatible profile found', output)

    def test_gpu_mismatch_keeps_resource_shortages_visible(self):
        for resource, amount, message in [('nvidia.com/gpu', '1', 'Insufficient GPU'),
                                          ('memory', '64Gi', 'Insufficient memory')]:
            with self.subTest(resource=resource):
                data = snapshot()
                data['nodes'][0]['metadata']['labels']['nvidia.com/gpu.product'] = 'NVIDIA-GB300'
                data['pods'] = [{'metadata': {'name': 'busy', 'namespace': 'other'}, 'spec': {
                    'nodeName': 'available-0', 'containers': [{'resources': {'requests': {resource: amount}}}]}}]
                self.output.truncate(0)
                self.output.seek(0)
                report = self.plan(data, '--profile', 'spark-fp8')
                self.assertEqual(report['status'], 'blocked')
                output = self.output.getvalue()
                self.assertIn('GPU product does not match this profile.', output)
                self.assertIn(message, output)
                self.assertNotIn('No compatible profile found', output)

    def test_too_few_matching_nodes_is_not_reported_as_missing_profile(self):
        data = snapshot(2)
        data['nodes'][0]['metadata']['labels']['nvidia.com/gpu.product'] = 'NVIDIA-GB300'
        report = self.plan(data, model='glm-5.3')
        self.assertEqual(report['status'], 'blocked')
        self.assertTrue(any(candidate['nodes'] for candidate in report['profiles'][0]['candidateNodes']))
        output = self.output.getvalue()
        self.assertIn('DOES NOT FIT', output)
        self.assertIn('No distinct compatible node group', output)
        self.assertNotIn('No compatible profile found', output)

    def test_default_shows_only_the_selected_fitting_profile(self):
        catalog = json.loads((HERE / 'recipes/index.json').read_text())
        model = next(item for item in catalog['recipes'] if item['id'] == 'qwen3.8-27b')
        selected = copy.deepcopy(model['profiles'][0])
        selected['id'] = 'matching-profile'
        model['profiles'][0]['hardware']['gpuProducts'] = ['other-gpu']
        model['profiles'].append(selected)
        alternative = copy.deepcopy(selected)
        alternative['id'] = 'another-matching-profile'
        model['profiles'].append(alternative)
        original = Path.read_text
        with patch.object(Path, 'read_text', lambda path, *a, **kw: json.dumps(catalog) if path == HERE/'recipes/index.json' else original(path, *a, **kw)):
            report = self.plan()
        output = self.output.getvalue()
        self.assertEqual(report['status'], 'fits')
        self.assertEqual(report['chosenProfile'], selected['id'])
        self.assertIn('FITS current scheduling allocations.', output)
        self.assertIn('Needs (matching-profile)', output)
        self.assertIn('104.0 GiB RAM', output)
        self.assertIn('Selected nodes: available-0', output)
        self.assertNotIn('Needs (spark-fp8)', output)
        self.assertNotIn('Needs (another-matching-profile)', output)
        self.assertNotIn('GPU product does not match', output)

    def test_default_shows_site_blocker_even_when_hardware_fits(self):
        data = snapshot()
        data['runtimeClasses'] = []
        report = self.plan(data)
        self.assertEqual(report['status'], 'blocked')
        self.assertIsNotNone(report['chosenProfile'])
        output = self.output.getvalue()
        self.assertIn('DOES NOT FIT', output)
        self.assertIn('RuntimeClass is not installed: nvidia', output)
        self.assertNotIn('Needs (', output)
        self.assertNotIn('Selected nodes:', output)

    def test_default_is_two_tables_with_status_without_detailed_deployment_diagnostics(self):
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
        for expected in ('EXISTING DEPLOYMENT', 'GPU allocation', 'Model files and caches', 'old-cache', '29.0 GiB', 'another-namespace'):
            self.assertIn(expected, output)
        for hidden in ('Needs (', 'Endpoint ', 'helm install', 'helm status', 'No changes made', 'Rank 0: Insufficient'):
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
        self.assertIsNone(parsed['storage']['caches'][1]['recipe'])
        self.assertIsNone(parsed['storage']['caches'][1]['profile'])

    def test_cache_table_follows_gpu_node_order_with_unknown_nodes_last(self):
        report = self.plan(snapshot(2))
        report['nodes'].reverse()
        caches = [
            ('a-cache', ['available-0'], 'recipe-a'),
            ('unknown-node', [], None),
            ('z-cache', ['available-1'], 'recipe-z'),
            ('other-node', ['other-node'], 'recipe-a'),
            ('b-cache', ['available-1'], 'recipe-a'),
            ('a-second-cache', ['available-1'], 'recipe-a'),
            ('shared-cache', ['available-0', 'available-1'], 'recipe-a')]
        report['storage']['caches'] = [
            {'claim': name, 'namespace': 'llm-stack', 'nodes': nodes, 'recipe': recipe,
             'profile': 'spark-profile' if recipe else None, 'bytesUsed': None, 'usageStatus': 'not-mounted'}
            for name, nodes, recipe in caches]
        before = copy.deepcopy(report)
        with patch.object(llm, 'print_table', wraps=llm.print_table) as table:
            llm.print_capacity_plan(report)
        gpu, cache = table.call_args_list
        self.assertEqual([row[0] for row in gpu.args[1]], ['available-1', 'available-0'])
        self.assertEqual(cache.args[0], ['NODE', 'RECIPE', 'PROFILE', 'CACHE', 'NAMESPACE', 'DISK USED'])
        self.assertEqual([row[3] for row in cache.args[1]],
                         ['a-second-cache', 'b-cache', 'z-cache', 'shared-cache', 'a-cache', 'other-node', 'unknown-node'])
        self.assertEqual(cache.args[1][3][0], 'available-1, available-0')
        self.assertEqual(cache.args[1][-1][:3], ['Unknown', 'Unknown', 'Unknown'])
        self.assertTrue(all(row[-1] == 'Not mounted' for row in cache.args[1]))
        self.assertEqual(report, before)

    def test_storage_inventory_failure_is_not_presented_as_no_caches(self):
        with patch.object(model_storage, 'collect', return_value={'caches': [], 'warnings': ['Forbidden']}):
            self.plan()
        self.assertIn('Storage inventory incomplete', self.output.getvalue())
        self.assertNotIn('No model cache claims found', self.output.getvalue())

    def test_invalid_inputs_stop_before_inventory(self):
        for args in (['--concurrency', '0'], ['--release', 'bad,name'], ['--storage-class', 'other,inject=true'],
                     ['--chart-source', ''], ['--chart-source=--unexpected-option']):
            with self.subTest(args=args), patch.object(capacity, 'inventory') as inventory, self.assertRaises(SystemExit):
                llm.main(['--context', 'test-cluster', 'plan', '--model', 'qwen3.8-27b', *args])
            inventory.assert_not_called()

    def test_generated_command_shell_quotes_context(self):
        context = 'cluster; echo unwanted'
        with patch.object(capacity, 'inventory', return_value=snapshot()):
            report = llm.main(['--context', context, 'plan', '--model', 'qwen3.8-27b'])
        command = shlex.split(report['deployment']['command'])
        self.assertEqual(command[command.index('--kube-context')+1], context)


class CacheRecipeMetadataTests(unittest.TestCase):
    def setUp(self):
        self.catalog = json.loads((HERE / 'recipes/index.json').read_text())

    def storage(self, owners):
        claims = [dict(retained_claim(name, release), namespace=namespace, releaseNamespace=owner_namespace)
                  for name, namespace, release, owner_namespace in owners]
        return {'claims': claims, 'caches': [
            {'namespace': claim['namespace'], 'claim': claim['claim'], 'nodes': claim['nodes'],
             'bytesUsed': None, 'usageStatus': 'not-mounted'} for claim in claims], 'warnings': []}

    def test_owner_values_are_scoped_by_namespace_and_read_once_per_release(self):
        storage = self.storage([('first-cache', 'one', 'custom', 'one'),
                                ('second-cache', 'one', 'custom', 'one'),
                                ('first-cache', 'two', 'custom', 'two')])
        def run(command):
            namespace = command[4]
            self.assertEqual(command, ['helm', '--kube-context', 'test-cluster', '--namespace', namespace,
                                       'get', 'values', 'custom', '--all', '--output', 'json'])
            return json.dumps({'recipe': 'recipe-' + namespace, 'profileName': 'profile-' + namespace})
        with patch.object(llm, 'run', side_effect=run) as calls:
            llm.cache_recipe_metadata('test-cluster', storage, {}, self.catalog)
        self.assertEqual(calls.call_count, 2)
        self.assertEqual([(row['recipe'], row['profile']) for row in storage['caches']],
                         [('recipe-one', 'profile-one'), ('recipe-one', 'profile-one'), ('recipe-two', 'profile-two')])

    def test_missing_or_unavailable_owner_does_not_guess_from_claim_name(self):
        storage = self.storage([('qwen-fp8-cache-0', 'one', '', None),
                                ('qwen-fp8-cache-1', 'one', 'qwen-fp8', None),
                                ('qwen-fp8-cache-2', 'one', 'qwen-fp8', 'other'),
                                ('qwen-fp8-cache-3', 'one', 'deleted-release', 'one'),
                                ('qwen-fp8-cache-4', 'one', 'deleted-release', 'one')])
        with patch.object(llm, 'run', side_effect=RuntimeError('release not found')) as calls:
            llm.cache_recipe_metadata('test-cluster', storage, {}, self.catalog)
        self.assertEqual(calls.call_count, 1)
        self.assertEqual(storage['warnings'], [])
        for row in storage['caches']:
            self.assertIsNone(row['recipe'])
            self.assertIsNone(row['profile'])
            self.assertIsNone(row['bytesUsed'])
            self.assertEqual(row['usageStatus'], 'not-mounted')
        self.assertEqual(storage['caches'][-1]['metadataError'], 'release not found')

    def test_missing_profile_and_invalid_values_remain_unknown(self):
        for values in ({'recipe': 'qwen3.8-27b'}, {'recipe': 'qwen3.8-27b', 'profileName': ''},
                       {'recipe': 'qwen3.8-27b', 'profileName': ['spark-fp8']}, []):
            with self.subTest(values=values):
                storage = self.storage([('custom-cache', 'one', 'custom', 'one')])
                with patch.object(llm, 'run', return_value=json.dumps(values)):
                    llm.cache_recipe_metadata('test-cluster', storage, {}, self.catalog)
                self.assertIsNone(storage['caches'][0]['profile'])
                self.assertEqual(storage['caches'][0]['recipe'], 'qwen3.8-27b' if isinstance(values, dict) else None)

    def test_implicit_profile_uses_owned_runtime_config_and_actual_claims(self):
        for recipe, suffix, config_key, identity, claims_key in (
                ('qwen3.8-27b-nvfp4', 'runtime', 'config.json', {'model': {'id': 'qwen3.8-27b-nvfp4'}}, 'claimNames'),
                ('glm-5.3', 'automatic', 'automatic.json', {'recipe': {'name': 'GLM-5.3'}}, 'claims')):
            with self.subTest(recipe=recipe):
                storage = self.storage([('first-cache', 'one', 'custom', 'one'),
                                        ('second-cache', 'one', 'custom', 'one'),
                                        ('old-cache', 'one', 'custom', 'one')])
                config = {**identity, 'profile': {'id': 'deployed-profile'}, 'unused': 'do-not-export',
                          claims_key: ['first-cache', 'second-cache']}
                resource = {'metadata': {'annotations': {'meta.helm.sh/release-name': 'custom',
                                                        'meta.helm.sh/release-namespace': 'one'}},
                            'data': {config_key: json.dumps(config)}}
                with patch.object(llm, 'run', side_effect=[json.dumps({'recipe': recipe, 'profileName': ''}),
                                                          json.dumps(resource)]) as calls:
                    llm.cache_recipe_metadata('test-cluster', storage, {}, self.catalog)
                self.assertEqual(calls.call_count, 2)
                self.assertEqual(calls.call_args.args[0], ['kubectl', '--context', 'test-cluster', '--namespace', 'one',
                                                          'get', 'configmap', 'custom-' + suffix, '-o', 'json'])
                self.assertEqual([row['profile'] for row in storage['caches']], ['deployed-profile', 'deployed-profile', None])
                self.assertNotIn('do-not-export', json.dumps(storage))

    def test_implicit_profile_rejects_foreign_or_mismatched_runtime_config(self):
        for change in ('owner', 'namespace', 'recipe', 'invalid-json', 'invalid-shape'):
            with self.subTest(change=change):
                storage = self.storage([('custom-cache', 'one', 'custom', 'one')])
                annotations = {'meta.helm.sh/release-name': 'custom', 'meta.helm.sh/release-namespace': 'one'}
                config = {'model': {'id': 'qwen3.8-27b-nvfp4'}, 'profile': {'id': 'spark-nvfp4'},
                          'claimNames': ['custom-cache']}
                if change == 'owner':
                    annotations['meta.helm.sh/release-name'] = 'other'
                elif change == 'namespace':
                    annotations['meta.helm.sh/release-namespace'] = 'other'
                elif change == 'recipe':
                    config['model']['id'] = 'other'
                elif change == 'invalid-shape':
                    config['profile'] = None
                resource = {'metadata': {'annotations': annotations},
                            'data': {'config.json': 'invalid' if change == 'invalid-json' else json.dumps(config)}}
                with patch.object(llm, 'run', side_effect=[json.dumps({'recipe': 'qwen3.8-27b-nvfp4'}),
                                                          json.dumps(resource)]):
                    llm.cache_recipe_metadata('test-cluster', storage, {}, self.catalog)
                self.assertIsNone(storage['caches'][0]['profile'])
                self.assertTrue(storage['caches'][0]['metadataError'])


if __name__ == '__main__':
    unittest.main()
