#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Plan independent model releases from Kubernetes capacity and recipe profiles."""
import argparse
import copy
import datetime as dt
import decimal
import hashlib
import ipaddress
import itertools
import importlib.util
import json
import math
import pathlib
import re
import subprocess
import sys

HERE = pathlib.Path(__file__).resolve().parent
GIB = 1024 ** 3


def require(condition, message):
    if not condition:
        raise ValueError(message)


def quantity(value):
    match = re.fullmatch(r'([0-9]+(?:\.[0-9]+)?)([EPTGMK]i|[EPTGMk]|m|u|n)?', str(value))
    require(match, 'Unsupported Kubernetes quantity: ' + str(value))
    number, suffix = match.groups()
    scale = {'': 1, 'm': .001, 'u': .000001, 'n': .000000001}
    for i, unit in enumerate('KMGTPE', 1):
        scale[unit + 'i'] = 1024 ** i
        scale[unit if unit != 'K' else 'k'] = 1000 ** i
    return decimal.Decimal(number) * decimal.Decimal(str(scale[suffix or '']))


def requests(pod, resource):
    spec = pod['spec']
    def amount(c):
        res = c.get('resources', {})
        return quantity(res.get('requests', {}).get(resource, res.get('limits', {}).get(resource, 0)))
    regular = sum(amount(c) for c in spec.get('containers', []))
    sidecars = 0
    init_peak = 0
    for c in spec.get('initContainers', []):
        if c.get('restartPolicy') == 'Always':
            sidecars += amount(c)
            init_peak = max(init_peak, sidecars)
        else:
            init_peak = max(init_peak, sidecars + amount(c))
    return max(regular + sidecars, init_peak, quantity(spec.get('resources', {}).get('requests', {}).get(resource, 0))) + quantity(spec.get('overhead', {}).get(resource, 0))


def command_json(command):
    return json.loads(subprocess.check_output(command, text=True, timeout=60))


def inventory(context):
    require(context, 'An explicit Kubernetes context is required.')
    result = {'schemaVersion': 1, 'context': context, 'capturedAt': dt.datetime.now(dt.timezone.utc).isoformat()}
    for key, resource in [('nodes', 'nodes'), ('pods', 'pods'), ('endpoints', 'inferenceendpoints.pylon.nvidia.com'),
                          ('storageClasses', 'storageclasses'), ('runtimeClasses', 'runtimeclasses')]:
        command = ['kubectl', '--context', context, 'get', resource, '-o', 'json']
        if key in ('pods', 'endpoints'):
            command += ['--all-namespaces']
        result[key] = command_json(command)['items']
    return result


def capacity(snapshot):
    busy = {}
    active = [p for p in snapshot['pods'] if p.get('status', {}).get('phase') not in ('Succeeded', 'Failed')]
    for p in active:
        if not p['spec'].get('nodeName') and requests(p, 'nvidia.com/gpu') > 0:
            raise ValueError('An unscheduled GPU workload exists. Resolve it before planning: ' + p['metadata']['name'])
        name = p['spec'].get('nodeName')
        used = busy.setdefault(name, {'nvidia.com/gpu': 0, 'memory': 0, 'cpu': 0, 'ephemeral-storage': 0})
        for resource in used:
            used[resource] += requests(p, resource)
    candidates, rejected = [], {}
    for n in snapshot['nodes']:
        name, labels = n['metadata']['name'], n['metadata'].get('labels', {})
        reasons = []
        conditions = {c['type']: c['status'] for c in n.get('status', {}).get('conditions', [])}
        if conditions.get('Ready') != 'True' or any(conditions.get(c) == 'True' for c in ('MemoryPressure', 'DiskPressure', 'PIDPressure')):
            reasons.append('node is not ready or has resource pressure')
        if n.get('spec', {}).get('unschedulable') or any(t.get('effect') in ('NoSchedule', 'NoExecute', 'PreferNoSchedule') for t in n.get('spec', {}).get('taints', [])):
            reasons.append('node is cordoned or tainted')
        if labels.get('nvidia.com/gpu.sharing-strategy', 'none') != 'none' or labels.get('nvidia.com/gpu.replicas', '1') != '1':
            reasons.append('shared GPUs are not qualified')
        alloc = n.get('status', {}).get('allocatable', {})
        free = {r: quantity(alloc.get(r, 0)) - busy.get(name, {}).get(r, 0)
                for r in ('nvidia.com/gpu', 'memory', 'cpu', 'ephemeral-storage')}
        if free['nvidia.com/gpu'] < 1:
            reasons.append('requires an idle exclusive GPU')
        if reasons:
            rejected[name] = reasons
        else:
            candidates.append({'name': name, 'uid': n['metadata']['uid'], 'free': free, 'node': n})
    return sorted(candidates, key=lambda n: n['name']), rejected


