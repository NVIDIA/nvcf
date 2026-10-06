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


class MemoryTests(unittest.TestCase):
    def test_spark_split_reproduces_the_original_hard_coded_numbers(self):
        plan = sizing.memory_plan(GLM, GB10, 2)
        self.assertTrue(plan['fits'])
        self.assertEqual((plan['requestGiB'], plan['limitGiB'], plan['hostAvailableGiB']), (110, 114, 113))
        self.assertIsNone(plan['gpuFreeGiB'])

    def test_glm_does_not_fit_on_one_gb10(self):
        self.assertFalse(sizing.memory_plan(GLM, GB10, 1)['fits'])

    def test_glm_fits_on_one_gb300(self):
        plan = sizing.memory_plan(GLM, GB300, 1)
        self.assertTrue(plan['fits'])
        self.assertEqual(plan['gpuFreeGiB'], 223 + GLM['memory']['discrete']['gpuHeadroomGiB'])
        self.assertEqual((plan['requestGiB'], plan['limitGiB'], plan['hostAvailableGiB']), (32, 64, 64))

    def test_model_node_count_is_the_smallest_that_fits(self):
        self.assertEqual(sizing.model_node_count(GLM, GB300), 1)
        self.assertEqual(sizing.model_node_count(GLM, GB10), 2)

    def test_model_that_fits_nowhere_reports_the_requirement(self):
        small = dict(GB300, memoryGiB=80.0)
        with self.assertRaisesRegex(ValueError, 'does not fit on 2'):
            sizing.model_node_count(GLM, small)

    def test_node_count_outside_the_supported_range_is_rejected(self):
        for count in (0, 3):
            with self.subTest(count=count), self.assertRaisesRegex(ValueError, '1 or 2'):
                sizing.memory_plan(GLM, GB300, count)


class PlacementArgumentTests(unittest.TestCase):
    def test_single_node_uses_only_the_local_gpu(self):
        self.assertEqual(sizing.placement_args(1), ['--device', 'CUDA0'])

    def test_split_adds_one_rpc_device_per_worker_and_an_equal_split(self):
        self.assertEqual(sizing.placement_args(2), ['--device', 'CUDA0,RPC0', '--tensor-split', '1,1'])
        self.assertEqual(sizing.placement_args(3), ['--device', 'CUDA0,RPC0,RPC1', '--tensor-split', '1,1,1'])


if __name__ == '__main__':
    unittest.main()
