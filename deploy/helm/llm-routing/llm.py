#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Check cluster prerequisites, list local recipes, check model capacity, discover deployed models and chat."""
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
def forward(context, namespace, work, local_port=None, keep_open=False):
    path = work / 'port-forward.log'
    with path.open('w') as log:
        process = subprocess.Popen(kube(context, namespace, 'port-forward', 'svc/llm-api-gateway',
                                        f'{local_port or ""}:8080', '--address', '127.0.0.1'), stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                output = path.read_text()
                if process.poll() is not None:
                    raise RuntimeError('Gateway connection failed: ' + output.strip()[-2000:])
                match = re.search(r'Forwarding from 127\.0\.0\.1:(\d+) -> 8080', output)
                if match:
                    yield 'https://127.0.0.1:' + match[1]
                    if keep_open:
                        process.wait()
                        raise RuntimeError('Gateway connection closed: ' + path.read_text().strip()[-2000:])
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
def private_access(ca, key):
    with tempfile.TemporaryDirectory(prefix='llm-gateway-') as directory:
        work = pathlib.Path(directory)
        for name, value in [('ca.crt', ca), ('api-key', key)]:
            path = work / name
            path.touch(mode=0o600)
            path.write_text(value)
        yield work


@contextlib.contextmanager
def gateway(context, namespace, ca_configmap=None, api_key_file=None):
    ca, key = access_material(context, namespace, ca_configmap, api_key_file)
    with private_access(ca, key) as work, forward(context, namespace, work) as url:
        yield Client(url, work / 'ca.crt', work / 'api-key')


def hold_gateway(context, namespace, port, ca_configmap=None, api_key_file=None):
    ca, key = access_material(context, namespace, ca_configmap, api_key_file)
    with private_access(ca, key) as work, forward(context, namespace, work, port, keep_open=True) as url:
        print(f'Gateway for context {context} is open at {url}/v1.')
        print('Run these in the terminal where you start an agent or client:\n')
        print(f'export LLM_GATEWAY_URL={url}/v1')
        print('export LLM_GATEWAY_CA=' + shlex.quote(str(work / 'ca.crt')))
        print('export LLM_API_KEY="$(cat ' + shlex.quote(str(work / 'api-key')) + ')"')
        print('\nKeep this command running. Press Ctrl-C to close the connection and remove the credential files.',
              flush=True)


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
    release = args.release or deployment['releaseName']
    if deployment['lifecycle'] != 'automatic':
        raise ValueError('The selected profile does not support automatic Helm installation.')
    chart = args.chart_source or str((HERE / 'recipes' / deployment['chart']['localPath']).resolve())
    command = ['helm', 'install', release,
               chart,
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
    return {'release': release, 'command': shlex.join(command),
            'guide': deployment['guide'].removeprefix('../')}


def model_releases(context, namespace, model):
    helm = ['helm', '--kube-context', context, '--namespace', namespace]
    releases = []
    while True:
        page = json.loads(run(helm + ['list', '--deployed', '--failed', '--pending', '--uninstalled',
                                     '--superseded', '--uninstalling', '--output', 'json',
                                     '--max', '256', '--offset', str(len(releases))]))
        releases.extend(page)
        if len(page) < 256:
            break
    chart_names = {profile['deployment']['chart']['name'] for profile in model['profiles']}
    matching = []
    for release in releases:
        if not any(release.get('chart', '').startswith(name + '-') for name in chart_names):
            continue
        values = json.loads(run(helm + ['get', 'values', release['name'], '--all', '--output', 'json']))
        if values.get('recipe') != model['id']:
            continue
        matching.append({'release': release['name'], 'status': release.get('status'),
                         'suspended': values.get('suspended', False), 'nodes': values.get('nodes', []),
                         'profile': values.get('profileName'),
                         'inspectCommand': shlex.join(helm + ['get', 'values', release['name'], '--all']),
                         'releaseCommand': shlex.join(helm + ['status', release['name']])})
    return matching, {release['name'] for release in releases}


def retained_cache_nodes(storage, namespace, release, model, storage_class):
    required, capacities, blockers, ranks = {}, {}, [], set()
    for claim in storage.get('claims', []):
        if claim['namespace'] != namespace:
            continue
        name = claim['claim']
        if model['runtime']['backend'] == 'sglang':
            match = re.fullmatch(re.escape(release) + r'-cache-(0|[1-9]\d*)', name)
            rank = int(match[1]) if match else None
        else:
            match = re.fullmatch(re.escape(release) + r'-rpc-cache-n([1-9]\d*)', name)
            rank = 0 if name == release + '-artifacts' else int(match[1]) if match else None
        if rank is None:
            if claim.get('release') == release:
                blockers.append('Retained claim requires explicit inspection before reuse: ' + name)
            continue
        if rank in ranks:
            blockers.append('Multiple retained cache claims map to rank ' + str(rank) + '.')
            continue
        ranks.add(rank)
        if claim.get('release') != release or claim.get('releaseNamespace') != namespace:
            blockers.append('Cache claim has different or unknown Helm ownership: ' + name)
        elif claim.get('storageClass') != storage_class:
            blockers.append('Retained cache requires its original StorageClass: ' + name)
        elif claim.get('deleting') or claim.get('phase') != 'Bound' or not claim.get('volume'):
            blockers.append('Retained cache must be Bound and not terminating: ' + name)
        elif claim.get('accessModes') != ['ReadWriteOnce'] or claim.get('volumeMode') != 'Filesystem':
            blockers.append('Retained cache requires ReadWriteOnce filesystem access: ' + name)
        elif len(claim['nodes']) != 1:
            blockers.append('Retained cache node placement is unknown or ambiguous: ' + name)
        else:
            required[rank] = claim['nodes'][0]
            capacities[rank] = claim.get('capacityBytes', 0)
    return required, capacities, blockers


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
    import model_storage
    storage = model_storage.collect(context, snapshot, run)
    prerequisites, existing_releases, required_nodes, cache_capacities = [], [], {}, {}
    if model['availability']['deployable']:
        release = args.release or model['profiles'][0]['deployment']['releaseName']
        required_nodes, cache_capacities, prerequisites = retained_cache_nodes(
            storage, args.namespace, release, model, args.storage_class)
        if storage['warnings']:
            prerequisites.append('Storage inventory is incomplete; inspect retained claims before installing.')
        try:
            existing_releases, names = model_releases(context, args.namespace, model)
            if release in names and not any(item['release'] == release for item in existing_releases):
                prerequisites.append('Helm release name is already in use: ' + release)
        except (RuntimeError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
            prerequisites.append('Helm release inventory failed: ' + str(error))
    report = analyzer.analyze(snapshot, catalog, args.model, capabilities=capabilities,
                              profile_id=args.profile, context_length=args.context_length,
                              concurrency=args.concurrency, required_nodes=required_nodes, cache_capacities=cache_capacities)
    report.update(context=context, namespace=args.namespace)
    report['deployment'] = None
    report['existingDeployments'] = []
    report['limitations'].append('Retained claim ownership, capacity, access mode and node placement are checked. Cached file integrity and compatibility are verified during startup.')
    report['storage'] = storage
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
    known_releases = {item.get('release') for item in report['existingDeployments']}
    report['existingDeployments'].extend(item for item in existing_releases if item['release'] not in known_releases)
    if report['existingDeployments']:
        report['status'] = 'existing'
    elif report['status'] == 'fits':
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


def node_ready(node):
    conditions = {item['type']: item.get('status') for item in node.get('status', {}).get('conditions', [])}
    return conditions.get('Ready') == 'True' and not node.get('spec', {}).get('unschedulable')


def gpu_node_problems(node):
    spec, status = node.get('spec', {}), node.get('status', {})
    labels = node['metadata'].get('labels', {})
    problems = []
    if not node_ready(node):
        problems.append('not Ready or cordoned')
    if any(taint.get('effect') in ('NoSchedule', 'NoExecute') for taint in spec.get('taints', [])):
        problems.append('NoSchedule or NoExecute taint')
    if not labels.get('nvidia.com/gpu.product'):
        problems.append('no nvidia.com/gpu.product label')
    if labels.get('nvidia.com/gpu.sharing-strategy', 'none') != 'none' or labels.get('nvidia.com/gpu.replicas', '1') != '1':
        problems.append('GPU shared by time-slicing or MPS')
    if (labels.get('nvidia.com/mig.capable') != 'false' and labels.get('nvidia.com/mig.strategy', 'none') != 'none') or any(
            name.startswith('nvidia.com/mig-') for name in status.get('allocatable', {})):
        problems.append('MIG enabled')
    return problems


def node_recipes(node, catalog, gpu_product):
    labels = node['metadata'].get('labels', {})
    product = gpu_product(labels.get('nvidia.com/gpu.product', ''))
    gpus = str(node.get('status', {}).get('allocatable', {}).get('nvidia.com/gpu'))
    matches = []
    for recipe in catalog['recipes']:
        for profile in recipe.get('profiles', []):
            hardware = profile['hardware']
            if (labels.get('kubernetes.io/os') == hardware['os'] and labels.get('kubernetes.io/arch') == hardware['architecture']
                    and product in {gpu_product(name) for name in hardware['gpuProducts']}
                    and gpus == str(hardware['gpuCount']) and recipe['id'] not in matches):
                matches.append(recipe['id'])
    return matches


def preloaded_images(values):
    images, architectures = set(), set()

    def walk(item):
        if isinstance(item, dict):
            if item.get('pullPolicy') == 'Never' and item.get('repository') and item.get('tag'):
                images.add(f"{item['repository']}:{item['tag']}")
            if isinstance(item.get('nodeSelector'), dict) and item['nodeSelector'].get('kubernetes.io/arch'):
                architectures.add(item['nodeSelector']['kubernetes.io/arch'])
            for value in item.values():
                walk(value)
        elif isinstance(item, list):
            for value in item:
                walk(value)
    walk(values)
    return sorted(images), architectures


def preflight(args):
    checks = []

    def check(name, ok, detail, warning=False):
        checks.append({'check': name, 'result': 'PASS' if ok else 'WARN' if warning else 'FAIL', 'detail': detail})
        return ok

    report = {'context': None, 'checks': checks, 'nodes': [],
              'notChecked': ['Outbound access from nodes to Hugging Face, Docker Hub, nvcr.io and GitHub.',
                             'Free disk on each node for model caches.']}
    check('Python', sys.version_info >= (3, 11), 'Python ' + '.'.join(map(str, sys.version_info[:3])) + '; 3.11 or newer required.')
    for name, command, minimum in [('kubectl', ['kubectl', 'version', '--client'], None),
                                   ('Helm', ['helm', 'version', '--short'], (3, 10))]:
        try:
            version = run(command).strip().splitlines()[0]
        except (OSError, RuntimeError, subprocess.SubprocessError, IndexError) as error:
            check(name, False, 'Not usable: ' + str(error))
            continue
        found = re.search(r'v(\d+)\.(\d+)', version)
        check(name, not minimum or bool(found and tuple(map(int, found.groups())) >= minimum),
              version + ('; ' + '.'.join(map(str, minimum)) + ' or newer required.' if minimum else ''))
    try:
        context = report['context'] = selected_context(args.context)
        nodes = json.loads(run(['kubectl', '--context', context, 'get', 'nodes', '-o', 'json']))['items']
    except (OSError, ValueError, RuntimeError, KeyError, subprocess.SubprocessError) as error:
        check('Cluster access', False, str(error))
        report['status'] = 'blocked'
        return report
    check('Cluster access', True, f'Context {context} reaches {len(nodes)} node(s).')
    try:
        allowed = run(['kubectl', '--context', context, 'auth', 'can-i', 'create',
                       'customresourcedefinitions.apiextensions.k8s.io']).strip().startswith('yes')
    except (RuntimeError, subprocess.SubprocessError):
        allowed = False
    check('Permissions', allowed, 'Can create the InferenceEndpoint CRD that the shared release installs.' if allowed else
          'Cannot create CustomResourceDefinitions. Use a cluster-admin context, or manage the CRD externally with operator.installCRDs=false.')

    def listing(name, resource):
        try:
            return json.loads(run(['kubectl', '--context', context, 'get', resource, '-o', 'json']))['items']
        except (ValueError, RuntimeError, KeyError, subprocess.SubprocessError) as error:
            check(name, False, f'Cannot list {resource}: {error}')

    catalog = json.loads((HERE / 'recipes/index.json').read_text())
    gpu_product = planning_modules().gpu_product
    gpu_nodes = [node for node in nodes if str(node.get('status', {}).get('allocatable', {}).get('nvidia.com/gpu', '0')) not in ('0', '')]
    for node in gpu_nodes:
        labels = node['metadata'].get('labels', {})
        report['nodes'].append({'name': node['metadata']['name'], 'product': labels.get('nvidia.com/gpu.product', ''),
                                'architecture': labels.get('kubernetes.io/arch', ''),
                                'gpus': node['status']['allocatable']['nvidia.com/gpu'],
                                'recipes': node_recipes(node, catalog, gpu_product), 'problems': gpu_node_problems(node)})
    usable = [node for node in report['nodes'] if node['recipes'] and not node['problems']]
    if check('GPU nodes', bool(gpu_nodes), f'{len(gpu_nodes)} node(s) advertise nvidia.com/gpu.' if gpu_nodes else
             'No node advertises nvidia.com/gpu. Install the NVIDIA device plugin.'):
        check('Recipe hardware', len(usable) == len(gpu_nodes),
              f'{len(usable)} of {len(gpu_nodes)} GPU node(s) match a recipe profile and have no problems.', warning=bool(usable))
    runtime_classes = listing('RuntimeClass', 'runtimeclasses')
    if runtime_classes is not None:
        runtime_class = any(item['metadata']['name'] == args.runtime_class for item in runtime_classes)
        check('RuntimeClass', runtime_class, f'RuntimeClass {args.runtime_class} ' + ('exists.' if runtime_class else 'is not installed.'))
    storage_classes = listing('StorageClass', 'storageclasses')
    if storage_classes is not None:
        storage = next((item for item in storage_classes if item['metadata']['name'] == args.storage_class), None)
        check('StorageClass', bool(storage) and storage.get('volumeBindingMode') == 'WaitForFirstConsumer' and not storage.get('allowedTopologies'),
              f'StorageClass {args.storage_class} ' + ('is not installed.' if not storage else
                                                      f"uses {storage.get('volumeBindingMode', 'Immediate')} binding"
                                                      + (' with allowedTopologies' if storage.get('allowedTopologies') else '')
                                                      + '; WaitForFirstConsumer with unrestricted topology required.'))
    if args.values:
        images, architectures = [], set()
        try:
            images, architectures = preloaded_images(json.loads(args.values.read_text()))
        except OSError as error:
            check('Images', False, f'Cannot read values file {args.values}: {error.strerror or error}')
        except ValueError:
            check('Images', False, f'Image check reads JSON values files only: {args.values}', warning=True)
        eligible = [node for node in nodes if node_ready(node) and (
            not architectures or node['metadata'].get('labels', {}).get('kubernetes.io/arch') in architectures)]
        if images and not eligible:
            check('Images', False, 'No Ready node matches the architecture selected in ' + str(args.values) + '.')
            images = []
        for image in images:
            listed = [node['metadata']['name'] for node in eligible if any(
                name in (image, 'docker.io/' + image, 'docker.io/library/' + image)
                for entry in node.get('status', {}).get('images', []) for name in entry.get('names', []))]
            missing = sorted({node['metadata']['name'] for node in eligible} - set(listed))
            check('Image', not missing, f'{image}: listed on {len(listed)} of {len(eligible)} eligible node(s)'
                  + ('; missing on ' + ', '.join(missing) + '. Kubelet lists at most 50 images per node.' if missing else '.'),
                  warning=bool(listed))
    report['status'] = 'blocked' if any(item['result'] == 'FAIL' for item in checks) else 'ready'
    return report


def print_preflight(report):
    if report['context']:
        print('Cluster: ' + report['context'])
    print_table(['CHECK', 'RESULT', 'DETAIL'], [[item['check'], item['result'], item['detail']] for item in report['checks']])
    if report['nodes']:
        print()
        print_table(['GPU NODE', 'PRODUCT', 'ARCH', 'GPUS', 'RECIPES', 'PROBLEMS'],
                    [[node['name'], node['product'] or '-', node['architecture'] or '-', node['gpus'],
                      ', '.join(node['recipes']) or '-', '; '.join(node['problems']) or '-'] for node in report['nodes']])
    print('\nNot checked:')
    for item in report['notChecked']:
        print('  ' + item)
    print('\n' + ('Prerequisites met. Continue with README.md#1-install-shared-infrastructure.' if report['status'] == 'ready'
                  else 'Fix each FAIL above, then rerun. See README.md#prerequisites.'))


def print_capacity_status(status):
    statuses = {'fits': 'FITS current scheduling allocations.', 'blocked': 'DOES NOT FIT current requirements.',
                'unsupported': 'NO DEPLOYABLE RECIPE.', 'existing': 'EXISTING DEPLOYMENT found in this namespace; inspect it before creating another.'}
    print(statuses[status])


def validation_summary(validation):
    status = validation.get('automaticHelmStatus', 'pending')
    failed = (validation.get('automaticHelmWorkload') or {}).get('failedChecks', [])
    return status + (' (failed: ' + ', '.join(failed) + ')' if failed else '')


def print_capacity_details(report):
    print(f"Model: {report['model']} | Cluster: {report['context']} | Namespace: {report['namespace']}")
    print_capacity_status(report['status'])
    detected_products = sorted({node.get('gpuProduct') or 'unknown' for node in report.get('nodes', [])
                                if (node['gpu']['allocatable'] or 0) > 0})
    for existing in report['existingDeployments']:
        if existing.get('endpoint'):
            print(f"Endpoint {existing['endpoint']}: Ready={existing['ready']}, Registered={existing['registered']}")
        else:
            print(f"Helm release {existing['release']}: status={existing['status']}, suspended={existing['suspended']}")
        print('Inspect the existing deployment: ' + existing['inspectCommand'])
        if existing.get('releaseCommand'):
            print(existing['releaseCommand'])
        print('If stopped, resume its existing release with the original chart and retained cache placement. See README.md#stop-and-resume.')
    for profile in report.get('profiles', []):
        requirements = profile.get('requirements', {})
        print(f"Evaluated profile: {profile['id']} ({requirements['modelNodeCount']} node(s))")
        products = dict.fromkeys(planning_modules().gpu_product(product)
                                 for product in requirements['hardware']['gpuProducts'])
        print('  Required GPU: ' + ', '.join(products))
        if detected_products and 'unknown' not in detected_products and not set(products).intersection(detected_products):
            print('  Result: incompatible with detected ' + ', '.join(detected_products) + ' GPUs')
        else:
            print('  Detected GPUs: ' + (', '.join(detected_products) or 'none advertised'))
            print('  Result: ' + profile['status'])
        validation = profile.get('validation', {})
        summary = validation_summary(validation)
        if validation.get('automaticHelmStatus') and summary != 'smoke-tested':
            print('  Automatic startup validation: ' + summary + '. See the recipe validation notes before deploying.')
        workload = validation.get('automaticHelmWorkload') or {}
        if workload.get('failedChecks') and workload.get('reason'):
            print('  Validation notes: ' + workload['reason'])
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
            validation = validation_summary(profile.get('validation', {}))
            products = dict.fromkeys(planning_modules().gpu_product(product)
                                    for product in profile.get('hardware', {}).get('gpuProducts', []))
            gpu = ', '.join(products) or '-'
            rows.append([recipe['id'], recipe.get('precision') or '-', profile['id'],
                         gpu, profile['modelNodeCount'], validation])
    print_table(['RECIPE', 'PRECISION', 'PROFILE', 'GPU', 'NODES', 'VALIDATION'], rows)
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
        profiles = report.get('profiles', [])
        gpu_mismatch = report['status'] == 'blocked' and profiles and all(
            profile.get('nodeBlockers')
            and not any(candidate['nodes'] for candidate in profile.get('candidateNodes', []))
            and all('GPU product does not match this profile.' in blocker['reasons']
                    for blocker in profile['nodeBlockers'])
            for profile in profiles)
        if gpu_mismatch:
            print("No compatible profile found for this model and the cluster's GPUs.")
        else:
            print_capacity_status(report['status'])
        for profile in report.get('profiles', []):
            if report['status'] != 'fits' or profile['id'] != report['chosenProfile']:
                continue
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
        if report['status'] == 'fits':
            print('Selected nodes: ' + ', '.join(report['chosenNodes']))
        elif report['status'] in ('blocked', 'unsupported') and not gpu_mismatch:
            reasons = [f"{profile['id']}: {reason}"
                       for profile in report.get('profiles', [])
                       for blocker in profile.get('nodeBlockers', [])
                       for reason in blocker['reasons']]
            for reason in dict.fromkeys(reasons + report.get('blockers', [])):
                print('  - ' + reason)
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
    check = commands.add_parser('preflight', help='Check workstation tools and cluster prerequisites, without changes.')
    check.add_argument('--runtime-class', default='nvidia')
    check.add_argument('--storage-class', default='local-path')
    check.add_argument('--values', type=pathlib.Path, help='Shared stack values file whose Never-pull images must be preloaded.')
    check.add_argument('--json', action='store_true', help='Print the complete check report as JSON.')
    recipe_list = commands.add_parser('recipes', help='List local recipes and hardware profiles without cluster access.')
    recipe_list.add_argument('--json', action='store_true', help='Print the complete local recipe catalog as JSON.')
    models = commands.add_parser('models', help='List models through the gateway.')
    models.add_argument('--json', action='store_true', help='Print the complete model list as JSON.')
    chat = commands.add_parser('chat', help='Chat with a model through the gateway.')
    chat.add_argument('--model', required=True)
    chat.add_argument('--stream', action='store_true')
    chat.add_argument('--max-tokens', type=int, help='Maximum generated tokens. Defaults to the model server setting.')
    chat.add_argument('prompt')
    connect = commands.add_parser('connect', help='Keep a gateway connection open for coding agents and other local clients.')
    connect.add_argument('--port', type=int, default=18443, help='Local port for the gateway (default: 18443).')
    plan = commands.add_parser('plan', help='Check recipe capacity and show placement or current allocations, without changes.')
    plan.add_argument('--model', required=True, help='Recipe ID shown by llm.py recipes.')
    plan.add_argument('--profile', help='Select one hardware profile instead of considering every supported profile.')
    plan.add_argument('--capabilities', type=pathlib.Path, help='Private JSON file with verified NVMe, device-memory or fabric facts.')
    plan.add_argument('--context-length', type=int)
    plan.add_argument('--concurrency', type=int)
    plan.add_argument('--runtime-class', default='nvidia')
    plan.add_argument('--storage-class', default='local-path')
    plan.add_argument('--shared-ca-configmap', default='llm-gateway-stack-ca')
    plan.add_argument('--release', help='Name for a new Helm release. Existing model releases are reported separately.')
    plan.add_argument('--chart-source', help='Exact Helm chart reference for the install command. Defaults to the recipe source chart.')
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
    if args.command == 'chat' and args.max_tokens is not None and args.max_tokens < 1:
        parser.error('--max-tokens must be a positive integer.')
    if args.command == 'connect' and not 0 < args.port < 65536:
        parser.error('--port must be between 1 and 65535.')
    if args.command in ('plan', 'preflight'):
        for field in ('runtime_class', 'storage_class'):
            if not re.fullmatch(r'[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?', getattr(args, field)):
                parser.error('--' + field.replace('_', '-') + ' must be a Kubernetes resource name.')
    if args.command == 'preflight':
        report = preflight(args)
        if args.json:
            print(json.dumps(report, indent=2))
        else:
            print_preflight(report)
        return report
    if args.command == 'plan':
        if not args.model.strip():
            parser.error('Choose a recipe model ID.')
        if not re.fullmatch(r'[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?', args.shared_ca_configmap):
            parser.error('--shared-ca-configmap must be a Kubernetes resource name.')
        if args.release and not re.fullmatch(r'[a-z0-9](?:[-a-z0-9]{0,51}[a-z0-9])?', args.release):
            parser.error('--release must be a Helm release name (at most 53 characters).')
        if args.release == 'llm-stack':
            parser.error('--release llm-stack is reserved for shared infrastructure.')
        if args.chart_source is not None and (not args.chart_source.strip() or args.chart_source.startswith('-')):
            parser.error('--chart-source must be a nonempty Helm chart reference.')
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
    if args.command == 'connect':
        return hold_gateway(context, args.namespace, args.port, args.ca_configmap, args.api_key_file)
    with gateway(context, args.namespace, args.ca_configmap, args.api_key_file) as client:
        if args.command == 'models':
            listing = model_list(client)
            if args.json:
                print(json.dumps(listing, indent=2))
            else:
                print_model_list(listing)
            return listing
        return client.completion(args.model, args.prompt, stream=args.stream, display=True,
                                 max_tokens=args.max_tokens, strict=False)


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
