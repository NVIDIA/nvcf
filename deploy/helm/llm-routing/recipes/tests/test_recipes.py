# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import recipes
spec = importlib.util.spec_from_file_location('runtime', ROOT / 'charts/sglang/files/runtime.py')
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


def node(name):
    return {'metadata': {'name': name, 'uid': 'uid-' + name, 'labels': {
        'kubernetes.io/arch': 'arm64', 'kubernetes.io/os': 'linux', 'nvidia.com/gpu.product': 'NVIDIA-GB10'}},
        'spec': {}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True'}],
                              'allocatable': {'cpu': '20', 'memory': '120Gi', 'nvidia.com/gpu': '1', 'ephemeral-storage': '500Gi'}}}


def snapshot(count=3):
    return {'context': 'recipe-test', 'capturedAt': '2026-10-06T00:00:00+00:00',
            'nodes': [node('spark-' + str(i)) for i in range(count)], 'pods': [], 'endpoints': [],
            'runtimeClasses': [{'metadata': {'name': 'nvidia'}}],
            'storageClasses': [{'metadata': {'name': 'local-path'}, 'volumeBindingMode': 'WaitForFirstConsumer'}]}


def facts(count=3):
    return {'nodes': {'spark-' + str(i): {'localNvme': True, 'fabric': 'pair-a', 'gbps': 200,
                                       'interface': 'enp1s0', 'address': '192.0.2.' + str(i + 1)} for i in range(count)}}


def plan(s=None, f=None, models=None, **kwargs):
    return recipes.plan(s if s is not None else snapshot(), f if f is not None else facts(),
                        models or ['qwen3.8-27b', 'qwen3.8-flash-next'], 'llm-gateway', 'local-path', 'nvidia', **kwargs)


def all_profile_releases():
    return plan()['releases'] + [plan(preference='latency')['releases'][1]] + plan(models=['qwen3.8-27b-nvfp4'])['releases']


