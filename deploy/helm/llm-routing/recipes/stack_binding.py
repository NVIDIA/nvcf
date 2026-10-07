# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Read-only binding between a model recipe and separately managed infrastructure."""
import copy
import importlib.util
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent
_stack = None


def stack_module():
    global _stack
    if _stack is None:
        spec = importlib.util.spec_from_file_location('llm_shared_stack', HERE.parent/'stack.py')
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        _stack = module
    return _stack


def load_connection(path):
    try:
        return stack_module().load_connection(path)
    except (OSError, ValueError, KeyError, TypeError) as error:
        raise RuntimeError('Cannot load shared stack connection: ' + str(error)) from error


def inspect_connection(connection):
    try:
        return stack_module().inspect_connection(connection)
    except (OSError, ValueError, KeyError, TypeError) as error:
        raise RuntimeError('Cannot verify shared stack connection: ' + str(error)) from error


def overlay(model, connection):
    """Bind shared fields without copying shared images, credentials or TLS keys."""
    result = copy.deepcopy(model)
    shared = {key: connection[key] for key in ('context', 'namespace', 'clusterId', 'caConfigMap', 'apiKeyFile')}
    for key, value in shared.items():
        if key in result and result[key] != value:
            raise RuntimeError('Model configuration differs from the stack connection: ' + key)
        result[key] = value
    nodes = result.setdefault('nodes', {})
    if 'control' in nodes and nodes['control'] != connection['controlNode']:
        raise RuntimeError('Model routing node differs from the stack connection.')
    nodes['control'] = connection['controlNode']
    releases = result.setdefault('releases', {})
    for field, key in (('stack', 'stackRelease'), ('operator', 'operatorRelease')):
        if field in releases and releases[field] != connection[key]:
            raise RuntimeError('Model release mapping differs from the stack connection: ' + field)
        releases[field] = connection[key]
    if 'externalStack' in result and result['externalStack'] != connection:
        raise RuntimeError('The model configuration is already bound to another stack connection.')
    result['externalStack'] = copy.deepcopy(connection)
    return result


def validate(config):
    connection = config['externalStack']
    try:
        stack_module().validate_connection(connection)
    except (ValueError, KeyError, TypeError) as error:
        raise RuntimeError('Invalid shared stack connection: ' + str(error)) from error
    if overlay(config, connection) != config:
        raise RuntimeError('Model configuration must preserve its shared stack binding.')
