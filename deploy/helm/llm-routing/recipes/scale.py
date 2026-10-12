# SPDX-License-Identifier: Apache-2.0
"""Start and stop an installed model by scaling its deployments, with a GPU and memory check before a start.

Every function here reads snapshots of the cluster and returns plain data, so a caller such as the command line or
a service can run the same check and show the same messages. Nothing in this module changes the cluster.
"""
import model_capacity as capacity

INSTANCE = 'app.kubernetes.io/instance'
GIB = 1024 ** 3


def deployment_node(deployment):
    """The one node a model deployment is pinned to, or None when it can float."""
    spec = deployment['spec']['template']['spec']
    hostname = spec.get('nodeSelector', {}).get('kubernetes.io/hostname')
    if hostname:
        return hostname
    affinity = spec.get('affinity', {}).get('nodeAffinity', {}).get('requiredDuringSchedulingIgnoredDuringExecution', {})
    names = set()
    for term in affinity.get('nodeSelectorTerms', []):
        for expression in term.get('matchFields', []) + term.get('matchExpressions', []):
            if expression.get('operator') == 'In' and expression.get('key') in ('metadata.name', 'kubernetes.io/hostname'):
                names.update(expression.get('values', []))
    return next(iter(names)) if len(names) == 1 else None


def release_deployments(deployments, release, namespace):
    """The model deployments a Helm release owns, excluding the small transport pods the operator adds."""
    owned = []
    for deployment in deployments:
        meta = deployment['metadata']
        selector = deployment['spec'].get('selector', {}).get('matchLabels', {})
        if meta.get('namespace', namespace) != namespace or selector.get(INSTANCE) != release or meta['name'].startswith('pylon-'):
            continue
        spec = deployment['spec']['template']['spec']
        owned.append({'deployment': meta['name'], 'replicas': deployment['spec'].get('replicas', 1),
                      'node': deployment_node(deployment),
                      'gpu': int(capacity.requests({'spec': spec}, 'nvidia.com/gpu')),
                      'memoryBytes': int(capacity.requests({'spec': spec}, 'memory')),
                      'cpuMillicores': int(capacity.requests({'spec': spec}, 'cpu') * 1000)})
    return sorted(owned, key=lambda item: item['deployment'])


def endpoint_release(endpoint):
    meta = endpoint['metadata']
    return meta.get('annotations', {}).get('meta.helm.sh/release-name') or meta['name']


def resolve_release(endpoints, namespace, model=None, release=None):
    """Name the Helm release for a served model ID. A release name is used as given."""
    if release:
        return release
    found = sorted({endpoint_release(endpoint) for endpoint in endpoints
                    if endpoint['metadata'].get('namespace') == namespace and endpoint.get('spec', {}).get('modelName') == model})
    if not found:
        raise ValueError('No installed release serves ' + str(model) + ' in namespace ' + namespace
                         + '. Install it first, or pass --release with its Helm release name.')
    if len(found) > 1:
        raise ValueError(str(model) + ' is served by several releases (' + ', '.join(found) + '). Pass --release to choose one.')
    return found[0]


def release_model(endpoints, namespace, release):
    """The served model ID of a release, when it has an endpoint."""
    for endpoint in endpoints:
        if endpoint['metadata'].get('namespace') == namespace and endpoint_release(endpoint) == release:
            return endpoint.get('spec', {}).get('modelName')
    return None


def gib(value):
    return str(round(value / GIB, 1)) + ' GiB'


def model_releases(deployments, namespace):
    """Releases in the namespace that own a GPU model deployment. Only these can be stopped to free a node."""
    found = set()
    for deployment in deployments:
        meta = deployment['metadata']
        instance = deployment['spec'].get('selector', {}).get('matchLabels', {}).get(INSTANCE)
        spec = deployment['spec']['template']['spec']
        if (instance and meta.get('namespace', namespace) == namespace and not meta['name'].startswith('pylon-')
                and capacity.requests({'spec': spec}, 'nvidia.com/gpu')):
            found.add(instance)
    return found


def holders(row, release, stoppable, namespace):
    """Group the other model workloads on a node by the release that owns them. System and monitoring pods are left out."""
    grouped = {}
    for workload in row['workloads']:
        if workload.get('release') == release or workload.get('release') not in stoppable or workload.get('namespace') != namespace:
            continue
        key = (workload.get('namespace'), workload.get('release') or workload['pod'])
        entry = grouped.setdefault(key, {'release': workload.get('release'), 'model': workload.get('model'), 'namespace': workload.get('namespace'),
                                         'pods': [], 'gpu': 0, 'memoryBytes': 0})
        entry['pods'].append(workload['pod'])
        entry['gpu'] += workload['gpu']
        entry['memoryBytes'] += workload['memoryBytes']
    return sorted(grouped.values(), key=lambda item: (-item['gpu'], -item['memoryBytes'], item['release'] or ''))


