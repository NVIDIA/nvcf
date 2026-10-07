#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Build a new local image set and publish its shared Helm values after preload."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
VALUES = HERE / 'values.yaml'
spec = importlib.util.spec_from_file_location('shared_image_builder', HERE.parent / 'build-shared-images.py')
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)


def publish_values(source, target):
    """Publish only portable image settings, preserving the prior file on failure."""
    images.stack.require(not target.is_symlink(), 'Shared values must not be a symlink.')
    saved = json.loads(source.read_text())
    def settings(component):
        image = component['image']
        images.stack.require(isinstance(image, dict) and isinstance(image.get('repository'), str)
                             and image['repository'].startswith('localhost/')
                             and isinstance(image.get('tag'), str) and image['tag'] and image['tag'] != 'latest'
                             and image.get('pullPolicy') == 'Never', 'Expected pinned, preloaded local images.')
        selector = component['nodeSelector']
        images.stack.require(selector.get('kubernetes.io/os') == 'linux'
                             and selector.get('kubernetes.io/arch') in ('arm64', 'amd64'),
                             'Expected Linux ARM64 or AMD64 image placement.')
        return {'image': {key: image[key] for key in ('repository', 'tag', 'pullPolicy')},
                'nodeSelector': {key: selector[key] for key in ('kubernetes.io/os', 'kubernetes.io/arch')}}
    try:
        values = {'gatewayStack': {}}
        for chart, field in [('llm-api-gateway', 'llmApiGateway'), ('llm-request-router', 'llmRequestRouter')]:
            values['gatewayStack'][chart] = {field: settings(saved['gatewayStack'][chart][field])}
        operator = saved['operator']
        values['operator'] = settings(operator)
        values['operator']['watchNamespaces'] = ['llm-stack']
        pylon = settings({'image': operator['pylon']['image'], 'nodeSelector': operator['nodeSelector']})
        values['operator']['pylon'] = {'image': pylon['image']}
    except (KeyError, TypeError, AttributeError) as error:
        raise ValueError('Generated values are missing required image settings.') from error
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode='w', dir=target.parent, prefix='.values-', suffix='.tmp', delete=False) as stream:
            temporary = Path(stream.name)
            stream.write(json.dumps(values, indent=2) + '\n')
            os.fchmod(stream.fileno(), 0o644)
        images.stack.require(not target.is_symlink(), 'Shared values must not be a symlink.')
        os.replace(temporary, target)
    finally:
        if temporary:
            temporary.unlink(missing_ok=True)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--context', help='Override the current kubectl context.')
    parser.add_argument('--namespace', default='llm-stack', help='Image repository prefix; shared values target llm-stack.')
    parser.add_argument('--control-node')
    parser.add_argument('--output-dir', type=Path, help='External parent directory for per-build archives and state.')
    parser.add_argument('--allow-containerd-import', action='store_true',
                        help='Allow image import Jobs to mount the nodes containerd sockets.')
    args = parser.parse_args(argv)
    images.stack.require(args.allow_containerd_import, 'Use --allow-containerd-import to build and preload images.')
    images.stack.require(not VALUES.is_symlink(), 'Shared values must not be a symlink.')
    state = Path(os.environ.get('XDG_STATE_HOME', Path.home() / '.local/state'))
    output = (args.output_dir or state / 'llm-routing/image-builds').expanduser().resolve()
    images.stack.require(not output.is_relative_to(images.stack.REPO), 'Keep image archives and build state outside the checkout.')
    output.mkdir(parents=True, exist_ok=True, mode=0o700)
    work = Path(tempfile.mkdtemp(prefix='build-', dir=output))
    command = ['--namespace', args.namespace, '--output-dir', str(work), '--allow-containerd-import']
    for name, value in [('--context', args.context), ('--control-node', args.control_node)]:
        if value is not None:
            command.extend([name, value])
    print('Build state: ' + str(work), flush=True)
    source = images.main(command)
    publish_values(source, VALUES)
    print('Updated shared Helm values: ' + str(VALUES), flush=True)
    print('Commit this values file to share the preloaded image references.', flush=True)
    return VALUES


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        raise SystemExit('Image preparation failed: ' + str(error))
