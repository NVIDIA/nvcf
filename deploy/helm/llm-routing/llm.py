#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Install shared Helm infrastructure and call models using the current kube context."""
import argparse
import base64
import contextlib
import http.client
import importlib.util
import json
import pathlib
import re
import signal
import subprocess
import sys
import tempfile
import time
import uuid

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('llm_gateway_client', HERE / 'recipes/client.py')
client_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client_module)
Client = client_module.Client


def run(command, timeout=60):
    result = subprocess.run([str(item) for item in command], capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f'{command[0]} failed: ' + result.stderr.strip()[-2000:])
    return result.stdout


def selected_context(override):
    context = override if override is not None else run(['kubectl', 'config', 'current-context']).strip()
    if not context or not context.strip() or context.startswith('-'):
        raise ValueError('Select a Kubernetes context with kubectl config use-context NAME, or pass --context.')
    return context


def kube(context, namespace, *args):
    return ['kubectl', '--context', context, '--namespace', namespace, *args]


def read_json(context, namespace, kind, name):
    return json.loads(run(kube(context, namespace, 'get', kind, name, '-o', 'json')))


def access_material(context, namespace, ca_configmap=None, api_key_file=None):
    service = read_json(context, namespace, 'service', 'llm-api-gateway')
    annotations = service.get('metadata', {}).get('annotations', {})
    release = annotations.get('meta.helm.sh/release-name')
    if not release or annotations.get('meta.helm.sh/release-namespace') != namespace:
        raise ValueError('Gateway Service must belong to a Helm release in the selected namespace.')
    values = json.loads(run(['helm', '--kube-context', context, '--namespace', namespace,
                             'get', 'values', release, '--all', '--output', 'json']))
    ca_name = ca_configmap or values.get('operator', {}).get('trustBundle', {}).get('configMap')
    if not ca_name:
        raise ValueError('This installation needs --ca-configmap NAME. See ADVANCED.md for existing stacks.')
    ca = read_json(context, namespace, 'configmap', ca_name)['data']['ca.crt']
    if api_key_file:
        key = pathlib.Path(api_key_file).expanduser().read_text().strip()
    else:
        caller = values.get('callerKey', {})
        secret_name = caller.get('existingSecret') or caller.get('secretName')
        if not secret_name:
            raise ValueError('This installation needs --api-key-file PATH. See ADVANCED.md for existing stacks.')
        secret = read_json(context, namespace, 'secret', secret_name)
        key = base64.b64decode(secret['data']['api-key'], validate=True).decode().strip()
    if not ca.strip() or not key:
        raise ValueError('The gateway CA and caller key must be nonempty.')
    return ca, key


@contextlib.contextmanager
def forward(context, namespace, work):
    path = work / 'port-forward.log'
    with path.open('w') as log:
        process = subprocess.Popen(kube(context, namespace, 'port-forward', 'svc/llm-api-gateway',
                                        ':8080', '--address', '127.0.0.1'), stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                output = path.read_text()
                if process.poll() is not None:
                    raise RuntimeError('Gateway connection failed: ' + output.strip()[-2000:])
                match = re.search(r'Forwarding from 127\.0\.0\.1:(\d+) -> 8080', output)
                if match:
                    yield 'https://127.0.0.1:' + match[1]
                    return
                time.sleep(0.1)
            raise RuntimeError('Timed out connecting to the gateway. Check VPN access and gateway readiness.')
        finally:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)


@contextlib.contextmanager
def gateway(context, namespace, ca_configmap=None, api_key_file=None):
    ca, key = access_material(context, namespace, ca_configmap, api_key_file)
    with tempfile.TemporaryDirectory(prefix='llm-gateway-') as directory:
        work = pathlib.Path(directory)
        for name, value in [('ca.crt', ca), ('api-key', key)]:
            path = work / name
            path.touch(mode=0o600)
            path.write_text(value)
        with forward(context, namespace, work) as url:
            yield Client(url, work / 'ca.crt', work / 'api-key')


def model_list(client):
    listing = client.public_json('/v1/models')
    if listing.get('object') != 'list' or not isinstance(listing.get('data'), list):
        raise RuntimeError('Gateway returned an invalid model list.')
    return listing


def verify_caller(client):
    # The gateway rejects an invalid key before looking up the requested model.
    payload = {'model': 'connection-check-' + uuid.uuid4().hex,
               'messages': [{'role': 'user', 'content': 'Check gateway access.'}]}
    for key, expected in [('invalid-connection-check', 401), ('configured', 404)]:
        connection, response = client.request('/v1/chat/completions', payload, key=key)
        try:
            response.read()
            if response.status != expected:
                raise RuntimeError('Gateway caller verification failed with HTTP ' + str(response.status))
        finally:
            connection.close()


def install(args, context):
    command = ['helm', '--kube-context', context, '--namespace', args.namespace,
               'upgrade', '--install', args.release, args.chart, '--create-namespace', '--wait', '--timeout', '10m']
    for path in args.values:
        command.extend(['--values', path])
    print(f'Installing shared infrastructure in {context}/{args.namespace}...', flush=True)
    run(command, timeout=900)
    print('Checking gateway access...', flush=True)
    with gateway(context, args.namespace, args.ca_configmap, args.api_key_file) as client:
        listing = model_list(client)
        verify_caller(client)
    print(f'Shared infrastructure ready. Gateway access verified. {len(listing["data"])} model(s) registered.')
    return listing


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--context', help='Override the current kubectl context.')
    parser.add_argument('--namespace', default='llm-stack', help='Shared stack namespace (default: llm-stack).')
    parser.add_argument('--ca-configmap', help='CA ConfigMap for an existing legacy stack.')
    parser.add_argument('--api-key-file', type=pathlib.Path, help='Caller key file for an existing legacy stack.')
    commands = parser.add_subparsers(dest='command', required=True)
    setup = commands.add_parser('install', help='Install or upgrade the shared Helm chart and verify gateway access.')
    setup.add_argument('--chart', type=pathlib.Path, required=True, help='Shared chart package or prepared source directory.')
    setup.add_argument('--release', default='llm-stack')
    setup.add_argument('--values', type=pathlib.Path, action='append', required=True)
    commands.add_parser('models', help='List models through the gateway.')
    chat = commands.add_parser('chat', help='Chat with a model through the gateway.')
    chat.add_argument('--model', required=True)
    chat.add_argument('--stream', action='store_true')
    chat.add_argument('prompt')
    args = parser.parse_args(argv)
    if not re.fullmatch(r'[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?', args.namespace):
        parser.error('--namespace must be a Kubernetes namespace name.')
    if args.command == 'chat' and (not args.model.strip() or not args.prompt.strip()):
        parser.error('Choose a served model and provide a nonempty prompt.')
    context = selected_context(args.context)
    if args.command == 'install':
        return install(args, context)
    with gateway(context, args.namespace, args.ca_configmap, args.api_key_file) as client:
        if args.command == 'models':
            listing = model_list(client)
            print(json.dumps(listing, indent=2))
            return listing
        return client.completion(args.model, args.prompt, stream=args.stream, display=True)


def terminate(signum, frame):
    raise SystemExit(128 + signum)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, terminate)
    try:
        main()
    except (ValueError, RuntimeError, KeyError, OSError, subprocess.SubprocessError, http.client.HTTPException) as error:
        raise SystemExit('LLM command failed: ' + str(error))
    except KeyboardInterrupt:
        raise SystemExit(130)
