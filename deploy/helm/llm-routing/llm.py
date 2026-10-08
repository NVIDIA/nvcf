#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""List local recipes, check model capacity, discover deployed models and chat."""
import argparse
import base64
import contextlib
import http.client
import importlib.util
import json
import pathlib
import re
import shlex
import signal
import subprocess
import sys
import tempfile
import time

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
        raise ValueError('This installation needs --ca-configmap NAME. See ADVANCED.md for connection options.')
    ca = read_json(context, namespace, 'configmap', ca_name)['data']['ca.crt']
    if api_key_file:
        key = pathlib.Path(api_key_file).expanduser().read_text().strip()
    else:
        caller = values.get('callerKey', {})
        secret_name = caller.get('existingSecret') or caller.get('secretName')
        if not secret_name:
            raise ValueError('This installation needs --api-key-file PATH. See ADVANCED.md for connection options.')
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


def planning_modules():
    # Keep gateway commands independent of the optional planning modules.
    recipe_path = str(HERE / 'recipes')
    if recipe_path not in sys.path:
        sys.path.insert(0, recipe_path)
    import model_capacity
    return model_capacity


def deployment_command(report, catalog, context, namespace, args, capabilities):
    model = next(model for model in catalog['recipes'] if model['id'] == report['model'])
    profile = next(profile for profile in model['profiles'] if profile['id'] == report['chosenProfile'])
    deployment = profile['deployment']
    names = {'qwen3.8-27b': 'qwen-fp8', 'qwen3.8-27b-nvfp4': 'qwen-nvfp4',
             'glm-5.3': 'glm', 'qwen3.8-flash-next': 'qwen-flash'}
    release = args.release or names.get(model['id'], model['id'].replace('.', '-'))
    if deployment['lifecycle'] != 'automatic':
        raise ValueError('The selected profile does not support automatic Helm installation.')
    command = ['helm', 'install', release,
               'dev-images/charts/' + deployment['chart']['archive'],
               '--kube-context', context, '--namespace', namespace]
    settings = {'recipe': model['id'], 'profileName': profile['id'], 'runtimeClassName': args.runtime_class,
                'storageClassName': args.storage_class, 'sharedCAConfigMap': args.shared_ca_configmap}
    for index, node in enumerate(report['chosenNodes']):
        settings[f'nodes[{index}]'] = node
    for key, value in settings.items():
        command += ['--set-string', f'{key}={value}']
    if model['runtime']['backend'] == 'sglang':
        offload = any(role.get('storage', {}).get('offloadMedium') == 'local-nvme' for role in profile['perNode'])
        node_capabilities = {}
        facts = capabilities.get('nodes', capabilities)
        for node in report['chosenNodes']:
            selected = {}
            if offload:
                selected['localNvme'] = facts[node]['localNvme']
            if profile.get('fabric'):
                selected.update({field: facts[node][field] for field in ('fabric', 'address', 'interface')})
                selected['linkGbps'] = facts[node]['gbps']
            if selected:
                node_capabilities[node] = selected
        if node_capabilities:
            command += ['--set-json', 'nodeCapabilities=' + json.dumps(node_capabilities, separators=(',', ':'))]
    if model['runtime']['backend'] == 'sglang':
        for key, value in [('contextLength', args.context_length), ('concurrency', args.concurrency)]:
            if value is not None:
                command += ['--set', f'{key}={value}']
    command += ['--wait', '--timeout', '120m']
    return {'release': release, 'phase': 'deploy', 'command': shlex.join(command),
            'guide': deployment['guide'].removeprefix('../')}


