#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Install model-neutral routing infrastructure and export its connection."""
import argparse
import base64
import contextlib
import hashlib
import json
import pathlib
import re
import secrets
import socket
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent
REPO = HERE.parents[2]
CRD = 'inferenceendpoints.pylon.nvidia.com'
COMPONENTS = ('gateway', 'router', 'operator', 'pylon')


def require(condition, message):
    if not condition:
        raise ValueError(message)


def run(command):
    result = subprocess.run([str(x) for x in command], capture_output=True, text=True, timeout=900)
    require(result.returncode == 0, 'Command failed: ' + ' '.join(str(x) for x in command[:6]) + '\n' + result.stderr[-4000:])
    return result.stdout


def save(path, value):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open('w') as output:
        path.chmod(0o600)
        output.write(value if isinstance(value, str) else json.dumps(value, indent=2) + '\n')


def dns(value):
    return isinstance(value, str) and len(value) <= 53 and re.fullmatch('[a-z0-9]([-a-z0-9]*[a-z0-9])?', value)


def validate_config(config):
    require(isinstance(config, dict), 'Stack config must be an object.')
    for field in ('context', 'controlNode'):
        require(isinstance(config.get(field), str) and config[field].strip() and not config[field].startswith('-'), 'Set ' + field + '.')
    for field in ('namespace', 'clusterId', 'stackRelease', 'operatorRelease', 'caConfigMap'):
        require(dns(config.get(field)), 'Set a DNS label for ' + field + '.')
    require(config['stackRelease'] != config['operatorRelease'], 'Stack and operator need distinct release names.')
    require(isinstance(config.get('installCRDs'), bool), 'Choose installCRDs explicitly. Use false to reuse a compatible CRD.')
    for field in ('runtimeClass', 'runtimeImage', 'storageClass', 'model', 'nodes'):
        require(field not in config, 'Model configuration belongs in its recipe, not the shared stack: ' + field)
    for component in COMPONENTS:
        image = config.get('images', {}).get(component, {})
        require(isinstance(image, dict) and isinstance(image.get('repository'), str) and '/' in image['repository']
                and not any(x.isspace() for x in image['repository']), 'Set images.' + component + '.repository.')
        require(isinstance(image.get('tag'), str) and image['tag'] and image['tag'] != 'latest', 'Pin images.' + component + '.tag.')
        require(image.get('pullPolicy', 'IfNotPresent') in ('Never', 'IfNotPresent', 'Always'), 'Invalid image pull policy.')
    if config.get('apiKeyFile'):
        require(pathlib.Path(config['apiKeyFile']).is_absolute(), 'apiKeyFile must be absolute.')
    return config


def kube(config, *args):
    return ['kubectl', '--context', config['context'], '-n', config['namespace'], *args]


def get(config, kind, name=None):
    return json.loads(run(kube(config, 'get', kind, *([name] if name else []), '-o', 'json')))


def argument(args, name):
    values = []
    for index, value in enumerate(args):
        if value.startswith(name + '='):
            values.append(value.split('=', 1)[1])
        elif value == name and index + 1 < len(args):
            values.append(args[index + 1])
    require(len(values) <= 1, 'Duplicate operator option: ' + name)
    return values[0] if values else None


def check_watchers(deployments, namespace, own_namespace, own_name):
    for deployment in deployments:
        metadata = deployment['metadata']
        if (metadata.get('namespace'), metadata['name']) == (own_namespace, own_name):
            continue
        for container in deployment['spec']['template']['spec']['containers']:
            args = container.get('args', [])
            if argument(args, '--pylon-image') is None:
                continue
            watches = argument(args, '--watch-namespaces')
            require(watches and namespace not in watches.split(','), 'Another Pylon operator watches the target namespace: ' + metadata['name'])