class PlannerTests(unittest.TestCase):
    def test_two_precisions_have_distinct_releases_and_exclusive_nodes(self):
        models = ['qwen3.8-27b', 'qwen3.8-27b-nvfp4']
        releases = plan(snapshot(2), {}, models=models)['releases']
        self.assertEqual([r['model'] for r in releases], models)
        self.assertEqual([r['values']['model']['precision'] for r in releases], ['FP8', 'NVFP4'])
        self.assertEqual(len({r['name'] for r in releases}), 2)
        self.assertEqual(len({n for r in releases for n in r['nodes']}), 2)
        self.assertTrue(all(len(r['nodes']) == 1 and not r['values']['profile']['offload'] for r in releases))
        with self.assertRaisesRegex(ValueError, 'No placement fits'):
            plan(snapshot(1), {}, models=models)

    def test_two_precisions_respect_occupied_nodes(self):
        s = snapshot()
        s['pods'] = [{'metadata': {'name': 'existing-model'}, 'spec': {'nodeName': 'spark-0', 'containers': [
            {'resources': {'requests': {'nvidia.com/gpu': 1}}}]}, 'status': {'phase': 'Running'}}]
        releases = plan(s, {}, models=['qwen3.8-27b', 'qwen3.8-27b-nvfp4'])['releases']
        self.assertEqual({n for r in releases for n in r['nodes']}, {'spark-1', 'spark-2'})

    def test_model_identity_is_scoped_to_target_namespace(self):
        s = snapshot()
        s['endpoints'] = [{'metadata': {'namespace': 'another-stack'}, 'spec': {'modelName': 'qwen3.8-27b'}}]
        self.assertEqual(plan(s)['namespace'], 'llm-gateway')
        s['endpoints'][0]['metadata']['namespace'] = 'llm-gateway'
        with self.assertRaisesRegex(ValueError, 'already registered'):
            plan(s)

    def test_fewest_nodes_uses_two_for_both_models(self):
        result = plan(snapshot(2), facts(2))
        self.assertEqual([r['profile'] for r in result['releases']], ['spark-fp8', 'spark-nvfp4-nvme'])
        self.assertEqual(len({n for r in result['releases'] for n in r['nodes']}), 2)

    def test_latency_prefers_distributed_profile(self):
        result = plan(preference='latency')
        self.assertEqual(result['releases'][1]['profile'], 'spark-nvfp4-tp2')
        self.assertEqual(len({n for r in result['releases'] for n in r['nodes']}), 3)

    def test_no_nvme_falls_back_to_pair(self):
        f = facts()
        for v in f['nodes'].values():
            v['localNvme'] = False
        self.assertEqual(plan(f=f)['releases'][1]['profile'], 'spark-nvfp4-tp2')

    def test_no_offload_with_only_two_available_fails(self):
        with self.assertRaisesRegex(ValueError, 'No placement fits'):
            plan(snapshot(2), facts(2), allow_offload=False)

    def test_workload_can_require_two_sparks(self):
        r = plan(requirements={'qwen3.8-flash-next': {'concurrency': 9}})
        self.assertEqual(r['releases'][1]['profile'], 'spark-nvfp4-tp2')
        self.assertEqual(r['releases'][0]['values']['concurrency'], 1)

    def test_unsupported_workload_fails(self):
        for req in [{'contextLength': 9999999}, {'concurrency': 25}, {'concurrency': 0}, {'typo': 1}]:
            with self.subTest(req=req), self.assertRaises(ValueError):
                plan(requirements={'qwen3.8-flash-next': req})

    def test_busy_gpu_never_reused(self):
        s = snapshot()
        s['pods'] = [{'metadata': {'name': 'other-model'}, 'spec': {'nodeName': 'spark-0', 'containers': [
            {'resources': {'limits': {'nvidia.com/gpu': '1'}}}]}, 'status': {'phase': 'Running'}}]
        r = plan(s)
        self.assertNotIn('spark-0', [n for release in r['releases'] for n in release['nodes']])
        self.assertIn('spark-0', r['excludedNodes'])

    def test_pending_gpu_request_blocks_planning(self):
        s = snapshot()
        s['pods'] = [{'metadata': {'name': 'pending-model'}, 'spec': {'containers': [
            {'resources': {'limits': {'nvidia.com/gpu': 1}}}]}}]
        with self.assertRaisesRegex(ValueError, 'unscheduled GPU'):
            plan(s)

    def test_memory_cpu_and_ephemeral_are_accounted(self):
        for resource, used in [('memory', '20Gi'), ('cpu', '18'), ('ephemeral-storage', '480Gi')]:
            with self.subTest(resource=resource):
                s = snapshot(1)
                s['pods'] = [{'metadata': {'name': 'busy'}, 'spec': {'nodeName': 'spark-0', 'containers': [
                    {'resources': {'requests': {resource: used}}}]}}]
                with self.assertRaisesRegex(ValueError, 'No placement fits'):
                    plan(s, facts(1), models=['qwen3.8-flash-next'])

    def test_taints_cordon_pressure_arch_and_sharing_excluded(self):
        cases = [('taint', lambda n: n['spec'].update(taints=[{'effect': 'NoSchedule'}])),
                 ('cordon', lambda n: n['spec'].update(unschedulable=True)),
                 ('pressure', lambda n: n['status']['conditions'].append({'type': 'MemoryPressure', 'status': 'True'})),
                 ('arch', lambda n: n['metadata']['labels'].update({'kubernetes.io/arch': 'amd64'})),
                 ('sharing', lambda n: n['metadata']['labels'].update({'nvidia.com/gpu.replicas': '2'}))]
        for label, change in cases:
            s = snapshot(1)
            change(s['nodes'][0])
            with self.subTest(label=label), self.assertRaisesRegex(ValueError, 'No placement fits'):
                plan(s, facts(1), models=['qwen3.8-27b'])

    def test_distributed_needs_same_fast_fabric(self):
        for key, value in [('fabric', 'other'), ('gbps', 10), ('address', 'bad'), ('interface', '')]:
            f = facts(2)
            f['nodes']['spark-1'][key] = value
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, 'No placement fits'):
                plan(snapshot(2), f, models=['qwen3.8-flash-next'], allow_offload=False)

    def test_no_unproven_nvme_offload(self):
        with self.assertRaisesRegex(ValueError, 'No placement fits'):
            plan(snapshot(1), {}, models=['qwen3.8-flash-next'])

    def test_duplicate_model_name_rejected(self):
        s = snapshot()
        s['endpoints'] = [{'metadata': {'namespace': 'llm-gateway'}, 'spec': {'modelName': 'qwen3.8-27b'}}]
        with self.assertRaisesRegex(ValueError, 'already registered'):
            plan(s)
        with self.assertRaisesRegex(ValueError, 'once'):
            plan(models=['qwen3.8-27b', 'qwen3.8-27b'])

    def test_missing_runtime_and_immediate_storage_rejected(self):
        for key, val in [('runtimeClasses', []), ('storageClasses', [{'metadata': {'name': 'local-path'}, 'volumeBindingMode': 'Immediate'}])]:
            s = snapshot()
            s[key] = val
            with self.subTest(key=key), self.assertRaises(ValueError):
                plan(s)

    def test_global_allocation_avoids_greedy_failure(self):
        f = facts(2)
        f['nodes']['spark-1']['localNvme'] = False
        r = plan(snapshot(2), f)
        self.assertEqual(r['releases'][0]['nodes'], ['spark-1'])
        self.assertEqual(r['releases'][1]['nodes'], ['spark-0'])

    def test_check_plan_rejects_node_replacement_or_new_allocation(self):
        s = snapshot()
        result = plan(s)
        release = result['releases'][0]['name']
        self.assertTrue(recipes.check_plan(result, release, s)['passed'])
        changed = copy.deepcopy(s)
        changed['nodes'][0]['metadata']['uid'] = 'replaced'
        with self.assertRaisesRegex(ValueError, 'identity changed'):
            recipes.check_plan(result, release, changed)
        changed = copy.deepcopy(s)
        changed['pods'] = [{'metadata': {'name': 'new-owner'}, 'spec': {'nodeName': 'spark-0',
                            'containers': [{'resources': {'limits': {'nvidia.com/gpu': 1}}}]}}]
        with self.assertRaisesRegex(ValueError, 'No placement fits'):
            recipes.check_plan(result, release, changed)

    def test_native_sidecar_init_and_overhead(self):
        p = {'spec': {'containers': [{'resources': {'requests': {'memory': '1Gi'}}}],
                     'initContainers': [{'restartPolicy': 'Always', 'resources': {'requests': {'memory': '2Gi'}}},
                                        {'resources': {'requests': {'memory': '4Gi'}}}], 'overhead': {'memory': '1Gi'}}}
        self.assertEqual(recipes.requests(p, 'memory'), 7 * recipes.GIB)

    def test_completed_jobs_do_not_reserve_gpus(self):
        s = snapshot(1)
        s['pods'] = [{'spec': {'nodeName': 'spark-0', 'containers': [{'resources': {'requests': {'nvidia.com/gpu': 1}}}]},
                      'status': {'phase': 'Succeeded'}}]
        self.assertEqual(len(plan(s, models=['qwen3.8-27b'])['releases']), 1)

    def test_quantities(self):
        self.assertEqual(recipes.quantity('1000m'), 1)
        self.assertEqual(recipes.quantity('1.5Gi'), 1610612736)
        self.assertEqual(recipes.quantity('128G'), 128000000000)
        with self.assertRaises(ValueError):
            recipes.quantity('bad')

    def test_inventory_is_read_only_and_context_is_explicit(self):
        with patch.object(recipes, 'command_json', return_value={'items': []}) as run:
            recipes.inventory('explicit-test')
        for c in run.call_args_list:
            self.assertEqual(c.args[0][:4], ['kubectl', '--context', 'explicit-test', 'get'])

    def test_live_plan_uses_read_only_inventory(self):
        live = snapshot()
        live['context'] = 'explicit-test'
        with patch.object(sys, 'argv', ['recipes.py', 'plan', '--context', 'explicit-test', '--model', 'qwen3.8-27b',
                                       '--namespace', 'llm-gateway', '--storage-class', 'local-path', '--runtime-class', 'nvidia', '--output', '/unused']), \
                patch.object(recipes, 'inventory', return_value=live) as inv, patch.object(recipes, 'save'):
            recipes.main()
        inv.assert_called_once_with('explicit-test')