def capacity_plan(context, args):
    analyzer = planning_modules()
    catalog = json.loads((HERE / 'recipes/index.json').read_text())
    capabilities = json.loads(args.capabilities.read_text()) if args.capabilities else {}
    if not isinstance(capabilities, dict):
        raise ValueError('Capabilities must be a JSON object.')
    model = next((model for model in catalog['recipes'] if model['id'] == args.model), None)
    if model is None:
        raise ValueError('Unknown model: ' + args.model + '. Run llm.py recipes to list local recipe IDs.')
    if model.get('runtime', {}) and model['runtime']['backend'] == 'llama.cpp' and any(
            value is not None for value in (args.context_length, args.concurrency)):
        raise ValueError('GGUF profiles use fixed recipe tuning. Omit --context-length and --concurrency.')
    snapshot = analyzer.inventory(context)
    report = analyzer.analyze(snapshot, catalog, args.model, capabilities=capabilities,
                              profile_id=args.profile, context_length=args.context_length,
                              concurrency=args.concurrency)
    report.update(context=context, namespace=args.namespace)
    report['deployment'] = None
    report['existingDeployments'] = []
    report['limitations'].append('Retained caches are listed separately. Cache contents and compatibility for reuse are not verified; preserve their original placement.')
    import model_storage
    report['storage'] = model_storage.collect(context, snapshot, run)
    model_names = {report['model'], report.get('modelName')}
    for endpoint in snapshot['endpoints']:
        metadata = endpoint.get('metadata', {})
        if metadata.get('namespace') != args.namespace or endpoint.get('spec', {}).get('modelName') not in model_names:
            continue
        annotations = metadata.get('annotations', {})
        name = metadata['name']
        release = annotations.get('meta.helm.sh/release-name')
        conditions = {item['type']: item.get('status', 'Unknown') for item in endpoint.get('status', {}).get('conditions', [])}
        entry = {'endpoint': name, 'release': release,
                 'ready': conditions.get('Ready', 'Unknown'), 'registered': conditions.get('Registered', 'Unknown'),
                 'inspectCommand': shlex.join(kube(context, args.namespace, 'get', 'inferenceendpoint', name, '-o', 'wide'))}
        if release:
            entry['releaseCommand'] = shlex.join(['helm', 'status', release, '--kube-context', context,
                                                 '--namespace', args.namespace])
        report['existingDeployments'].append(entry)
    if report['existingDeployments']:
        report['status'] = 'existing'
    elif report['status'] == 'fits':
        prerequisites = []
        if not any(item['metadata']['name'] == args.runtime_class for item in snapshot['runtimeClasses']):
            prerequisites.append('RuntimeClass is not installed: ' + args.runtime_class)
        storage = next((item for item in snapshot['storageClasses'] if item['metadata']['name'] == args.storage_class), None)
        if not storage or storage.get('volumeBindingMode') != 'WaitForFirstConsumer' or storage.get('allowedTopologies'):
            prerequisites.append('Use an installed StorageClass with WaitForFirstConsumer and unrestricted topology: ' + args.storage_class)
        if prerequisites:
            report['status'] = 'blocked'
            report['blockers'].extend(prerequisites)
        else:
            report['deployment'] = deployment_command(report, catalog, context, args.namespace, args, capabilities)
    return report


