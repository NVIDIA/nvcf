# SPDX-License-Identifier: Apache-2.0
"""Read-only scheduling capacity for model profiles from the common catalog."""
import copy
import ipaddress
import itertools
import re

from recipes import gpu_product, positive_number, quantity, requests

GIB = 1024 ** 3
RESOURCES = ('nvidia.com/gpu', 'cpu', 'memory', 'ephemeral-storage')
LIMITATIONS = [
    'This is a scheduling snapshot, not a GPU reservation. Concurrent installations can change capacity.',
    'CPU and memory availability uses Kubernetes reservations, not live utilization or host MemAvailable.',
    'Physical disk space, storage provisioning, cache placement, model download and runtime startup are not verified.',
]


def number(value):
    return int(value) if value == int(value) else float(value)


def node_usage(snapshot):
    rows, pending = [], []
    workloads = {}
    endpoint_models = {}
    for endpoint in snapshot.get('endpoints', []):
        meta = endpoint['metadata']
        release = meta.get('annotations', {}).get('meta.helm.sh/release-name') or meta['name']
        endpoint_models[(meta.get('namespace', 'default'), release)] = endpoint.get('spec', {}).get('modelName')
    for pod in snapshot['pods']:
        if pod.get('status', {}).get('phase') in ('Succeeded', 'Failed'):
            continue
        amounts = {resource: requests(pod, resource) for resource in RESOURCES}
        meta = pod['metadata']
        workload = {'namespace': meta.get('namespace', 'default'), 'pod': meta['name'],
                    'phase': pod.get('status', {}).get('phase', 'Unknown'),
                    'gpu': number(amounts['nvidia.com/gpu']), 'cpuMillicores': number(amounts['cpu'] * 1000),
                    'memoryBytes': number(amounts['memory']), 'ephemeralStorageBytes': number(amounts['ephemeral-storage'])}
        release = meta.get('labels', {}).get('app.kubernetes.io/instance') or meta.get('annotations', {}).get('meta.helm.sh/release-name')
        condition = next((item for item in pod.get('status', {}).get('conditions', []) if item.get('type') == 'PodScheduled'), {})
        selector = pod['spec'].get('nodeSelector', {})
        affinity = pod['spec'].get('affinity', {}).get('nodeAffinity', {}).get('requiredDuringSchedulingIgnoredDuringExecution', {})
        targets = set()
        if selector.get('kubernetes.io/hostname'):
            targets.add(selector['kubernetes.io/hostname'])
        for term in affinity.get('nodeSelectorTerms', []):
            for expression in term.get('matchFields', []) + term.get('matchExpressions', []):
                if expression.get('operator') == 'In' and expression.get('key') in ('metadata.name', 'kubernetes.io/hostname'):
                    targets.update(expression.get('values', []))
        workload.update(release=release, model=endpoint_models.get((workload['namespace'], release)),
                        owners=[{'kind': owner['kind'], 'name': owner['name']} for owner in meta.get('ownerReferences', [])],
                        scheduling={'status': condition.get('status'), 'reason': condition.get('reason'), 'message': condition.get('message')},
                        targetNodes=sorted(targets), nodeSelector=copy.deepcopy(selector), requiredNodeAffinity=copy.deepcopy(affinity))
        name = pod['spec'].get('nodeName')
        if not name and amounts['nvidia.com/gpu'] > 0:
            pending.append(workload)
        if name:
            workloads.setdefault(name, []).append((workload, amounts))
    for node in sorted(snapshot['nodes'], key=lambda item: item['metadata']['name']):
        name = node['metadata']['name']
        entries = workloads.get(name, [])
        allocatable = node.get('status', {}).get('allocatable', {})
        row = {'name': name, 'uid': node['metadata'].get('uid'),
               'workloads': [entry[0] for entry in entries], 'blockers': []}
        for resource, key, suffix, scale in [('nvidia.com/gpu', 'gpu', '', 1), ('cpu', 'cpu', 'Millicores', 1000),
                                             ('memory', 'memory', 'Bytes', 1), ('ephemeral-storage', 'ephemeralStorage', 'Bytes', 1)]:
            used = sum(entry[1][resource] for entry in entries) * scale
            total = quantity(allocatable[resource]) * scale if resource in allocatable else None
            row[key] = {'allocatable' + suffix: number(total) if total is not None else None,
                        'allocated' + suffix: number(used),
                        'free' + suffix: number(total - used) if total is not None else None}
        conditions = {item['type']: item['status'] for item in node.get('status', {}).get('conditions', [])}
        if not row['uid'] or node['metadata'].get('deletionTimestamp'):
            row['blockers'].append('Node identity is missing or the node is terminating.')
        if conditions.get('Ready') != 'True':
            row['blockers'].append('Node is not Ready.')
        if any(conditions.get(condition) == 'True' for condition in ('MemoryPressure', 'DiskPressure', 'PIDPressure')):
            row['blockers'].append('Node reports resource pressure.')
        if node.get('spec', {}).get('unschedulable'):
            row['blockers'].append('Node is cordoned.')
        if any(taint.get('effect') in ('NoSchedule', 'NoExecute', 'PreferNoSchedule') for taint in node.get('spec', {}).get('taints', [])):
            row['blockers'].append('Node has a scheduling or eviction taint.')
        labels = node['metadata'].get('labels', {})
        if labels.get('nvidia.com/gpu.sharing-strategy', 'none') != 'none' or labels.get('nvidia.com/gpu.replicas', '1') != '1':
            row['blockers'].append('GPU sharing is enabled; the profiles require exclusive GPUs.')
        if any(key.startswith('nvidia.com/mig-') and quantity(value) > 0 for key, value in allocatable.items()):
            row['blockers'].append('MIG is enabled; the profiles require a full GPU.')
        rows.append(row)
    return rows, pending


