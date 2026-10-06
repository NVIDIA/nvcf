# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""GPU-derived settings and memory sizing for recipe placement. No cluster access."""
import math
import re

# The data model and charts handle any count; only these are exercised on hardware.
MODEL_NODE_COUNTS = (1, 2)


def check_gpu(gpu):
    capability = gpu.get('computeCapability')
    # NVIDIA minor versions are a single digit, so "10.3" is unambiguous as "103".
    if not isinstance(capability, str) or re.fullmatch(r'\d+\.\d', capability) is None:
        raise ValueError('gpu.computeCapability must look like "10.3".')
    if not isinstance(gpu.get('name'), str) or not gpu['name'].strip():
        raise ValueError('gpu.name must be the GPU name reported by nvidia-smi.')
    memory = gpu.get('memoryGiB')
    if isinstance(memory, bool) or not isinstance(memory, (int, float)) or memory <= 0:
        raise ValueError('gpu.memoryGiB must be a positive number.')
    if not isinstance(gpu.get('unifiedMemory'), bool):
        raise ValueError('gpu.unifiedMemory must be true or false.')
    override = gpu.get('cudaArchitectures')
    if override is not None and (not isinstance(override, str) or not override.strip()):
        raise ValueError('gpu.cudaArchitectures must be null or a CMake architecture list.')


def cuda_architectures(gpu):
    if gpu.get('cudaArchitectures'):
        return gpu['cudaArchitectures']
    major, minor = gpu['computeCapability'].split('.')
    return str(int(major)) + str(int(minor)) + 'a-real'


def gpu_product(gpu):
    return re.sub(r'\s+', '-', gpu['name'].strip())


def memory_plan(recipe, gpu, count):
    """Return per-node fit, pod memory in GiB and the free-memory thresholds checked before loading."""
    if count not in MODEL_NODE_COUNTS:
        raise ValueError('The recipe supports 1 or 2 model nodes, not ' + str(count) + '.')
    share = recipe['lock']['weightGiB'] / count
    if gpu['unifiedMemory']:
        rules = recipe['memory']['unified']
        limit = math.ceil(share) + rules['limitOverShareGiB']
        return {'fits': limit <= gpu['memoryGiB'] - rules['reservedGiB'],
                'requestGiB': math.floor(share) - rules['requestUnderShareGiB'], 'limitGiB': limit,
                'hostAvailableGiB': math.ceil(share) + rules['checkOverShareGiB'], 'gpuFreeGiB': None}
    rules = recipe['memory']['discrete']
    gpu_need = math.ceil(share) + rules['gpuHeadroomGiB']
    return {'fits': gpu_need <= gpu['memoryGiB'],
            'requestGiB': rules['hostRequestGiB'], 'limitGiB': rules['hostLimitGiB'],
            'hostAvailableGiB': rules['hostLimitGiB'], 'gpuFreeGiB': gpu_need}


def model_node_count(recipe, gpu):
    for count in MODEL_NODE_COUNTS:
        if memory_plan(recipe, gpu, count)['fits']:
            return count
    plan = memory_plan(recipe, gpu, MODEL_NODE_COUNTS[-1])
    need = plan['gpuFreeGiB'] or plan['limitGiB']
    raise ValueError(recipe['name'] + ' does not fit on ' + str(MODEL_NODE_COUNTS[-1]) + ' nodes of ' + gpu['name'] +
                     ': each needs ' + str(need) + ' GiB, and ' + str(gpu['memoryGiB']) + ' GiB is available.')


def placement_args(count):
    devices = ['CUDA0'] + ['RPC' + str(index) for index in range(count - 1)]
    args = ['--device', ','.join(devices)]
    if count > 1:
        args += ['--tensor-split', ','.join(['1'] * count)]
    return args
