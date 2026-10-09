#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Build and preload development images, then emit values for Helm installation."""
import datetime
import hashlib
import importlib.util
import json
import re
import os
import secrets
from pathlib import Path
import subprocess
import tarfile
import tempfile

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[3]
spec = importlib.util.spec_from_file_location('development_image_preload', HERE / 'image_preload.py')
preload = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preload)
COMPONENTS = tuple(preload.image_tools.COMPONENTS)
require = preload.require


def dns(value):
    return isinstance(value, str) and len(value) <= 53 and re.fullmatch('[a-z0-9]([-a-z0-9]*[a-z0-9])?', value)


def validate_config(config):
    require(isinstance(config, dict), 'Image configuration must be an object.')
    for field in ('context', 'controlNode'):
        require(isinstance(config.get(field), str) and config[field].strip() and not config[field].startswith('-'),
                'Set ' + field + '.')
    require(dns(config.get('namespace')), 'Set a DNS label for namespace.')
    require(config.get('imagePlatform') in ('linux/arm64', 'linux/amd64'), 'Select a supported image platform.')
    require(isinstance(config.get('images'), dict) and set(config['images']) == set(COMPONENTS),
            'Bind all four routing image components.')
    require(isinstance(config.get('containerd'), dict), 'Bind image preload nodes and their UIDs.')
    for image in config['images'].values():
        require(isinstance(image, dict) and isinstance(image.get('repository'), str)
                and image['repository'].startswith('localhost/') and not any(c.isspace() for c in image['repository'])
                and isinstance(image.get('tag'), str) and re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', image['tag'])
                and image['tag'] != 'latest' and image.get('pullPolicy') == 'Never',
                'Development builds require pinned localhost images with Never pull policy.')
    return config


def discover_config(context, namespace, control_node=None, architecture=None):
    """Bind development image loading to compatible node identities, without stack inspection."""
    nodes = json.loads(subprocess.check_output(['kubectl', '--context', context, 'get', 'nodes', '-o', 'json'],
                                              text=True, timeout=60))['items']
    def schedulable(node):
        labels = node.get('metadata', {}).get('labels', {})
        conditions = {item['type']: item['status'] for item in node.get('status', {}).get('conditions', [])}
        return (conditions.get('Ready') == 'True' and not node.get('metadata', {}).get('deletionTimestamp')
                and labels.get('kubernetes.io/os') == 'linux'
                and labels.get('kubernetes.io/arch') in ('arm64', 'amd64')
                and not node.get('spec', {}).get('unschedulable')
                and not any(conditions.get(name) == 'True' for name in ('MemoryPressure', 'DiskPressure', 'PIDPressure'))
                and not any(taint.get('effect') in ('NoSchedule', 'NoExecute') for taint in node.get('spec', {}).get('taints', [])))
    choices = sorted((node for node in nodes if schedulable(node)
                      and (not architecture or node['metadata']['labels']['kubernetes.io/arch'] == architecture)
                      and (not control_node or node['metadata']['name'] == control_node)), key=lambda node: node['metadata']['name'])
    require(choices, 'Choose a Ready, schedulable Linux routing node with --control-node.')
    control = choices[0]
    architecture = control['metadata']['labels']['kubernetes.io/arch']
    targets = sorted((node for node in nodes if schedulable(node)
                      and node['metadata']['labels']['kubernetes.io/arch'] == architecture), key=lambda node: node['metadata']['name'])
    require(all(node['metadata'].get('uid') for node in targets), 'Image target nodes must have stable UIDs.')
    containerd = {'archiveNode': control['metadata']['name'], 'runAsUser': 1000,
                  'nodeNames': [node['metadata']['name'] for node in targets],
                  'nodeUIDs': {node['metadata']['name']: node['metadata']['uid'] for node in targets}}
    if all(re.search(r'[+-]k3s\d*', node.get('status', {}).get('nodeInfo', {}).get('kubeletVersion', ''))
           and node.get('status', {}).get('nodeInfo', {}).get('containerRuntimeVersion', '').startswith('containerd://') for node in targets):
        containerd['socketPath'] = '/run/k3s/containerd/containerd.sock'
    tag = 'dev-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d%H%M%S') + '-' + secrets.token_hex(3)
    return validate_config({'context': context, 'namespace': namespace, 'controlNode': control['metadata']['name'],
                            'imagePlatform': 'linux/' + architecture, 'containerd': containerd,
                            'images': {name: {'repository': 'localhost/' + namespace + '/' + name,
                                             'tag': tag, 'pullPolicy': 'Never'} for name in COMPONENTS}})


