#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Build and preload development images, then emit values for Helm installation."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('shared_image_setup', HERE / 'stack.py')
stack = importlib.util.module_from_spec(spec)
spec.loader.exec_module(stack)


def helm_values(config):
    selector = {'kubernetes.io/os': 'linux', 'kubernetes.io/arch': config['imagePlatform'].split('/')[1]}
    values = {'gatewayStack': {}, 'operator': {
        'clusterId': config['clusterId'], 'watchNamespaces': [config['namespace']],
        'installCRDs': config['installCRDs'], 'image': config['images']['operator'],
        'pylon': {'image': config['images']['pylon']}, 'nodeSelector': selector}}
    for component, chart, field in [('gateway', 'llm-api-gateway', 'llmApiGateway'),
                                    ('router', 'llm-request-router', 'llmRequestRouter')]:
        values['gatewayStack'][chart] = {field: {'image': config['images'][component], 'nodeSelector': selector}}
    return values


def write_new(path, value):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'w') as stream:
        stream.write(json.dumps(value, indent=2) + '\n')


def selected_context(override):
    context = override
    if context is None:
        try:
            context = subprocess.check_output(['kubectl', 'config', 'current-context'],
                                              text=True, stderr=subprocess.PIPE, timeout=30).strip()
        except (OSError, subprocess.SubprocessError) as error:
            raise ValueError('Could not read the current kubectl context. Select one with kubectl config use-context NAME, or pass --context.') from error
    stack.require(isinstance(context, str) and context.strip() and not context.strip().startswith('-'),
                  'No valid Kubernetes context is selected. Select one with kubectl config use-context NAME, or pass --context.')
    return context.strip()


def prepare(args):
    context = selected_context(args.context)
    stack.require(stack.dns(args.namespace), 'Set a DNS label for --namespace.')
    stack.require(args.allow_containerd_import, 'Use --allow-containerd-import to build and preload images on the selected nodes.')
    output = args.output_dir.expanduser().resolve()
    stack.require(not output.is_relative_to(stack.REPO), 'Keep image archives and generated values outside the checkout.')
    configuration = output / 'image-build-config.json'
    values_path = output / 'shared.values.yaml'
    stack.require(not configuration.is_symlink() and not values_path.is_symlink(), 'Image build configuration and values must not be symlinks.')
    if configuration.exists():
        saved = json.loads(configuration.read_text())
        stack.require(saved.get('schemaVersion') == 1 and saved.get('kind') == 'llm-shared-image-build',
                      'Output directory contains another image build configuration.')
        config = stack.validate_config(saved['config'])
        stack.require(config['context'] == context and config['namespace'] == args.namespace,
                      'Context or namespace differs from the saved image build. Use the original arguments or a new output directory.')
        stack.require(not args.control_node or config['controlNode'] == args.control_node,
                      'Control node differs from the saved image build.')
    else:
        stack.require(not values_path.exists() and not (output / 'image-preparation').exists(),
                      'Output directory contains image preparation artifacts without their original configuration.')
        config = stack.discover_config(context, args.namespace, args.control_node, build_images=True)
    stack.require(config.get('imagePlatform') in ('linux/arm64', 'linux/amd64'), 'Development builds support linux/arm64 and linux/amd64.')
    stack.require(all(image['repository'].startswith('localhost/') and image['pullPolicy'] == 'Never'
                      for image in config['images'].values()), 'Use the original generated localhost images with Never pull policy.')
    values = helm_values(config)
    if values_path.exists():
        stack.require(json.loads(values_path.read_text()) == values, 'Generated shared.values.yaml was changed. Preserve it and use a new output directory.')
    output.mkdir(parents=True, exist_ok=True, mode=0o700)
    if not configuration.exists():
        write_new(configuration, {'schemaVersion': 1, 'kind': 'llm-shared-image-build', 'config': config})
    print('Image build configuration: ' + str(configuration), flush=True)
    stack.prepare_images(config, output, allow_containerd_import=True)
    if not values_path.exists():
        write_new(values_path, values)
    print('Images are ready for Helm installation.', flush=True)
    print('Helm values: ' + str(values_path), flush=True)
    return values_path


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--context', help='Override the current kubectl context.')
    parser.add_argument('--namespace', default='llm-stack')
    parser.add_argument('--control-node')
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--allow-containerd-import', action='store_true',
                        help='Allow temporary image preparation Jobs to mount the nodes containerd sockets.')
    return prepare(parser.parse_args(argv))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        raise SystemExit('Image preparation failed: ' + str(error))