def check_crd(crd):
    spec = crd['spec']
    require(spec.get('group') == 'pylon.nvidia.com' and spec.get('scope') == 'Namespaced'
            and spec.get('names', {}).get('kind') == 'InferenceEndpoint', 'Incompatible InferenceEndpoint CRD.')
    versions = [v for v in spec.get('versions', []) if v.get('name') == 'v1alpha1' and v.get('served')]
    require(len(versions) == 1, 'InferenceEndpoint v1alpha1 must be served.')
    properties = versions[0].get('schema', {}).get('openAPIV3Schema', {}).get('properties', {}).get('spec', {}).get('properties', {})
    require({'modelName', 'service', 'inferenceAPIFormat', 'health', 'maxEngineConcurrency'} <= properties.keys(), 'CRD lacks required model registration fields.')


def ownership(resource, release, namespace):
    annotations = resource.get('metadata', {}).get('annotations', {})
    require(annotations.get('meta.helm.sh/release-name') == release and annotations.get('meta.helm.sh/release-namespace') == namespace,
            'Unexpected Helm owner: ' + resource['metadata']['name'])


def connection_resources(connection):
    return {'namespace/' + connection['namespace']: None, 'customresourcedefinition/' + CRD: None,
            'deployment/' + connection['operatorRelease']: connection['operatorRelease'],
            'deployment/llm-api-gateway': connection['stackRelease'],
            'deployment/llm-request-router': connection['stackRelease'],
            'service/llm-api-gateway': connection['stackRelease'],
            'service/llm-request-router': connection['stackRelease'],
            'configmap/' + connection['caConfigMap']: connection['stackRelease']}


def validate_connection(connection):
    require(isinstance(connection, dict) and connection.get('schemaVersion') == 1 and connection.get('kind') == 'llm-stack-connection', 'Unsupported stack connection.')
    for field in ('context', 'namespace', 'clusterId', 'stackRelease', 'operatorRelease', 'controlNode', 'caConfigMap', 'apiKeyFile', 'controlNodeUID'):
        require(isinstance(connection.get(field), str) and connection[field].strip() and not connection[field].startswith('-'), 'Missing connection field: ' + field)
    require(pathlib.Path(connection['apiKeyFile']).is_absolute(), 'Connection apiKeyFile must be absolute.')
    resources = connection.get('resources')
    require(isinstance(resources, dict) and set(resources) == set(connection_resources(connection))
            and all(isinstance(uid, str) and uid for uid in resources.values()), 'Connection must bind every shared resource UID.')
    for field in ('caSHA256', 'apiKeySHA256'):
        require(isinstance(connection.get(field), str) and re.fullmatch('[a-f0-9]{64}', connection[field]), 'Missing connection fingerprint: ' + field)
    return connection


def load_connection(path):
    return validate_connection(json.loads(pathlib.Path(path).read_text()))


def inspect_connection(connection):
    """Read live identity/ownership without adopting or mutating shared resources."""
    validate_connection(connection)
    node = get(connection, 'node', connection['controlNode'])
    require(node['metadata']['uid'] == connection['controlNodeUID'], 'The routing node was replaced.')
    require(any(c['type'] == 'Ready' and c['status'] == 'True' for c in node.get('status', {}).get('conditions', [])), 'Routing node is not Ready.')
    resources = {}
    for key, release in connection_resources(connection).items():
        resource = get(connection, *key.split('/', 1))
        require(resource['metadata']['uid'] == connection['resources'][key], 'Shared resource was replaced: ' + key)
        if release:
            ownership(resource, release, connection['namespace'])
        resources[key] = resource
    check_crd(resources['customresourcedefinition/' + CRD])
    deployments = json.loads(run(kube(connection, 'get', 'deployments', '-A', '-o', 'json')))['items']
    check_watchers(deployments, connection['namespace'], connection['namespace'], connection['operatorRelease'])
    operator = resources['deployment/' + connection['operatorRelease']]
    args = operator['spec']['template']['spec']['containers'][0].get('args', [])
    expected = {'--cluster-id': connection['clusterId'], '--watch-namespaces': connection['namespace'],
                '--router-grpc-address': 'http://llm-request-router.' + connection['namespace'] + '.svc.cluster.local:50071',
                '--trust-bundle-configmap': connection['caConfigMap']}
    for name, value in expected.items():
        require(argument(args, name) == value, 'Operator connection changed: ' + name)
    for name in (connection['operatorRelease'], 'llm-api-gateway', 'llm-request-router'):
        deployment = resources['deployment/' + name]
        status = deployment.get('status', {})
        require(status.get('observedGeneration', 0) >= deployment['metadata'].get('generation', 1)
                and status.get('availableReplicas', 0) >= 1, 'Shared deployment is not available: ' + name)
    ca = resources['configmap/' + connection['caConfigMap']]['data']['ca.crt']
    require(hashlib.sha256(ca.encode()).hexdigest() == connection['caSHA256'], 'Shared CA changed.')
    key = pathlib.Path(connection['apiKeyFile']).read_text().strip()
    require(key and hashlib.sha256(key.encode()).hexdigest() == connection['apiKeySHA256'], 'Caller credential changed.')
    return {'nodes': {connection['controlNode']: node['metadata']['uid']}, 'ca': ca,
            'resources': {key: value['metadata']['uid'] for key, value in resources.items()}}