def gpu_product(value):
    return re.sub(r'\s+', '-', value.strip())


def positive_number(value):
    return type(value) in (int, float) and math.isfinite(value) and value > 0


def profile_hardware(profile):
    hardware = profile.get('hardware')
    require(isinstance(hardware, dict), 'Profile must declare hardware requirements: ' + profile['id'])
    for field in ('os', 'architecture'):
        require(isinstance(hardware.get(field), str) and bool(hardware[field].strip()),
                'Profile hardware must declare ' + field + ': ' + profile['id'])
    for field in ('gpuProducts', 'cudaDeviceNames'):
        values = hardware.get(field)
        require(isinstance(values, list) and values and all(isinstance(value, str) and value.strip() for value in values),
                'Profile hardware must declare ' + field + ': ' + profile['id'])
    require(type(hardware.get('gpuCount')) is int and hardware['gpuCount'] == 1,
            'The current runtime supports one assigned GPU per rank; profile hardware.gpuCount must be 1.')
    require(hardware.get('memoryMode') in ('unified', 'discrete'), 'Profile hardware must declare unified or discrete memoryMode.')
    if hardware['memoryMode'] == 'discrete':
        require(positive_number(hardware.get('minDeviceMemoryGiB')), 'Discrete profile hardware must declare positive minDeviceMemoryGiB.')
    if profile.get('fabric'):
        require(positive_number(profile.get('minFabricGbps')), 'Distributed profile must declare positive minFabricGbps.')
    return hardware


def matches_hardware(candidate, hardware, facts):
    node = candidate['node']
    labels = node['metadata'].get('labels', {})
    if labels.get('kubernetes.io/os') != hardware['os'] or labels.get('kubernetes.io/arch') != hardware['architecture']:
        return False
    product = labels.get('nvidia.com/gpu.product', '')
    if gpu_product(product) not in {gpu_product(value) for value in hardware['gpuProducts']}:
        return False
    if quantity(node.get('status', {}).get('allocatable', {}).get('nvidia.com/gpu', 0)) != hardware['gpuCount']:
        return False
    if candidate['free']['nvidia.com/gpu'] < hardware['gpuCount']:
        return False
    if hardware['memoryMode'] == 'discrete':
        memory = facts.get('cudaTotalMemoryGiB')
        if not positive_number(memory) or memory < hardware['minDeviceMemoryGiB']:
            return False
    return True


def valid_name(value):
    return isinstance(value, str) and bool(re.fullmatch('[a-z0-9]([-a-z0-9]*[a-z0-9])?', value)) and len(value) <= 40