def print_capacity_details(report):
    print(f"Model: {report['model']} | Cluster: {report['context']} | Namespace: {report['namespace']}")
    statuses = {'fits': 'FITS current scheduling allocations.', 'blocked': 'DOES NOT FIT current requirements.',
                'unsupported': 'NO DEPLOYABLE RECIPE.', 'existing': 'EXISTING DEPLOYMENT found in this namespace; inspect it before creating another.'}
    print(statuses[report['status']])
    for existing in report['existingDeployments']:
        print(f"Endpoint {existing['endpoint']}: Ready={existing['ready']}, Registered={existing['registered']}")
        print('Inspect the existing deployment: ' + existing['inspectCommand'])
        if existing.get('releaseCommand'):
            print(existing['releaseCommand'])
        print('If stopped, resume its existing release with the original chart and retained cache placement. See README.md#stop-and-resume.')
    for profile in report.get('profiles', []):
        requirements = profile.get('requirements', {})
        print(f"Profile: {profile['id']} ({requirements['modelNodeCount']} node(s))")
        validation = profile.get('validation', {}).get('automaticHelmStatus')
        if validation and validation != 'smoke-tested':
            print('  Automatic startup validation: ' + validation + '. See the recipe validation notes before deploying.')
        for rank in requirements.get('perNode', []):
            resources = rank.get('resources', rank)
            print(f"  Rank {rank.get('rank', 0)}: {resources.get('gpuRequest', 0):g} GPU, "
                  f"{resources.get('memoryRequestBytes', 0) / 1024**3:.1f} GiB RAM, "
                  f"{resources.get('cpuRequestMillicores', 0) / 1000:g} CPU")
            storage = rank.get('storage', {})
            if storage.get('claimRequestBytes'):
                print(f"    Cache claim: {storage['claimRequestBytes'] / 1024**3:g} GiB (physical free disk not checked)")
    print('Node allocations (allocatable / reserved / free; RAM in GiB):')
    def quantity_text(value, divisor=1):
        if value is None:
            return 'unknown'
        return f'{value / divisor:.1f}' if divisor != 1 else f'{value:g}'

    for node in report.get('nodes', []):
        gpu, memory = node['gpu'], node['memory']
        gpu_text = ' / '.join(quantity_text(gpu[key]) for key in ('allocatable', 'allocated', 'free'))
        memory_text = ' / '.join(quantity_text(memory[key], 1024**3) for key in ('allocatableBytes', 'allocatedBytes', 'freeBytes'))
        print(f"  {node['name']}: GPU {gpu_text}; RAM {memory_text}")
        for workload in node.get('workloads', []):
            if not any(workload[key] for key in ('gpu', 'cpuMillicores', 'memoryBytes', 'ephemeralStorageBytes')):
                continue
            owner = workload.get('release') or workload['pod']
            model = ' model=' + workload['model'] if workload.get('model') else ''
            print(f"    {workload['namespace']}/{owner}: {workload['gpu']:g} GPU, "
                  f"{workload['memoryBytes'] / 1024**3:.2f} GiB RAM, {workload['cpuMillicores'] / 1000:g} CPU{model}")
    for workload in report.get('pendingWorkloads', []):
        print(f"Pending workload: {workload['namespace']}/{workload['pod']}")
        if workload.get('scheduling', {}).get('message'):
            print('  ' + workload['scheduling']['message'])
        if workload.get('targetNodes'):
            print('  Requested nodes: ' + ', '.join(workload['targetNodes']))
    if report['status'] != 'fits':
        for profile in report.get('profiles', []):
            for blocker in profile.get('nodeBlockers', []):
                print(f"  {profile['id']} / {blocker['node']} / rank {blocker['rank']}: " + ' '.join(blocker['reasons']))
    for blocker in report.get('blockers', []):
        print('  - ' + blocker)
    if report['status'] == 'blocked':
        print('For GPU or memory shortages, stop a model you no longer need and rerun this check. Stop keeps downloads; complete removal can reclaim cache storage. See README.md#4-stop-or-uninstall-a-model.')
    deployment = report.get('deployment')
    if deployment:
        print('Selected nodes: ' + ', '.join(report['chosenNodes']))
        print('Run from deploy/helm/llm-routing:')
        print(deployment['command'])
        if deployment['phase'] == 'qualify':
            print('This command runs qualification only. Continue the download and serve phases in ' + deployment['guide'])
    print('No changes made. Recheck capacity before deploying.')
    for limitation in report.get('limitations', []):
        print('  ' + limitation)


def print_table(headers, rows):
    def cell(value):
        return ' '.join(str(value).split())
    rows = [[cell(value) for value in row] for row in rows]
    widths = [max([len(header)] + [len(row[index]) for row in rows])
              for index, header in enumerate(headers)]
    def line(row):
        return '  '.join(value.ljust(width) for value, width in zip(row, widths)).rstrip()
    print(line(headers))
    print(line(['-' * width for width in widths]))
    for row in rows:
        print(line(row))


