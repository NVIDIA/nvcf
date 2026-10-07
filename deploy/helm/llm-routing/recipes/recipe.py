#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Explicit phases for the LLM routing recipes. See README.md first."""
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
import monitoring
import monitoring_setup
import console_output
import sizing
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
    return console_output.run(command, **kwargs)


def output(command, **kwargs):
    return console_output.output(command, **kwargs)


RECIPE_KEYS = ('name', 'releaseName', 'llamaCppRevision', 'servedName', 'endpointName', 'firstShard',
               'artifactsSize', 'rpcCacheSize', 'canary', 'serverArgs', 'runtimeEnv', 'memory')
# The tool derives these from nodes.model; a recipe that sets them would conflict.
PLACEMENT_FLAGS = ('--device', '-dev', '--tensor-split', '-ts', '--rpc')
QUALIFICATION_ATTEMPT = 2
NAME = r'[a-z0-9]([-a-z0-9]*[a-z0-9])?'


def available_recipes():
    return sorted(path.parent.name for path in HERE.glob('*/recipe.json'))


def recipe_name(c):
    name = c.get('recipe')
    if name is None:
        names = available_recipes()
        require(len(names) == 1, 'Set recipe in the configuration. Available: ' + ', '.join(names))
        name = names[0]
    return name


def load_recipe(name):
    names = available_recipes()
    require(name in names, 'Unknown recipe: ' + str(name) + '. Available: ' + ', '.join(names))
    folder = HERE/name
    definition = json.loads((folder/'recipe.json').read_text())
    missing = [key for key in RECIPE_KEYS if key not in definition]
    require(not missing, 'Recipe ' + name + ' is missing: ' + ', '.join(missing))
    require(definition['name'] == name, 'Recipe name must match its folder: ' + name)
    require(re.fullmatch(NAME, definition['releaseName']) is not None, 'Invalid releaseName in recipe ' + name)
    flags = sorted({arg.split('=', 1)[0] for arg in definition['serverArgs']} & set(PLACEMENT_FLAGS))
    require(not flags, 'Recipe ' + name + ' must not set placement arguments (' + ', '.join(flags) +
            '). The tool derives them from nodes.model.')
    lock = json.loads((folder/'model.lock.json').read_text())
    require(definition['firstShard'] in [item['rfilename'] for item in lock['files']],
            'Recipe ' + name + ' firstShard is not in its model lock.')
    definition['lock'] = lock
    return definition


# Optional pod resource overrides: resources.<model|rpc>.<requests|limits>.<cpu|memory>.
RESOURCE_ROLES = ('model', 'rpc')
RESOURCE_KINDS = ('requests', 'limits')
RESOURCE_NAMES = ('cpu', 'memory')
QUANTITY = r'[0-9]+(\.[0-9]+)?(m|k|Ki|Mi|Gi|Ti|Pi|Ei|M|G|T|P|E)?'
QUANTITY_UNITS = {'': 1, 'm': 1e-3, 'k': 1e3, 'M': 1e6, 'G': 1e9, 'T': 1e12, 'P': 1e15, 'E': 1e18,
                  'Ki': 2**10, 'Mi': 2**20, 'Gi': 2**30, 'Ti': 2**40, 'Pi': 2**50, 'Ei': 2**60}


def quantity(value):
    match = re.fullmatch(r'([0-9.]+)([A-Za-z]*)', str(value))
    return float(match[1]) * QUANTITY_UNITS[match[2]]


def check_resources(resources):
    require(isinstance(resources, dict) and set(resources) <= set(RESOURCE_ROLES),
            'resources may set only model and rpc.')
    for role, spec in resources.items():
        require(isinstance(spec, dict), 'resources.'+role+' must be an object.')
        require(set(spec) <= set(RESOURCE_KINDS), 'resources.'+role+' may set only requests and limits.')
        for kind, values in spec.items():
            require(isinstance(values, dict), 'resources.'+role+'.'+kind+' must be an object.')
            for name, value in values.items():
                require(name != 'nvidia.com/gpu', 'resources.'+role+'.'+kind+' cannot set nvidia.com/gpu. Each model pod uses one GPU.')
                require(name in RESOURCE_NAMES, 'resources.'+role+'.'+kind+' may set only cpu and memory.')
                require(isinstance(value, str) and re.fullmatch(QUANTITY, value) is not None,
                        'resources.'+role+'.'+kind+'.'+name+' must be a Kubernetes quantity string, such as "64Gi" or "8".')


def with_overrides(resources, role, override):
    merged = copy.deepcopy(resources)
    for kind, values in (override or {}).items():
        merged[kind].update(values)
    for name in RESOURCE_NAMES:
        require(quantity(merged['requests'][name]) <= quantity(merged['limits'][name]),
                'resources.'+role+': '+name+' request '+merged['requests'][name]+' exceeds limit '+merged['limits'][name]+'.')
    return merged


