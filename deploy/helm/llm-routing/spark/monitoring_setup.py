# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Read-only attachment to an installed LLM routing stack for monitoring."""
import json
import pathlib
import re
import urllib.parse

import monitoring


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def validate(config):
    for key in ('context', 'namespace', 'releasePrefix', 'clusterId', 'storageClass', 'caConfigMap'):
        require(isinstance(config.get(key), str) and bool(config[key].strip()), key+' must be explicit.')
    for key in ('namespace', 'releasePrefix', 'clusterId'):
        require(re.fullmatch(r'[a-z0-9]([-a-z0-9]*[a-z0-9])?', config[key]) is not None, 'Invalid '+key)
    require(len(config['releasePrefix']) <= 30, 'releasePrefix must be at most 30 characters.')
    require(isinstance(config.get('nodes'), dict) and config['nodes'].get('control'), 'Set nodes.control for monitoring.')
    require(config.get('images', {}).get('pullPolicy') in ('Never', 'IfNotPresent', 'Always'), 'Invalid pull policy.')
    monitoring.settings(config)


def discover_config(context, namespace, output, saved=None):
    """Inspect routing resources without requiring a model or its deployment recipe."""
    require(context, 'Set --context to the Kubernetes context for the existing installation.')
    kc = ['kubectl', '--context', context, '--request-timeout=30s']
    scope = ['-n', namespace] if namespace else ['--all-namespaces']
    deployments = json.loads(output(kc+['get', 'deployments']+scope+['-o', 'json']))['items']
    gateways = [d for d in deployments if d['metadata']['name'] == 'llm-api-gateway'
                and d['metadata'].get('annotations', {}).get('meta.helm.sh/release-name')]
    require(gateways, 'No Helm-managed LLM gateway found. Check --context and --namespace.')
    require(len(gateways) == 1, 'Multiple LLM installations found. Select one with --namespace: '+
            ', '.join(sorted(d['metadata']['namespace'] for d in gateways)))
    gateway = gateways[0]
    namespace = gateway['metadata']['namespace']
    kc += ['-n', namespace]
    hm = ['helm', '--kube-context', context, '-n', namespace]

    def owner(obj):
        metadata = obj['metadata']
        annotations = metadata.get('annotations', {})
        require(metadata.get('namespace') == namespace and annotations.get('meta.helm.sh/release-namespace') == namespace,
                'Unexpected Helm namespace for '+metadata['name'])
        release = annotations.get('meta.helm.sh/release-name')
        require(release, 'Missing Helm ownership for '+metadata['name'])
        return release

    def values(release):
        return json.loads(output(hm+['get', 'values', release, '--all', '-o', 'json']))

    stack = owner(gateway)
    stack_values = values(stack)
    router = json.loads(output(kc+['get', 'deployment', 'llm-request-router', '-o', 'json']))
    require(owner(router) == stack, 'Gateway and router belong to different releases.')
    control = gateway['spec']['template']['spec'].get('nodeSelector', {}).get('kubernetes.io/hostname')
    if not control:
        selector = ','.join(k+'='+v for k, v in sorted(gateway['spec']['selector']['matchLabels'].items()))
        pods = json.loads(output(kc+['get', 'pods', '-l', selector, '-o', 'json']))['items']
        placements = sorted({p.get('spec', {}).get('nodeName') for p in pods
                             if p.get('spec', {}).get('nodeName') and not p['metadata'].get('deletionTimestamp')
                             and any(c['type'] == 'Ready' and c['status'] == 'True' for c in p.get('status', {}).get('conditions', []))})
        require(placements, 'No ready gateway placement found. Wait for the routing stack to become ready.')
        control = placements[0]
    releases = json.loads(output(hm+['list', '-o', 'json']))
    operators = []
    for release in releases:
        if not release['chart'].startswith('pylon-operator-'):
            continue
        config = values(release['name'])
        address = urllib.parse.urlsplit(config.get('router', {}).get('grpcAddress', ''))
        namespaces = config.get('watchNamespaces', [])
        if (config.get('clusterId') == stack_values.get('clusterId')
                and (not namespaces or namespace in namespaces)
                and address.hostname in ('llm-request-router', 'llm-request-router.'+namespace,
                                         'llm-request-router.'+namespace+'.svc',
                                         'llm-request-router.'+namespace+'.svc.cluster.local')):
            operators.append((release['name'], config))
    require(len(operators) == 1, 'Expected one Pylon Operator for the selected routing stack and namespace.')
    operator, op_values = operators[0]
    operator_deployments = json.loads(output(kc+['get', 'deployments', '-l', 'app.kubernetes.io/instance='+operator, '-o', 'json']))['items']
    require(len(operator_deployments) == 1 and owner(operator_deployments[0]) == operator,
            'Expected one Helm-owned Pylon Operator deployment.')
    ca = op_values.get('trustBundle', {}).get('configMap')
    prefix = (stack.removesuffix('-stack') or stack)[:30].rstrip('-')
    containerd = None
    importers = []
    for release in releases:
        if release['chart'].startswith('pylon-image-loader-'):
            config = values(release['name'])
            if config.get('archiveNode') == control and control in config.get('nodeNames', []):
                importers.append((release['name'], config))
    require(len(importers) <= 1, 'Multiple image import configurations match the control node.')
    if importers:
        name, importer = importers[0]
        require(name.endswith('-images'), 'Image importer release must end in -images.')
        prefix = name.removesuffix('-images')
        containerd = {k: importer[k] for k in ('archiveNode', 'runAsUser', 'socketPath', 'nodeNames')}
    if saved:
        storage_class = saved['storageClass']
    else:
        storage = json.loads(output(kc+['get', 'storageclasses', '-o', 'json']))['items']
        defaults = [s for s in storage if any(s['metadata'].get('annotations', {}).get(key) == 'true'
                    for key in ('storageclass.kubernetes.io/is-default-class', 'storageclass.beta.kubernetes.io/is-default-class'))]
        selected = defaults or storage
        require(len(selected) == 1, 'Select one default StorageClass or set storageClass explicitly in the configuration.')
        storage_class = selected[0]['metadata']['name']
    image = stack_values.get('llm-api-gateway', {}).get('llmApiGateway', {}).get('image', {})
    config = {'context': context, 'namespace': namespace, 'releasePrefix': prefix, 'clusterId': stack_values.get('clusterId'),
              'releases': {'stack': stack, 'operator': operator}, 'nodes': {'control': control},
              'storageClass': storage_class, 'caConfigMap': ca,
              'images': {'pullPolicy': image.get('pullPolicy', 'IfNotPresent')},
              'apiKeyFile': None, 'containerd': containerd, 'monitoring': {'enabled': True}}
    validate(config)
    return config