class HardwareProfileTests(unittest.TestCase):
    def setUp(self):
        self.catalog = json.loads((ROOT/'catalog.json').read_text())
        self.tmp = tempfile.TemporaryDirectory(prefix='recipe-hardware-test-')
        self.addCleanup(self.tmp.cleanup)
        self.directory = pathlib.Path(self.tmp.name)
        patcher = patch.object(recipes, 'HERE', self.directory)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.write_catalog()

    def write_catalog(self):
        (self.directory/'catalog.json').write_text(json.dumps(self.catalog))

    def alternate_profile(self):
        model = self.catalog['models'][0]
        model['validation'] = 'pending-hardware-validation'
        model.pop('validatedWorkload', None)
        profile = model['profiles'][0]
        profile['id'] = 'fixture-discrete'
        profile['hardware'] = {'os': 'linux', 'architecture': 'amd64',
                               'gpuProducts': ['NVIDIA-Test-GPU'], 'cudaDeviceNames': ['NVIDIA Test GPU'],
                               'gpuCount': 1, 'memoryMode': 'discrete', 'minDeviceMemoryGiB': 80}
        self.write_catalog()
        inventory = snapshot(1)
        inventory['nodes'][0]['metadata']['labels'].update({
            'kubernetes.io/arch': 'amd64', 'nvidia.com/gpu.product': 'NVIDIA Test GPU'})
        return inventory, {'nodes': {'spark-0': {'cudaTotalMemoryGiB': 80}}}

    def test_common_capacity_accepts_hardware_independently_of_profiles(self):
        inventory = snapshot(1)
        inventory['nodes'][0]['metadata']['labels'].update({
            'kubernetes.io/arch': 'amd64', 'nvidia.com/gpu.product': 'NVIDIA-Test-GPU'})
        inventory['nodes'][0]['status']['allocatable']['nvidia.com/gpu'] = '2'
        candidates, rejected = recipes.capacity(inventory)
        self.assertEqual([candidate['name'] for candidate in candidates], ['spark-0'])
        self.assertEqual(candidates[0]['free']['nvidia.com/gpu'], 2)
        self.assertEqual(rejected, {})
        with self.assertRaisesRegex(ValueError, 'No placement fits.*compatible hardware'):
            plan(inventory, models=['qwen3.8-27b'])

    def test_profile_data_enables_alternate_hardware_without_machine_branches(self):
        inventory, capabilities = self.alternate_profile()
        result = plan(inventory, capabilities, models=['qwen3.8-27b'])
        values = result['releases'][0]['values']
        self.assertEqual(values['gpu'], {'product': 'NVIDIA-Test-GPU'})
        self.assertEqual(values['profile']['hardware'], self.catalog['models'][0]['profiles'][0]['hardware'])
        self.assertIn('GPU node(s)', result['releases'][0]['reason'])
        self.assertNotIn('Spark', result['releases'][0]['reason'])
        self.assertEqual(result['validation'], 'pending-hardware-validation')
        self.assertNotIn('validatedWorkload', self.catalog['models'][0])

    def test_current_profiles_preserve_gb10_eligibility_and_normalize_product_aliases(self):
        inventory = snapshot(2)
        inventory['nodes'][0]['metadata']['labels']['nvidia.com/gpu.product'] = 'NVIDIA GB10'
        result = plan(inventory, facts(2), models=['qwen3.8-27b', 'qwen3.8-27b-nvfp4'])
        self.assertEqual([release['nodes'] for release in result['releases']], [['spark-0'], ['spark-1']])
        for release in result['releases']:
            self.assertEqual(release['values']['gpu'], {'product': 'NVIDIA-GB10'})
            self.assertEqual(release['values']['profile']['hardware']['memoryMode'], 'unified')

    def test_each_profile_checks_os_architecture_product_and_declared_gpu_count(self):
        for field, value in [('kubernetes.io/os', 'windows'), ('kubernetes.io/arch', 'amd64'),
                             ('nvidia.com/gpu.product', 'NVIDIA-Other-GPU')]:
            inventory = snapshot(1)
            inventory['nodes'][0]['metadata']['labels'][field] = value
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'No placement fits.*compatible hardware'):
                plan(inventory, models=['qwen3.8-27b'])
        inventory = snapshot(1)
        inventory['nodes'][0]['status']['allocatable']['nvidia.com/gpu'] = '2'
        with self.assertRaisesRegex(ValueError, 'No placement fits.*compatible hardware'):
            plan(inventory, models=['qwen3.8-27b'])

    def test_discrete_profile_requires_explicit_sufficient_device_memory(self):
        inventory, capabilities = self.alternate_profile()
        for memory in (None, 79, True, '80', float('nan'), float('inf')):
            capabilities['nodes']['spark-0']['cudaTotalMemoryGiB'] = memory
            with self.subTest(memory=memory), self.assertRaisesRegex(ValueError, 'No placement fits'):
                plan(inventory, capabilities, models=['qwen3.8-27b'])
        capabilities['nodes']['spark-0']['cudaTotalMemoryGiB'] = 80
        self.assertEqual(plan(inventory, capabilities, models=['qwen3.8-27b'])['releases'][0]['nodes'], ['spark-0'])

    def test_profile_hardware_contract_is_required_and_discrete_memory_is_not_inferred(self):
        profile = self.catalog['models'][0]['profiles'][0]
        original = copy.deepcopy(profile['hardware'])
        invalid = [None, {}, dict(original, architecture=''), dict(original, gpuProducts=[]),
                   dict(original, cudaDeviceNames=[]), dict(original, gpuCount=True), dict(original, gpuCount=2),
                   dict(original, memoryMode='unknown'), dict(original, memoryMode='discrete'),
                   dict(original, memoryMode='discrete', minDeviceMemoryGiB=0)]
        for hardware in invalid:
            profile['hardware'] = hardware
            self.write_catalog()
            with self.subTest(hardware=hardware), self.assertRaisesRegex(ValueError, 'hardware|memoryMode'):
                plan(models=['qwen3.8-27b'])

    def test_distributed_group_requires_one_canonical_gpu_product(self):
        model = next(model for model in self.catalog['models'] if model['id'] == 'qwen3.8-flash-next')
        model['profiles'] = [profile for profile in model['profiles'] if profile['nodes'] == 2]
        model['profiles'][0]['hardware']['gpuProducts'].append('NVIDIA-Test-GPU')
        model['profiles'][0]['hardware']['cudaDeviceNames'].append('NVIDIA Test GPU')
        self.write_catalog()
        inventory = snapshot(2)
        inventory['nodes'][1]['metadata']['labels']['nvidia.com/gpu.product'] = 'NVIDIA GB10'
        release = plan(inventory, facts(2), models=[model['id']])['releases'][0]
        self.assertEqual(release['values']['gpu'], {'product': 'NVIDIA-GB10'})
        inventory['nodes'][1]['metadata']['labels']['nvidia.com/gpu.product'] = 'NVIDIA-Test-GPU'
        with self.assertRaisesRegex(ValueError, 'No placement fits'):
            plan(inventory, facts(2), models=[model['id']])

    def test_network_capacity_comes_from_distributed_profile(self):
        model = next(model for model in self.catalog['models'] if model['id'] == 'qwen3.8-flash-next')
        model['profiles'] = [profile for profile in model['profiles'] if profile['nodes'] == 2]
        model['profiles'][0]['minFabricGbps'] = 100
        self.write_catalog()
        capabilities = facts(2)
        for node_facts in capabilities['nodes'].values():
            node_facts['gbps'] = 100
        self.assertEqual(len(plan(snapshot(2), capabilities, models=[model['id']])['releases'][0]['nodes']), 2)
        capabilities['nodes']['spark-1']['gbps'] = 99
        with self.assertRaisesRegex(ValueError, 'No placement fits'):
            plan(snapshot(2), capabilities, models=[model['id']])


