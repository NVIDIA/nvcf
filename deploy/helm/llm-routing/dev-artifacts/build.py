#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Build and preload images, package charts, and publish shared Helm inputs."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import shutil
import tempfile

HERE = Path(__file__).resolve().parent
VALUES = HERE / 'values.yaml'
CHARTS = HERE / 'charts'
spec = importlib.util.spec_from_file_location('shared_image_builder', HERE / 'image_builder.py')
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)


def publish_values(source, target):
    """Publish only portable image settings, preserving the prior file on failure."""
    images.require(not target.is_symlink(), 'Shared values must not be a symlink.')
    saved = json.loads(source.read_text())
    def settings(component):
        image = component['image']
        images.require(isinstance(image, dict) and isinstance(image.get('repository'), str)
                             and image['repository'].startswith('localhost/')
                             and isinstance(image.get('tag'), str) and image['tag'] and image['tag'] != 'latest'
                             and image.get('pullPolicy') == 'Never', 'Expected pinned, preloaded local images.')
        selector = component['nodeSelector']
        images.require(selector.get('kubernetes.io/os') == 'linux'
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
        pylon = settings({'image': operator['pylon']['image'], 'nodeSelector': operator['nodeSelector']})
        values['operator']['pylon'] = {'image': pylon['image']}
        values['verification'] = settings(saved['verification'])
    except (KeyError, TypeError, AttributeError) as error:
        raise ValueError('Generated values are missing required image settings.') from error
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode='w', dir=target.parent, prefix='.values-', suffix='.tmp', delete=False) as stream:
            temporary = Path(stream.name)
            stream.write(json.dumps(values, indent=2) + '\n')
            os.fchmod(stream.fileno(), 0o644)
        images.require(not target.is_symlink(), 'Shared values must not be a symlink.')
        os.replace(temporary, target)
    finally:
        if temporary:
            temporary.unlink(missing_ok=True)


def publish_preparation(source, charts_source):
    """Publish the chart bundle and restore it if updating shared values fails."""
    images.require(not CHARTS.is_symlink(), 'Shared charts must not be a symlink.')
    images.require(not CHARTS.exists() or CHARTS.is_dir(), 'Shared charts must be a directory.')
    staged = Path(tempfile.mkdtemp(prefix='.charts-', dir=CHARTS.parent))
    backup = None
    try:
        shutil.copytree(charts_source, staged, dirs_exist_ok=True)
        if CHARTS.exists():
            backup = Path(tempfile.mkdtemp(prefix='.charts-old-', dir=CHARTS.parent))
            backup.rmdir()
            os.replace(CHARTS, backup)
        try:
            os.replace(staged, CHARTS)
            publish_values(source, VALUES)
        except BaseException:
            if CHARTS.exists():
                shutil.rmtree(CHARTS)
            if backup and backup.exists():
                os.replace(backup, CHARTS)
            raise
    finally:
        if staged.exists():
            shutil.rmtree(staged)
        if backup and backup.exists():
            shutil.rmtree(backup)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--context', help='Override the current kubectl context.')
    parser.add_argument('--namespace', default='llm-stack', help='Image repository prefix; shared values target llm-stack.')
    parser.add_argument('--control-node')
    directories = parser.add_mutually_exclusive_group()
    directories.add_argument('--output-dir', type=Path, help='External parent directory for per-build archives and state.')
    directories.add_argument('--resume-from', type=Path, help='Resume the printed build directory after an interrupted preparation.')
    parser.add_argument('--allow-containerd-import', action='store_true',
                        help='Allow image import Jobs to mount the nodes containerd sockets.')
    args = parser.parse_args(argv)
    images.require(args.allow_containerd_import, 'Use --allow-containerd-import to build and preload images.')
    images.require(not VALUES.is_symlink(), 'Shared values must not be a symlink.')
    images.require(not CHARTS.is_symlink(), 'Shared charts must not be a symlink.')
    if args.resume_from:
        work = args.resume_from.expanduser().resolve()
        images.require(not work.is_relative_to(images.REPO), 'Keep build state outside the checkout.')
        images.require((work / 'image-build-config.json').is_file(), 'Resume an existing image build directory.')
    else:
        state = Path(os.environ.get('XDG_STATE_HOME', Path.home() / '.local/state'))
        output = (args.output_dir or state / 'llm-routing/image-builds').expanduser().resolve()
        images.require(not output.is_relative_to(images.REPO), 'Keep image archives and build state outside the checkout.')
        output.mkdir(parents=True, exist_ok=True, mode=0o700)
        work = Path(tempfile.mkdtemp(prefix='build-', dir=output))
    print('Build state: ' + str(work), flush=True)
    args.output_dir = work
    source = images.prepare(args)
    charts = Path(tempfile.mkdtemp(prefix='charts-', dir=work))
    subprocess.run(['bash', str(HERE / 'package-charts.sh'), '--output-dir', str(charts)], check=True)
    publish_preparation(source, charts)
    print('Updated shared Helm values: ' + str(VALUES), flush=True)
    print('Updated chart packages: ' + str(CHARTS), flush=True)
    print('Commit dev-artifacts/values.yaml and dev-artifacts/charts together to share this preparation.', flush=True)
    return VALUES


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        raise SystemExit('Image preparation failed: ' + str(error))
