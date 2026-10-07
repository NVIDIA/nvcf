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


# Server settings that depend on GPU memory. Recipes set defaults per memory type.
TUNING_KEYS = ('contextPerSlot', 'slots', 'batchSize', 'ubatchSize', 'defaultMaxTokens', 'threads')
# llama.cpp rounds each slot's context up to a multiple of this.
CONTEXT_STEP = 256


def check_tuning(tuning, partial=False):
    """partial checks only names and types, for an override merged over recipe defaults."""
    if not isinstance(tuning, dict):
        raise ValueError('tuning must be an object.')
    unknown = sorted(set(tuning) - set(TUNING_KEYS))
    if unknown:
        raise ValueError('tuning may set only ' + ', '.join(TUNING_KEYS) + '. Unknown: ' + ', '.join(unknown) + '.')
    missing = [key for key in TUNING_KEYS if key not in tuning]
    if missing and not partial:
        raise ValueError('tuning is missing: ' + ', '.join(missing) + '.')
    for key, value in tuning.items():
        if isinstance(value, bool) or not isinstance(value, int) or value < 1:
            raise ValueError('tuning.' + key + ' must be a positive integer.')
    if partial:
        return
    if tuning['contextPerSlot'] % CONTEXT_STEP:
        raise ValueError('tuning.contextPerSlot must be a multiple of 256. llama.cpp rounds it up to one.')
    if tuning['ubatchSize'] > tuning['batchSize']:
        raise ValueError('tuning.ubatchSize must not exceed tuning.batchSize.')
    if tuning['defaultMaxTokens'] >= tuning['contextPerSlot']:
        raise ValueError('tuning.defaultMaxTokens must be smaller than tuning.contextPerSlot.')


def server_tuning(recipe, gpu, override=None):
    """Recipe defaults for the GPU memory type, with the saved configuration's overrides."""
    tuning = dict(recipe['tuning']['unified' if gpu['unifiedMemory'] else 'discrete'])
    tuning.update(override or {})
    return tuning


def tuning_args(tuning):
    # llama.cpp divides --ctx-size equally between the --parallel slots.
    return ['--ctx-size', str(tuning['contextPerSlot'] * tuning['slots']), '--parallel', str(tuning['slots']),
            '--batch-size', str(tuning['batchSize']), '--ubatch-size', str(tuning['ubatchSize']),
            '--predict', str(tuning['defaultMaxTokens']), '--threads', str(tuning['threads'])]


def kv_cache_gib(recipe, tuning):
    return recipe['kvCacheKiBPerToken'] * tuning['contextPerSlot'] * tuning['slots'] / 1024**2


def memory_plan(recipe, gpu, count, tuning):
    """Return per-node fit, pod memory in GiB and the free-memory thresholds checked before loading.

    needGiB is compared with capacityGiB: the pod memory limit with unified memory, GPU memory otherwise.
    """
    if count not in MODEL_NODE_COUNTS:
        raise ValueError('The recipe supports 1 or 2 model nodes, not ' + str(count) + '.')
    # A layer split puts each layer's KV cache on the node that holds the layer.
    share = (recipe['lock']['weightGiB'] + kv_cache_gib(recipe, tuning)) / count
    if gpu['unifiedMemory']:
        rules = recipe['memory']['unified']
        limit = math.ceil(share) + rules['limitOverShareGiB']
        capacity = gpu['memoryGiB'] - rules['reservedGiB']
        return {'fits': limit <= capacity, 'needGiB': limit, 'capacityGiB': capacity,
                'requestGiB': math.floor(share) - rules['requestUnderShareGiB'], 'limitGiB': limit,
                'hostAvailableGiB': math.ceil(share) + rules['checkOverShareGiB'], 'gpuFreeGiB': None}
    rules = recipe['memory']['discrete']
    gpu_need = math.ceil(share) + rules['gpuHeadroomGiB']
    return {'fits': gpu_need <= gpu['memoryGiB'], 'needGiB': gpu_need, 'capacityGiB': gpu['memoryGiB'],
            'requestGiB': rules['hostRequestGiB'], 'limitGiB': rules['hostLimitGiB'],
            'hostAvailableGiB': rules['hostLimitGiB'], 'gpuFreeGiB': gpu_need}


def largest_context(recipe, gpu, count, tuning, capacity=None):
    """Largest contextPerSlot up to the configured one that fits capacity (GiB, default the plan's); 0 if none."""
    context = tuning['contextPerSlot'] - tuning['contextPerSlot'] % CONTEXT_STEP
    while context >= CONTEXT_STEP:
        plan = memory_plan(recipe, gpu, count, dict(tuning, contextPerSlot=context))
        if plan['needGiB'] <= (plan['capacityGiB'] if capacity is None else capacity):
            return context
        context -= CONTEXT_STEP
    return 0


def describe_tuning(tuning):
    return str(tuning['contextPerSlot']) + ' tokens per slot and ' + str(tuning['slots']) + ' slot' + ('s' if tuning['slots'] != 1 else '')


def model_node_count(recipe, gpu, tuning):
    for count in MODEL_NODE_COUNTS:
        if memory_plan(recipe, gpu, count, tuning)['fits']:
            return count
    plan = memory_plan(recipe, gpu, MODEL_NODE_COUNTS[-1], tuning)
    raise ValueError(recipe['name'] + ' does not fit on ' + str(MODEL_NODE_COUNTS[-1]) + ' nodes of ' + gpu['name'] +
                     ' with ' + describe_tuning(tuning) + ': each needs ' + str(plan['needGiB']) + ' GiB, and ' +
                     str(round(plan['capacityGiB'], 1)) + ' GiB is available.')


def placement_args(count):
    devices = ['CUDA0'] + ['RPC' + str(index) for index in range(count - 1)]
    args = ['--device', ','.join(devices)]
    if count > 1:
        args += ['--tensor-split', ','.join(['1'] * count)]
    return args