def profile_requirements(profile):
    roles = []
    for entry in profile['perNode']:
        combined = copy.deepcopy(entry['resources'])
        for workload in entry.get('additionalWorkloads', []):
            for key in ('gpuRequest', 'cpuRequestMillicores', 'memoryRequestBytes', 'ephemeralStorageRequestBytes'):
                combined[key] = combined.get(key, 0) + workload['resources'].get(key, 0)
        storage = copy.deepcopy(entry.get('storage', {}))
        combined['ephemeralStorageRequestBytes'] = combined.get('ephemeralStorageRequestBytes', 0) + storage.get('offloadBytes', 0)
        roles.append({'rank': entry['rank'], 'role': entry['role'],
                      **{key: combined.get(key, 0) for key in ('gpuRequest', 'cpuRequestMillicores', 'memoryRequestBytes', 'ephemeralStorageRequestBytes')},
                      'storage': storage})
    return {'profile': profile['id'], 'id': profile['id'], 'modelNodeCount': profile['modelNodeCount'],
            'hardware': copy.deepcopy(profile['hardware']), 'perNode': roles,
            'fabric': copy.deepcopy(profile.get('fabric')), 'workload': copy.deepcopy(profile['workload']),
            'minimumAvailableMemoryBytesPerNode': profile.get('minimumAvailableMemoryBytesPerNode')}


def candidate_blockers(node, usage, profile, role, facts):
    reasons = list(usage['blockers'])
    hardware = profile['hardware']
    labels = node['metadata'].get('labels', {})
    for label, field in [('kubernetes.io/os', 'os'), ('kubernetes.io/arch', 'architecture')]:
        if labels.get(label) != hardware[field]:
            reasons.append('Node ' + field + ' does not match ' + hardware[field] + '.')
    if gpu_product(labels.get('nvidia.com/gpu.product', '')) not in {gpu_product(product) for product in hardware['gpuProducts']}:
        reasons.append('GPU product does not match this profile.')
    gpu_total = usage['gpu']['allocatable']
    if gpu_total is not None and gpu_total != hardware['gpuCount']:
        reasons.append('GPU count does not match the exclusive hardware profile.')
    minimum_host = hardware.get('minHostMemoryGiB')
    if minimum_host:
        total_host = node.get('status', {}).get('capacity', {}).get('memory')
        if total_host is not None:
            if quantity(total_host) < minimum_host * GIB:
                reasons.append('Host memory capacity is below the hardware profile minimum.')
        elif usage['memory']['allocatableBytes'] is None or usage['memory']['allocatableBytes'] < minimum_host * GIB:
            reasons.append('Host memory capacity meeting the hardware profile minimum is unverified.')
    if hardware.get('memoryMode') == 'discrete':
        measured, minimum = facts.get('cudaTotalMemoryGiB'), hardware.get('minDeviceMemoryGiB')
        if not positive_number(minimum) or not positive_number(measured) or measured < minimum:
            reasons.append('Verified device memory is missing or insufficient.')
    for field, free_key, required, label in [('gpu', 'free', role['gpuRequest'], 'GPU'),
                                           ('cpu', 'freeMillicores', role['cpuRequestMillicores'], 'CPU millicores'),
                                           ('memory', 'freeBytes', role['memoryRequestBytes'], 'memory bytes'),
                                           ('ephemeralStorage', 'freeBytes', role['ephemeralStorageRequestBytes'], 'ephemeral storage bytes')]:
        free = usage[field][free_key]
        if required and free is None:
            reasons.append('Allocatable ' + label + ' is unknown.')
        elif required and free < required:
            reasons.append(f'Insufficient {label}: {free} free, {required} required.')
    if role['storage'].get('offloadMedium') == 'local-nvme' and facts.get('localNvme') is not True:
        reasons.append('Local NVMe for Kubernetes ephemeral storage has not been verified.')
    if profile.get('fabric'):
        if not facts.get('fabric'):
            reasons.append('A verified fabric group is required.')
        if not positive_number(facts.get('gbps')) or facts['gbps'] < profile['fabric']['minimumGbps']:
            reasons.append('Verified fabric bandwidth is missing or below the profile minimum.')
        if not re.fullmatch(r'[a-zA-Z0-9_.:-]+', facts.get('interface', '')):
            reasons.append('A fabric network interface is required.')
        try:
            address = ipaddress.ip_address(facts.get('address', ''))
            if address.version != 4 or address.is_unspecified or address.is_loopback:
                raise ValueError('Invalid fabric address')
        except ValueError:
            reasons.append('A usable fabric IPv4 address is required.')
    return reasons