def attach(recipe, output, save):
    """Bind local monitoring state without modifying serving resources or owner progress."""
    if recipe.state.get('inventory'):
        recipe.bound_cluster()
    live = discover_config(recipe.c['context'], recipe.c['namespace'], output, saved=recipe.c)
    for field in ('context', 'namespace', 'clusterId', 'caConfigMap'):
        require(live[field] == recipe.c[field], 'Existing installation differs from saved configuration: '+field)
    require(live['nodes']['control'] == recipe.c['nodes']['control'], 'Existing control node differs from saved configuration.')
    require(live['releases'] == {'stack': recipe.stack, 'operator': recipe.operator},
            'Existing routing Helm releases differ from the saved installation.')
    nodes = json.loads(output(recipe.kc+['get', 'nodes', '-o', 'json']))['items']
    selected = {n['metadata']['name']: n for n in nodes if n['metadata']['name'] in recipe.c['nodes'].values()}
    require(set(recipe.c['nodes'].values()) <= set(selected), 'Configured placement nodes do not exist.')
    control = selected[recipe.c['nodes']['control']]
    require(any(c['type'] == 'Ready' and c['status'] == 'True' for c in control.get('status', {}).get('conditions', [])),
            'Monitoring control node is not Ready.')
    ca = json.loads(output(recipe.kc+['get', 'configmap', recipe.c['caConfigMap'], '-o', 'json'])).get('data', {}).get('ca.crt')
    require(isinstance(ca, str) and ca.strip(), 'Gateway CA ConfigMap has no ca.crt.')
    existing_ca = recipe.work/'ca.crt'
    require(not existing_ca.exists() or existing_ca.read_text() == ca,
            'Saved gateway CA differs from the cluster. Verify the installation before replacing it.')
    key = recipe.c.get('apiKeyFile') or recipe.state.get('stack', {}).get('apiKeyFile')
    key_path = pathlib.Path(key).expanduser().resolve(strict=True) if key else None
    require(not key_path or key_path.is_file(), 'Configured API-key path must be a file.')
    if recipe.state and not recipe.state.get('attachedMonitoring'):
        require(recipe.state.get('inventory') and recipe.state.get('stack'),
                'Saved installation has no routing stack checkpoint. Use a separate monitoring work directory.')
        print('Existing routing stack matches the saved setup. Installer progress retained.')
        return
    save(existing_ca, ca)
    recipe.stamp('attachedMonitoring')
    recipe.stamp('inventory', {'nodes': {name: node['metadata']['uid'] for name, node in selected.items()}})
    recipe.stamp('stack', {'apiKeyFile': str(key_path) if key_path else None})
    print('Routing stack inspected. Run monitoring, then verify-monitoring.')