def validate(c):
    load_recipe(recipe_name(c))
    for key in ('context', 'namespace', 'releasePrefix', 'clusterId', 'storageClass', 'runtimeClass'):
        require(isinstance(c.get(key), str) and bool(c[key].strip()), key + ' must be explicit.')
    for key in ('namespace', 'releasePrefix', 'clusterId'):
        require(re.fullmatch(r'[a-z0-9]([-a-z0-9]*[a-z0-9])?', c[key]) is not None, 'Invalid ' + key)
    require(len(c['releasePrefix']) <= 30, 'releasePrefix must be at most 30 characters.')
    nodes = c.get('nodes', {})
    require(set(nodes) == {'control', 'model'}, 'Set nodes.control and nodes.model.')
    require(isinstance(nodes['control'], str) and bool(nodes['control'].strip()), 'nodes.control must name a node.')
    model = nodes['model']
    require(isinstance(model, list) and all(isinstance(n, str) and n.strip() for n in model), 'nodes.model must list node names.')
    require(len(model) in sizing.MODEL_NODE_COUNTS, 'nodes.model must list 1 or 2 nodes.')
    require(len(set(model)) == len(model), 'nodes.model must not repeat a node.')
    try:
        sizing.check_gpu(c.get('gpu') or {})
    except ValueError as error:
        raise RuntimeError(str(error)) from None
    require(c['images']['pullPolicy'] in ('Never', 'IfNotPresent', 'Always'), 'Invalid pull policy.')
    require(re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', c['images']['tag']) is not None, 'Invalid image tag.')
    require('/' in c['images']['prefix'] and not c['images']['prefix'].endswith('/'), 'Use registry/path as image prefix.')
    require(not c['images'].get('pullSecrets'), 'Pylon does not propagate image-pull secrets. Use nodes with registry access or pre-import all application images.')
    require(c.get('caConfigMap'), 'caConfigMap is required for verified QUIC and client TLS.')
    require(not c.get('retainedModels'), 'Verification targets the recipe model. Remove retainedModels from the configuration.')
    require(not c.get('testFixture'), 'The recipe deploys its own model. Remove testFixture from the configuration.')
    if 'resources' in c:
        check_resources(c['resources'])
    monitoring.settings(c)



def all_nodes(c):
    return sorted({c['nodes']['control'], *c['nodes']['model']})


def default_work_dir(context):
    require(isinstance(context, str) and bool(context.strip()),
            'Set LLM_ROUTING_CONTEXT or --context. The current kubectl context is not selected automatically.')
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
        raise ContextSelectionError('Multiple Kubernetes contexts found. Pass --context NAME or set LLM_ROUTING_CONTEXT.')
    return names.pop()


def cli_settings(args):
    """Resolve local paths and context without changing files or the cluster."""
    config = None
    if args.config:
        path = args.config.expanduser()
        require(path.exists() or args.phase in ('init', 'attach-monitoring', 'monitoring', 'dashboard'), 'Configuration file does not exist. Run init, attach-existing or monitoring first.')
        if path.exists():
            config = json.loads(path.read_text())
    work = args.work_dir.expanduser().resolve() if args.work_dir else None
    if config is None and work and not args.config and (work/'config.json').exists():
        config = json.loads((work/'config.json').read_text())
    context = args.context or os.environ.get('LLM_ROUTING_CONTEXT') or (config.get('context') if config else None)
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
    require(isinstance(context, str) and bool(context.strip()), 'Set --context NAME or LLM_ROUTING_CONTEXT.')
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
    router = get('deployment', 'llm-request-router')
    require(owner(router) == stack, 'Gateway and router belong to different releases.')
    control = placement(gateway)
    require(placement(router) == control, 'This recipe requires gateway and router on the same control node.')
    endpoints = json.loads(output(kc+['get', 'inferenceendpoints', '-o', 'json']))['items']
    require(len(endpoints) == 1, 'Expected one InferenceEndpoint in namespace '+namespace+'.')
    endpoint = endpoints[0]
    matches = [name for name in available_recipes()
               if (load_recipe(name)['endpointName'], load_recipe(name)['servedName'])
               == (endpoint['metadata']['name'], endpoint['spec']['modelName'])]
    require(len(matches) == 1, 'Existing endpoint does not match a recipe in this checkout: '+endpoint['metadata']['name'])
    model = owner(endpoint)
    require(endpoint['spec']['service']['name'] == model, 'Existing endpoint does not serve its own Helm release.')
    backend = values(model)
    targets = backend['targets']
    require(targets and [t['id'] for t in targets] == ['n'+str(i) for i in range(len(targets))],
            'Model release targets are not in the recipe format.')
    leader = get('deployment', model)
    workers = [get('deployment', model+'-rpc-'+t['id']) for t in targets[1:]]
    service = get('service', model)
    require(all(owner(d) == model for d in [leader, service, *workers]), 'Unexpected model resource ownership.')
    require([placement(d) for d in [leader, *workers]] == [t['node'] for t in targets],
            'Model placement differs from Helm values.')
    nodes = {'control': control, 'model': [t['node'] for t in targets]}
    gpu = {key: backend.get('gpu', {}).get(key) for key in ('name', 'computeCapability', 'memoryGiB', 'unifiedMemory', 'cudaArchitectures')}
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
    prefix = model.removesuffix('-'+load_recipe(matches[0])['releaseName'])[:30]
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
    config = {'context': context, 'namespace': namespace, 'recipe': matches[0], 'releasePrefix': prefix, 'clusterId': v['clusterId'],
              'releases': {'stack': stack, 'operator': operator, 'model': model}, 'nodes': nodes, 'gpu': gpu,
              'storageClass': backend['artifacts']['storageClassName'], 'runtimeClass': backend['runtimeClassName'],
              'images': {'prefix': image_prefix, 'tag': image['tag'], 'pullPolicy': image['pullPolicy'],
                         'pullSecrets': [], 'repositories': images},
              'runtimeImage': backend['image'], 'tls': {'selfSigned': {'enabled': True}},
              'caConfigMap': ca, 'apiKeyFile': None, 'containerd': containerd}
    validate(config)
    return config


class Recipe:
    def __init__(self, config, work, source=None, monitoring_only=False):
        self.c = config
        self.work = pathlib.Path(work).expanduser().resolve()
        repo = HERE.parents[3]
        require(not self.work.is_relative_to(repo), 'Keep generated work and credentials outside the checkout.')
        self.source = pathlib.Path(source).expanduser().resolve() if source else repo
        self.state_path = self.work/'state.json'
        self.state = json.loads(self.state_path.read_text()) if self.state_path.exists() else {}
        require(monitoring_only or not self.state.get('attachedMonitoring'),
                'This work directory is attached for monitoring. Use the deployment owner work directory for other phases.')
        (monitoring_setup.validate if monitoring_only else validate)(config)
        self.work.mkdir(parents=True, exist_ok=True, mode=0o700)
        identity = {k: config[k] for k in ('context', 'namespace', 'releasePrefix', 'clusterId', 'nodes')}
        identity['releases'] = config.get('releases', {})
        require(not self.state or self.state['identity'] == identity, 'Work directory belongs to a different installation.')
        self.identity = identity
        self.kc = ['kubectl', '--context', config['context'], '-n', config['namespace']]
        self.hm = ['helm', '--kube-context', config['context'], '-n', config['namespace']]
        self.monitoring_only = monitoring_only
        self.definition = None if monitoring_only else load_recipe(recipe_name(config))
        # Target n0 runs the model server. Later targets run RPC workers.
        self.targets = [] if monitoring_only else [{'id': 'n'+str(index), 'node': node} for index, node in enumerate(config['nodes']['model'])]
        self.workers = self.targets[1:]
        self.plan = None if monitoring_only else sizing.memory_plan(self.definition, config['gpu'], len(self.targets))
        releases = config.get('releases', {})
        self.backend = None if monitoring_only else releases.get('model', config['releasePrefix'] + '-' + self.definition['releaseName'])
        self.operator = releases.get('operator', config['releasePrefix'] + '-operator')
        self.stack = releases.get('stack', config['releasePrefix'] + '-stack')
        names = (self.operator, self.stack) if monitoring_only else (self.backend, self.operator, self.stack)
        for name in names:
            require(re.fullmatch(r'[a-z0-9]([-a-z0-9]*[a-z0-9])?', name) is not None and len(name) <= 53, 'Invalid release name.')

    def reinitialize(self, config_path):
        require(not (self.work/'temporary-gateway-key.json').exists(),
                'A temporary gateway key still needs cleanup. No progress was reset.')
        if self.state.get('inventory'):
            self.bound_cluster()
        cluster_setup.validate_reinitialization(self.c, self.backend)
        if self.state_path.exists():
            require(json.loads(self.state_path.read_text()) == self.state,
                    'Progress changed during inspection. Stop concurrent recipe commands and retry init.')
            archive = pathlib.Path(tempfile.mkdtemp(prefix='before-reinit-', dir=self.work))
            save(archive/'config.json', self.c)
            self.state_path.rename(archive/'state.json')
            print('Previous progress archived:', archive)
        print('Reusing configuration:', config_path)
        print('Run render, inventory, then preflight.')

    def stamp(self, phase, value=True):
        self.state.update(identity=self.identity)
        self.state[phase] = value
        save(self.state_path, self.state)

    def image(self, component, tag=None):
        return self.repository(component) + ':' + (tag or self.c['images']['tag'])

    def repository(self, component):
        return self.c['images'].get('repositories', {}).get(component, self.c['images']['prefix'] + '/' + component)

    def source_identity(self):
        return {'revision': self.checkout_revision()}

    def source_check(self):
        require(self.source.is_dir(), 'Source checkout does not exist. Use --source-dir to select an existing checkout.')
        try:
            root = output(['git', 'rev-parse', '--show-toplevel'], cwd=self.source, stderr=subprocess.PIPE).strip()
        except (OSError, subprocess.CalledProcessError):
            raise RuntimeError('Source directory must be an existing Git checkout.') from None
        require(pathlib.Path(root).resolve() == self.source, 'Source directory must be the repository root.')
        require(all((self.source/path).is_dir() for path in COMPONENTS.values()),
                'Source checkout is missing required gateway, router or operator sources.')
        self.chart_digest()

    def chart_digest(self):
        digest = hashlib.sha256()
        for name in ('llm-gateway-stack', 'llm-api-gateway', 'llm-request-router'):
            chart = self.source/'deploy/helm'/name/name
            require((chart/'Chart.yaml').is_file(), 'Missing routing chart: '+name)
            for path in sorted(chart.rglob('*')):
                relative = path.relative_to(chart)
                if path.is_file() and (relative.parts[0] in ('templates', 'files', 'crds')
                        or str(relative) in ('Chart.yaml', 'values.yaml', 'values.schema.json', '.helmignore')):
                    digest.update((name+'/'+relative.as_posix()).encode()+b'\0')
                    digest.update(hashlib.sha256(path.read_bytes()).digest())
        return digest.hexdigest()

    def checkout_revision(self):
        revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=self.source, text=True).strip()
        dirty = subprocess.check_output(['git', 'status', '--porcelain'], cwd=self.source, text=True).strip()
        return revision + ('-dirty' if dirty else '')

    def prepare(self):
        self.source_check()
        run(['helm', 'dependency', 'build', '--skip-refresh', self.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack'])
        print('Using source checkout:', self.source)

    def backend_values(self, phase='serve', register=False, render=False):
        d, plan, gpu = self.definition, self.plan, self.c['gpu']
        overrides = self.c.get('resources', {})
        memory = {'requests': str(plan['requestGiB'])+'Gi', 'limits': str(plan['limitGiB'])+'Gi'}
        if phase == 'serve':
            rpc_resources = with_overrides({'requests': {'cpu': '2', 'memory': memory['requests'], 'nvidia.com/gpu': 1},
                                            'limits': {'cpu': '8', 'memory': memory['limits'], 'nvidia.com/gpu': 1}},
                                           'rpc', overrides.get('rpc'))
        else:
            rpc_resources = {'requests': {'cpu': '2', 'memory': '2Gi', 'nvidia.com/gpu': 1},
                             'limits': {'cpu': '8', 'memory': '8Gi', 'nvidia.com/gpu': 1}}
        return {
            'phase': phase, 'image': self.c['runtimeImage'], 'runtimeClassName': self.c['runtimeClass'],
            # The chart reads only gpu.product; attach-existing reads the rest back from the release.
            'targets': copy.deepcopy(self.targets), 'gpu': dict(gpu, product=sizing.gpu_product(gpu)),
            'artifacts': {'storageClassName': self.c['storageClass'], 'size': d['artifactsSize']},
            'build': {'revision': d['llamaCppRevision'], 'cudaArchitectures': sizing.cuda_architectures(gpu), 'parallel': 8},
            'runtime': {'sha256': self.state.get('runtimeSha256', 'a'*64 if render else ''), 'env': dict(d['runtimeEnv'])},
            'qualification': {'attempt': self.state.get('qualificationAttempt', QUALIFICATION_ATTEMPT)},
            'model': {'lock': d['lock'], 'servedName': d['servedName'], 'firstShard': d['firstShard'],
                      'endpointName': d['endpointName'], 'register': register, 'canary': dict(d['canary']),
                      'args': d['serverArgs'] + sizing.placement_args(len(self.targets)),
                      'resources': with_overrides({'requests': {'cpu': '4', 'memory': memory['requests'], 'nvidia.com/gpu': 1},
                                                   'limits': {'cpu': '12', 'memory': memory['limits'], 'nvidia.com/gpu': 1}},
                                                  'model', overrides.get('model'))},
            'rpc': {'resources': rpc_resources,
                    'cache': {'enabled': phase == 'serve' and bool(self.workers), 'size': d['rpcCacheSize'],
                              'storageClassName': self.c['storageClass']}},
            'chain': {'runtimeRelease': self.backend, 'artifactClaim': self.backend+'-artifacts',
                      'attempt': self.state.get('chainAttempt', 1)},
        }

    def operator_values(self):
        return {'fullnameOverride': self.operator, 'clusterId': self.c['clusterId'],
                'image': {'repository': self.repository('operator'), 'tag': self.c['images']['tag'], 'pullPolicy': self.c['images']['pullPolicy']},
                'router': {'grpcAddress': 'http://llm-request-router.'+self.c['namespace']+'.svc.cluster.local:50071'},
                'pylon': {'image': {'repository': self.repository('pylon'), 'tag': self.c['images']['tag'], 'pullPolicy': self.c['images']['pullPolicy']}},
                'watchNamespaces': [self.c['namespace']], 'trustBundle': {'configMap': self.c['caConfigMap']},
                'devInsecureTransport': False, 'nodeSelector': {'kubernetes.io/hostname': self.c['nodes']['control']}}

    def stack_values(self, token_hash, key_hash, ui_key=None):
        values = {'clusterId': self.c['clusterId'], 'clusterCredential': {'sha256': token_hash},
                  'apiKeys': [{'id': 'poc-client', 'sha256': key_hash}], 'tls': copy.deepcopy(self.c['tls']),
                  'recipeSource': self.source_identity(), 'recipeChartsSha256': self.chart_digest()}
        if ui_key is not None:
            values['apiKeys'].append({'id': 'demo-ui', 'sha256': hashlib.sha256(ui_key.encode()).hexdigest()})
            values['demoUiApiKey'] = ui_key
        values['tls'].setdefault('selfSigned', {})['caName'] = self.c['caConfigMap']
        for component, chart, service in [('gateway', 'llm-api-gateway', 'llmApiGateway'), ('router', 'llm-request-router', 'llmRequestRouter')]:
            registry, repository = self.repository(component).split('/', 1)
            values[chart] = {service: {'replicaCount': 1, 'nodeSelector': {'kubernetes.io/hostname': self.c['nodes']['control']},
                                      'image': {'registry': registry, 'repository': repository, 'tag': self.c['images']['tag'], 'pullPolicy': self.c['images']['pullPolicy']}}}
        if monitoring.enabled(self.c):
            for chart, service in [('llm-api-gateway', 'llmApiGateway'), ('llm-request-router', 'llmRequestRouter')]:
                values[chart][service]['metrics'] = {'enabled': True}
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
        if self.c['images']['pullPolicy'] == 'Never':
            eligible = {n['metadata']['name'] for n in nodes if n['metadata']['labels'].get('kubernetes.io/arch') == 'arm64'}
            imports = set(self.c['containerd'].get('nodeNames', all_nodes(self.c)))
            require(eligible <= imports, 'Pre-import Pylon on every ARM64 node where it can schedule. Set containerd.nodeNames explicitly.')
        roles = [('control', self.c['nodes']['control'])] + [('model', target['node']) for target in self.targets]
        for role, name in roles:
            found = [n for n in nodes if n['metadata']['name'] == name]
            require(len(found) == 1, 'Missing '+role+' node: '+name)
            node = found[0]
            require(node['metadata']['labels'].get('kubernetes.io/arch') == 'arm64', 'Selected nodes must be ARM64.')
            require(any(c['type'] == 'Ready' and c['status'] == 'True' for c in node['status']['conditions']), 'Node is not Ready.')
            if role == 'model':
                require(int(node['status']['allocatable'].get('nvidia.com/gpu', 0)) >= 1, 'GPU device plugin has not advertised a GPU on '+name+'.')
                busy = [p['metadata']['name'] for p in pods if p['spec'].get('nodeName') == name
                        and cluster_setup.requests_gpu(p)]
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
        print('Inventory passed.')

    def attach_existing(self):
        if self.state and not self.state.get('attachedExisting'):
            self.bound_cluster()
            require(all(self.state.get(phase) for phase in ('stack', 'serve', 'registered')),
                    'Saved installation is incomplete. Finish installing and registering model before attaching.')
            live = discover_config(self.c['context'], self.c['namespace'])
            for field in ('context', 'namespace', 'clusterId', 'nodes', 'gpu', 'runtimeClass',
                          'runtimeImage', 'storageClass', 'caConfigMap'):
                require(live[field] == self.c[field], 'Existing installation differs from saved configuration: '+field)
            require(live['releases'] == {'stack': self.stack, 'operator': self.operator, 'model': self.backend},
                    'Existing Helm releases differ from the saved installation.')
            for component in COMPONENTS:
                require(live['images']['repositories'][component] == self.repository(component),
                        'Existing image repository differs: '+component)
            # Retain installer checkpoints and credentials; attachment must not turn
            # an installation owner into a restricted iteration-only work directory.
            print('Existing installation matches the saved setup.')
            return
        if self.state:
            self.bound_cluster()
        require(set(self.c.get('releases', {})) >= {'stack', 'operator', 'model'}, 'Set explicit releases.stack, releases.operator and releases.model.')
        key = pathlib.Path(self.c['apiKeyFile']).expanduser().resolve(strict=True) if self.c.get('apiKeyFile') else None
        nodes = json.loads(output(self.kc+['get', 'nodes', '-o', 'json']))['items']
        names = {n['metadata']['name'] for n in nodes}
        require(set(all_nodes(self.c)) <= names, 'Configured placement nodes do not exist.')
        owners = {'llm-api-gateway': self.stack, 'llm-request-router': self.stack,
                  self.operator: self.operator, self.backend: self.backend}
        owners.update({self.backend+'-rpc-'+worker['id']: self.backend for worker in self.workers})
        for deployment, release in owners.items():
            obj = json.loads(output(self.kc+['get', 'deployment', deployment, '-o', 'json']))
            annotations = obj['metadata'].get('annotations', {})
            require(annotations.get('meta.helm.sh/release-name') == release and annotations.get('meta.helm.sh/release-namespace') == self.c['namespace'], 'Unexpected deployment ownership: '+deployment)
        values = json.loads(output(self.hm+['get', 'values', self.stack, '-o', 'json']))
        require(values.get('clusterId') == self.c['clusterId'], 'Existing cluster identity differs from configuration.')
        for component, chart, service in [('gateway', 'llm-api-gateway', 'llmApiGateway'), ('router', 'llm-request-router', 'llmRequestRouter')]:
            live = values[chart][service]['image']
            require(live['registry']+'/'+live['repository'] == self.repository(component), 'Existing image repository differs: '+component)
        endpoint = json.loads(output(self.kc+['get', 'inferenceendpoint', self.definition['endpointName'], '-o', 'json']))
        require(endpoint['spec']['service']['name'] == self.backend and endpoint['spec']['modelName'] == self.definition['servedName'], 'Existing model endpoint differs from the recipe.')
        ca = json.loads(output(self.kc+['get', 'configmap', self.c['caConfigMap'], '-o', 'json']))['data']['ca.crt']
        save(self.work/'ca.crt', ca)
        self.stamp('attachedExisting')
        self.stamp('inventory', {'nodes': {n['metadata']['name']: n['metadata']['uid'] for n in nodes}})
        self.stamp('stack', {'apiKeyFile': str(key) if key else None, 'source': values.get('recipeSource')})
        self.stamp('serve')
        print('Existing installation inspected. Run verify-gateway next.')
        if values.get('recipeChartsSha256') != self.chart_digest():
            console_output.warn('Image updates require matching recorded charts. Use a coordinated stack installation first.')

    def attach_monitoring(self):
        monitoring_setup.attach(self, output, save)

    def bound_cluster(self):
        require(self.state.get('inventory'), 'Run inventory for this installation first.')
        live = json.loads(output(self.kc+['get', 'nodes', '-o', 'json']))['items']
        old = self.state['inventory']['nodes']
        current = {n['metadata']['name']: n['metadata']['uid'] for n in live}
        selected = [self.c['nodes']['control']] if self.monitoring_only else all_nodes(self.c)
        require(all(old.get(name) and current.get(name) == old[name] for name in selected), 'The selected node identities changed or the context points to another cluster.')

    def logs(self, component, release=None, job=None):
        selector = 'app.kubernetes.io/instance='+(release or self.backend)+',app.kubernetes.io/component='+component
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
        prefixes = {'qualificationAttempt': self.backend+'-qualify-', 'chainAttempt': self.backend+'-chain-'}
        jobs = json.loads(output(self.kc+['get', 'jobs', '-o', 'json']))['items']
        jobs = [job for job in jobs if any(re.fullmatch(re.escape(prefix)+r'\d+', job['metadata']['name']) for prefix in prefixes.values())]
        require(bool(jobs), 'No qualification or chain Jobs to retry.')
        for job in jobs:
            name = job['metadata']['name']
            release = self.backend+'-chain' if name.startswith(prefixes['chainAttempt']) else self.backend
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
                console_output.warn('Could not retrieve logs for ' + name + '. Check the saved error and pod status.')
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
            item = json.loads(output(self.hm+['status', self.backend, '-o', 'json'], timeout=45))
            require(item.get('name') == self.backend and item.get('namespace') == self.c['namespace'],
                    'Unexpected model Helm release identity.')
            require(item.get('info', {}).get('status') == 'deployed',
                    'Model Helm release is '+str(item.get('info', {}).get('status'))+
                    '. Resolve the Helm operation, then rerun load.')
            return item['version']

        revision = release()
        values = json.loads(output(self.hm+['get', 'values', self.backend, '--revision', str(revision), '-o', 'json'], timeout=45))
        if values.get('phase') != 'serve':
            require(values.get('phase') == 'download', 'Model Helm release is not at the completed download or serve phase.')
            existing = output(self.kc+['get', 'deployment', self.backend, '--ignore-not-found', '-o', 'json'], timeout=45)
            require(not existing.strip(), 'A model Deployment already exists outside the expected serve phase.')
            require(release() == revision, 'Model Helm revision changed while checking load. Retry after the operation completes.')
            return False
        require(values == self.backend_values('serve'), 'Deployed model values differ from this load configuration.')
        leader = self.targets[0]['node']
        # (node, container, component, volume name, claim) for each Deployment; PVCs map to None.
        expected = {
            ('Deployment', self.backend): (leader, 'llama', 'model-server', 'artifacts', self.backend+'-artifacts'),
            ('Deployment', self.backend+'-artifacts'): (leader, 'artifacts', 'artifacts', 'artifacts', self.backend+'-artifacts'),
            ('PersistentVolumeClaim', self.backend+'-artifacts'): None,
        }
        for worker in self.workers:
            expected[('Deployment', self.backend+'-rpc-'+worker['id'])] = (
                worker['node'], 'rpc', 'rpc-'+worker['id'], 'rpc-cache', self.backend+'-rpc-cache-'+worker['id'])
            expected[('PersistentVolumeClaim', self.backend+'-rpc-cache-'+worker['id'])] = None
        names = [('deployment/' if kind == 'Deployment' else 'pvc/')+name for kind, name in expected]

        def ready_resources():
            items = json.loads(output(self.kc+['get', *names, '-o', 'json'], timeout=45))['items']
            require({(item.get('kind'), item['metadata']['name']) for item in items} == set(expected)
                    and len(items) == len(expected), 'Missing model resources while resuming load.')
            identities = {}
            for item in items:
                meta, spec, status = item['metadata'], item['spec'], item.get('status', {})
                name, kind = meta['name'], item['kind']
                owner = meta.get('annotations', {})
                require(meta.get('namespace') == self.c['namespace'] and not meta.get('deletionTimestamp')
                        and owner.get('meta.helm.sh/release-name') == self.backend
                        and owner.get('meta.helm.sh/release-namespace') == self.c['namespace']
                        and meta.get('labels', {}).get('app.kubernetes.io/managed-by') == 'Helm',
                        'Unexpected model resource ownership: '+name)
                require(meta.get('uid'), 'Missing model resource identity: '+name)
                if kind == 'PersistentVolumeClaim':
                    require(status.get('phase') == 'Bound' and spec.get('volumeName')
                            and spec.get('storageClassName') == self.c['storageClass'],
                            'Model storage is not bound as configured: '+name)
                    identities[kind+'/'+name] = [meta['uid'], spec['volumeName']]
                    continue
                generation = meta.get('generation')
                require(generation and status.get('observedGeneration') == generation
                        and spec.get('replicas') == 1
                        and all(status.get(field, 0) == 1 for field in
                                ('replicas', 'updatedReplicas', 'readyReplicas', 'availableReplicas'))
                        and not status.get('unavailableReplicas', 0),
                        'Model Deployment is not ready at its current generation: '+name+'. Wait, then rerun load.')
                node, container, component, volume, claim = expected[(kind, name)]
                template = spec['template']
                labels = {'app.kubernetes.io/instance': self.backend, 'app.kubernetes.io/component': component}
                require(all(template['metadata'].get('labels', {}).get(key) == value for key, value in labels.items())
                        and spec.get('selector', {}).get('matchLabels') == labels,
                        'Unexpected model Deployment selector: '+name)
                pod = template['spec']
                require(pod.get('nodeSelector', {}).get('kubernetes.io/hostname') == node,
                        'Model Deployment targets another node: '+name)
                containers = pod.get('containers', [])
                require(len(containers) == 1 and containers[0].get('name') == container
                        and containers[0].get('image') == self.c['runtimeImage'],
                        'Model Deployment image differs: '+name)
                volumes = {item['name']: item for item in pod.get('volumes', [])}
                mounts = {item['name']: item for item in containers[0].get('volumeMounts', [])}
                require(volumes.get(volume, {}).get('persistentVolumeClaim', {}).get('claimName') == claim
                        and mounts.get(volume, {}).get('mountPath') == '/'+volume,
                        'Model Deployment storage differs: '+name)
                if component == 'model-server':
                    env = {item['name']: item.get('value') for item in containers[0].get('env', [])}
                    require(env.get('FIRST_SHARD') == values['model']['firstShard']
                            and env.get('SERVED_MODEL') == values['model']['servedName']
                            and env.get('RPC_ENDPOINTS') == ','.join(self.backend+'-rpc-'+w['id']+':50052' for w in self.workers)
                            and json.loads(env.get('SERVER_ARGS') or 'null') == values['model']['args'],
                            'Model server settings or RPC connection differ: '+name)
                if component != 'artifacts':
                    require(pod.get('runtimeClassName') == self.c['runtimeClass']
                            and template['metadata'].get('annotations', {}).get('checksum/runtime') == self.state['runtimeSha256']
                            and all(str(containers[0].get('resources', {}).get(field, {}).get('nvidia.com/gpu')) == '1'
                                    for field in ('requests', 'limits')),
                            'Model GPU or runtime configuration differs: '+name)
                identities[kind+'/'+name] = [meta['uid'], generation]
            return identities

        identities = ready_resources()
        require(ready_resources() == identities and release() == revision,
                'Model resources or Helm revision changed while resuming load. Retry after the operation completes.')
        save(self.work/'evidence'/'load-resume.json', {'release': self.backend, 'revision': revision, 'resources': identities})
        self.stamp('serve')
        print('Resumed completed model load. Run verify-direct next.')
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
            for target in self.targets:
                raw = output(self.kc+['exec', 'deploy/'+self.backend+'-rpc-'+target['id'], '-c', 'rpc', '--', 'cat', '/proc/meminfo'])
                available = next(int(line.split()[1])*1024 for line in raw.splitlines() if line.startswith('MemAvailable:'))
                require(available > self.plan['hostAvailableGiB']*1024**3,
                        'Insufficient host memory on '+target['node']+': '+str(available//1024**3)+' GiB available, more than '+
                        str(self.plan['hostAvailableGiB'])+' GiB required.')
        if phase == 'preflight':
            require(self.plan['fits'], self.definition['name']+' does not fit on '+str(len(self.targets))+' node(s) of '+
                    self.c['gpu']['name']+' per gpu.memoryGiB. Add a model node to nodes.model or fix the gpu settings.')
        if phase == 'qualify':
            require(re.fullmatch(r'[a-f0-9]{64}', self.state['runtimeSha256']) is not None, 'Invalid built runtime checksum.')
            if retry:
                self.retry_qualification()
            self.state['download'] = False
            self.stamp('qualify', False)
        values = self.backend_values(phase)
        timeout = {'preflight': '30m', 'build': '120m', 'qualify': '20m', 'download': '360m', 'serve': '70m'}[phase]
        self.helm_apply(self.backend, HERE/'charts/gguf-backend', values, timeout, jobs=phase != 'serve')
        if phase in ('preflight', 'build', 'download'):
            records = self.logs(phase)
            require(bool(records), phase+' did not record PASS.')
            if phase == 'preflight':
                self.check_preflight(records)
            if phase == 'build':
                self.stamp('runtimeSha256', records[-1]['runtimeSHA256'])
            if phase == 'download':
                lock = self.definition['lock']
                require(records[-1]['verifiedBytes'] == lock['weightFileBytes'] and records[-1]['verifiedFiles'] == len(lock['files']), 'Model download incomplete.')
        elif phase == 'qualify':
            records = self.logs('qualification', job=self.backend+'-qualify-'+str(values['qualification']['attempt']))
            require(bool(records), 'RPC qualification did not record PASS.')
            if not self.workers:
                self.stamp(phase)
                return
            chain = self.backend_values('chain')
            self.helm_apply(self.backend+'-chain', HERE/'charts/gguf-backend', chain, '15m', jobs=True)
            records = self.logs('chain-check', job=self.backend+'-chain-'+str(chain['chain']['attempt']))
            require(bool(records), 'RPC chain check did not record PASS.')
        self.stamp(phase)

    def check_preflight(self, records):
        """Compare each model node's measured GPU and memory with the configuration."""
        gpu = self.c['gpu']
        require(len(records) == len(self.targets),
                'Every model node must pass CUDA preflight: '+str(len(records))+' of '+str(len(self.targets))+' passed.')
        for record in records:
            capability = '.'.join(str(part) for part in record['capability'])
            require(sizing.gpu_product({'name': record['gpu']}) == sizing.gpu_product(gpu) and capability == gpu['computeCapability'],
                    'Detected GPU '+record['gpu']+' ('+capability+') differs from gpu settings '+gpu['name']+' ('+
                    gpu['computeCapability']+'). Run init again or correct the gpu section.')
            available = record['memoryBefore']['MemAvailable']
            require(available > self.plan['hostAvailableGiB']*1024**3,
                    'Insufficient host memory before CUDA allocation: '+str(available//1024**3)+' GiB available, more than '+
                    str(self.plan['hostAvailableGiB'])+' GiB required.')
            if self.plan['gpuFreeGiB'] is not None:
                free = record['cudaFreeBytes']
                require(free >= self.plan['gpuFreeGiB']*1024**3,
                        'Insufficient GPU memory: '+str(free//1024**3)+' GiB free, '+str(self.plan['gpuFreeGiB'])+' GiB required.')

    def deploy_stack(self):
        require(not self.state.get('attachedExisting'), 'Existing installations support verification and image iteration, not fresh stack deployment.')
        self.bound_cluster()
        self.prepare()
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
        ui_key_path = self.work/'demo-ui-api-key'
        if not ui_key_path.exists():
            save(ui_key_path, secrets.token_urlsafe(48)+'\n')
        ui_key = ui_key_path.read_text().strip()
        require(bool(ui_key) and ui_key != key, 'The demo UI needs a nonempty, separate API key.')
        values = self.stack_values(token_hash, hashlib.sha256(key.encode()).hexdigest(), ui_key)
        self.helm_apply(self.stack, self.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack', values)
        ca = json.loads(output(self.kc+['get', 'configmap', self.c['caConfigMap'], '-o', 'json']))['data']['ca.crt']
        save(self.work/'ca.crt', ca)
        self.stamp('stack', {'apiKeyFile': str(key_path), 'source': self.source_identity()})
        if monitoring.enabled(self.c):
            monitoring.Monitoring(self, run, output, save).install()

    def register(self):
        require(not self.state.get('attachedExisting'), 'Do not re-register or adopt an attached existing backend.')
        self.bound_cluster()
        require(self.state.get('serve') and self.state.get('direct') and self.state.get('stack'), 'Load, directly verify the model, and deploy the stack before registration.')
        self.helm_apply(self.backend, HERE/'charts/gguf-backend', self.backend_values(register=True), '10m')
        for condition in ('Ready', 'TransportReady', 'Registered'):
            run(self.kc+['wait', 'inferenceendpoint/'+self.definition['endpointName'], '--for=condition='+condition, '--timeout=300s'])
        self.stamp('registered')

    @contextlib.contextmanager
    def forward(self, gateway, port):
        service = 'llm-api-gateway' if gateway else self.backend
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
                   '--model', self.definition['servedName'], '--ca-file', str(self.work/'ca.crt')]
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
                   '--model', self.definition['servedName'], '--output', str(self.work/'evidence'/('gateway.json' if gateway else 'direct.json'))]
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
                command += ['-f', str(HERE/'operator.Dockerfile'), '--build-arg', 'SOURCE_REVISION='+self.checkout_revision()]
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

    def import_images(self, archive, allow, component=None, tag=None, monitoring_only=False):
        require(allow, 'Import requires --allow-containerd-import, which grants the Jobs access to node runtime sockets.')
        self.bound_cluster()
        import tarfile
        archive = pathlib.Path(archive).resolve(strict=True)
        archive_limit = monitoring.MAX_ARCHIVE_BYTES if monitoring_only else 1024**3
        require(archive.stat().st_size < archive_limit, 'The importer archive limit is '+str(archive_limit // 1024**3)+' GiB.')
        with tarfile.open(archive) as tar:
            manifest = json.load(tar.extractfile('manifest.json'))
        tags = {value for entry in manifest for value in entry.get('RepoTags', [])}
        expected = monitoring.image_list(self) if monitoring_only else [self.image(name, tag) for name in ([component] if component else self.components())]
        for image in expected:
            aliases = {image, image.removeprefix('docker.io/'), image.removeprefix('docker.io/library/')}
            require(bool(tags & aliases), 'Archive is missing the configured image: '+image)
        cfg = self.c.get('containerd')
        require(cfg, 'No image importer was discovered. Use registry distribution or configure containerd import settings.')
        nodes = [self.c['nodes']['control']] if monitoring_only or component in ('gateway', 'router') else cfg.get('nodeNames', all_nodes(self.c))
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
                  'archiveSizeLimit': str(archive_limit // 1024**3)+'Gi',
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
            if size > expected_size:
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
        require(not self.state.get('attachedExisting') or self.state.get('gateway'), 'Verify the model through the attached gateway before its first update.')
        chart, service = {'gateway': ('llm-api-gateway', 'llmApiGateway'), 'router': ('llm-request-router', 'llmRequestRouter')}[component]
        require(re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', tag) is not None, 'Invalid image tag.')
        values = json.loads(output(self.hm+['get', 'values', self.stack, '-o', 'json']))
        require(values.get('recipeChartsSha256') == self.chart_digest(),
                'Installed chart fingerprint is missing or differs from this checkout. Use a coordinated stack installation before image updates.')
        current = values[chart][service]['image']
        require(current['registry']+'/'+current['repository'] == self.repository(component), 'Live image repository differs from config.')
        require(current['tag'] != tag, 'Use a fresh tag.')
        self.prepare()
        before = json.loads(output(self.kc+['get', 'pods', '-o', 'json']))['items']
        value_key = chart+'.'+service+'.image.tag'
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
        path = self.work/'evidence'/('update-'+stamp+'.json')
        result = {'component': component, 'previousTag': current['tag'], 'newTag': tag, 'helmValueChanged': value_key,
                  'context': self.c['context'], 'namespace': self.c['namespace'], 'release': self.stack,
                  'chart': str(self.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack'),
                  'source': self.source_identity(), 'chartsSha256': self.chart_digest()}
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
        require(result.get('chartsSha256') == self.chart_digest(),
                'Rollback record has a different or missing chart fingerprint. Use a coordinated stack installation.')
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
        # Split models lose their RPC workers; a single-node model loses its model server.
        if self.workers:
            down['rpc']['replicas'] = 0
            stopped = {'rpc-'+worker['id'] for worker in self.workers}
        else:
            down['model']['replicas'] = 0
            stopped = {'model-server'}
        # Validate the same direct path before making an intentional interruption.
        self.verify(False, port)
        started = time.monotonic()
        observation = {'started': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'interruptionObserved': False}
        try:
            self.helm_apply(self.backend, HERE/'charts/gguf-backend', down, wait=False)
            time.sleep(15)
            down_pods = json.loads(output(self.kc+['get', 'pods', '-o', 'json']))
            save(self.work/'evidence/recovery-down.json', down_pods)
            require(not any(p['metadata'].get('labels', {}).get('app.kubernetes.io/instance') == self.backend
                            and p['metadata'].get('labels', {}).get('app.kubernetes.io/component') in stopped
                            and p['status']['phase'] == 'Running' for p in down_pods['items']),
                    'Interrupted model pods are still running. Restore and inspect the rollout.')
            try:
                with self.forward(False, port):
                    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=10)
                    try:
                        connection.request('POST', '/v1/chat/completions', json.dumps({'model': values['model']['servedName'], 'messages': [{'role': 'user', 'content': 'What is 31 plus 17?'}], 'max_tokens': 32}), {'Content-Type': 'application/json'})
                        response = connection.getresponse()
                        response.read()
                        observation['statusWhileInterrupted'] = response.status
                        observation['interruptionObserved'] = response.status != 200
                    finally:
                        connection.close()
            except (OSError, http.client.HTTPException, RuntimeError) as error:
                observation['interruptionObserved'] = True
                observation['failureType'] = type(error).__name__
        finally:
            self.helm_apply(self.backend, HERE/'charts/gguf-backend', values, '70m')
            observation['restoreSeconds'] = time.monotonic() - started
            save(self.work/'evidence/recovery.json', observation)
        require(observation['interruptionObserved'], 'Model interruption was not demonstrated. Recovery restored the model but this test did not prove the failure path.')
        self.verify(False, port)
        self.verify(True, port+1)

    def render(self):
        self.prepare()
        renders = [('stack', self.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack', self.stack_values('a'*64, 'b'*64, 'offline-demo-ui-placeholder')),
                   ('operator', self.source/'deploy/helm/pylon-operator/pylon-operator', self.operator_values())]
        phases = ('preflight', 'build', 'qualify', 'chain', 'download', 'serve') if self.workers else ('preflight', 'build', 'qualify', 'download', 'serve')
        for phase in phases:
            renders.append((self.definition['releaseName']+'-'+phase, HERE/'charts/gguf-backend', self.backend_values(phase, register=phase=='serve', render=True)))
        if monitoring.enabled(self.c):
            values = monitoring.chart_values(self)
            values['grafana']['adminPassword'] = 'offline-render-only'
            renders.append(('monitoring', monitoring.CHART, values))
        for name, chart, values in renders:
            path = self.work/'render'/(name+'-values.json')
            save(path, values)
            # Offline: no kube context, lookup, or API traffic. Generated TLS stays private.
            run(['helm', 'lint', chart, '-f', path])
            release = {'stack': self.stack, 'operator': self.operator, self.definition['releaseName']+'-chain': self.backend+'-chain',
                       'monitoring': self.c['releasePrefix']+'-monitoring'}.get(name, self.backend)
            save(self.work/'render'/(name+'.yaml'), output(['helm', 'template', release, chart, '-n', self.c['namespace'], '-f', path]))
        print('Helm lint/render passed for', len(renders), 'configurations.')


def main(argv=None, console=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=pathlib.Path)
    parser.add_argument('--context', help='Kubernetes context; defaults to LLM_ROUTING_CONTEXT, saved settings, or the sole kubeconfig context.')
    parser.add_argument('--namespace', help='Select the namespace when the cluster has multiple installations.')
    parser.add_argument('--work-dir', type=pathlib.Path, help='Private local state directory; defaults to a per-context directory.')
    parser.add_argument('--source-dir', type=pathlib.Path, help='Existing source checkout; defaults to the checkout containing this script.')
    parser.add_argument('--recipe', help='Recipe folder for init; defaults to the only recipe present. Available: '+', '.join(available_recipes())+'.')
    parser.add_argument('phase', choices=['init', 'paths', 'context', 'prepare', 'render', 'inventory', 'attach-existing', 'attach-monitoring', 'build-images', 'push-images', 'export-images', 'import-images', 'stack', 'preflight', 'build-runtime', 'qualify', 'download', 'load', 'verify-direct', 'register', 'verify-gateway', 'chat', 'cleanup-key', 'update', 'rollback', 'recover', 'monitoring', 'dashboard', 'verify-monitoring', 'uninstall-monitoring', 'monitoring-images', 'export-monitoring-images', 'import-monitoring-images'])
    parser.add_argument('prompt', nargs='?', help='Prompt for the chat command.')
    parser.add_argument('--stream', action='store_true', help='Stream the chat response.')
    parser.add_argument('--component', choices=list(COMPONENTS))
    parser.add_argument('--tag')
    parser.add_argument('--archive', type=pathlib.Path)
    parser.add_argument('--allow-containerd-import', action='store_true')
    parser.add_argument('--result', type=pathlib.Path)
    parser.add_argument('--port', type=int, help='Local port; defaults to 13000 for monitoring/dashboard and 18443 for other commands.')
    parser.add_argument('--admin', action='store_true', help='Open the dashboard in your browser, signed in as admin.')
    parser.add_argument('--confirm-model-interruption', action='store_true')
    parser.add_argument('--verify-traffic', action='store_true', help='Send gateway verification requests and check monitoring counter increases.')
    parser.add_argument('--model', help='Served model to use with verify-monitoring --verify-traffic. Defaults to monitoring.model or gateway discovery.')
    parser.add_argument('--retry', action='store_true', help='Archive an unsuccessful qualification and run new qualification and chain Jobs.')
    args = parser.parse_intermixed_args(argv)
    if console:
        console.phase = args.phase
    require(args.phase == 'chat' or (args.prompt is None and not args.stream), 'Prompt and --stream are supported only for chat.')
    require(not args.verify_traffic or args.phase == 'verify-monitoring', '--verify-traffic requires verify-monitoring.')
    require(args.model is None or (args.phase == 'verify-monitoring' and args.verify_traffic), '--model requires verify-monitoring --verify-traffic.')
    require(not args.retry or args.phase == 'qualify', '--retry is supported only for qualify.')
    require(not args.recipe or args.phase == 'init', '--recipe is supported only for init. Later commands use the saved configuration.')
    require(not args.admin or args.phase == 'dashboard', '--admin is supported only for dashboard.')
    if args.port is None:
        args.port = 13000 if args.phase in ('monitoring', 'dashboard') else 18443
    try:
        context, work, config_path, config = cli_settings(args)
    except ContextSelectionError as error:
        if console:
            raise
        parser.exit(2, 'error: ' + str(error) + '\n')
    action = lambda: execute(args, parser, context, work, config_path, config)
    result = console.run(args.phase, work, action) if console else action()
    if args.phase == 'monitoring':
        action = lambda: result.dashboard(args.port)
        return console.run('dashboard', work, action) if console else action()
    return result


def execute(args, parser, context, work, config_path, config):
    if args.phase == 'context':
        print(context)
        return
    if args.phase == 'init':
        if config is not None:
            try:
                Recipe(config, work, args.source_dir).reinitialize(config_path)
            except cluster_setup.ClusterSetupError as error:
                parser.exit(2, 'error: ' + str(error) + '\n')
            return
        require(not config_path.exists() and not (work/'state.json').exists(),
                'Saved state is missing its configuration. Init does not overwrite an installation.')
        require(not config_path.is_relative_to(HERE.parents[3]), 'Keep generated configuration outside the checkout.')
        try:
            definition = load_recipe(args.recipe or recipe_name({}))
            config = cluster_setup.discover_config(context, args.namespace, definition)
        except cluster_setup.ClusterSetupError as error:
            parser.exit(2, 'error: ' + str(error) + '\n')
        validate(config)
        config_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd = os.open(config_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'w') as file:
            file.write(json.dumps(config, indent=2) + '\n')
        print('Configuration created:', config_path)
        print('Recipe:', config['recipe'])
        print('GPU:', config['gpu']['name'], '('+config['gpu']['computeCapability']+',',
              str(config['gpu']['memoryGiB'])+' GiB', 'shared with the CPU)' if config['gpu']['unifiedMemory'] else 'GPU memory)')
        print('Model nodes:', ', '.join(config['nodes']['model']))
        print('Routing node:', config['nodes']['control'])
        print('Run render, inventory, then preflight.')
        return
    if args.phase == 'paths':
        source = args.source_dir.expanduser().resolve() if args.source_dir else HERE.parents[3]
        print(json.dumps({'workDir': str(work), 'config': str(config_path), 'source': str(source)}, indent=2))
        return
    discovered = config is None
    if discovered:
        require(args.phase in ('attach-existing', 'attach-monitoring', 'monitoring', 'dashboard'), 'Run attach-existing first for deployment commands, or monitoring to install the dashboard.')
        require(not (work/'state.json').exists(), 'Saved state is missing its configuration. Use a new work directory.')
        require(not config_path.is_relative_to(HERE.parents[3]), 'Keep generated configuration outside the checkout.')
        config = (monitoring_setup.discover_config(context, args.namespace, output) if args.phase in ('attach-monitoring', 'monitoring', 'dashboard')
                  else discover_config(context, args.namespace))
    saved_config = config
    enable_monitoring = args.phase == 'monitoring' and not monitoring.enabled(config)
    if enable_monitoring:
        require(not config_path.is_relative_to(HERE.parents[3]), 'Keep generated configuration outside the checkout.')
        config = copy.deepcopy(config)
        config.setdefault('monitoring', {})['enabled'] = True
    monitoring_only = args.phase in ('attach-monitoring', 'monitoring', 'dashboard', 'verify-monitoring', 'uninstall-monitoring',
                                    'monitoring-images', 'export-monitoring-images', 'import-monitoring-images', 'cleanup-key')
    recipe = Recipe(config, work, args.source_dir, monitoring_only=monitoring_only)
    if args.phase in ('monitoring', 'dashboard'):
        if not (recipe.state.get('inventory') and recipe.state.get('stack')):
            recipe.attach_monitoring()
        if discovered:
            save(config_path, config)
            print('Discovered monitoring configuration:', config_path)
    if args.phase == 'prepare': recipe.prepare()
    elif args.phase == 'render': recipe.render()
    elif args.phase == 'inventory': recipe.inventory()
    elif args.phase == 'attach-existing':
        recipe.attach_existing()
        if discovered:
            save(config_path, config)
            print('Discovered configuration:', config_path)
        print('Run verify-gateway next.')
    elif args.phase == 'attach-monitoring':
        recipe.attach_monitoring()
        if discovered:
            save(config_path, config)
            print('Discovered monitoring configuration:', config_path)
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
    elif args.phase == 'monitoring':
        monitor = monitoring.Monitoring(recipe, run, output, save)
        monitor.install()
        if enable_monitoring and not discovered:
            require(config_path.exists() and json.loads(config_path.read_text()) == saved_config,
                    'Configuration changed during monitoring installation. Check saved settings before retrying.')
            save(config_path, config)
        return monitor
    elif args.phase == 'dashboard': monitoring.Monitoring(recipe, run, output, save).dashboard(args.port, admin=args.admin)
    elif args.phase == 'verify-monitoring': monitoring.Monitoring(recipe, run, output, save).verify(args.port, args.verify_traffic, args.model)
    elif args.phase == 'uninstall-monitoring': monitoring.Monitoring(recipe, run, output, save).uninstall()
    elif args.phase == 'monitoring-images': print('\n'.join(monitoring.image_list(recipe)))
    elif args.phase == 'export-monitoring-images':
        monitoring.Monitoring(recipe, run, output, save).export_images(args.archive or recipe.work/'monitoring-arm64-images.tar')
    elif args.phase == 'import-monitoring-images':
        recipe.import_images(args.archive or recipe.work/'monitoring-arm64-images.tar', args.allow_containerd_import, monitoring_only=True)
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


def cli(argv=None):
    console = console_output.Console()
    try:
        main(argv, console=console)
        return 0
    except SystemExit as error:
        if not console.log_path:
            return error.code
        return console.failure(error)
    except (Exception, KeyboardInterrupt) as error:
        return console.failure(error)


if __name__ == '__main__':
    sys.exit(cli())
