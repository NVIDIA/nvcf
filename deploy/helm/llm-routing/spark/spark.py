#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Explicit phases for the pinned two-GPU Spark recipe. See README.md first."""
import argparse
import base64
import contextlib
import copy
import datetime
import hashlib
import http.client
import json
import os
import pathlib
import re
import secrets
import socket
import subprocess
import sys
import tempfile
import time

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import gateway_access
import cluster_setup
LOCK = json.loads((HERE/'source.lock.json').read_text())
MODEL = json.loads((HERE/'model.lock.json').read_text())
COMPONENTS = {'gateway': 'src/invocation-plane-services/llm-api-gateway',
              'router': 'src/libraries/rust/stargate', 'pylon': 'src/libraries/rust/stargate',
              'operator': 'src/compute-plane-services/pylon-operator'}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as output:
        os.fchmod(output.fileno(), 0o600)
        output.write(value if isinstance(value, str) else json.dumps(value, indent=2) + '\n')


def run(command, **kwargs):
    return subprocess.run([str(x) for x in command], check=True, **kwargs)


def output(command, **kwargs):
    return subprocess.check_output([str(x) for x in command], text=True, **kwargs)


def validate(c):
    for key in ('context', 'namespace', 'releasePrefix', 'clusterId', 'storageClass', 'runtimeClass'):
        require(isinstance(c.get(key), str) and bool(c[key].strip()), key + ' must be explicit.')
    for key in ('namespace', 'releasePrefix', 'clusterId'):
        require(re.fullmatch(r'[a-z0-9]([-a-z0-9]*[a-z0-9])?', c[key]) is not None, 'Invalid ' + key)
    require(len(c['releasePrefix']) <= 30, 'releasePrefix must be at most 30 characters.')
    require(all(c['nodes'].get(role) for role in ('leader', 'worker', 'control')), 'All three placement roles are required.')
    require(c['nodes']['leader'] != c['nodes']['worker'], 'GLM needs exactly two distinct GPU nodes.')
    require(c['images']['pullPolicy'] in ('Never', 'IfNotPresent', 'Always'), 'Invalid pull policy.')
    require(re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', c['images']['tag']) is not None, 'Invalid image tag.')
    require('/' in c['images']['prefix'] and not c['images']['prefix'].endswith('/'), 'Use registry/path as image prefix.')
    require(not c['images'].get('pullSecrets'), 'Pylon does not propagate image-pull secrets. Use nodes with registry access or pre-import all application images.')
    require(c.get('caConfigMap'), 'caConfigMap is required for verified QUIC and client TLS.')
    require(not c.get('retainedModels'), 'Verification targets GLM. Remove retainedModels from the configuration.')
    require(not c.get('testFixture'), 'The recipe deploys GLM. Remove testFixture from the configuration.')



def default_work_dir(context):
    require(isinstance(context, str) and bool(context.strip()),
            'Set SPARK_CONTEXT or --context. The current kubectl context is not selected automatically.')
    xdg = os.environ.get('XDG_STATE_HOME')
    if xdg:
        root = pathlib.Path(xdg)
        require(root.is_absolute(), 'XDG_STATE_HOME must be an absolute directory.')
    else:
        root = pathlib.Path.home()/'.local/state'
    scope = hashlib.sha256(context.encode()).hexdigest()[:20]
    return (root/'nvcf/llm-routing'/scope).resolve()


class ContextSelectionError(RuntimeError):
    pass


class DockerUnavailableError(RuntimeError):
    pass


def kubeconfig_context():
    """Choose a sole local context without using current-context or reading raw credentials."""
    try:
        config = json.loads(output(['kubectl', 'config', 'view', '-o', 'json'], stderr=subprocess.PIPE))
    except (OSError, subprocess.CalledProcessError, ValueError):
        raise ContextSelectionError('Could not read kubeconfig. Set KUBECONFIG or pass --context NAME.') from None
    contexts = config.get('contexts') or []
    names = {item.get('name') for item in contexts if isinstance(item, dict)}
    names = {name for name in names if isinstance(name, str) and name.strip()}
    if not names:
        raise ContextSelectionError('No Kubernetes context found. Set KUBECONFIG or pass --context NAME.')
    if len(names) != 1:
        raise ContextSelectionError('Multiple Kubernetes contexts found. Pass --context NAME or set SPARK_CONTEXT.')
    return names.pop()


def cli_settings(args):
    """Resolve local paths and context without changing files or the cluster."""
    config = None
    if args.config:
        path = args.config.expanduser()
        require(path.exists() or args.phase == 'init', 'Configuration file does not exist. Run init or attach-existing first.')
        if path.exists():
            config = json.loads(path.read_text())
    work = args.work_dir.expanduser().resolve() if args.work_dir else None
    if config is None and work and not args.config and (work/'config.json').exists():
        config = json.loads((work/'config.json').read_text())
    context = args.context or os.environ.get('SPARK_CONTEXT') or (config.get('context') if config else None)
    if not context:
        context = kubeconfig_context()
    work = work or default_work_dir(context)
    require(not work.is_relative_to(HERE.parents[3]), 'Keep generated work and credentials outside the checkout.')
    config_path = args.config.expanduser().resolve() if args.config else work/'config.json'
    if config is None and config_path.exists():
        config = json.loads(config_path.read_text())
    if config is not None:
        require(config.get('context') == context, 'Selected context differs from the saved deployment configuration.')
        require(not args.namespace or config.get('namespace') == args.namespace,
                'Selected namespace differs from the saved deployment. Use a separate --work-dir for another installation.')
    require(isinstance(context, str) and bool(context.strip()), 'Set --context NAME or SPARK_CONTEXT.')
    return context, work, config_path, config


def discover_config(context, namespace=None):
    """Read one installed recipe and return only reusable deployment settings."""
    require(context, 'Set --context to the Kubernetes context for the existing installation.')
    kc = ['kubectl', '--context', context, '--request-timeout=30s']
    scope = ['-n', namespace] if namespace else ['--all-namespaces']
    deployments = json.loads(output(kc+['get', 'deployments']+scope+['-o', 'json']))['items']
    candidates = [d for d in deployments if d['metadata']['name'] == 'llm-api-gateway'
                  and d['metadata'].get('annotations', {}).get('meta.helm.sh/release-name')]
    require(candidates, 'No Helm-managed LLM gateway found. Check --context and --namespace.')
    require(len(candidates) == 1, 'Multiple LLM installations found. Select one with --namespace: '+
            ', '.join(sorted(d['metadata']['namespace'] for d in candidates)))
    gateway = candidates[0]
    namespace = gateway['metadata']['namespace']
    kc += ['-n', namespace]
    hm = ['helm', '--kube-context', context, '-n', namespace]

    def get(kind, name):
        return json.loads(output(kc+['get', kind, name, '-o', 'json']))

    def owner(obj):
        annotations = obj['metadata'].get('annotations', {})
        require(annotations.get('meta.helm.sh/release-namespace') == namespace,
                'Unexpected Helm namespace for '+obj['metadata']['name'])
        release = annotations.get('meta.helm.sh/release-name')
        require(release, 'Missing Helm ownership for '+obj['metadata']['name'])
        return release

    def values(name):
        return json.loads(output(hm+['get', 'values', name, '--all', '-o', 'json']))

    def placement(obj):
        node = obj['spec']['template']['spec'].get('nodeSelector', {}).get('kubernetes.io/hostname')
        require(node, 'Expected explicit node placement for '+obj['metadata']['name'])
        return node

    stack = owner(gateway)
    v = values(stack)
    require(v.get('sparkRecipeSource') == {k: LOCK[k] for k in ('repository', 'revision')},
            'Installed source differs from source.lock.json. Use the matching recipe checkout.')
    router = get('deployment', 'llm-request-router')
    require(owner(router) == stack, 'Gateway and router belong to different releases.')
    control = placement(gateway)
    require(placement(router) == control, 'This recipe requires gateway and router on the same control node.')
    endpoint = get('inferenceendpoint', 'glm53-iq2')
    glm = owner(endpoint)
    require(endpoint['spec']['service']['name'] == glm and endpoint['spec']['modelName'] == 'GLM-5.3-UD-IQ2_M',
            'Existing endpoint is not the GLM deployment supported by this recipe.')
    leader = get('deployment', glm)
    worker = get('deployment', glm+'-rpc-worker')
    service = get('service', glm)
    require(all(owner(d) == glm for d in (leader, worker, service)), 'Unexpected GLM resource ownership.')
    backend = values(glm)
    nodes = {'control': control, 'leader': placement(leader), 'worker': placement(worker)}
    targets = {t['id']: t['node'] for t in backend['targets']}
    require(all(targets.get(role) == nodes[role] for role in ('leader', 'worker')), 'GLM placement differs from Helm values.')
    releases = json.loads(output(hm+['list', '-o', 'json']))
    operators = []
    for release in releases:
        if not release['chart'].startswith('pylon-operator-'):
            continue
        config = values(release['name'])
        if (config.get('clusterId') == v.get('clusterId')
                and namespace in config.get('watchNamespaces', [])
                and config.get('router', {}).get('grpcAddress') == 'http://llm-request-router.'+namespace+'.svc.cluster.local:50071'):
            operators.append((release['name'], config))
    require(len(operators) == 1, 'Expected one Pylon Operator for the selected cluster and namespace.')
    operator, op = operators[0]
    require(op.get('fullnameOverride') == operator, 'Operator Deployment name must match its Helm release for this recipe.')
    operator_deployment = get('deployment', operator)
    require(owner(operator_deployment) == operator and placement(operator_deployment) == control,
            'Operator ownership or placement differs from this recipe.')
    ca = op['trustBundle']['configMap']
    auth = v['llm-api-gateway']['llmApiGateway']['auth']
    require(auth.get('mode') == 'staticKeys' and auth.get('staticKeys', {}).get('existingSecret'),
            'Automatic test keys require staticKeys gateway authentication.')
    images = {}
    for component, chart, field in [('gateway', 'llm-api-gateway', 'llmApiGateway'),
                                     ('router', 'llm-request-router', 'llmRequestRouter')]:
        im = v[chart][field]['image']
        images[component] = im['registry']+'/'+im['repository']
        obj = gateway if component == 'gateway' else router
        expected = images[component]+':'+im['tag']
        require(any(c['image'] == expected for c in obj['spec']['template']['spec']['containers']),
                'Live '+component+' image differs from its Helm values.')
    images['operator'] = op['image']['repository']
    images['pylon'] = op['pylon']['image']['repository']
    importers = []
    for release in releases:
        if release['chart'].startswith('pylon-image-loader-'):
            config = values(release['name'])
            if config.get('archiveNode') == control and control in config.get('nodeNames', []):
                importers.append((release['name'], config))
    require(len(importers) <= 1, 'Multiple image import configurations match the control node.')
    containerd = None
    prefix = glm.removesuffix('-glm')[:30]
    if importers:
        name, config = importers[0]
        require(name.endswith('-images'), 'Image importer release must end in -images.')
        prefix = name.removesuffix('-images')
        containerd = {k: config[k] for k in ('archiveNode', 'runAsUser', 'socketPath', 'nodeNames')}
    image = v['llm-api-gateway']['llmApiGateway']['image']
    image_prefix = images['gateway'].rsplit('/', 1)[0]
    if '/' not in image_prefix:
        image_prefix += '/attached'
    # TLS private material and existing caller/cluster key hashes are intentionally omitted.
    config = {'context': context, 'namespace': namespace, 'releasePrefix': prefix, 'clusterId': v['clusterId'],
              'releases': {'stack': stack, 'operator': operator, 'glm': glm}, 'nodes': nodes,
              'storageClass': backend['artifacts']['storageClassName'], 'runtimeClass': backend['runtimeClassName'],
              'images': {'prefix': image_prefix, 'tag': image['tag'], 'pullPolicy': image['pullPolicy'],
                         'pullSecrets': [], 'repositories': images},
              'runtimeImage': backend['image'], 'tls': {'selfSigned': {'enabled': True}},
              'caConfigMap': ca, 'apiKeyFile': None, 'containerd': containerd}
    validate(config)
    return config


class Recipe:
    def __init__(self, config, work, source=None):
        self.c = config
        validate(config)
        self.work = pathlib.Path(work).expanduser().resolve()
        repo = HERE.parents[3]
        require(not self.work.is_relative_to(repo), 'Keep generated work and credentials outside the checkout.')
        self.work.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.source = pathlib.Path(source).resolve() if source else self.work/'source'
        self.state_path = self.work/'state.json'
        self.state = json.loads(self.state_path.read_text()) if self.state_path.exists() else {}
        identity = {k: config[k] for k in ('context', 'namespace', 'releasePrefix', 'clusterId', 'nodes')}
        identity['releases'] = config.get('releases', {})
        require(not self.state or self.state['identity'] == identity, 'Work directory belongs to a different installation.')
        self.identity = identity
        self.kc = ['kubectl', '--context', config['context'], '-n', config['namespace']]
        self.hm = ['helm', '--kube-context', config['context'], '-n', config['namespace']]
        releases = config.get('releases', {})
        self.glm = releases.get('glm', config['releasePrefix'] + '-glm')
        self.operator = releases.get('operator', config['releasePrefix'] + '-operator')
        self.stack = releases.get('stack', config['releasePrefix'] + '-stack')
        for name in (self.glm, self.operator, self.stack):
            require(re.fullmatch(r'[a-z0-9]([-a-z0-9]*[a-z0-9])?', name) is not None and len(name) <= 53, 'Invalid release name.')

    def stamp(self, phase, value=True):
        self.state.update(identity=self.identity)
        self.state[phase] = value
        save(self.state_path, self.state)

    def image(self, component, tag=None):
        return self.repository(component) + ':' + (tag or self.c['images']['tag'])

    def repository(self, component):
        return self.c['images'].get('repositories', {}).get(component, self.c['images']['prefix'] + '/' + component)

    def source_identity(self):
        require(re.fullmatch(r'[0-9a-f]{40}', LOCK['revision']) is not None, 'Pin a full immutable source revision in source.lock.json.')
        return {key: LOCK[key] for key in ('repository', 'revision')}

    def source_check(self):
        identity = self.source_identity()
        require(self.source.is_dir(), 'Run prepare first, or pass --source-dir pointing to the prepared pinned checkout.')
        require(output(['git', 'rev-parse', 'HEAD'], cwd=self.source).strip() == identity['revision'], 'Dependency source revision differs from source.lock.json.')

    def prepare(self):
        identity = self.source_identity()
        if not self.source.exists():
            run(['git', 'clone', '--filter=blob:none', '--no-checkout', identity['repository'], self.source])
            run(['git', 'fetch', 'origin', identity['revision']], cwd=self.source)
            run(['git', 'checkout', '--detach', identity['revision']], cwd=self.source)
        self.source_check()
        run(['helm', 'dependency', 'build', '--skip-refresh', self.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack'])
        print('Prepared source:', self.source)

    def backend_values(self, phase='serve', register=False, render=False):
        values = json.loads((HERE/'backend.defaults.json').read_text())
        values.update(phase=phase, image=self.c['runtimeImage'], runtimeClassName=self.c['runtimeClass'])
        values['targets'] = [{'id': r, 'node': self.c['nodes'][r]} for r in ('leader', 'worker')]
        values['artifacts'] = {'storageClassName': self.c['storageClass'], 'size': '400Gi'}
        values['rpc']['cache'].update(storageClassName=self.c['storageClass'], enabled=phase == 'serve')
        if phase != 'serve':
            values['rpc']['resources'] = {'requests': {'cpu': '2', 'memory': '2Gi', 'nvidia.com/gpu': 1},
                                          'limits': {'cpu': '8', 'memory': '8Gi', 'nvidia.com/gpu': 1}}
        values['runtime']['sha256'] = self.state.get('runtimeSha256', 'a'*64 if render else '')
        values['model']['lock'] = MODEL
        values['model']['register'] = register
        values['qualification']['attempt'] = self.state.get('qualificationAttempt', values['qualification']['attempt'])
        values.setdefault('chain', {}).update(runtimeRelease=self.glm, artifactClaim=self.glm+'-artifacts', attempt=self.state.get('chainAttempt', 1))
        return values

    def operator_values(self):
        return {'fullnameOverride': self.operator, 'clusterId': self.c['clusterId'],
                'image': {'repository': self.repository('operator'), 'tag': self.c['images']['tag'], 'pullPolicy': self.c['images']['pullPolicy']},
                'router': {'grpcAddress': 'http://llm-request-router.'+self.c['namespace']+'.svc.cluster.local:50071'},
                'pylon': {'image': {'repository': self.repository('pylon'), 'tag': self.c['images']['tag'], 'pullPolicy': self.c['images']['pullPolicy']}},
                'watchNamespaces': [self.c['namespace']], 'trustBundle': {'configMap': self.c['caConfigMap']},
                'devInsecureTransport': False, 'nodeSelector': {'kubernetes.io/hostname': self.c['nodes']['control']}}

    def stack_values(self, token_hash, key_hash):
        values = {'clusterId': self.c['clusterId'], 'clusterCredential': {'sha256': token_hash},
                  'apiKeys': [{'id': 'poc-client', 'sha256': key_hash}], 'tls': copy.deepcopy(self.c['tls']),
                  'sparkRecipeSource': self.source_identity()}
        values['tls'].setdefault('selfSigned', {})['caName'] = self.c['caConfigMap']
        for component, chart, service in [('gateway', 'llm-api-gateway', 'llmApiGateway'), ('router', 'llm-request-router', 'llmRequestRouter')]:
            registry, repository = self.repository(component).split('/', 1)
            values[chart] = {service: {'replicaCount': 1, 'nodeSelector': {'kubernetes.io/hostname': self.c['nodes']['control']},
                                      'image': {'registry': registry, 'repository': repository, 'tag': self.c['images']['tag'], 'pullPolicy': self.c['images']['pullPolicy']}}}
        return values

    def helm_apply(self, release, chart, values, timeout='5m', wait=True, jobs=False):
        path = self.work/(release+'-values.json')
        save(path, values)
        command = self.hm + ['upgrade', '--install', release, str(chart), '--create-namespace', '-f', str(path), '--timeout', timeout]
        if wait:
            command += ['--wait']
        if jobs:
            command += ['--wait-for-jobs']
        # Helm NOTES must never contain the generated credential itself.
        run(command)

    def inventory(self):
        require(not self.state.get('attachedExisting'), 'This work directory is attached for iteration. Do not run fresh-install phases.')
        nodes = json.loads(output(self.kc+['get', 'nodes', '-o', 'json']))['items']
        pods = json.loads(output(self.kc+['get', 'pods', '-A', '-o', 'json']))['items']
        crds = json.loads(output(self.kc+['get', 'crds', '-o', 'json']))['items']
        selected = self.c['nodes']
        if self.c['images']['pullPolicy'] == 'Never':
            eligible = {n['metadata']['name'] for n in nodes if n['metadata']['labels'].get('kubernetes.io/arch') == 'arm64'}
            imports = set(self.c['containerd'].get('nodeNames', selected.values()))
            require(eligible <= imports, 'Pre-import Pylon on every ARM64 node where it can schedule. Set containerd.nodeNames explicitly.')
        for role in ('control', 'leader', 'worker'):
            found = [n for n in nodes if n['metadata']['name'] == selected[role]]
            require(len(found) == 1, 'Missing node for '+role)
            node = found[0]
            require(node['metadata']['labels'].get('kubernetes.io/arch') == 'arm64', 'Selected nodes must be ARM64.')
            require(any(c['type'] == 'Ready' and c['status'] == 'True' for c in node['status']['conditions']), 'Node is not Ready.')
            if role != 'control':
                require(int(node['status']['allocatable'].get('nvidia.com/gpu', 0)) >= 1, 'GPU device plugin has not advertised a GPU.')
                busy = [p['metadata']['name'] for p in pods if p['spec'].get('nodeName') == selected[role]
                        and p['status']['phase'] not in ('Succeeded', 'Failed')
                        and any(int(c.get('resources', {}).get('requests', {}).get('nvidia.com/gpu', 0)) > 0 for c in p['spec']['containers'])]
                require(not busy, 'Selected model GPU is occupied: '+', '.join(busy))
        for crd in crds:
            if crd['metadata']['name'] == 'inferenceendpoints.pylon.nvidia.com':
                owner = crd['metadata'].get('annotations', {})
                require(owner.get('meta.helm.sh/release-name') == self.operator and owner.get('meta.helm.sh/release-namespace') == self.c['namespace'], 'Existing Pylon CRD has another owner. Review compatibility and watch scopes first.')
        existing = [p for p in pods if p['metadata']['namespace'] == self.c['namespace']]
        require(not existing or self.state.get('inventory'), 'Use an unused namespace for the first run.')
        run(self.kc+['get', 'runtimeclass', self.c['runtimeClass']])
        run(self.kc+['get', 'storageclass', self.c['storageClass']])
        save(self.work/'evidence/inventory.json', {'nodes': nodes, 'pods': pods})
        self.stamp('inventory', {'nodes': {n['metadata']['name']: n['metadata']['uid'] for n in nodes}})
        print('Inventory passed. Actual CUDA, memory and RPC checks are separate phases.')

    def attach_existing(self):
        require(not self.state or self.state.get('attachedExisting'), 'Use a separate work directory for an existing installation.')
        if self.state:
            self.bound_cluster()
        require(set(self.c.get('releases', {})) >= {'stack', 'operator', 'glm'}, 'Set explicit releases.stack, releases.operator and releases.glm.')
        key = pathlib.Path(self.c['apiKeyFile']).expanduser().resolve(strict=True) if self.c.get('apiKeyFile') else None
        nodes = json.loads(output(self.kc+['get', 'nodes', '-o', 'json']))['items']
        names = {n['metadata']['name'] for n in nodes}
        require(set(self.c['nodes'].values()) <= names, 'Configured placement nodes do not exist.')
        owners = {'llm-api-gateway': self.stack, 'llm-request-router': self.stack,
                  self.operator: self.operator, self.glm: self.glm, self.glm+'-rpc-worker': self.glm}
        for deployment, release in owners.items():
            obj = json.loads(output(self.kc+['get', 'deployment', deployment, '-o', 'json']))
            annotations = obj['metadata'].get('annotations', {})
            require(annotations.get('meta.helm.sh/release-name') == release and annotations.get('meta.helm.sh/release-namespace') == self.c['namespace'], 'Unexpected deployment ownership: '+deployment)
        values = json.loads(output(self.hm+['get', 'values', self.stack, '-o', 'json']))
        require(values.get('clusterId') == self.c['clusterId'], 'Existing cluster identity differs from configuration.')
        for component, chart, service in [('gateway', 'llm-api-gateway', 'llmApiGateway'), ('router', 'llm-request-router', 'llmRequestRouter')]:
            live = values[chart][service]['image']
            require(live['registry']+'/'+live['repository'] == self.repository(component), 'Existing image repository differs: '+component)
        endpoint = json.loads(output(self.kc+['get', 'inferenceendpoint', 'glm53-iq2', '-o', 'json']))
        require(endpoint['spec']['service']['name'] == self.glm and endpoint['spec']['modelName'] == 'GLM-5.3-UD-IQ2_M', 'Existing GLM endpoint differs from the recipe.')
        ca = json.loads(output(self.kc+['get', 'configmap', self.c['caConfigMap'], '-o', 'json']))['data']['ca.crt']
        save(self.work/'ca.crt', ca)
        self.stamp('attachedExisting')
        self.stamp('inventory', {'nodes': {n['metadata']['name']: n['metadata']['uid'] for n in nodes}})
        self.stamp('stack', {'apiKeyFile': str(key) if key else None, 'source': values.get('sparkRecipeSource')})
        self.stamp('serve')
        print('Existing installation inspected without changing it. Run verify-gateway next.')
        if values.get('sparkRecipeSource') != self.source_identity():
            print('Image updates are disabled for this source revision. Use a coordinated stack installation to change gateway/router contracts.')

    def bound_cluster(self):
        require(self.state.get('inventory'), 'Run inventory for this installation first.')
        live = json.loads(output(self.kc+['get', 'nodes', '-o', 'json']))['items']
        old = self.state['inventory']['nodes']
        current = {n['metadata']['name']: n['metadata']['uid'] for n in live}
        require(all(current.get(name) == old.get(name) for name in self.c['nodes'].values()), 'The selected node identities changed or the context points to another cluster.')

    def logs(self, component, release=None, job=None):
        selector = 'app.kubernetes.io/instance='+(release or self.glm)+',app.kubernetes.io/component='+component
        # Job labels live on pod templates in these charts, so select pods.
        pods = json.loads(output(self.kc+['get', 'pods', '-l', selector, '-o', 'json']))['items']
        if job:
            pods = [p for p in pods if any(owner.get('kind') == 'Job' and owner['name'] == job
                    for owner in p['metadata'].get('ownerReferences', []))]
        require(bool(pods), 'No pods for '+component)
        records = []
        for pod in pods:
            text = output(self.kc+['logs', pod['metadata']['name']])
            save(self.work/'evidence'/(pod['metadata']['name']+'.log'), text)
            if job and pod['status']['phase'] != 'Succeeded':
                continue
            for line in text.splitlines():
                try:
                    record = json.loads(line)
                    if record.get('result') == 'PASS':
                        records.append(record)
                except (ValueError, AttributeError):
                    pass
        return records

    def retry_qualification(self):
        require(not self.state.get('qualify'), 'Qualification already passed. Retry is for an unsuccessful qualification phase.')
        prefixes = {'qualificationAttempt': self.glm+'-qualify-', 'chainAttempt': self.glm+'-chain-'}
        jobs = json.loads(output(self.kc+['get', 'jobs', '-o', 'json']))['items']
        jobs = [job for job in jobs if any(re.fullmatch(re.escape(prefix)+r'\d+', job['metadata']['name']) for prefix in prefixes.values())]
        require(bool(jobs), 'No qualification or chain Jobs to retry.')
        for job in jobs:
            name = job['metadata']['name']
            release = self.glm+'-chain' if name.startswith(prefixes['chainAttempt']) else self.glm
            owner = job['metadata'].get('annotations', {})
            require(owner.get('meta.helm.sh/release-name') == release and owner.get('meta.helm.sh/release-namespace') == self.c['namespace'], 'Unexpected Job ownership: '+name)
            require(any(c['type'] in ('Complete', 'Failed') and c['status'] == 'True' for c in job.get('status', {}).get('conditions', [])), 'Job is still active: '+name)
        evidence = self.work/'evidence'/('qualification-retry-'+datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ'))
        save(evidence/'jobs.json', {'items': jobs})
        pods = json.loads(output(self.kc+['get', 'pods', '-o', 'json']))['items']
        job_uids = {job['metadata']['uid'] for job in jobs}
        pods = [pod for pod in pods if any(owner.get('kind') == 'Job' and owner.get('uid') in job_uids for owner in pod['metadata'].get('ownerReferences', []))]
        save(evidence/'pods.json', {'items': pods})
        for pod in pods:
            name = pod['metadata']['name']
            try:
                save(evidence/(name+'.log'), output(self.kc+['logs', name], stderr=subprocess.STDOUT))
            except subprocess.CalledProcessError as error:
                save(evidence/(name+'-log-error.txt'), error.output or str(error))
                print('Could not retrieve logs for', name, '- saved the error with its pod status.')
        values = self.backend_values('qualify')
        defaults = {'qualificationAttempt': values['qualification']['attempt'], 'chainAttempt': values['chain']['attempt']}
        for key, prefix in prefixes.items():
            previous = [int(job['metadata']['name'][len(prefix):]) for job in jobs if job['metadata']['name'].startswith(prefix)]
            self.state[key] = max([defaults[key], *previous]) + 1
        self.state['download'] = False
        self.stamp('qualify', False)
        print('Saved previous qualification evidence:', evidence)

    def resume_load(self):
        """Recover the local load checkpoint without changing an already deployed model."""
        def release():
            item = json.loads(output(self.hm+['status', self.glm, '-o', 'json'], timeout=45))
            require(item.get('name') == self.glm and item.get('namespace') == self.c['namespace'],
                    'Unexpected GLM Helm release identity.')
            require(item.get('info', {}).get('status') == 'deployed',
                    'GLM Helm release is '+str(item.get('info', {}).get('status'))+
                    '. Resolve the Helm operation, then rerun load.')
            return item['version']

        revision = release()
        values = json.loads(output(self.hm+['get', 'values', self.glm, '--revision', str(revision), '-o', 'json'], timeout=45))
        if values.get('phase') != 'serve':
            require(values.get('phase') == 'download', 'GLM Helm release is not at the completed download or serve phase.')
            existing = output(self.kc+['get', 'deployment', self.glm, '--ignore-not-found', '-o', 'json'], timeout=45)
            require(not existing.strip(), 'A model Deployment already exists outside the expected serve phase.')
            require(release() == revision, 'GLM Helm revision changed while checking load. Retry after the operation completes.')
            return False
        require(values == self.backend_values('serve'), 'Deployed GLM values differ from this load configuration.')
        expected = {
            ('Deployment', self.glm): ('leader', 'llama', 'model-server'),
            ('Deployment', self.glm+'-rpc-worker'): ('worker', 'rpc', 'rpc-worker'),
            ('Deployment', self.glm+'-artifacts'): ('leader', 'artifacts', 'artifacts'),
            ('PersistentVolumeClaim', self.glm+'-artifacts'): None,
            ('PersistentVolumeClaim', self.glm+'-rpc-cache'): None,
        }
        names = [('deployment/' if kind == 'Deployment' else 'pvc/')+name for kind, name in expected]

        def ready_resources():
            items = json.loads(output(self.kc+['get', *names, '-o', 'json'], timeout=45))['items']
            require({(item.get('kind'), item['metadata']['name']) for item in items} == set(expected)
                    and len(items) == len(expected), 'Missing GLM resources while resuming load.')
            identities = {}
            for item in items:
                meta, spec, status = item['metadata'], item['spec'], item.get('status', {})
                name, kind = meta['name'], item['kind']
                owner = meta.get('annotations', {})
                require(meta.get('namespace') == self.c['namespace'] and not meta.get('deletionTimestamp')
                        and owner.get('meta.helm.sh/release-name') == self.glm
                        and owner.get('meta.helm.sh/release-namespace') == self.c['namespace']
                        and meta.get('labels', {}).get('app.kubernetes.io/managed-by') == 'Helm',
                        'Unexpected GLM resource ownership: '+name)
                require(meta.get('uid'), 'Missing GLM resource identity: '+name)
                if kind == 'PersistentVolumeClaim':
                    require(status.get('phase') == 'Bound' and spec.get('volumeName')
                            and spec.get('storageClassName') == self.c['storageClass'],
                            'GLM storage is not bound as configured: '+name)
                    identities[kind+'/'+name] = [meta['uid'], spec['volumeName']]
                    continue
                generation = meta.get('generation')
                require(generation and status.get('observedGeneration') == generation
                        and spec.get('replicas') == 1
                        and all(status.get(field, 0) == 1 for field in
                                ('replicas', 'updatedReplicas', 'readyReplicas', 'availableReplicas'))
                        and not status.get('unavailableReplicas', 0),
                        'GLM Deployment is not ready at its current generation: '+name+'. Wait, then rerun load.')
                role, container, component = expected[(kind, name)]
                template = spec['template']
                labels = {'app.kubernetes.io/instance': self.glm, 'app.kubernetes.io/component': component}
                require(all(template['metadata'].get('labels', {}).get(key) == value for key, value in labels.items())
                        and spec.get('selector', {}).get('matchLabels') == labels,
                        'Unexpected GLM Deployment selector: '+name)
                pod = template['spec']
                require(pod.get('nodeSelector', {}).get('kubernetes.io/hostname') == self.c['nodes'][role],
                        'GLM Deployment targets another node: '+name)
                containers = pod.get('containers', [])
                require(len(containers) == 1 and containers[0].get('name') == container
                        and containers[0].get('image') == self.c['runtimeImage'],
                        'GLM Deployment image differs: '+name)
                volume = 'rpc-cache' if component == 'rpc-worker' else 'artifacts'
                volumes = {item['name']: item for item in pod.get('volumes', [])}
                mounts = {item['name']: item for item in containers[0].get('volumeMounts', [])}
                require(volumes.get(volume, {}).get('persistentVolumeClaim', {}).get('claimName') == self.glm+'-'+volume
                        and mounts.get(volume, {}).get('mountPath') == '/'+volume,
                        'GLM Deployment storage differs: '+name)
                if component == 'model-server':
                    env = {item['name']: item.get('value') for item in containers[0].get('env', [])}
                    require(env.get('FIRST_SHARD') == values['model']['firstShard']
                            and env.get('SERVED_MODEL') == values['model']['servedName']
                            and env.get('RPC_ENDPOINT') == self.glm+'-rpc-worker:50052'
                            and json.loads(env.get('SERVER_ARGS') or 'null') == values['model']['args'],
                            'GLM model or RPC connection differs: '+name)
                if component != 'artifacts':
                    require(pod.get('runtimeClassName') == self.c['runtimeClass']
                            and template['metadata'].get('annotations', {}).get('checksum/runtime') == self.state['runtimeSha256']
                            and all(str(containers[0].get('resources', {}).get(field, {}).get('nvidia.com/gpu')) == '1'
                                    for field in ('requests', 'limits')),
                            'GLM GPU or runtime configuration differs: '+name)
                identities[kind+'/'+name] = [meta['uid'], generation]
            return identities

        identities = ready_resources()
        require(ready_resources() == identities and release() == revision,
                'GLM resources or Helm revision changed while resuming load. Retry after the operation completes.')
        save(self.work/'evidence'/'load-resume.json', {'release': self.glm, 'revision': revision, 'resources': identities})
        self.stamp('serve')
        print('Resumed completed GLM load without changing the deployment. Run verify-direct next.')
        return True

    def backend_phase(self, phase, retry=False):
        self.bound_cluster()
        require(not self.state.get('serve'), 'The model has already been loaded. Use the update or explicit recovery commands, not preparation phases.')
        require(not retry or phase == 'qualify', '--retry is supported only for qualify.')
        prerequisites = {'preflight': 'inventory', 'build': 'preflight', 'qualify': 'runtimeSha256', 'download': 'qualify', 'serve': 'download'}
        require(self.state.get(prerequisites[phase]), 'Missing successful '+prerequisites[phase]+' phase.')
        if phase == 'serve':
            if self.resume_load():
                return
            for role in ('leader', 'worker'):
                raw = output(self.kc+['exec', 'deploy/'+self.glm+'-rpc-'+role, '-c', 'rpc', '--', 'cat', '/proc/meminfo'])
                available = next(int(line.split()[1])*1024 for line in raw.splitlines() if line.startswith('MemAvailable:'))
                require(available > 113*1024**3, 'Insufficient actual host memory on '+role)
        if phase == 'qualify':
            require(re.fullmatch(r'[a-f0-9]{64}', self.state['runtimeSha256']) is not None, 'Invalid built runtime checksum.')
            if retry:
                self.retry_qualification()
            self.state['download'] = False
            self.stamp('qualify', False)
        values = self.backend_values(phase)
        timeout = {'preflight': '30m', 'build': '120m', 'qualify': '20m', 'download': '360m', 'serve': '70m'}[phase]
        self.helm_apply(self.glm, HERE/'charts/gguf-backend', values, timeout, jobs=phase != 'serve')
        if phase in ('preflight', 'build', 'download'):
            records = self.logs(phase)
            require(bool(records), phase+' did not record PASS.')
            if phase == 'preflight':
                require(len(records) == 2, 'Both GPU nodes must pass actual CUDA preflight.')
                require(all(r['memoryBefore']['MemAvailable'] > 113*1024**3 for r in records), 'Insufficient actual host memory before CUDA allocation.')
            if phase == 'build':
                self.stamp('runtimeSha256', records[-1]['runtimeSHA256'])
            if phase == 'download':
                require(records[-1]['verifiedBytes'] == MODEL['weightFileBytes'] and records[-1]['verifiedFiles'] == 6, 'Model download incomplete.')
        elif phase == 'qualify':
            records = self.logs('qualification', job=self.glm+'-qualify-'+str(values['qualification']['attempt']))
            require(bool(records), 'RPC qualification did not record PASS.')
            chain = self.backend_values('chain')
            self.helm_apply(self.glm+'-chain', HERE/'charts/gguf-backend', chain, '15m', jobs=True)
            records = self.logs('chain-check', job=self.glm+'-chain-'+str(chain['chain']['attempt']))
            require(bool(records), 'RPC chain check did not record PASS.')
        self.stamp(phase)

    def deploy_stack(self):
        require(not self.state.get('attachedExisting'), 'Existing installations support verification and image iteration, not fresh stack deployment.')
        self.source_check()
        self.bound_cluster()
        op_chart = self.source/'deploy/helm/pylon-operator/pylon-operator'
        self.helm_apply(self.operator, op_chart, self.operator_values())
        secret = json.loads(output(self.kc+['get', 'secret', self.operator+'-cluster-credential', '-o', 'json']))
        token_hash = hashlib.sha256(base64.b64decode(secret['data']['cluster-token'])).hexdigest()
        key_path = pathlib.Path(self.c['apiKeyFile']).expanduser().resolve() if self.c.get('apiKeyFile') else self.work/'api-key'
        if not key_path.exists():
            require(not self.c.get('apiKeyFile'), 'Configured API-key file does not exist.')
            save(key_path, secrets.token_urlsafe(48)+'\n')
        key = key_path.read_text().strip()
        require(bool(key), 'API-key file is empty.')
        values = self.stack_values(token_hash, hashlib.sha256(key.encode()).hexdigest())
        self.helm_apply(self.stack, self.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack', values)
        ca = json.loads(output(self.kc+['get', 'configmap', self.c['caConfigMap'], '-o', 'json']))['data']['ca.crt']
        save(self.work/'ca.crt', ca)
        self.stamp('stack', {'apiKeyFile': str(key_path), 'source': self.source_identity()})

    def register(self):
        require(not self.state.get('attachedExisting'), 'Do not re-register or adopt an attached existing backend.')
        self.bound_cluster()
        require(self.state.get('serve') and self.state.get('direct') and self.state.get('stack'), 'Load, directly verify GLM, and deploy the stack before registration.')
        self.helm_apply(self.glm, HERE/'charts/gguf-backend', self.backend_values(register=True), '10m')
        for condition in ('Ready', 'TransportReady', 'Registered'):
            run(self.kc+['wait', 'inferenceendpoint/glm53-iq2', '--for=condition='+condition, '--timeout=300s'])
        self.stamp('registered')

    @contextlib.contextmanager
    def forward(self, gateway, port):
        service = 'llm-api-gateway' if gateway else self.glm
        remote_port = '8080' if gateway else '8000'
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', port))
        with (self.work/'port-forward.log').open('a') as log:
            proc = subprocess.Popen(self.kc+['port-forward', 'svc/'+service, str(port)+':'+remote_port, '--address', '127.0.0.1'], stdout=log, stderr=log)
            try:
                for _ in range(100):
                    require(proc.poll() is None, 'Port-forward exited. Read port-forward.log.')
                    try:
                        with socket.create_connection(('127.0.0.1', port), timeout=0.2):
                            break
                    except OSError:
                        time.sleep(0.2)
                else:
                    raise RuntimeError('Port-forward did not become available.')
                yield
            finally:
                proc.terminate()
                proc.wait(timeout=10)

    def chat(self, prompt, stream, port):
        require(prompt is None or (isinstance(prompt, str) and bool(prompt.strip())), 'Provide a nonempty chat prompt.')
        self.bound_cluster()
        require(self.state.get('stack'), 'Attach to or deploy the stack first.')
        url = 'https://127.0.0.1:' + str(port)
        command = [sys.executable, str(HERE/'client.py'), '--mode', 'chat', '--url', url,
                   '--ca-file', str(self.work/'ca.crt')]
        with self.forward(True, port):
            existing_key = self.state['stack'].get('apiKeyFile')
            access = contextlib.nullcontext(existing_key) if existing_key else gateway_access.temporary_gateway_key(self, url)
            with access as key_path:
                command += ['--api-key-file', str(key_path)]
                if stream:
                    command += ['--stream']
                run(command + (['--', prompt] if prompt is not None else []))

    def verify(self, gateway, port):
        self.bound_cluster()
        if gateway:
            require(self.state.get('stack'), 'Deploy or attach to the stack first.')
        url = ('https' if gateway else 'http')+'://127.0.0.1:'+str(port)
        command = [sys.executable, str(HERE/'client.py'), '--mode', 'verify', '--url', url,
                   '--output', str(self.work/'evidence'/('gateway.json' if gateway else 'direct.json'))]
        if gateway:
            command += ['--ca-file', str(self.work/'ca.crt'), '--cluster-id', self.c['clusterId']]
        self.stamp('gateway' if gateway else 'direct', False)
        with self.forward(gateway, port):
            if gateway:
                existing_key = self.state['stack'].get('apiKeyFile')
                access = contextlib.nullcontext(existing_key) if existing_key else gateway_access.temporary_gateway_key(self, url)
                with access as key:
                    run(command+['--api-key-file', str(key)])
            else:
                run(command)
        # Temporary key removal and rejection must pass before verification passes.
        self.stamp('gateway' if gateway else 'direct')

    def cleanup_key(self, port):
        self.bound_cluster()
        require(self.state.get('stack'), 'Attach to the stack first.')
        with self.forward(True, port):
            gateway_access.cleanup_gateway_key(self, 'https://127.0.0.1:'+str(port))
        print('Temporary gateway key cleanup completed.')

    def build_images(self, component=None, tag=None):
        self.source_check()
        try:
            run(['docker', 'info'], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=15)
        except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
            raise DockerUnavailableError('Start Docker, then rerun build-images.') from None
        for name in ([component] if component else self.components()):
            command = ['docker', 'buildx', 'build', '--platform', 'linux/arm64', '--load', '--tag', self.image(name, tag)]
            if name in ('router', 'pylon'):
                command += ['--target', 'stargate-runtime' if name == 'router' else 'pylon-runtime', '--build-arg', 'CARGO_PROFILE=integration']
            if name == 'operator':
                command += ['-f', str(HERE/'operator.Dockerfile'), '--build-arg', 'SOURCE_REVISION='+LOCK['revision']]
            run(command+[str(self.source/COMPONENTS[name])])

    def export_images(self, component=None, tag=None):
        self.source_check()
        images = [self.image(name, tag) for name in ([component] if component else self.components())]
        fd, temporary = tempfile.mkstemp(prefix='.arm64-images-', suffix='.tar', dir=self.work)
        os.close(fd)
        archive = self.work/'arm64-images.tar'
        try:
            run(['docker', 'save', '--output', temporary] + images)
            os.chmod(temporary, 0o600)
            os.replace(temporary, archive)
        finally:
            pathlib.Path(temporary).unlink(missing_ok=True)
        print('Image archive:', archive)

    def components(self):
        return list(COMPONENTS)

    def import_images(self, archive, allow, component=None, tag=None):
        require(allow, 'Import requires --allow-containerd-import, which grants the Jobs access to node runtime sockets.')
        self.bound_cluster()
        import tarfile
        archive = pathlib.Path(archive).resolve(strict=True)
        require(archive.stat().st_size < 1024**3, 'The default importer has a 1 GiB archive volume. Review and enlarge its chart before importing a larger archive.')
        with tarfile.open(archive) as tar:
            manifest = json.load(tar.extractfile('manifest.json'))
        tags = {value for entry in manifest for value in entry.get('RepoTags', [])}
        expected = [self.image(name, tag) for name in ([component] if component else self.components())]
        for image in expected:
            aliases = {image, image.removeprefix('docker.io/'), image.removeprefix('docker.io/library/')}
            require(bool(tags & aliases), 'Archive is missing the configured image: '+image)
        cfg = self.c.get('containerd')
        require(cfg, 'No image importer was discovered. Use registry distribution or configure containerd import settings.')
        nodes = [self.c['nodes']['control']] if component in ('gateway', 'router') else cfg.get('nodeNames', sorted(set(self.c['nodes'].values())))
        with archive.open('rb') as stream:
            archive_hash = hashlib.file_digest(stream, 'sha256').hexdigest()
        release = self.c['releasePrefix']+'-images'
        existing = json.loads(output(self.hm+['list', '--deployed', '--failed', '--pending', '--uninstalled',
                                              '--superseded', '--uninstalling', '--filter', '^'+re.escape(release)+'$', '-o', 'json']))
        require(all(item['chart'].startswith('pylon-image-loader-') for item in existing),
                'The image import release belongs to another chart.')
        prior_jobs = json.loads(output(self.kc+['get', 'jobs', '-o', 'json']))['items']
        owned = [job for job in prior_jobs if job['metadata'].get('annotations', {}).get('meta.helm.sh/release-name') == release]
        active = [job for job in owned if not any(c.get('type') in ('Complete', 'Failed') and c.get('status') == 'True'
                                                  for c in job.get('status', {}).get('conditions', []))]
        attempt_path = self.work/'image-import-attempt.json'
        if active:
            previous = json.loads(attempt_path.read_text()) if attempt_path.exists() else {}
            require(previous.get('failed') and previous.get('release') == release
                    and previous.get('context') == self.c['context'] and previous.get('namespace') == self.c['namespace']
                    and all(previous.get('jobs', {}).get(job['metadata']['name']) == job['metadata']['uid'] for job in active),
                    'Another image import is active. Wait for it to finish.')
            status = json.loads(output(self.hm+['status', release, '-o', 'json']))
            require(status.get('version') == previous.get('revision'), 'Image import revision changed. Do not replace another attempt.')
        values = {'enabled': True, 'nodeNames': nodes, 'archiveNode': cfg.get('archiveNode', self.c['nodes']['control']),
                  'archiveName': 'arm64-images.tar', 'archiveSha256': archive_hash,
                  'runAsUser': cfg.get('runAsUser', 1000), 'socketPath': cfg['socketPath']}
        self.helm_apply(release, HERE/'charts/image-loader', values, wait=False)
        status = json.loads(output(self.hm+['status', release, '-o', 'json']))
        revision = status['version']
        require(isinstance(revision, int) and revision > 0, 'Image import release has no valid revision.')
        base = release+'-'+str(revision)
        names = [base+'-server']+[base+'-'+node for node in nodes]
        jobs = json.loads(output(self.kc+['get', 'jobs']+names+['-o', 'json']))['items']
        job_uids = {}
        for job in jobs:
            metadata = job['metadata']
            annotations = metadata.get('annotations', {})
            require(annotations.get('meta.helm.sh/release-name') == release
                    and annotations.get('meta.helm.sh/release-namespace') == self.c['namespace'],
                    'Image import Job has different Helm ownership.')
            job_uids[metadata['name']] = metadata['uid']
        require(set(job_uids) == set(names), 'Image import Jobs differ from the installed revision.')
        server = base+'-server'
        try:
            deadline = time.monotonic() + 180
            while True:
                pods = json.loads(output(self.kc+['get', 'pods', '-l', 'job-name='+server, '-o', 'json']))['items']
                if pods:
                    break
                require(time.monotonic() < deadline, 'Archive server pod was not created within 180 seconds.')
                time.sleep(2)
            require(len(pods) == 1 and not pods[0]['metadata'].get('deletionTimestamp'), 'Expected one active archive server pod.')
            pod = pods[0]
            require(any(owner.get('kind') == 'Job' and owner.get('uid') == job_uids[server]
                        for owner in pod['metadata'].get('ownerReferences', [])), 'Archive server pod has different Job ownership.')
            pod_name = pod['metadata']['name']
            run(self.kc+['wait', 'pod/'+pod_name, '--for=condition=Ready', '--timeout=180s'])
            ready = json.loads(output(self.kc+['get', 'pod', pod_name, '-o', 'json']))
            require(ready['metadata']['uid'] == pod['metadata']['uid'] and not ready['metadata'].get('deletionTimestamp')
                    and ready.get('spec', {}).get('nodeName') == values['archiveNode'],
                    'Archive server pod changed or has different placement.')
            upload = """import hashlib, os, sys
path, expected_hash, expected_size = sys.argv[1:]
expected_size = int(expected_size)
partial = path + '.upload'
size = 0
checksum = hashlib.sha256()
try:
    with open(partial, 'xb') as target:
        os.chmod(partial, 0o600)
        while chunk := sys.stdin.buffer.read(1024 * 1024):
            size += len(chunk)
            if size > expected_size or size >= 1024**3:
                raise RuntimeError('Uploaded archive exceeds its expected size')
            checksum.update(chunk)
            target.write(chunk)
    if size != expected_size or checksum.hexdigest() != expected_hash:
        raise RuntimeError('Uploaded archive size or SHA-256 differs')
    os.replace(partial, path)
finally:
    if os.path.exists(partial):
        os.unlink(partial)
"""
            print('Uploading image archive through Kubernetes:', archive.stat().st_size, 'bytes')
            with archive.open('rb') as stream:
                run(self.kc+['exec', '-i', pod['metadata']['name'], '-c', 'server', '--', 'python3', '-c', upload,
                             '/images/arm64-images.tar', archive_hash, str(archive.stat().st_size)], stdin=stream, timeout=1200)
            run(self.kc+['wait', '--for=condition=complete', '--timeout=15m']+['job/'+name for name in names])
            completed = json.loads(output(self.kc+['get', 'jobs']+names+['-o', 'json']))['items']
            require({job['metadata']['name']: job['metadata']['uid'] for job in completed} == job_uids,
                    'Image import Jobs changed during verification.')
        except BaseException:
            save(attempt_path, {'failed': True, 'release': release, 'revision': revision, 'jobs': job_uids,
                                'context': self.c['context'], 'namespace': self.c['namespace']})
            raise
        attempt_path.unlink(missing_ok=True)
        save(self.work/'evidence/image-import.json', {'release': release, 'revision': revision,
             'archiveSha256': archive_hash, 'nodeNames': nodes, 'jobs': job_uids})
        print('Images imported on:', ', '.join(nodes))

    def update(self, component, tag):
        self.bound_cluster()
        self.source_check()
        require(not self.state.get('attachedExisting') or self.state.get('gateway'), 'Verify GLM through the attached gateway before its first update.')
        chart, service = {'gateway': ('llm-api-gateway', 'llmApiGateway'), 'router': ('llm-request-router', 'llmRequestRouter')}[component]
        require(re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', tag) is not None, 'Invalid image tag.')
        values = json.loads(output(self.hm+['get', 'values', self.stack, '-o', 'json']))
        require(values.get('sparkRecipeSource') == self.source_identity(),
                'Installed stack source is missing or differs from source.lock.json. Use a coordinated stack installation before image updates.')
        current = values[chart][service]['image']
        require(current['registry']+'/'+current['repository'] == self.repository(component), 'Live image repository differs from config.')
        require(current['tag'] != tag, 'Use a fresh tag.')
        before = json.loads(output(self.kc+['get', 'pods', '-o', 'json']))['items']
        value_key = chart+'.'+service+'.image.tag'
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
        path = self.work/'evidence'/('update-'+stamp+'.json')
        result = {'component': component, 'previousTag': current['tag'], 'newTag': tag, 'helmValueChanged': value_key,
                  'context': self.c['context'], 'namespace': self.c['namespace'], 'release': self.stack,
                  'chart': str(self.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack'), 'source': self.source_identity()}
        save(path, result)
        run(self.hm+['upgrade', self.stack, result['chart'], '--reuse-values', '--set-string', value_key+'='+tag, '--wait', '--timeout', '5m'])
        after = {p['metadata']['uid'] for p in json.loads(output(self.kc+['get', 'pods', '-o', 'json']))['items']}
        changed = [p['metadata']['name'] for p in before if p['status']['phase'] == 'Running'
                   and not p['metadata']['name'].startswith(chart+'-') and p['metadata']['uid'] not in after]
        result['backendPodsChanged'] = changed
        save(path, result)
        require(not changed, 'Backend pods changed. Review saved update evidence.')
        print('Update recorded:', path, 'Run verify-gateway next.')

    def rollback(self, result_file):
        self.bound_cluster()
        result = json.loads(pathlib.Path(result_file).read_text())
        require(all(result[k] == self.c[k] for k in ('context', 'namespace')) and result['release'] == self.stack, 'Rollback record belongs to another installation.')
        require(result.get('source') == self.source_identity(), 'Rollback record has a different or unknown source revision. Use a coordinated stack installation.')
        chart, service = {'gateway': ('llm-api-gateway', 'llmApiGateway'), 'router': ('llm-request-router', 'llmRequestRouter')}[result['component']]
        current = json.loads(output(self.hm+['get', 'values', self.stack, '-o', 'json']))
        require(current[chart][service]['image']['tag'] == result['newTag'], 'Another image update happened after this record. Review instead of overwriting it.')
        self.update(result['component'], result['previousTag'])

    def recovery(self, confirm, port):
        self.bound_cluster()
        require(not self.state.get('attachedExisting'), 'Recovery changes belong to the existing backend owner. This attachment supports image iteration only.')
        require(confirm and self.state.get('registered'), 'Recovery requires a registered model and --confirm-model-interruption.')
        values = self.backend_values(register=True)
        down = copy.deepcopy(values)
        down['rpc']['replicas'] = 0
        # Validate the same direct path before making an intentional interruption.
        self.verify(False, port)
        started = time.monotonic()
        observation = {'started': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'interruptionObserved': False}
        try:
            self.helm_apply(self.glm, HERE/'charts/gguf-backend', down, wait=False)
            time.sleep(15)
            down_pods = json.loads(output(self.kc+['get', 'pods', '-o', 'json']))
            save(self.work/'evidence/recovery-down.json', down_pods)
            require(not any(p['metadata']['name'].startswith(self.glm+'-rpc-worker-') and p['status']['phase'] == 'Running' for p in down_pods['items']), 'Worker is still running. Restore and inspect the rollout.')
            try:
                with self.forward(False, port):
                    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=10)
                    try:
                        connection.request('POST', '/v1/chat/completions', json.dumps({'model': values['model']['servedName'], 'messages': [{'role': 'user', 'content': 'What is 31 plus 17?'}], 'max_tokens': 32}), {'Content-Type': 'application/json'})
                        response = connection.getresponse()
                        response.read()
                        observation['statusWhileWorkerDown'] = response.status
                        observation['interruptionObserved'] = response.status != 200
                    finally:
                        connection.close()
            except (OSError, http.client.HTTPException, RuntimeError) as error:
                observation['interruptionObserved'] = True
                observation['failureType'] = type(error).__name__
        finally:
            self.helm_apply(self.glm, HERE/'charts/gguf-backend', values, '70m')
            observation['restoreSeconds'] = time.monotonic() - started
            save(self.work/'evidence/recovery.json', observation)
        require(observation['interruptionObserved'], 'Worker interruption was not demonstrated. Recovery restored the model but this test did not prove the failure path.')
        self.verify(False, port)
        self.verify(True, port+1)

    def render(self):
        self.source_check()
        renders = [('stack', self.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack', self.stack_values('a'*64, 'b'*64)),
                   ('operator', self.source/'deploy/helm/pylon-operator/pylon-operator', self.operator_values())]
        for phase in ('preflight', 'build', 'qualify', 'chain', 'download', 'serve'):
            renders.append(('glm-'+phase, HERE/'charts/gguf-backend', self.backend_values(phase, register=phase=='serve', render=True)))
        for name, chart, values in renders:
            path = self.work/'render'/(name+'-values.json')
            save(path, values)
            # Offline: no kube context, lookup, or API traffic. Generated TLS stays private.
            run(['helm', 'lint', chart, '-f', path], stdout=subprocess.DEVNULL)
            save(self.work/'render'/(name+'.yaml'), output(['helm', 'template', self.c['releasePrefix']+'-'+name, chart, '-n', self.c['namespace'], '-f', path]))
        print('Offline Helm lint/render passed for', len(renders), 'configurations. No deployment was performed.')


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=pathlib.Path)
    parser.add_argument('--context', help='Kubernetes context; defaults to SPARK_CONTEXT, saved settings, or the sole kubeconfig context.')
    parser.add_argument('--namespace', help='Select the namespace when the cluster has multiple installations.')
    parser.add_argument('--work-dir', type=pathlib.Path, help='Private local state directory; defaults to a per-context directory.')
    parser.add_argument('--source-dir', type=pathlib.Path)
    parser.add_argument('phase', choices=['init', 'paths', 'context', 'prepare', 'render', 'inventory', 'attach-existing', 'build-images', 'push-images', 'export-images', 'import-images', 'stack', 'preflight', 'build-runtime', 'qualify', 'download', 'load', 'verify-direct', 'register', 'verify-gateway', 'chat', 'cleanup-key', 'update', 'rollback', 'recover'])
    parser.add_argument('prompt', nargs='?', help='Prompt for the chat command.')
    parser.add_argument('--stream', action='store_true', help='Stream the chat response.')
    parser.add_argument('--component', choices=list(COMPONENTS))
    parser.add_argument('--tag')
    parser.add_argument('--archive', type=pathlib.Path)
    parser.add_argument('--allow-containerd-import', action='store_true')
    parser.add_argument('--result', type=pathlib.Path)
    parser.add_argument('--port', type=int, default=18443)
    parser.add_argument('--confirm-model-interruption', action='store_true')
    parser.add_argument('--retry', action='store_true', help='Archive an unsuccessful qualification and run new qualification and chain Jobs.')
    args = parser.parse_intermixed_args(argv)
    require(args.phase == 'chat' or (args.prompt is None and not args.stream), 'Prompt and --stream are supported only for chat.')
    require(not args.retry or args.phase == 'qualify', '--retry is supported only for qualify.')
    try:
        context, work, config_path, config = cli_settings(args)
    except ContextSelectionError as error:
        parser.exit(2, 'error: ' + str(error) + '\n')
    if args.phase == 'context':
        print(context)
        return
    if args.phase == 'init':
        require(config is None and not config_path.exists() and not (work/'state.json').exists(),
                'Configuration or deployment state already exists. Init does not overwrite an installation.')
        require(not config_path.is_relative_to(HERE.parents[3]), 'Keep generated configuration outside the checkout.')
        try:
            config = cluster_setup.discover_config(context, args.namespace)
        except cluster_setup.ClusterSetupError as error:
            parser.exit(2, 'error: ' + str(error) + '\n')
        validate(config)
        config_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd = os.open(config_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'w') as file:
            file.write(json.dumps(config, indent=2) + '\n')
        print('Configuration:', config_path)
        print('Model GPUs:', config['nodes']['leader'], 'and', config['nodes']['worker'])
        print('Routing node:', config['nodes']['control'])
        return
    if args.phase == 'paths':
        source = args.source_dir.expanduser().resolve() if args.source_dir else work/'source'
        print(json.dumps({'workDir': str(work), 'config': str(config_path), 'source': str(source)}, indent=2))
        return
    discovered = config is None
    if discovered:
        require(args.phase == 'attach-existing', 'Run attach-existing first, or provide --config for a new installation.')
        require(not (work/'state.json').exists(), 'Saved state is missing its configuration. Use a new work directory.')
        config = discover_config(context, args.namespace)
    recipe = Recipe(config, work, args.source_dir)
    if args.phase == 'prepare': recipe.prepare()
    elif args.phase == 'render': recipe.render()
    elif args.phase == 'inventory': recipe.inventory()
    elif args.phase == 'attach-existing':
        recipe.attach_existing()
        if discovered:
            save(config_path, config)
            print('Discovered configuration:', config_path)
        print('Run verify-gateway next. Run prepare before editing or building application images.')
    elif args.phase == 'build-images':
        try:
            recipe.build_images(args.component, args.tag)
        except DockerUnavailableError as error:
            parser.exit(2, 'error: ' + str(error) + '\n')
    elif args.phase == 'export-images': recipe.export_images(args.component, args.tag)
    elif args.phase == 'push-images':
        for name in ([args.component] if args.component else recipe.components()):
            run(['docker', 'push', recipe.image(name, args.tag)])
    elif args.phase == 'import-images':
        recipe.import_images(args.archive or recipe.work/'arm64-images.tar', args.allow_containerd_import, args.component, args.tag)
    elif args.phase == 'stack': recipe.deploy_stack()
    elif args.phase in ('preflight', 'build-runtime', 'qualify', 'download', 'load'):
        recipe.backend_phase({'build-runtime': 'build', 'load': 'serve'}.get(args.phase, args.phase), retry=args.retry)
    elif args.phase == 'verify-direct': recipe.verify(False, args.port)
    elif args.phase == 'register': recipe.register()
    elif args.phase == 'verify-gateway': recipe.verify(True, args.port)
    elif args.phase == 'chat': recipe.chat(args.prompt, args.stream, args.port)
    elif args.phase == 'cleanup-key': recipe.cleanup_key(args.port)
    elif args.phase == 'update':
        require(args.component in ('gateway', 'router') and args.tag, 'Update requires --component gateway|router and --tag.')
        recipe.update(args.component, args.tag)
    elif args.phase == 'rollback':
        require(args.result, '--result is required.')
        recipe.rollback(args.result)
    elif args.phase == 'recover': recipe.recovery(args.confirm_model_interruption, args.port)


if __name__ == '__main__':
    main()