class StackBindingTests(unittest.TestCase):
    def cli(self, extra):
        return ['recipes.py', 'plan', '--model', 'qwen3.8-27b', '--storage-class', 'local-path',
                '--runtime-class', 'nvidia', '--output', '/unused'] + extra

    def test_bound_plan_verifies_connection_and_derives_target(self):
        connection = {'context': 'recipe-test', 'namespace': 'llm-gateway', 'clusterId': 'test-stack'}
        api = Mock()
        api.load_connection.return_value = connection
        with patch.object(sys, 'argv', self.cli(['--stack-connection', '/connection.json'])), \
                patch.object(recipes, 'stack_api', return_value=api), \
                patch.object(recipes, 'inventory', return_value=snapshot()) as inventory, \
                patch.object(recipes, 'save') as save:
            recipes.main()
        api.load_connection.assert_called_once_with(pathlib.Path('/connection.json'))
        api.inspect_connection.assert_called_once_with(connection)
        inventory.assert_called_once_with('recipe-test')
        result = save.call_args.args[1]
        self.assertEqual((result['context'], result['namespace']), ('recipe-test', 'llm-gateway'))
        self.assertEqual(result['stackConnection'], connection)

    def test_explicit_target_mismatch_rejected_before_live_inventory(self):
        for option in ('--context', '--namespace'):
            api = Mock()
            api.load_connection.return_value = {'context': 'recipe-test', 'namespace': 'llm-gateway'}
            with self.subTest(option=option), patch.object(sys, 'argv', self.cli([
                    '--stack-connection', '/connection.json', option, 'other-target'])), \
                    patch.object(recipes, 'stack_api', return_value=api), patch.object(recipes, 'inventory') as inventory:
                with self.assertRaisesRegex(ValueError, 'Stack connection .* differs'):
                    recipes.main()
                inventory.assert_not_called()
                api.inspect_connection.assert_not_called()

    def test_offline_inventory_does_not_inspect_live_stack(self):
        with tempfile.TemporaryDirectory() as d:
            path = pathlib.Path(d) / 'inventory.json'
            path.write_text(json.dumps(snapshot()))
            with patch.object(sys, 'argv', self.cli(['--inventory', str(path), '--namespace', 'llm-gateway'])), \
                    patch.object(recipes, 'stack_api') as api, patch.object(recipes, 'inventory') as inventory, \
                    patch.object(recipes, 'save') as save:
                recipes.main()
            api.assert_not_called()
            inventory.assert_not_called()
            self.assertNotIn('stackConnection', save.call_args.args[1])

    def test_bound_plan_rejects_saved_inventory(self):
        api = Mock()
        api.load_connection.return_value = {'context': 'recipe-test', 'namespace': 'llm-gateway'}
        with patch.object(sys, 'argv', self.cli(['--stack-connection', '/connection.json', '--inventory', '/snapshot.json'])), \
                patch.object(recipes, 'stack_api', return_value=api), patch.object(recipes, 'inventory') as inventory:
            with self.assertRaisesRegex(ValueError, 'requires live inventory'):
                recipes.main()
            api.inspect_connection.assert_not_called()
            inventory.assert_not_called()

    def test_check_plan_revalidates_binding_before_capacity(self):
        result = plan(models=['qwen3.8-27b'])
        connection = result['stackConnection'] = {'context': result['context'], 'namespace': result['namespace']}
        api = Mock()
        api.inspect_connection.side_effect = ValueError('Shared resource was replaced')
        with patch.object(recipes, 'stack_api', return_value=api), patch.object(recipes, 'inventory') as inventory:
            with self.assertRaisesRegex(ValueError, 'Shared resource was replaced'):
                recipes.check_plan(result, result['releases'][0]['name'])
            inventory.assert_not_called()
        api.inspect_connection.side_effect = None
        with patch.object(recipes, 'stack_api', return_value=api), \
                patch.object(recipes, 'inventory', return_value=snapshot()) as inventory:
            self.assertTrue(recipes.check_plan(result, result['releases'][0]['name'])['passed'])
            inventory.assert_called_once_with(connection['context'])
        self.assertEqual(api.inspect_connection.call_count, 2)

    def test_modified_plan_target_rejected_before_binding_inspection(self):
        result = plan(models=['qwen3.8-27b'])
        result['stackConnection'] = {'context': result['context'], 'namespace': 'another-stack'}
        with patch.object(recipes, 'stack_api') as api, patch.object(recipes, 'inventory') as inventory:
            with self.assertRaisesRegex(ValueError, 'Stack connection namespace differs'):
                recipes.check_plan(result, result['releases'][0]['name'])
            api.assert_not_called()
            inventory.assert_not_called()