def operator_values(config):
    return {'fullnameOverride': config['operatorRelease'], 'clusterId': config['clusterId'],
            'image': config['images']['operator'], 'pylon': {'image': config['images']['pylon']},
            'router': {'grpcAddress': 'http://llm-request-router.' + config['namespace'] + '.svc.cluster.local:50071'},
            'watchNamespaces': [config['namespace']], 'trustBundle': {'configMap': config['caConfigMap']},
            'nodeSelector': {'kubernetes.io/hostname': config['controlNode']}, 'installCRDs': config['installCRDs']}


def stack_values(config, token_hash, key_hash):
    values = {'clusterId': config['clusterId'], 'clusterCredential': {'sha256': token_hash},
              'apiKeys': [{'id': 'stack-client', 'sha256': key_hash}],
              'tls': {'selfSigned': {'enabled': True, 'caName': config['caConfigMap']}}}
    for component, chart, field in [('gateway', 'llm-api-gateway', 'llmApiGateway'), ('router', 'llm-request-router', 'llmRequestRouter')]:
        image = dict(config['images'][component])
        image['registry'], image['repository'] = image['repository'].split('/', 1)
        values[chart] = {field: {'replicaCount': 1, 'image': image, 'nodeSelector': {'kubernetes.io/hostname': config['controlNode']}}}
    return values


def chart_paths():
    helm = HERE.parent
    return helm/'pylon-operator/pylon-operator', helm/'llm-gateway-stack/llm-gateway-stack'


@contextlib.contextmanager
def forward_connection(connection, work, port):
    work = pathlib.Path(work)
    with socket.socket() as probe:
        probe.bind(('127.0.0.1', port))
    with (work/'port-forward.log').open('a') as log:
        process = subprocess.Popen(kube(connection, 'port-forward', 'svc/llm-api-gateway', str(port)+':8080', '--address', '127.0.0.1'), stdout=log, stderr=log)
        try:
            for _ in range(100):
                require(process.poll() is None, 'Port forward exited. Read port-forward.log.')
                try:
                    with socket.create_connection(('127.0.0.1', port), timeout=.2):
                        break
                except OSError:
                    time.sleep(.2)
            else:
                raise ValueError('Port forward timed out.')
            yield 'https://127.0.0.1:' + str(port) + '/v1'
        finally:
            process.terminate()
            process.wait(timeout=10)