def analyze(snapshot, catalog, model_id, capabilities=None, profile_id=None, context_length=None, concurrency=None):
    """Report scheduling fit without allocating resources or contacting Kubernetes."""
    for field in ('nodes', 'pods'):
        if not isinstance(snapshot.get(field), list):
            raise ValueError('Inventory must contain ' + field + '.')
    recipe = next((entry for entry in catalog['recipes'] if entry['id'] == model_id), None)
    if recipe is None:
        raise ValueError('Unknown model: ' + model_id)
    for name, value in [('context_length', context_length), ('concurrency', concurrency)]:
        if value is not None and (type(value) is not int or value < 1):
            raise ValueError(name + ' must be a positive integer.')
    usage, pending = node_usage(snapshot)
    report = {'model': model_id, 'modelName': recipe.get('servedModelId'), 'displayName': recipe['name'],
              'status': 'blocked', 'chosenProfile': None, 'chosenNodes': [], 'requirements': [], 'profiles': [],
              'nodes': usage, 'pendingWorkloads': pending, 'blockers': [], 'limitations': list(LIMITATIONS)}
    if not recipe['availability']['deployable']:
        report.update(status='unsupported', blockers=[recipe['availability'].get('reason', 'No executable recipe is available.')])
        return report
    profiles = [profile for profile in recipe['profiles'] if profile_id is None or profile['id'] == profile_id]
    if not profiles:
        raise ValueError('Unknown profile for ' + model_id + ': ' + str(profile_id))
    facts = capabilities or {}
    if not isinstance(facts, dict):
        raise ValueError('Capabilities must map node names to capability facts.')
    facts = facts.get('nodes', facts)
    if not isinstance(facts, dict) or any(not isinstance(value, dict) for value in facts.values()):
        raise ValueError('Capabilities must map node names to capability facts.')
    names = [node['metadata']['name'] for node in snapshot['nodes']]
    uids = [node['metadata'].get('uid') for node in snapshot['nodes'] if node['metadata'].get('uid')]
    global_blockers = []
    if len(set(names)) != len(names) or len(set(uids)) != len(uids):
        global_blockers.append('Inventory contains duplicate node identities.')
    for workload in pending:
        global_blockers.append('Unscheduled GPU workload exists: ' + workload['namespace'] + '/' + workload['pod'] + '.')
    nodes = {node['metadata']['name']: node for node in snapshot['nodes']}
    for profile in profiles:
        required = profile_requirements(profile)
        report['requirements'].append(required)
        result = {'id': profile['id'], 'status': 'blocked', 'requirements': required,
                  'candidateNodes': [], 'chosenNodes': [], 'blockers': list(global_blockers),
                  'nodeBlockers': [], 'validation': copy.deepcopy(profile.get('validation', {}))}
        workload = profile['workload']
        ctx = context_length if context_length is not None else workload['defaultContextTokens']
        conc = concurrency if concurrency is not None else workload['defaultConcurrency']
        result['requestedWorkload'] = {'contextLength': ctx, 'concurrency': conc}
        if ctx > workload['maximumContextTokens'] or conc > workload['maximumConcurrency']:
            result['blockers'].append('Requested context or concurrency exceeds this profile envelope.')
        eligible = []
        for role in required['perNode']:
            candidates = []
            for row in usage:
                reasons = candidate_blockers(nodes[row['name']], row, profile, role, facts.get(row['name'], {}))
                if reasons:
                    result['nodeBlockers'].append({'node': row['name'], 'rank': role['rank'], 'reasons': reasons})
                else:
                    candidates.append(row['name'])
            eligible.append(candidates)
            result['candidateNodes'].append({'rank': role['rank'], 'nodes': candidates})
        selection = None
        for group in itertools.product(*eligible):
            if len(set(group)) != profile['modelNodeCount']:
                continue
            products = {gpu_product(nodes[name]['metadata'].get('labels', {}).get('nvidia.com/gpu.product', '')) for name in group}
            if len(products) != 1:
                continue
            if profile.get('fabric'):
                if len({facts[name]['fabric'] for name in group}) != 1 or len({facts[name]['address'] for name in group}) != len(group):
                    continue
            selection = list(group)
            break
        if selection is None:
            result['blockers'].append('No distinct compatible node group has the required free resources and capabilities.')
        if not result['blockers']:
            result.update(status='fits', chosenNodes=selection)
            if report['chosenProfile'] is None:
                report.update(status='fits', chosenProfile=profile['id'], chosenNodes=selection)
        report['profiles'].append(result)
    if report['status'] != 'fits':
        report['blockers'] = list(global_blockers)
        for profile in report['profiles']:
            report['blockers'].extend(profile['id'] + ': ' + reason for reason in profile['blockers'] if reason not in global_blockers)
    return report