class RuntimeTests(unittest.TestCase):
    def test_nvfp4_uses_checkpoint_quantization_and_bounded_spark_pools(self):
        values = plan(models=['qwen3.8-27b-nvfp4'])['releases'][0]['values']
        args = runtime.command(values, '/snapshot', 0)
        self.assertEqual(values['model']['precision'], 'NVFP4')
        self.assertEqual(args[args.index('--served-model-name') + 1], 'qwen3.8-27b-nvfp4')
        self.assertEqual(args[args.index('--mem-fraction-static') + 1], '0.80')
        self.assertEqual(args[args.index('--max-running-requests') + 1], '1')
        self.assertEqual(args[args.index('--kv-cache-dtype') + 1], 'fp8_e4m3')
        self.assertNotIn('--quantization', args)
        self.assertNotIn('--speculative-algorithm', args)
        self.assertNotIn('--ple-offload-embedding', args)

    def test_served_name_matches_endpoint_and_checkpoint_is_local(self):
        for release in all_profile_releases():
            args = runtime.command(release['values'], '/cache/snapshot', 0)
            self.assertEqual(args[args.index('--served-model-name') + 1], release['model'])
            self.assertEqual(args[args.index('--model-path') + 1], '/cache/snapshot')
            self.assertEqual(args[args.index('--context-length') + 1], '8192')

    def test_distributed_ranks_match_topology(self):
        values = plan(preference='latency')['releases'][1]['values']
        for rank in (0, 1):
            args = runtime.command(values, '/snapshot', rank)
            self.assertEqual(args[args.index('--nnodes') + 1], '2')
            self.assertEqual(args[args.index('--node-rank') + 1], str(rank))
            self.assertEqual(args[args.index('--tp') + 1], '2')
            self.assertIn('--no-ple-offload-embedding', args)
            self.assertNotIn('--ple-offload-backend', args)

    def test_one_node_offload_is_file_backed(self):
        values = plan()['releases'][1]['values']
        args = runtime.command(values, '/snapshot', 0)
        self.assertEqual(args[args.index('--ple-offload-backend') + 1], 'file')
        self.assertNotIn('--nnodes', args)

    def test_27b_uses_spark_precision_and_memory_settings(self):
        values = plan()['releases'][0]['values']
        args = runtime.command(values, '/snapshot', 0)
        self.assertEqual(values['model']['precision'], 'FP8')
        self.assertEqual(args[args.index('--mem-fraction-static') + 1], '0.80')
        self.assertEqual(args[args.index('--mamba-ssm-dtype') + 1], 'float32')
        self.assertNotIn('--speculative-algorithm', args)