class Stack:
    def __init__(self, config, work):
        self.config = validate_config(config)
        self.work = pathlib.Path(work).expanduser().resolve()
        require(not self.work.is_relative_to(REPO), 'Keep generated configuration, credentials and evidence outside the checkout.')
        self.work.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.hm = ['helm', '--kube-context', config['context'], '-n', config['namespace']]

    def render(self):
        operator, stack = chart_paths()
        run(['helm', 'dependency', 'build', '--skip-refresh', stack])
        for release, chart, values in [(self.config['operatorRelease'], operator, operator_values(self.config)),
                                       (self.config['stackRelease'], stack, stack_values(self.config, 'a'*64, 'b'*64))]:
            path = self.work/'render'/(release + '-values.json')
            save(path, values)
            save(self.work/'render'/(release + '.yaml'), run(self.hm + ['template', release, chart, '-f', path]))
        print('Rendered shared gateway, router and operator resources.')

    def prepare_reinstall(self, connection, state, namespace, crd):
        config = self.config
        fields = ('context', 'namespace', 'clusterId', 'stackRelease', 'operatorRelease', 'controlNode', 'caConfigMap')
        require(all(connection[key] == config[key] for key in fields), 'Reinstall config differs from its saved connection.')
        require(connection['controlNodeUID'] == state['controlNodeUID'], 'Reinstall routing node differs from its saved connection.')
        require(namespace is not None and namespace['metadata']['uid'] == state.get('namespaceUID')
                == connection['resources']['namespace/' + config['namespace']], 'Reinstall requires the original namespace.')
        require(not namespace['metadata'].get('deletionTimestamp'), 'Reinstall namespace is terminating.')
        require(crd is not None and crd['metadata']['uid'] == connection['resources']['customresourcedefinition/' + CRD],
                'Reinstall requires the original shared CRD.')
        check_crd(crd)
        key_path = pathlib.Path(config['apiKeyFile']) if config.get('apiKeyFile') else self.work/'api-key'
        require(str(key_path.resolve()) == connection['apiKeyFile'], 'Reinstall caller-key path differs from its saved connection.')
        require(key_path.is_file() and hashlib.sha256(key_path.read_text().strip().encode()).hexdigest()
                == connection['apiKeySHA256'], 'Reinstall caller credential is missing or changed.')
        releases = json.loads(run(self.hm + ['list', '--deployed', '--failed', '--pending', '--superseded',
                                             '--uninstalled', '--uninstalling', '-o', 'json']))
        require(not releases, 'Uninstall all releases in the saved namespace before --reinstall.')
        retained_names = {config['caConfigMap'], config['operatorRelease'] + '-cluster-credential'}
        kinds = 'pods,deployments,replicasets,statefulsets,daemonsets,jobs,cronjobs,replicationcontrollers,services,inferenceendpoints,configmaps,secrets,serviceaccounts,roles,rolebindings'
        resources = get(config, kinds)['items']
        retained = {}
        for resource in resources:
            kind, metadata = resource['kind'], resource['metadata']
            name = metadata['name']
            if kind == 'Secret' and name in retained_names:
                require(not metadata.get('deletionTimestamp'), 'Retained credential is terminating: ' + name)
                retained[name] = resource
            else:
                require((kind, name) in (('ConfigMap', 'kube-root-ca.crt'), ('ServiceAccount', 'default')),
                        'Remaining resource blocks reinstall: ' + kind + '/' + name)
        for kind in ('clusterroles', 'clusterrolebindings'):
            require(not any(item['metadata']['name'] == config['operatorRelease'] for item in get(config, kind)['items']),
                    'Remaining operator RBAC blocks reinstall: ' + kind)
        require(set(retained) == retained_names, 'Reinstall requires the retained CA and operator credential Secrets.')
        ca = retained[config['caConfigMap']]
        credential = retained[config['operatorRelease'] + '-cluster-credential']
        ownership(ca, config['stackRelease'], config['namespace'])
        ownership(credential, config['operatorRelease'], config['namespace'])
        require(ca.get('data', {}).get('tls.key') and hashlib.sha256(base64.b64decode(ca['data']['tls.crt'])).hexdigest()
                == connection['caSHA256'], 'Retained CA is missing or changed.')
        values_path = self.work/(config['stackRelease'] + '-values.json')
        require(values_path.is_file(), 'Reinstall requires the original installed stack values.')
        token = base64.b64decode(credential.get('data', {}).get('cluster-token', ''))
        token_hash = hashlib.sha256(token).hexdigest()
        require(token and json.loads(values_path.read_text()).get('clusterCredential', {}).get('sha256') == token_hash,
                'Retained operator credential is missing or changed.')
        archive = self.work/('before-reinstall-' + str(time.time_ns()))
        archive.mkdir(mode=0o700)
        for name in ('connection.json', 'install.json', 'config.json', 'ca.crt', 'empty-verification.json',
                     config['stackRelease'] + '-values.json', config['operatorRelease'] + '-values.json'):
            source = self.work/name
            if source.is_file():
                save(archive/name, source.read_text())
        identity = {'caSecretUID': ca['metadata']['uid'], 'credentialUID': credential['metadata']['uid'],
                    'tokenSHA256': token_hash}
        save(archive/'retained-identity.json', identity)
        print('Previous installation evidence: ' + str(archive), flush=True)
        return identity

    def install(self, reinstall=False):
        config = self.config
        checkpoint = self.work/'install.json'
        previous = None
        if reinstall:
            require(checkpoint.is_file() and (self.work/'connection.json').is_file(),
                    '--reinstall requires the original install checkpoint and saved connection.')
            previous = load_connection(self.work/'connection.json')
        if (self.work/'connection.json').exists() and not reinstall:
            require(json.loads(checkpoint.read_text())['config'] == config, 'Use the original stack config for this work directory.')
            inspect_connection(load_connection(self.work/'connection.json'))
            print('Shared stack already installed and verified.')
            return
        nodes = get(config, 'nodes')['items']
        control = next((n for n in nodes if n['metadata']['name'] == config['controlNode']), None)
        require(control is not None and any(c['type'] == 'Ready' and c['status'] == 'True' for c in control.get('status', {}).get('conditions', [])), 'Choose a Ready routing node.')
        deployments = json.loads(run(kube(config, 'get', 'deployments', '-A', '-o', 'json')))['items']
        check_watchers(deployments, config['namespace'], config['namespace'], config['operatorRelease'])
        namespaces = get(config, 'namespaces')['items']
        namespace = next((n for n in namespaces if n['metadata']['name'] == config['namespace']), None)
        crds = get(config, 'crds')['items']
        crd = next((c for c in crds if c['metadata']['name'] == CRD), None)
        if config['installCRDs']:
            if crd:
                ownership(crd, config['operatorRelease'], config['namespace'])
                require(checkpoint.exists(), 'CRD already exists. Use installCRDs=false in an isolated namespace.')
        else:
            require(crd is not None, 'No shared CRD exists. Use installCRDs=true for the first stack.')
            check_crd(crd)
        state = {'config': config, 'controlNodeUID': control['metadata']['uid']}
        if checkpoint.exists():
            saved = json.loads(checkpoint.read_text())
            require(saved['config'] == config and saved['controlNodeUID'] == state['controlNodeUID'], 'Install checkpoint differs from the config or routing node.')
            if namespace:
                require(saved.get('namespaceUID') == namespace['metadata']['uid'], 'Namespace was replaced or an incomplete install needs inspection.')
            state = saved
        else:
            require(namespace is None, 'Use a new namespace for a new stack. Existing installations are never adopted.')
            save(checkpoint, state)
        retained = self.prepare_reinstall(previous, state, namespace, crd) if reinstall else None
        operator, stack = chart_paths()
        run(['helm', 'dependency', 'build', '--skip-refresh', stack])
        key_path = pathlib.Path(config['apiKeyFile']) if config.get('apiKeyFile') else self.work/'api-key'
        if not key_path.exists():
            require(not config.get('apiKeyFile'), 'Configured API-key file does not exist.')
            save(key_path, secrets.token_urlsafe(48) + '\n')
        key = key_path.read_text().strip()
        require(bool(key), 'Caller key is empty.')
        def apply(release, chart, values):
            path = self.work/(release + '-values.json')
            save(path, values)
            print('Installing ' + release, flush=True)
            action = ['install'] if reinstall else ['upgrade', '--install']
            run(self.hm + action + [release, chart, '--create-namespace', '-f', path, '--wait', '--timeout', '5m'])
        try:
            apply(config['operatorRelease'], operator, operator_values(config))
        finally:
            ns = get(config, 'namespace', config['namespace'])
            state['namespaceUID'] = ns['metadata']['uid']
            save(checkpoint, state)
        secret = get(config, 'secret', config['operatorRelease'] + '-cluster-credential')
        token_hash = hashlib.sha256(base64.b64decode(secret['data']['cluster-token'])).hexdigest()
        if retained:
            require(secret['metadata']['uid'] == retained['credentialUID'] and token_hash == retained['tokenSHA256'],
                    'Operator credential changed during reinstall.')
        apply(config['stackRelease'], stack, stack_values(config, token_hash, hashlib.sha256(key.encode()).hexdigest()))
        connection = {key: config[key] for key in ('context', 'namespace', 'clusterId', 'stackRelease', 'operatorRelease', 'controlNode', 'caConfigMap')}
        connection.update(schemaVersion=1, kind='llm-stack-connection', controlNodeUID=control['metadata']['uid'],
                          apiKeyFile=str(key_path.resolve()), apiKeySHA256=hashlib.sha256(key.encode()).hexdigest())
        connection['resources'] = {key: get(config, *key.split('/', 1))['metadata']['uid'] for key in connection_resources(connection)}
        ca = get(config, 'configmap', config['caConfigMap'])['data']['ca.crt']
        connection['caSHA256'] = hashlib.sha256(ca.encode()).hexdigest()
        inspect_connection(connection)
        if previous:
            require(connection['caSHA256'] == previous['caSHA256'], 'CA changed during reinstall.')
            require(get(config, 'secret', config['caConfigMap'])['metadata']['uid'] == retained['caSecretUID'],
                    'CA Secret was replaced during reinstall.')
            require(all(connection['resources'][key] != previous['resources'][key]
                        for key, release in connection_resources(connection).items() if release),
                    'Reinstall must create new shared resources.')
        save(self.work/'ca.crt', ca)
        save(self.work/'connection.json', connection)
        print('Shared stack installed. Connection: ' + str(self.work/'connection.json'))

    @contextlib.contextmanager
    def forward(self, port):
        with forward_connection(self.config, self.work, port) as url:
            yield url

    def chat(self, model, prompt, stream, port):
        require(isinstance(model, str) and model.strip(), 'Choose a served model ID.')
        require(isinstance(prompt, str) and prompt.strip(), 'Provide a nonempty prompt.')
        connection = load_connection(self.work/'connection.json')
        require(all(connection[key] == self.config[key] for key in ('context', 'namespace', 'clusterId', 'stackRelease', 'operatorRelease', 'controlNode', 'caConfigMap')), 'Stack config differs from its saved connection.')
        live = inspect_connection(connection)
        save(self.work/'ca.crt', live['ca'])
        with self.forward(port) as url:
            command = [sys.executable, str(HERE/'recipes/client.py'), '--mode', 'chat', '--url', url.removesuffix('/v1'),
                       '--model', model, '--ca-file', str(self.work/'ca.crt'), '--api-key-file', connection['apiKeyFile']]
            if stream:
                command.append('--stream')
            subprocess.run(command + ['--', prompt], check=True)

    def verify(self, expect_empty, models, port):
        connection = load_connection(self.work/'connection.json')
        require(all(connection[key] == self.config[key] for key in ('context', 'namespace', 'clusterId', 'stackRelease', 'operatorRelease', 'controlNode', 'caConfigMap')), 'Stack config differs from its saved connection.')
        live = inspect_connection(connection)
        save(self.work/'ca.crt', live['ca'])
        key = pathlib.Path(connection['apiKeyFile']).read_text().strip()
        with self.forward(port) as url:
            if models:
                import importlib.util
                spec = importlib.util.spec_from_file_location('model_verify', HERE/'recipes/verify.py')
                verifier = importlib.util.module_from_spec(spec)
                spec.loader.exec_module(verifier)
                result = verifier.verify(url, self.work/'ca.crt', key, models)
            else:
                require(expect_empty, 'Specify --expect-empty or two or more --model IDs.')
                class NoRedirect(urllib.request.HTTPRedirectHandler):
                    def redirect_request(self, *args, **kwargs):
                        raise ValueError('Gateway redirects are not accepted.')
                opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(self.work/'ca.crt'))))
                result = {'passed': True, 'models': [], 'registry': []}
                for path, field in [('/models', 'data'), ('/registry', 'models')]:
                    with opener.open(urllib.request.Request(url + path, headers={'Authorization': 'Bearer ' + key}), timeout=30) as response:
                        data = response.read(1024*1024 + 1)
                    require(len(data) <= 1024*1024 and json.loads(data)[field] == [], 'Expected an empty ' + path)
                for token, expected in [(key, 404), ('invalid-stack-test-key', 401)]:
                    request = urllib.request.Request(url + '/chat/completions', data=json.dumps({'model': 'uninstalled-model', 'messages': [{'role': 'user', 'content': 'hello'}]}).encode(), headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
                    try:
                        opener.open(request, timeout=30).close()
                    except urllib.error.HTTPError as error:
                        require(error.code == expected, 'Unexpected empty-stack inference HTTP status: ' + str(error.code))
                    else:
                        raise ValueError('An empty stack must not serve inference.')
                result.update(missingModelStatus=404, invalidKeyStatus=401)
        save(self.work/('empty-verification.json' if expect_empty else 'model-verification.json'), result)
        print('PASS: ' + ('empty registry and authentication' if expect_empty else 'independent models, chat, streaming and authentication'))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=pathlib.Path, help='Stack configuration. Later commands use the saved work-directory configuration.')
    parser.add_argument('--work-dir', type=pathlib.Path, required=True)
    commands = parser.add_subparsers(dest='command', required=True)
    commands.add_parser('render')
    install = commands.add_parser('install', help='Install shared infrastructure and verify the empty gateway.')
    install.add_argument('--port', type=int, default=18477)
    install.add_argument('--reinstall', action='store_true', help='Reinstall a fully uninstalled stack in its original namespace, preserving retained credentials and caches.')
    chat = commands.add_parser('chat')
    chat.add_argument('--model', required=True)
    chat.add_argument('--stream', action='store_true')
    chat.add_argument('--port', type=int, default=18477)
    chat.add_argument('prompt')
    verify = commands.add_parser('verify')
    group = verify.add_mutually_exclusive_group(required=True)
    group.add_argument('--expect-empty', action='store_true')
    group.add_argument('--model', action='append')
    verify.add_argument('--port', type=int, default=18477)
    args = parser.parse_args(argv)
    config_path = (args.config or args.work_dir/'config.json').expanduser()
    require(config_path.is_file(), 'Provide --config for the first installation, or use its saved --work-dir.')
    stack = Stack(json.loads(config_path.read_text()), args.work_dir)
    if args.command == 'verify':
        require(not args.model or (len(args.model) >= 2 and len(set(args.model)) == len(args.model)), 'Verify at least two distinct model IDs together.')
        stack.verify(args.expect_empty, args.model, args.port)
    elif args.command == 'chat':
        stack.chat(args.model, args.prompt, args.stream, args.port)
    elif args.command == 'install':
        if args.reinstall:
            stack.install(reinstall=True)
        else:
            stack.install()
        save(stack.work/'config.json', stack.config)
        stack.verify(True, None, args.port)
    else:
        getattr(stack, args.command)()


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.SubprocessError) as error:
        raise SystemExit('Stack operation failed: ' + str(error))
