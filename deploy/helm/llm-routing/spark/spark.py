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
import time

HERE = pathlib.Path(__file__).resolve().parent
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

    def source_check(self):
        require(self.source.is_dir(), 'Run prepare first, or pass --source-dir pointing to the prepared pinned checkout.')
        require(output(['git', 'rev-parse', 'HEAD'], cwd=self.source).strip() == LOCK['revision'], 'Dependency source revision differs from source.lock.json.')
        patch = HERE/LOCK['patch']
        require(hashlib.sha256(patch.read_bytes()).hexdigest() == LOCK['patchSha256'], 'Dependency patch checksum mismatch.')
        run(['git', 'apply', '--reverse', '--check', patch], cwd=self.source, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def prepare(self):
        if not self.source.exists():
            run(['git', 'clone', '--filter=blob:none', '--no-checkout', LOCK['repository'], self.source])
            run(['git', 'fetch', 'origin', LOCK['revision']], cwd=self.source)
            run(['git', 'checkout', '--detach', LOCK['revision']], cwd=self.source)
        require(output(['git', 'rev-parse', 'HEAD'], cwd=self.source).strip() == LOCK['revision'], 'Use the exact dependency revision.')
        patch = HERE/LOCK['patch']
        require(hashlib.sha256(patch.read_bytes()).hexdigest() == LOCK['patchSha256'], 'Dependency patch checksum mismatch.')
        applied = subprocess.run(['git', 'apply', '--reverse', '--check', str(patch)], cwd=self.source, capture_output=True).returncode == 0
        if not applied:
            run(['git', 'apply', '--check', patch], cwd=self.source)
            run(['git', 'apply', patch], cwd=self.source)
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
                  'apiKeys': [{'id': 'poc-client', 'sha256': key_hash}], 'tls': copy.deepcopy(self.c['tls'])}
        values['tls'].setdefault('selfSigned', {})['caName'] = self.c['caConfigMap']
        for component, chart, service in [('gateway', 'llm-api-gateway', 'llmApiGateway'), ('router', 'llm-request-router', 'llmRequestRouter')]:
            registry, repository = self.repository(component).split('/', 1)
            values[chart] = {service: {'replicaCount': 1, 'nodeSelector': {'kubernetes.io/hostname': self.c['nodes']['control']},
                                      'image': {'registry': registry, 'repository': repository, 'tag': self.c['images']['tag'], 'pullPolicy': self.c['images']['pullPolicy']}}}
        values['llm-api-gateway']['llmApiGateway']['olric'] = {'env': 'local'}
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
        require(set(self.c.get('releases', {})) >= {'stack', 'operator', 'glm'}, 'Set explicit releases.stack, releases.operator and releases.glm.')
        require(self.c.get('apiKeyFile'), 'Existing installation access requires an approved apiKeyFile.')
        key = pathlib.Path(self.c['apiKeyFile']).expanduser().resolve(strict=True)
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
        self.stamp('stack', {'apiKeyFile': str(key)})
        self.stamp('serve')
        print('Existing installation inspected without changing it. Run verify-gateway before update.')

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

    def backend_phase(self, phase, retry=False):
        self.bound_cluster()
        require(not self.state.get('serve'), 'The model has already been loaded. Use the update or explicit recovery commands, not preparation phases.')
        require(not retry or phase == 'qualify', '--retry is supported only for qualify.')
        prerequisites = {'preflight': 'inventory', 'build': 'preflight', 'qualify': 'runtimeSha256', 'download': 'qualify', 'serve': 'download'}
        require(self.state.get(prerequisites[phase]), 'Missing successful '+prerequisites[phase]+' phase.')
        if phase == 'serve':
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
        self.stamp('stack', {'apiKeyFile': str(key_path)})

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

    def verify(self, gateway, port):
        self.bound_cluster()
        if gateway:
            require(self.state.get('stack'), 'Deploy the stack first.')
        command = [sys.executable, str(HERE/'client.py'), '--mode', 'verify', '--url', ('https' if gateway else 'http')+'://127.0.0.1:'+str(port),
                   '--output', str(self.work/'evidence'/('gateway.json' if gateway else 'direct.json'))]
        if gateway:
            command += ['--ca-file', str(self.work/'ca.crt'), '--api-key-file', self.state['stack']['apiKeyFile']]
        with self.forward(gateway, port):
            run(command)
        self.stamp('gateway' if gateway else 'direct')

    def build_images(self, component=None, tag=None):
        self.source_check()
        for name in ([component] if component else self.components()):
            command = ['docker', 'buildx', 'build', '--platform', 'linux/arm64', '--load', '--tag', self.image(name, tag)]
            if name in ('router', 'pylon'):
                command += ['--target', 'stargate-runtime' if name == 'router' else 'pylon-runtime', '--build-arg', 'CARGO_PROFILE=integration']
            if name == 'operator':
                command += ['-f', str(HERE/'operator.Dockerfile'), '--build-arg', 'SOURCE_REVISION='+LOCK['revision']+'+spark-poc']
            run(command+[str(self.source/COMPONENTS[name])])

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
        cfg = self.c['containerd']
        nodes = [self.c['nodes']['control']] if component in ('gateway', 'router') else cfg.get('nodeNames', sorted(set(self.c['nodes'].values())))
        with archive.open('rb') as stream:
            archive_hash = hashlib.file_digest(stream, 'sha256').hexdigest()
        values = {'enabled': True, 'nodeNames': nodes, 'archiveNode': cfg['archiveNode'],
                  'archiveDirectory': cfg['archiveDirectory'], 'archiveName': archive.name,
                  'archiveSha256': archive_hash,
                  'runAsUser': cfg['runAsUser'], 'socketPath': cfg['socketPath']}
        self.helm_apply(self.c['releasePrefix']+'-images', HERE/'charts/image-loader', values, '10m', jobs=True)

    def update(self, component, tag):
        self.bound_cluster()
        self.source_check()
        require(not self.state.get('attachedExisting') or self.state.get('gateway'), 'Verify GLM through the attached gateway before its first update.')
        chart, service = {'gateway': ('llm-api-gateway', 'llmApiGateway'), 'router': ('llm-request-router', 'llmRequestRouter')}[component]
        require(re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', tag) is not None, 'Invalid image tag.')
        values = json.loads(output(self.hm+['get', 'values', self.stack, '-o', 'json']))
        current = values[chart][service]['image']
        require(current['registry']+'/'+current['repository'] == self.repository(component), 'Live image repository differs from config.')
        require(current['tag'] != tag, 'Use a fresh tag.')
        before = json.loads(output(self.kc+['get', 'pods', '-o', 'json']))['items']
        value_key = chart+'.'+service+'.image.tag'
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
        path = self.work/'evidence'/('update-'+stamp+'.json')
        result = {'component': component, 'previousTag': current['tag'], 'newTag': tag, 'helmValueChanged': value_key,
                  'context': self.c['context'], 'namespace': self.c['namespace'], 'release': self.stack,
                  'chart': str(self.source/'deploy/helm/llm-gateway-stack/llm-gateway-stack')}
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True, type=pathlib.Path)
    parser.add_argument('--work-dir', required=True, type=pathlib.Path)
    parser.add_argument('--source-dir', type=pathlib.Path)
    parser.add_argument('phase', choices=['prepare', 'render', 'inventory', 'attach-existing', 'build-images', 'push-images', 'import-images', 'stack', 'preflight', 'build-runtime', 'qualify', 'download', 'load', 'verify-direct', 'register', 'verify-gateway', 'update', 'rollback', 'recover'])
    parser.add_argument('--component', choices=list(COMPONENTS))
    parser.add_argument('--tag')
    parser.add_argument('--archive', type=pathlib.Path)
    parser.add_argument('--allow-containerd-import', action='store_true')
    parser.add_argument('--result', type=pathlib.Path)
    parser.add_argument('--port', type=int, default=18443)
    parser.add_argument('--confirm-model-interruption', action='store_true')
    parser.add_argument('--retry', action='store_true', help='Archive an unsuccessful qualification and run new qualification and chain Jobs.')
    args = parser.parse_args()
    require(not args.retry or args.phase == 'qualify', '--retry is supported only for qualify.')
    recipe = Recipe(json.loads(args.config.read_text()), args.work_dir, args.source_dir)
    if args.phase == 'prepare': recipe.prepare()
    elif args.phase == 'render': recipe.render()
    elif args.phase == 'inventory': recipe.inventory()
    elif args.phase == 'attach-existing': recipe.attach_existing()
    elif args.phase == 'build-images': recipe.build_images(args.component, args.tag)
    elif args.phase == 'push-images':
        for name in ([args.component] if args.component else recipe.components()):
            run(['docker', 'push', recipe.image(name, args.tag)])
    elif args.phase == 'import-images':
        require(args.archive, '--archive is required.')
        recipe.import_images(args.archive, args.allow_containerd_import, args.component, args.tag)
    elif args.phase == 'stack': recipe.deploy_stack()
    elif args.phase in ('preflight', 'build-runtime', 'qualify', 'download', 'load'):
        recipe.backend_phase({'build-runtime': 'build', 'load': 'serve'}.get(args.phase, args.phase), retry=args.retry)
    elif args.phase == 'verify-direct': recipe.verify(False, args.port)
    elif args.phase == 'register': recipe.register()
    elif args.phase == 'verify-gateway': recipe.verify(True, args.port)
    elif args.phase == 'update':
        require(args.component in ('gateway', 'router') and args.tag, 'Update requires --component gateway|router and --tag.')
        recipe.update(args.component, args.tag)
    elif args.phase == 'rollback':
        require(args.result, '--result is required.')
        recipe.rollback(args.result)
    elif args.phase == 'recover': recipe.recovery(args.confirm_model_interruption, args.port)


if __name__ == '__main__':
    main()
