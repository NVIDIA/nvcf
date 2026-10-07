# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import json
import pathlib
import unittest

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('recipe_sizing', HERE/'sizing.py')
sizing = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sizing)

GLM = json.loads((HERE/'glm-5.3/recipe.json').read_text())
GLM['lock'] = json.loads((HERE/'glm-5.3/model.lock.json').read_text())
GB10 = {'name': 'NVIDIA GB10', 'computeCapability': '12.1', 'memoryGiB': 121.6, 'unifiedMemory': True, 'cudaArchitectures': None}
GB300 = {'name': 'NVIDIA GB300', 'computeCapability': '10.3', 'memoryGiB': 268.0, 'unifiedMemory': False, 'cudaArchitectures': None}


class ArchitectureTests(unittest.TestCase):
    def test_compute_capability_becomes_an_arch_specific_cmake_target(self):
        for capability, expected in (('10.3', '103a-real'), ('12.1', '121a-real'), ('9.0', '90a-real'), ('10.0', '100a-real')):
            with self.subTest(capability=capability):
                self.assertEqual(sizing.cuda_architectures(dict(GB300, computeCapability=capability)), expected)

    def test_explicit_architectures_pass_through_verbatim(self):
        self.assertEqual(sizing.cuda_architectures(dict(GB300, cudaArchitectures='103f;120a-real')), '103f;120a-real')

    def test_malformed_compute_capability_is_rejected(self):
        for capability in ('103', '10.10', 'ten.three', '', '10.3.1', 10.3):
            with self.subTest(capability=capability), self.assertRaisesRegex(ValueError, 'computeCapability'):
                sizing.check_gpu(dict(GB300, computeCapability=capability))

    def test_gpu_product_label_replaces_spaces(self):
        self.assertEqual(sizing.gpu_product(GB300), 'NVIDIA-GB300')
        self.assertEqual(sizing.gpu_product(dict(GB300, name='  NVIDIA   GB10 ')), 'NVIDIA-GB10')

    def test_gpu_settings_are_validated(self):
        sizing.check_gpu(GB300)
        for change in ({'name': ''}, {'memoryGiB': 0}, {'memoryGiB': 'big'}, {'unifiedMemory': 'no'}, {'cudaArchitectures': 103}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                sizing.check_gpu(dict(GB300, **change))


UNIFIED = GLM['tuning']['unified']
DISCRETE = GLM['tuning']['discrete']
# Small numbers that are easy to follow: 64 KiB per token, so 16384 tokens use 1 GiB.
SYNTHETIC = {'name': 'synthetic', 'lock': {'weightGiB': 100.0}, 'kvCacheKiBPerToken': 64,
             'memory': {'unified': {'reservedGiB': 4, 'checkOverShareGiB': 1, 'requestUnderShareGiB': 1, 'limitOverShareGiB': 2},
                        'discrete': {'gpuHeadroomGiB': 5, 'hostRequestGiB': 32, 'hostLimitGiB': 64}},
             'tuning': GLM['tuning']}
TUNING = {'contextPerSlot': 8192, 'slots': 2, 'batchSize': 512, 'ubatchSize': 512, 'defaultMaxTokens': 1024, 'threads': 8}


class MemoryTests(unittest.TestCase):
    def test_spark_split_reproduces_the_original_hard_coded_numbers(self):
        plan = sizing.memory_plan(GLM, GB10, 2, UNIFIED)
        self.assertTrue(plan['fits'])
        self.assertEqual((plan['requestGiB'], plan['limitGiB'], plan['hostAvailableGiB']), (110, 114, 113))
        self.assertIsNone(plan['gpuFreeGiB'])

    def test_glm_does_not_fit_on_one_gb10(self):
        self.assertFalse(sizing.memory_plan(GLM, GB10, 1, UNIFIED)['fits'])

    def test_glm_default_tuning_passes_preflight_on_a_measured_gb300(self):
        # nvidia-smi reports 250.7 GiB; CUDA reported 249.0 GiB free before loading.
        plan = sizing.memory_plan(GLM, dict(GB300, memoryGiB=250.7), 1, DISCRETE)
        self.assertTrue(plan['fits'])
        self.assertLessEqual(plan['gpuFreeGiB'], 249)
        self.assertEqual((plan['requestGiB'], plan['limitGiB'], plan['hostAvailableGiB']), (32, 64, 64))

    def test_model_node_count_is_the_smallest_that_fits(self):
        self.assertEqual(sizing.model_node_count(GLM, GB300, DISCRETE), 1)
        self.assertEqual(sizing.model_node_count(GLM, GB10, UNIFIED), 2)

    def test_model_that_fits_nowhere_reports_the_requirement(self):
        small = dict(GB300, memoryGiB=80.0)
        with self.assertRaisesRegex(ValueError, 'does not fit on 2 nodes .* 65536 tokens per slot and 2 slots'):
            sizing.model_node_count(GLM, small, DISCRETE)

    def test_node_count_outside_the_supported_range_is_rejected(self):
        for count in (0, 3):
            with self.subTest(count=count), self.assertRaisesRegex(ValueError, '1 or 2'):
                sizing.memory_plan(GLM, GB300, count, DISCRETE)


class KvCacheMemoryTests(unittest.TestCase):
    def test_kv_cache_covers_every_slot(self):
        self.assertEqual(sizing.kv_cache_gib(SYNTHETIC, TUNING), 1.0)
        self.assertEqual(sizing.kv_cache_gib(SYNTHETIC, dict(TUNING, slots=4)), 2.0)

    def test_discrete_gpu_needs_weights_kv_cache_and_headroom(self):
        plan = sizing.memory_plan(SYNTHETIC, dict(GB300, memoryGiB=200.0), 1, TUNING)
        self.assertEqual((plan['gpuFreeGiB'], plan['needGiB'], plan['capacityGiB']), (106, 106, 200.0))
        self.assertTrue(plan['fits'])

    def test_split_divides_weights_and_kv_cache_across_nodes(self):
        plan = sizing.memory_plan(SYNTHETIC, dict(GB300, memoryGiB=200.0), 2, dict(TUNING, slots=4))
        self.assertEqual(plan['gpuFreeGiB'], 51 + 5)

    def test_unified_memory_limits_include_the_kv_cache(self):
        plan = sizing.memory_plan(SYNTHETIC, GB10, 1, dict(TUNING, contextPerSlot=32768))
        self.assertEqual((plan['requestGiB'], plan['limitGiB'], plan['hostAvailableGiB']), (103, 106, 105))
        self.assertEqual((plan['needGiB'], plan['capacityGiB']), (106, 121.6 - 4))

    def test_a_larger_context_can_stop_fitting(self):
        gpu = dict(GB300, memoryGiB=110.0)
        self.assertTrue(sizing.memory_plan(SYNTHETIC, gpu, 1, TUNING)['fits'])
        self.assertFalse(sizing.memory_plan(SYNTHETIC, gpu, 1, dict(TUNING, contextPerSlot=131072))['fits'])

    def test_largest_context_that_fits_the_configured_gpu(self):
        # 105 GiB for weights and cache leaves 5 GiB: 81920 tokens, or 40960 per slot.
        gpu = dict(GB300, memoryGiB=110.0)
        self.assertEqual(sizing.largest_context(SYNTHETIC, gpu, 1, dict(TUNING, contextPerSlot=65536)), 40960)

    def test_largest_context_that_fits_measured_free_memory(self):
        gpu = dict(GB300, memoryGiB=110.0)
        self.assertEqual(sizing.largest_context(SYNTHETIC, gpu, 1, dict(TUNING, contextPerSlot=65536), 107.0), 16384)

    def test_largest_context_never_exceeds_the_configured_context(self):
        self.assertEqual(sizing.largest_context(SYNTHETIC, GB300, 1, TUNING), 8192)

    def test_largest_context_is_zero_when_the_weights_alone_do_not_fit(self):
        self.assertEqual(sizing.largest_context(SYNTHETIC, dict(GB300, memoryGiB=100.0), 1, TUNING), 0)


class TuningTests(unittest.TestCase):
    def test_defaults_follow_the_gpu_memory_type(self):
        self.assertEqual(sizing.server_tuning(GLM, GB10), UNIFIED)
        self.assertEqual(sizing.server_tuning(GLM, GB300), DISCRETE)

    def test_saved_configuration_overrides_single_defaults(self):
        tuning = sizing.server_tuning(GLM, GB300, {'contextPerSlot': 32768})
        self.assertEqual(tuning, dict(DISCRETE, contextPerSlot=32768))
        self.assertEqual(DISCRETE, GLM['tuning']['discrete'])

    def test_spark_defaults_reproduce_the_original_server_arguments(self):
        self.assertEqual(sizing.tuning_args(UNIFIED), ['--ctx-size', '2048', '--parallel', '1', '--batch-size', '128',
                                                       '--ubatch-size', '128', '--predict', '512', '--threads', '8'])

    def test_context_is_per_slot(self):
        args = sizing.tuning_args(dict(TUNING, contextPerSlot=65536, slots=2))
        self.assertEqual(args[args.index('--ctx-size')+1], '131072')
        self.assertEqual(args[args.index('--parallel')+1], '2')

    def test_recipe_defaults_are_valid(self):
        for kind in ('unified', 'discrete'):
            with self.subTest(kind=kind):
                sizing.check_tuning(GLM['tuning'][kind])

    def test_invalid_tuning_is_rejected(self):
        cases = [
            ('may set only', dict(TUNING, ctxSize=4096)),
            ('missing: threads', {k: v for k, v in TUNING.items() if k != 'threads'}),
            ('positive integer', dict(TUNING, slots=0)),
            ('positive integer', dict(TUNING, slots=True)),
            ('positive integer', dict(TUNING, contextPerSlot='8192')),
            ('positive integer', dict(TUNING, batchSize=512.0)),
            ('multiple of 256', dict(TUNING, contextPerSlot=8000)),
            ('ubatchSize must not exceed', dict(TUNING, ubatchSize=1024)),
            ('defaultMaxTokens must be smaller', dict(TUNING, defaultMaxTokens=8192)),
            ('must be an object', [TUNING]),
        ]
        for message, tuning in cases:
            with self.subTest(message=message), self.assertRaisesRegex(ValueError, message):
                sizing.check_tuning(tuning)

    def test_partial_override_checks_names_and_types_only(self):
        sizing.check_tuning({'slots': 3}, partial=True)
        for tuning in ({'slots': 0}, {'ctxSize': 4096}):
            with self.subTest(tuning=tuning), self.assertRaises(ValueError):
                sizing.check_tuning(tuning, partial=True)


class PlacementArgumentTests(unittest.TestCase):
    def test_single_node_uses_only_the_local_gpu(self):
        self.assertEqual(sizing.placement_args(1), ['--device', 'CUDA0'])

    def test_split_adds_one_rpc_device_per_worker_and_an_equal_split(self):
        self.assertEqual(sizing.placement_args(2), ['--device', 'CUDA0,RPC0', '--tensor-split', '1,1'])
        self.assertEqual(sizing.placement_args(3), ['--device', 'CUDA0,RPC0,RPC1', '--tensor-split', '1,1,1'])


if __name__ == '__main__':
    unittest.main()