def plan(snapshot, capabilities, model_ids, namespace, storage_class, runtime_class,
         context_length=8192, concurrency=1, allow_offload=True, preference='fewest-nodes', requirements=None, placements=None):
    catalog = json.loads((HERE / 'catalog.json').read_text())
    models = {m['id']: m for m in catalog['models']}
    require(model_ids and len(set(model_ids)) == len(model_ids), 'Select each model once.')
    require(all(m in models for m in model_ids), 'Unknown model. Run catalog to list recipes.')
    require(valid_name(namespace), 'Invalid namespace.')
    require(isinstance(context_length, int) and context_length > 0 and isinstance(concurrency, int) and concurrency > 0,
            'Context length and concurrency must be positive integers.')
    require(preference in ('fewest-nodes', 'latency'), 'Unknown placement preference.')
    require(snapshot.get('context'), 'Inventory must contain its context.')
    require(any(c['metadata']['name'] == runtime_class for c in snapshot['runtimeClasses']), 'RuntimeClass is not installed.')
    storage = next((c for c in snapshot['storageClasses'] if c['metadata']['name'] == storage_class), None)
    require(storage and storage.get('volumeBindingMode') == 'WaitForFirstConsumer',
            'Use a StorageClass with WaitForFirstConsumer to bind storage to selected nodes.')
    require(not storage.get('allowedTopologies'), 'StorageClass allowedTopologies requires a topology-aware profile; use an unrestricted class.')
    placements = placements or {}
    require(set(placements) <= set(model_ids), 'Cache placement contains an unselected model.')
    capabilities = capabilities.get('nodes', {})
    candidates, rejected = capacity(snapshot)
    require(len(candidates) == len({n['uid'] for n in candidates}), 'Duplicate node identities in inventory.')
    registered = {e['spec']['modelName'] for e in snapshot['endpoints']
                  if e.get('metadata', {}).get('namespace') == namespace}
    choices, failures = {}, {}
    requirements = requirements or {}
    require(set(requirements) <= set(model_ids), 'Workload requirements contain an unselected model.')
    for model_id in model_ids:
        require(model_id not in registered, 'Model name is already registered: ' + model_id)
        model = models[model_id]
        workload = requirements.get(model_id, {})
        require(set(workload) <= {'contextLength', 'concurrency'}, 'Unknown workload requirement for ' + model_id)
        ctx, conc = workload.get('contextLength', context_length), workload.get('concurrency', concurrency)
        require(type(ctx) is int and type(conc) is int and ctx > 0 and conc > 0, 'Invalid workload requirements.')
        choices[model_id], failures[model_id] = [], []
        for profile in model['profiles']:
            hardware = profile_hardware(profile)
            if ctx > profile['maxContext'] or conc > profile['maxConcurrency']:
                failures[model_id].append(profile['id'] + ': requested context/concurrency exceeds the recipe envelope')
                continue
            if profile['offload'] and not allow_offload:
                failures[model_id].append(profile['id'] + ': NVMe offload disabled')
                continue
            eligible = []
            for n in candidates:
                facts = capabilities.get(n['name'], {})
                if not matches_hardware(n, hardware, facts):
                    continue
                if n['free']['memory'] < profile['memoryGiB'] * GIB or n['free']['cpu'] < profile['cpu']:
                    continue
                if profile['offload']:
                    if not facts.get('localNvme') or n['free']['ephemeral-storage'] < profile['offloadGiB'] * GIB:
                        continue
                if profile.get('fabric'):
                    if not facts.get('fabric') or not re.fullmatch(r'[a-zA-Z0-9_.:-]+', facts.get('interface', '')):
                        continue
                    try:
                        address = ipaddress.ip_address(facts.get('address', ''))
                        require(address.version == 4 and not address.is_unspecified and not address.is_loopback, 'Invalid fabric IP')
                    except ValueError:
                        continue
                    if not positive_number(facts.get('gbps')) or facts['gbps'] < profile['minFabricGbps']:
                        continue
                eligible.append(n)
            for group in itertools.combinations(eligible, profile['nodes']):
                if model_id in placements:
                    ordered = placements[model_id]
                    if len(ordered) != len(group) or set(ordered) != {n['name'] for n in group}:
                        continue
                    group = tuple(next(n for n in group if n['name'] == name) for name in ordered)
                if len({gpu_product(n['node']['metadata']['labels']['nvidia.com/gpu.product']) for n in group}) != 1:
                    continue
                if profile.get('fabric'):
                    facts = [capabilities[n['name']] for n in group]
                    if len({f['fabric'] for f in facts}) != 1 or len({f['address'] for f in facts}) != len(group):
                        continue
                choices[model_id].append((profile, group, ctx, conc))
            if not any(c[0]['id'] == profile['id'] for c in choices[model_id]):
                failures[model_id].append(profile['id'] + ': no compatible hardware group with sufficient idle GPU, memory, CPU, NVMe or fabric capacity')
    solutions = []
    def search(index, used, selected):
        if index == len(model_ids):
            solutions.append(selected)
            return
        for choice in choices[model_ids[index]]:
            names = {n['name'] for n in choice[1]}
            if not names & used:
                search(index + 1, used | names, selected + [choice])
    search(0, set(), [])
    require(solutions, 'No placement fits all requested models without sharing GPUs. ' + json.dumps({'profiles': failures, 'excludedNodes': rejected}))
    def cost(solution):
        count = sum(p['nodes'] for p, _, _, _ in solution)
        offload = sum(p['offload'] for p, _, _, _ in solution)
        return ((count, offload) if preference == 'fewest-nodes' else (offload, count)) + tuple(n['name'] for _, group, _, _ in solution for n in group)
    selected = min(solutions, key=cost)
    releases = []
    for model_id, (profile, group, ctx, conc) in zip(model_ids, selected):
        model = models[model_id]
        release = model_id.replace('.', '-')
        port_seed = int(hashlib.sha256((namespace + '/' + release).encode()).hexdigest()[:4], 16)
        values = {'phase': 'qualify', 'model': {k: model[k] for k in ('id', 'repository', 'revision', 'precision')},
                  'image': model['image'], 'imagePullPolicy': 'IfNotPresent', 'runtimeClassName': runtime_class,
                  'storageClassName': storage_class, 'profile': copy.deepcopy(profile),
                  'gpu': {'product': gpu_product(group[0]['node']['metadata']['labels']['nvidia.com/gpu.product'])},
                  'contextLength': ctx, 'concurrency': conc, 'register': True,
                  'ports': {'http': 18000 + port_seed % 2000, 'rendezvous': 21000 + port_seed % 2000, 'bootstrap': 24000 + port_seed % 2000},
                  'targets': [{'node': n['name'], 'uid': n['uid'], **capabilities.get(n['name'], {})} for n in group]}
        releases.append({'name': release, 'model': model_id, 'profile': profile['id'], 'nodes': [n['name'] for n in group],
                         'reason': f"{profile['nodes']} GPU node(s) satisfy context {ctx}, concurrency {conc}, {model['precision']}; " +
                                   ('NVMe file offload required.' if profile['offload'] else 'weights remain in memory.'), 'values': values})
    return {'schemaVersion': 1, 'context': snapshot['context'], 'namespace': namespace,
            'inventoryTime': snapshot.get('capturedAt'), 'preference': preference, 'releases': releases,
            'excludedNodes': rejected, 'validation': 'pending-hardware-validation',
            'capacityBasis': 'Kubernetes allocatable minus pod requests; runtime checks host memory and disk before loading.'}