def plan_start(snapshot, deployments, release, namespace):
    """Check that every stopped deployment of a release can be scheduled on its node, before anything scales."""
    owned = release_deployments(deployments, release, namespace)
    report = {'release': release, 'namespace': namespace, 'status': 'ready', 'toStart': [], 'running': [], 'blockers': [], 'holders': []}
    if not owned:
        raise ValueError('Release ' + release + ' has no model deployments in namespace ' + namespace + '.')
    report['running'] = [item['deployment'] for item in owned if item['replicas'] > 0]
    report['toStart'] = [item for item in owned if item['replicas'] == 0]
    if not report['toStart']:
        report['status'] = 'running'
        return report
    rows = {row['name']: row for row in capacity.node_usage(snapshot)[0]}
    stoppable = model_releases(deployments, namespace)
    demand = {}
    for item in report['toStart']:
        if item['node'] is None:
            report['blockers'].append('Deployment ' + item['deployment'] + ' is not pinned to one node, so its placement cannot be checked.')
            continue
        total = demand.setdefault(item['node'], {'gpu': 0, 'memoryBytes': 0, 'cpuMillicores': 0, 'deployments': []})
        for key in ('gpu', 'memoryBytes', 'cpuMillicores'):
            total[key] += item[key]
        total['deployments'].append(item['deployment'])
    for node, need in sorted(demand.items()):
        row = rows.get(node)
        if row is None:
            report['blockers'].append('Node ' + node + ' is not in the cluster.')
            continue
        for problem in row['blockers']:
            report['blockers'].append('Node ' + node + ': ' + problem)
        shortages = []
        if need['gpu'] > row['gpu']['free']:
            shortages.append('GPU: needs ' + str(need['gpu']) + ', ' + str(row['gpu']['free']) + ' free')
        if need['memoryBytes'] > row['memory']['freeBytes']:
            shortages.append('memory: needs ' + gib(need['memoryBytes']) + ', ' + gib(row['memory']['freeBytes']) + ' free')
        if need['cpuMillicores'] > row['cpu']['freeMillicores']:
            shortages.append('CPU: needs ' + str(need['cpuMillicores']) + 'm, ' + str(row['cpu']['freeMillicores']) + 'm free')
        if shortages:
            who = holders(row, release, stoppable, namespace)
            report['holders'].extend({**entry, 'node': node} for entry in who)
            held = ', '.join((entry['model'] or entry['release'] or entry['pods'][0]) for entry in who if entry['gpu'] or entry['memoryBytes'])
            report['blockers'].append('Node ' + node + ' cannot fit ' + ', '.join(need['deployments']) + ' (' + '; '.join(shortages) + ')'
                                      + ('. In use by ' + held + '.' if held else '.'))
    if report['blockers']:
        report['status'] = 'blocked'
    return report


def plan_stop(deployments, release, namespace):
    owned = release_deployments(deployments, release, namespace)
    if not owned:
        raise ValueError('Release ' + release + ' has no model deployments in namespace ' + namespace + '.')
    running = [item for item in owned if item['replicas'] > 0]
    return {'release': release, 'namespace': namespace, 'status': 'running' if running else 'stopped',
            'toStop': running, 'stopped': [item['deployment'] for item in owned if item['replicas'] == 0]}


def start_commands(report):
    return [['scale', 'deployment/' + item['deployment'], '--replicas=1'] for item in report['toStart']]


def stop_commands(report):
    return [['scale', 'deployment/' + item['deployment'], '--replicas=0'] for item in report['toStop']]


def blocker_stop_hint(report, program='llm.py'):
    """The commands that free a blocked start, one per distinct holder."""
    seen, hints = set(), []
    for entry in report['holders']:
        if not (entry['gpu'] or entry['memoryBytes']):
            continue
        # A release name is never ambiguous, so it is preferred over the served model ID.
        target = ('--release ' + entry['release']) if entry.get('release') else ('--model ' + str(entry['model']))
        if target not in seen:
            seen.add(target)
            hints.append(program + ' stop ' + target)
    return hints