def helm_values(config):
    selector = {'kubernetes.io/os': 'linux', 'kubernetes.io/arch': config['imagePlatform'].split('/')[1]}
    values = {'gatewayStack': {}, 'operator': {
        'image': config['images']['operator'],
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
    require(isinstance(context, str) and context.strip() and not context.strip().startswith('-'),
            'No valid Kubernetes context is selected. Select one with kubectl config use-context NAME, or pass --context.')
    return context.strip()


def read_values(path):
    """Read JSON or YAML values without requiring an additional Python package."""
    path = path.expanduser().resolve(strict=True)
    try:
        return json.loads(path.read_text())
    except json.JSONDecodeError:
        with tempfile.TemporaryDirectory(prefix='llm-image-values-') as directory:
            chart = Path(directory)
            (chart / 'Chart.yaml').write_text('apiVersion: v2\nname: image-values-reader\nversion: 0.1.0\n')
            (chart / 'templates').mkdir()
            (chart / 'templates/values.yaml').write_text('{{ .Values | toJson }}\n')
            rendered = subprocess.check_output(['helm', 'template', 'image-values-reader', str(chart),
                                                '--values', str(path)], text=True, stderr=subprocess.PIPE, timeout=30)
        return json.loads('\n'.join(line for line in rendered.splitlines()
                                    if line.strip() and not line.startswith(('#', '---'))))


def preload_settings(values):
    """Extract exact local image references and their shared Linux architecture."""
    try:
        operator = values['operator']
        components = {'operator': operator,
                      'pylon': dict(operator['pylon'], nodeSelector=operator.get('nodeSelector', {}))}
        for name, chart, field in [('gateway', 'llm-api-gateway', 'llmApiGateway'),
                                  ('router', 'llm-request-router', 'llmRequestRouter')]:
            components[name] = values['gatewayStack'][chart][field]
        local = {name: item for name, item in components.items() if item['image'].get('pullPolicy') == 'Never'}
        if not local:
            return {}, None
        architectures = {item.get('nodeSelector', {}).get('kubernetes.io/arch') for item in local.values()}
        require(len(architectures) == 1 and architectures <= {'arm64', 'amd64'},
                'Load-only values must select one shared image architecture: arm64 or amd64.')
        require(all(item.get('nodeSelector', {}).get('kubernetes.io/os') == 'linux' for item in local.values()),
                'Load-only values must select Linux nodes.')
        images = {}
        for name, item in local.items():
            image = item['image']
            repository, tag = image['repository'], image['tag']
            registry = image.get('registry')
            if registry:
                require(isinstance(registry, str), 'Image registry must be a string.')
                repository = registry.rstrip('/') + '/' + repository
            require(isinstance(repository, str) and '/' in repository and not repository.startswith('-')
                    and not any(c.isspace() for c in repository) and '@' not in repository,
                    'Set a valid image repository for ' + name + '.')
            require(isinstance(tag, str) and re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', tag)
                    and tag != 'latest', 'Set a pinned image tag for ' + name + '.')
            images[name] = {'repository': repository, 'tag': tag, 'pullPolicy': 'Never'}
        return images, next(iter(architectures))
    except (KeyError, TypeError, AttributeError) as error:
        raise ValueError('Values must contain image settings for gateway, router, operator and Pylon.') from error


def archive_matches(path, references, platform):
    """Require every selected tag to have the selected platform in a Docker archive."""
    with tarfile.open(path) as archive:
        tags = set()
        other_platform_tags = set()
        for entry in json.load(archive.extractfile('manifest.json')):
            config = json.load(archive.extractfile(entry['Config']))
            if config.get('os') == 'linux' and config.get('architecture') == platform.split('/')[1]:
                tags.update(entry.get('RepoTags') or [])
            else:
                other_platform_tags.update(entry.get('RepoTags') or [])
    return all(bool(preload.image_aliases(image) & tags)
               and not (preload.image_aliases(image) & other_platform_tags) for image in references)


def matching_archive(directory, references, platform):
    """Find selected image references in locally saved build output."""
    saved = json.loads((directory / 'image-build-config.json').read_text())
    require(saved.get('schemaVersion') == 1 and saved.get('kind') == 'llm-shared-image-build',
            'Expected a saved development image build.')
    config = validate_config(saved['config'])
    require(config['imagePlatform'] == platform, 'Saved build architecture differs from the selected values.')
    saved_references = {image['repository'] + ':' + image['tag'] for image in config['images'].values()}
    require(all(preload.image_aliases(image) & saved_references for image in references),
            'Saved build image tags differ from the selected values.')
    archive = directory / 'image-preparation/images.tar'
    require(archive_matches(archive, references, platform),
            'Saved archive does not contain the selected image tags and architecture.')
    return archive.resolve()


def load(args):
    local, architecture = preload_settings(read_values(args.values))
    if not local:
        print('Image pulls are enabled. No development images require preloading.', flush=True)
        return args.values
    context = selected_context(args.context)
    require(dns(args.namespace), 'Set a DNS label for --namespace.')
    config = discover_config(context, args.namespace, args.control_node, architecture=architecture)
    config['images'] = local
    references = [image['repository'] + ':' + image['tag'] for image in local.values()]
    def output(command):
        return subprocess.check_output(command, text=True, stderr=subprocess.PIPE, timeout=60)
    missing = preload.missing_nodes(config, output)
    if not missing:
        print('Development images are already listed on all target nodes. No import needed.', flush=True)
        return args.values
    print('Images are not listed on: ' + ', '.join(missing) + '.', flush=True)
    print('Kubelet image inventories can be truncated (normally at 50 entries). Unlisted images are treated as missing.', flush=True)
    require(args.allow_containerd_import, 'Use --allow-containerd-import to preload development images.')
    state = Path(os.environ.get('XDG_STATE_HOME', Path.home() / '.local/state')) / 'llm-routing'
    archive = None
    for candidate in sorted((state / 'image-builds').glob('*/')):
        try:
            archive = matching_archive(candidate, references, config['imagePlatform'])
            break
        except (OSError, ValueError, KeyError, TypeError, AttributeError, tarfile.TarError):
            continue
    instruction = ('No matching image archive for these tags and architecture. Run '
                   'python3 dev-artifacts/build.py --allow-containerd-import to build and load a new image set, '
                   'then use its updated values.')
    require(archive is not None, instruction)
    identity = hashlib.sha256(json.dumps(config, sort_keys=True).encode()).hexdigest()[:24]
    work = (args.output_dir or state / 'image-loads').expanduser().resolve() / identity
    require(not work.is_relative_to(REPO), 'Keep image import state outside the checkout.')
    print('Loading development images from ' + str(archive), flush=True)
    preload.Preparation(config, work, True).execute(archive=archive)
    return args.values


def prepare(args):
    context = selected_context(args.context)
    require(dns(args.namespace), 'Set a DNS label for --namespace.')
    require(args.allow_containerd_import, 'Use --allow-containerd-import to build and preload images on the selected nodes.')
    output = args.output_dir.expanduser().resolve()
    require(not output.is_relative_to(REPO), 'Keep image archives and generated values outside the checkout.')
    configuration = output / 'image-build-config.json'
    values_path = output / 'shared.values.yaml'
    require(not configuration.is_symlink() and not values_path.is_symlink(), 'Image build configuration and values must not be symlinks.')
    if configuration.exists():
        saved = json.loads(configuration.read_text())
        require(saved.get('schemaVersion') == 1 and saved.get('kind') == 'llm-shared-image-build',
                'Output directory contains another image build configuration.')
        config = validate_config(saved['config'])
        require(config['context'] == context and config['namespace'] == args.namespace,
                'Context or namespace differs from the saved image build. Use the original arguments or a new output directory.')
        require(not args.control_node or config['controlNode'] == args.control_node,
                'Control node differs from the saved image build.')
    else:
        require(not values_path.exists() and not (output / 'image-preparation').exists(),
                'Output directory contains image preparation artifacts without their original configuration.')
        config = discover_config(context, args.namespace, args.control_node)
    values = helm_values(config)
    if values_path.exists():
        require(json.loads(values_path.read_text()) == values, 'Generated shared.values.yaml was changed. Preserve it and use a new output directory.')
    output.mkdir(parents=True, exist_ok=True, mode=0o700)
    if not configuration.exists():
        write_new(configuration, {'schemaVersion': 1, 'kind': 'llm-shared-image-build', 'config': config})
    print('Image build configuration: ' + str(configuration), flush=True)
    preload.prepare(config, output, allow_containerd_import=True)
    if not values_path.exists():
        write_new(values_path, values)
    print('Images are ready for Helm installation.', flush=True)
    print('Helm values: ' + str(values_path), flush=True)
    return values_path