class HelmTests(unittest.TestCase):
    def render(self, values, phase):
        with tempfile.TemporaryDirectory() as d:
            p = pathlib.Path(d) / 'values.json'
            p.write_text(json.dumps(values))
            return subprocess.run(['helm', 'template', 'model-test', str(ROOT / 'charts/sglang'), '-n', 'llm-gateway',
                                   '-f', str(p), '--set', 'phase=' + phase], text=True, capture_output=True)

    def test_all_profiles_all_phases(self):
        releases = all_profile_releases()
        for release in releases:
            for phase in ('qualify', 'download', 'serve'):
                with self.subTest(profile=release['profile'], phase=phase):
                    r = self.render(release['values'], phase)
                    self.assertEqual(r.returncode, 0, r.stderr)
                    self.assertEqual(r.stdout.count('kind: PersistentVolumeClaim'), len(release['nodes']))
                    self.assertNotIn('kind: Secret', r.stdout)
                    self.assertNotIn('kind: ClusterRole', r.stdout)
                    self.assertIn('helm.sh/resource-policy: keep', r.stdout)
                    if phase == 'serve':
                        self.assertEqual(r.stdout.count('kind: Deployment'), len(release['nodes']))
                        self.assertEqual(r.stdout.count('kind: InferenceEndpoint'), 1)
                        self.assertIn('modelName: "' + release['model'] + '"', r.stdout)
                    else:
                        self.assertEqual(r.stdout.count('kind: Job'), len(release['nodes']))
                        self.assertNotIn('kind: InferenceEndpoint', r.stdout)
                    if phase == 'download':
                        self.assertNotIn('nvidia.com/gpu:', r.stdout)
                    if len(release['nodes']) == 2 and phase != 'download':
                        self.assertIn('hostNetwork: true', r.stdout)
                        self.assertIn('NCCL_SOCKET_IFNAME', r.stdout)
                        self.assertIn('NCCL_IB_DISABLE', r.stdout)

    def test_invalid_topology_revision_image_and_workload_fail(self):
        original = plan()['releases'][0]['values']
        changes = [lambda v: v.update(image='image:latest'), lambda v: v['model'].update(revision='main'),
                   lambda v: v.update(concurrency=999), lambda v: v.update(targets=[]),
                   lambda v: v['profile'].update(nodes=2)]
        for change in changes:
            values = copy.deepcopy(original)
            change(values)
            self.assertNotEqual(self.render(values, 'serve').returncode, 0)

    def test_can_render_without_registering(self):
        values = plan()['releases'][0]['values']
        values['register'] = False
        r = self.render(values, 'serve')
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertNotIn('kind: InferenceEndpoint', r.stdout)


if __name__ == '__main__':
    unittest.main()