def print_recipe_catalog(catalog):
    rows = []
    for recipe in catalog['recipes']:
        profiles = recipe.get('profiles', [])
        if not recipe['availability']['deployable'] or not profiles:
            rows.append([recipe['id'], recipe.get('precision') or '-', '-', '-', '-',
                         recipe['availability']['status']])
            continue
        for profile in profiles:
            lifecycle = profile['deployment']['lifecycle']
            validation_key = 'automaticHelmStatus' if lifecycle == 'automatic' else 'runtimeStatus'
            validation = profile.get('validation', {}).get(validation_key, 'pending')
            rows.append([recipe['id'], recipe.get('precision') or '-', profile['id'],
                         profile['modelNodeCount'], lifecycle, validation])
    print_table(['RECIPE', 'PRECISION', 'PROFILE', 'NODES', 'LIFECYCLE', 'VALIDATION'], rows)
    print('Use a RECIPE with plan --model. Validation records prior tests. Plan checks current cluster capacity.')


def print_model_list(listing):
    if not listing['data']:
        print('No models available.')
        return
    print_table(['MODEL', 'OWNER'],
                [[model['id'], model.get('owned_by') or '-'] for model in listing['data']])


def print_capacity_plan(report, verbose=False):
    if verbose:
        print_capacity_details(report)
    else:
        print(f"Model: {report['model']} | Cluster: {report['context']}")
        for profile in report.get('profiles', []):
            requirements = profile['requirements']
            print(f"Needs ({profile['id']}): {requirements['modelNodeCount']} node(s)")
            for rank in requirements.get('perNode', []):
                resources = rank.get('resources', rank)
                cache = rank.get('storage', {}).get('claimRequestBytes')
                text = (f"  Rank {rank.get('rank', 0)}: {resources.get('gpuRequest', 0):g} GPU, "
                        f"{resources.get('memoryRequestBytes', 0) / 1024**3:.1f} GiB RAM, "
                        f"{resources.get('cpuRequestMillicores', 0) / 1000:g} CPU")
                if cache:
                    text += f", {cache / 1024**3:g} GiB cache required"
                print(text)
        if report['status'] == 'unsupported':
            print('No deployable recipe.')
        print('\nGPU allocation (Kubernetes reservations)')
        def quantity(value):
            return 'unknown' if value is None else f'{value:g}'
        rows = []
        for node in report.get('nodes', []):
            gpu, memory = node['gpu'], node['memory']['freeBytes']
            owners = sorted({w.get('model') or w.get('release') or w['pod']
                             for w in node.get('workloads', []) if w['gpu']})
            rows.append([node['name'], f"{quantity(gpu['allocated'])} / {quantity(gpu['allocatable'])}",
                         quantity(gpu['free']), 'unknown' if memory is None else f'{memory / 1024**3:.1f} GiB',
                         ', '.join(owners) or '-'])
        print_table(['NODE', 'USED GPUs', 'FREE GPUs', 'FREE RAM', 'MODELS'], rows)
    print('\nModel files and caches')
    storage = report.get('storage', {'caches': [], 'warnings': []})
    statuses = {'not-mounted': 'Not mounted', 'not-provisioned': 'Not provisioned', 'unavailable': 'Unavailable'}
    rows = []
    for cache in storage['caches']:
        used = (f"{cache['bytesUsed'] / 1024**3:.1f} GiB" if cache['bytesUsed'] is not None
                else statuses.get(cache['usageStatus'], 'Unknown'))
        # Keep small but nonempty caches distinct from empty volumes.
        if cache['bytesUsed'] is not None and 0 < cache['bytesUsed'] < 1024**3 / 10:
            used = f"{cache['bytesUsed'] / 1024:.0f} KiB"
        rows.append([cache['claim'], cache['namespace'], ', '.join(cache['nodes']) or '-', used])
    print_table(['CACHE', 'NAMESPACE', 'NODE', 'DISK USED'], rows)
    if not rows and not storage['warnings']:
        print('No model cache claims found.')
    if storage['warnings']:
        print('Storage inventory incomplete; use --verbose for details.')
    if verbose:
        for warning in storage['warnings']:
            print('  ' + warning)
        for cache in storage['caches']:
            if cache.get('measurementError'):
                print(f"  {cache['namespace']}/{cache['claim']}: {cache['measurementError']}")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--context', help='Override the current kubectl context.')
    parser.add_argument('--namespace', default='llm-stack', help='Shared stack namespace (default: llm-stack).')
    parser.add_argument('--ca-configmap', help='CA ConfigMap for gateway trust.')
    parser.add_argument('--api-key-file', type=pathlib.Path, help='Caller key file for gateway requests.')
    commands = parser.add_subparsers(dest='command', required=True)
    recipe_list = commands.add_parser('recipes', help='List local recipes and hardware profiles without cluster access.')
    recipe_list.add_argument('--json', action='store_true', help='Print the complete local recipe catalog as JSON.')
    models = commands.add_parser('models', help='List models through the gateway.')
    models.add_argument('--json', action='store_true', help='Print the complete model list as JSON.')
    chat = commands.add_parser('chat', help='Chat with a model through the gateway.')
    chat.add_argument('--model', required=True)
    chat.add_argument('--stream', action='store_true')
    chat.add_argument('prompt')
    plan = commands.add_parser('plan', help='Check recipe capacity and show placement or current allocations, without changes.')
    plan.add_argument('--model', required=True, help='Recipe ID shown by llm.py recipes.')
    plan.add_argument('--profile', help='Select one hardware profile instead of considering every supported profile.')
    plan.add_argument('--capabilities', type=pathlib.Path, help='Private JSON file with verified NVMe, device-memory or fabric facts.')
    plan.add_argument('--context-length', type=int)
    plan.add_argument('--concurrency', type=int)
    plan.add_argument('--runtime-class', default='nvidia')
    plan.add_argument('--storage-class', default='local-path')
    plan.add_argument('--shared-ca-configmap', default='llm-gateway-stack-ca')
    plan.add_argument('--release', help='Name for a new Helm release. Existing endpoints are reported separately.')
    plan.add_argument('--verbose', action='store_true', help='Show deployment checks, per-pod allocations and an install command when eligible.')
    plan.add_argument('--json', action='store_true', help='Print the complete capacity report as JSON.')
    args = parser.parse_args(argv)
    if args.command == 'recipes':
        catalog = json.loads((HERE / 'recipes/index.json').read_text())
        if args.json:
            print(json.dumps(catalog, indent=2))
        else:
            print_recipe_catalog(catalog)
        return catalog
    if not re.fullmatch(r'[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?', args.namespace):
        parser.error('--namespace must be a Kubernetes namespace name.')
    if args.command == 'chat' and (not args.model.strip() or not args.prompt.strip()):
        parser.error('Choose a served model and provide a nonempty prompt.')
    if args.command == 'plan':
        if not args.model.strip():
            parser.error('Choose a recipe model ID.')
        for field in ('runtime_class', 'storage_class', 'shared_ca_configmap'):
            if not re.fullmatch(r'[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?', getattr(args, field)):
                parser.error('--' + field.replace('_', '-') + ' must be a Kubernetes resource name.')
        if args.release and not re.fullmatch(r'[a-z0-9](?:[-a-z0-9]{0,51}[a-z0-9])?', args.release):
            parser.error('--release must be a Helm release name (at most 53 characters).')
        if any(value is not None and value <= 0 for value in (args.context_length, args.concurrency)):
            parser.error('Context length and concurrency must be positive integers.')
    context = selected_context(args.context)
    if args.command == 'plan':
        report = capacity_plan(context, args)
        if args.json:
            print(json.dumps(report, indent=2))
        else:
            print_capacity_plan(report, verbose=args.verbose)
        return report
    with gateway(context, args.namespace, args.ca_configmap, args.api_key_file) as client:
        if args.command == 'models':
            listing = model_list(client)
            if args.json:
                print(json.dumps(listing, indent=2))
            else:
                print_model_list(listing)
            return listing
        return client.completion(args.model, args.prompt, stream=args.stream, display=True)


def terminate(signum, frame):
    raise SystemExit(128 + signum)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, terminate)
    try:
        result = main()
        if isinstance(result, dict) and result.get('status') in ('blocked', 'unsupported', 'existing'):
            raise SystemExit(2)
    except (ValueError, RuntimeError, KeyError, OSError, subprocess.SubprocessError, http.client.HTTPException) as error:
        raise SystemExit('LLM command failed: ' + str(error))
    except KeyboardInterrupt:
        raise SystemExit(130)