def stack_api():
    spec = importlib.util.spec_from_file_location('llm_stack_connection', HERE.parent / 'stack.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def inspect_stack_connection(connection, context, namespace):
    require(isinstance(connection, dict), 'Invalid stack connection in plan.')
    require(connection.get('context') == context, 'Stack connection context differs from plan.')
    require(connection.get('namespace') == namespace, 'Stack connection namespace differs from plan.')
    stack_api().inspect_connection(connection)


def check_plan(result, release_name, snapshot=None):
    if 'stackConnection' in result:
        inspect_stack_connection(result['stackConnection'], result['context'], result['namespace'])
    snapshot = snapshot if snapshot is not None else inventory(result['context'])
    require(snapshot['context'] == result['context'], 'Inventory context differs from plan.')
    selected = next((r for r in result['releases'] if r['name'] == release_name), None)
    require(selected, 'Release is not in the plan.')
    values = selected['values']
    nodes = {n['metadata']['name']: n for n in snapshot['nodes']}
    for target in values['targets']:
        require(target['node'] in nodes and nodes[target['node']]['metadata']['uid'] == target['uid'],
                'Selected node identity changed. Create a new plan.')
    scoped = copy.deepcopy(snapshot)
    scoped['nodes'] = [nodes[t['node']] for t in values['targets']]
    facts = {'nodes': {t['node']: t for t in values['targets']}}
    checked = plan(scoped, facts, [selected['model']], result['namespace'], values['storageClassName'],
                   values['runtimeClassName'], values['contextLength'], values['concurrency'],
                   values['profile']['offload'], 'latency')
    require(checked['releases'][0]['profile'] == selected['profile'], 'Eligible profile changed. Create a new plan.')
    return {'passed': True, 'release': release_name, 'nodes': selected['nodes']}


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open('x') as f:
        path.chmod(0o600)
        json.dump(value, f, indent=2)
        f.write('\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    sub.add_parser('catalog')
    deployment = sub.add_parser('deploy', help='Plan, qualify, download, serve and verify fresh model releases.')
    deployment.add_argument('--stack-connection', type=pathlib.Path, required=True)
    deployment.add_argument('--model', action='append', required=True)
    deployment.add_argument('--storage-class', required=True)
    deployment.add_argument('--runtime-class', required=True)
    deployment.add_argument('--work-dir', type=pathlib.Path, required=True)
    deployment.add_argument('--reuse-caches', action='store_true', help='Reinstall using verified retained model cache claims and offline snapshots.')
    deployment.add_argument('--capabilities', type=pathlib.Path)
    deployment.add_argument('--requirements', type=pathlib.Path)
    deployment.add_argument('--context-length', type=int, default=8192)
    deployment.add_argument('--concurrency', type=int, default=1)
    deployment.add_argument('--no-nvme-offload', action='store_true')
    deployment.add_argument('--preference', choices=['fewest-nodes', 'latency'], default='fewest-nodes')
    inv = sub.add_parser('inventory')
    inv.add_argument('--context', required=True)
    inv.add_argument('--output', type=pathlib.Path, required=True)
    p = sub.add_parser('plan')
    source = p.add_mutually_exclusive_group()
    source.add_argument('--inventory', type=pathlib.Path)
    source.add_argument('--context')
    p.add_argument('--stack-connection', type=pathlib.Path, help='Bind placement to a verified shared-stack connection.')
    p.add_argument('--capabilities', type=pathlib.Path)
    p.add_argument('--model', action='append', required=True)
    p.add_argument('--namespace', help='Defaults to the stack connection namespace when bound.')
    p.add_argument('--storage-class', required=True)
    p.add_argument('--runtime-class', required=True)
    p.add_argument('--context-length', type=int, default=8192)
    p.add_argument('--concurrency', type=int, default=1)
    p.add_argument('--requirements', type=pathlib.Path)
    p.add_argument('--no-nvme-offload', action='store_true')
    p.add_argument('--preference', choices=['fewest-nodes', 'latency'], default='fewest-nodes')
    p.add_argument('--output', type=pathlib.Path, required=True)
    check = sub.add_parser('check-plan')
    check.add_argument('--plan', type=pathlib.Path, required=True)
    check.add_argument('--release', required=True)
    r = sub.add_parser('render')
    r.add_argument('--plan', type=pathlib.Path, required=True)
    r.add_argument('--output', type=pathlib.Path, required=True)
    args = parser.parse_args()
    if args.action == 'catalog':
        print((HERE / 'catalog.json').read_text())
    elif args.action == 'deploy':
        import deploy_models
        deploy_models.deploy(args)
    elif args.action == 'inventory':
        save(args.output, inventory(args.context))
    elif args.action == 'plan':
        connection = stack_api().load_connection(args.stack_connection) if args.stack_connection else None
        context, namespace = args.context, args.namespace
        if connection is not None:
            require(not args.inventory, '--stack-connection requires live inventory. Omit --inventory.')
            context, namespace = context or connection['context'], namespace or connection['namespace']
            inspect_stack_connection(connection, context, namespace)
        require(args.inventory or context, 'Set --stack-connection, --context, or --inventory.')
        require(namespace, 'Set --namespace or --stack-connection.')
        snapshot = json.loads(args.inventory.read_text()) if args.inventory else inventory(context)
        require(not context or snapshot.get('context') == context, 'Inventory context differs from selected context.')
        capabilities = json.loads(args.capabilities.read_text()) if args.capabilities else {}
        requirements = json.loads(args.requirements.read_text()) if args.requirements else {}
        result = plan(snapshot, capabilities, args.model, namespace, args.storage_class, args.runtime_class,
                      args.context_length, args.concurrency, not args.no_nvme_offload, args.preference, requirements)
        if connection is not None:
            result['stackConnection'] = copy.deepcopy(connection)
        save(args.output, result)
        for release in result['releases']:
            print(release['model'] + ': ' + release['reason'] + ' Nodes: ' + ', '.join(release['nodes']))
    elif args.action == 'check-plan':
        result = json.loads(args.plan.read_text())
        print(json.dumps(check_plan(result, args.release)))
    elif args.action == 'render':
        result = json.loads(args.plan.read_text())
        require(not args.output.exists(), 'Use a new render output directory.')
        args.output.mkdir(parents=True, mode=0o700)
        for release in result['releases']:
            values = args.output / (release['name'] + '-values.json')
            save(values, release['values'])
            for phase in ('qualify', 'download', 'serve'):
                command = ['helm', 'template', release['name'], str(HERE / 'charts/sglang'), '--namespace', result['namespace'],
                           '--values', str(values), '--set', 'phase=' + phase]
                rendered = subprocess.check_output(command, text=True, timeout=60)
                path = args.output / (release['name'] + '-' + phase + '.yaml')
                path.write_text(rendered)
                path.chmod(0o600)
        print('Rendered qualification, download and serving manifests. No cluster resources changed.')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as e:
        print('Error: ' + str(e), file=sys.stderr)
        sys.exit(1)
