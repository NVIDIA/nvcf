# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Discover fresh Spark installation settings without changing the cluster."""
import datetime
import json
import pathlib
import re
import secrets
import subprocess

HERE = pathlib.Path(__file__).resolve().parent
CONTROL_LABELS = ('node-role.kubernetes.io/control-plane', 'node-role.kubernetes.io/master')


class ClusterSetupError(RuntimeError):
    pass


def require(condition, message):
    if not condition:
        raise ClusterSetupError(message)


def items(context, resource, all_namespaces=False, namespace=None):
    command = ['kubectl', '--context', context, 'get', resource]
    if all_namespaces:
        command.append('--all-namespaces')
    if namespace:
        command += ['-n', namespace]
    try:
        result = json.loads(subprocess.check_output(command + ['-o', 'json'], text=True,
                                                   stderr=subprocess.PIPE, timeout=30))
    except (OSError, subprocess.SubprocessError, ValueError):
        raise ClusterSetupError('Could not inspect ' + resource + '. Check Kubernetes access and permissions.') from None
    require(isinstance(result, dict) and isinstance(result.get('items'), list),
            'Kubernetes returned an invalid list for ' + resource + '.')
    return result['items']


def eligible(node):
    labels = node.get('metadata', {}).get('labels', {})
    conditions = {item['type']: item['status'] for item in node.get('status', {}).get('conditions', [])}
    return (labels.get('kubernetes.io/arch') == 'arm64'
            and labels.get('kubernetes.io/os') == 'linux'
            and conditions.get('Ready') == 'True'
            and not any(conditions.get(name) == 'True' for name in ('MemoryPressure', 'DiskPressure', 'PIDPressure'))
            and not node.get('metadata', {}).get('deletionTimestamp')
            and not node.get('spec', {}).get('unschedulable')
            and not any(taint.get('effect') in ('NoSchedule', 'NoExecute')
                        for taint in node.get('spec', {}).get('taints', [])))


def control_plane(node):
    return any(label in node.get('metadata', {}).get('labels', {}) for label in CONTROL_LABELS)


def requests_gpu(pod):
    if pod.get('status', {}).get('phase') in ('Succeeded', 'Failed'):
        return False
    spec = pod.get('spec', {})
    for container in spec.get('containers', []) + spec.get('initContainers', []):
        resources = container.get('resources', {})
        value = resources.get('requests', {}).get('nvidia.com/gpu', resources.get('limits', {}).get('nvidia.com/gpu', 0))
        if int(value) > 0:
            return True
    return bool(spec.get('resourceClaims'))


def discover_config(context, namespace=None):
    """Return fresh settings using eligible nodes and the cluster defaults."""
    require(isinstance(context, str) and bool(context.strip()), 'Select a Kubernetes context first.')
    config = json.loads((HERE/'config.example.json').read_text())
    namespace = namespace or config['namespace']
    require(isinstance(namespace, str) and len(namespace) <= 63
            and re.fullmatch(r'[a-z0-9]([-a-z0-9]*[a-z0-9])?', namespace), 'Invalid namespace.')
    namespaces = items(context, 'namespaces')
    require(not any(item['metadata']['name'] == namespace for item in namespaces),
            'Namespace ' + namespace + ' already exists. Use attach-existing or choose an unused --namespace.')
    crds = items(context, 'crds')
    require(not any(item['metadata']['name'] == 'inferenceendpoints.pylon.nvidia.com' for item in crds),
            'A Pylon InferenceEndpoint CRD already exists. Check its owner before a new operator installation.')
    nodes = items(context, 'nodes')
    candidates = [node for node in nodes if eligible(node)]
    pods = items(context, 'pods', all_namespaces=True)
    busy = {pod.get('spec', {}).get('nodeName') for pod in pods if requests_gpu(pod)}
    idle = [node for node in candidates
            if int(node.get('status', {}).get('allocatable', {}).get('nvidia.com/gpu', 0)) >= 1
            and node['metadata']['name'] not in busy]
    controls = [node for node in candidates if control_plane(node)]
    workers = [node for node in idle if not control_plane(node)]
    if len(controls) != 1 or len(workers) < 2:
        workers = idle
        selected = {node['metadata']['name'] for node in sorted(workers, key=lambda item: item['metadata']['name'])[:2]}
        controls = [node for node in candidates if node['metadata']['name'] not in selected]
    workers = sorted(workers, key=lambda item: item['metadata']['name'])[:2]
    controls = sorted(controls, key=lambda item: item['metadata']['name'])
    require(len(workers) == 2 and controls,
            'Need two idle GPU nodes and one separate Ready ARM64 routing node. Free the required GPUs or set placement nodes explicitly in the configuration.')
    control = controls[0]['metadata']['name']
    worker_names = sorted(node['metadata']['name'] for node in workers)
    imports = [node for node in nodes if node.get('metadata', {}).get('labels', {}).get('kubernetes.io/arch') == 'arm64']
    require(all(node['metadata'].get('labels', {}).get('kubernetes.io/hostname') == node['metadata']['name'] for node in imports),
            'Node hostname labels differ from node names. Configure placement and image import targets explicitly.')
    require(all(re.search(r'[+-]k3s\d*', node.get('status', {}).get('nodeInfo', {}).get('kubeletVersion', ''))
                and node.get('status', {}).get('nodeInfo', {}).get('containerRuntimeVersion', '').startswith('containerd://')
                for node in imports),
            'Automatic image import uses the K3s containerd socket. Set containerd.socketPath explicitly for this cluster.')
    storage = items(context, 'storageclasses')
    defaults = [item for item in storage if any(item.get('metadata', {}).get('annotations', {}).get(key) == 'true'
                for key in ('storageclass.kubernetes.io/is-default-class', 'storageclass.beta.kubernetes.io/is-default-class'))]
    selected_storage = defaults if defaults else storage
    require(len(selected_storage) == 1,
            'Select one default StorageClass or set storageClass explicitly in the configuration.')
    runtimes = items(context, 'runtimeclasses')
    nvidia = [item for item in runtimes if item.get('handler') == 'nvidia']
    named = [item for item in nvidia if item['metadata']['name'] == 'nvidia']
    selected_runtime = named if named else nvidia
    require(len(selected_runtime) == 1,
            'Expected an NVIDIA RuntimeClass with handler nvidia. Set runtimeClass explicitly for this cluster.')
    config.update(context=context, namespace=namespace, clusterId=namespace,
                  nodes={'control': control, 'leader': worker_names[0], 'worker': worker_names[1]},
                  storageClass=selected_storage[0]['metadata']['name'], runtimeClass=selected_runtime[0]['metadata']['name'])
    config['images'].update(prefix='localhost/' + namespace,
                            tag='dev-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d%H%M%S') + '-' + secrets.token_hex(3),
                            pullPolicy='Never', pullSecrets=[])
    config['containerd'] = {'socketPath': '/run/k3s/containerd/containerd.sock', 'archiveNode': control,
                           'runAsUser': 1000, 'nodeNames': sorted(node['metadata']['name'] for node in imports)}
    return config


def validate_reinitialization(config):
    """Inspect an uninstalled demo before allowing local progress to be reset."""
    context, namespace = config['context'], config['namespace']
    prefix = config['releasePrefix']
    releases = config.get('releases', {})
    glm = releases.get('glm', prefix + '-glm')
    operator = releases.get('operator', prefix + '-operator')
    stack = releases.get('stack', prefix + '-stack')
    try:
        records = json.loads(subprocess.check_output(
            ['helm', '--kube-context', context, '-n', namespace, 'list', '--deployed', '--failed',
             '--pending', '--uninstalling', '--superseded', '--uninstalled', '-o', 'json'],
            text=True, stderr=subprocess.PIPE, timeout=30))
    except (OSError, subprocess.SubprocessError, ValueError):
        raise ClusterSetupError('Could not inspect Helm releases. No progress was reset.') from None
    require(isinstance(records, list) and not records,
            'Helm releases still exist in this namespace. Finish uninstalling before init. No progress was reset.')

    def owned(resource, release):
        metadata = resource.get('metadata', {})
        annotations = metadata.get('annotations', {})
        require(not metadata.get('deletionTimestamp')
                and annotations.get('meta.helm.sh/release-name') == release
                and annotations.get('meta.helm.sh/release-namespace') == namespace
                and annotations.get('helm.sh/resource-policy') == 'keep',
                'Retained resource has another owner or is not retained: ' + metadata.get('name', 'unknown'))

    crds = [item for item in items(context, 'crds')
            if item['metadata']['name'] == 'inferenceendpoints.pylon.nvidia.com']
    for crd in crds:
        owned(crd, operator)
        spec = crd['spec']
        require(spec.get('group') == 'pylon.nvidia.com' and spec.get('scope') == 'Namespaced'
                and spec.get('names', {}).get('kind') == 'InferenceEndpoint'
                and [(v['name'], v['served'], v['storage']) for v in spec.get('versions', [])]
                == [('v1alpha1', True, True)]
                and set(crd.get('status', {}).get('storedVersions', [])) <= {'v1alpha1'},
                'Retained Pylon CRD has an incompatible API version.')
        require(not items(context, 'inferenceendpoints.pylon.nvidia.com', all_namespaces=True),
                'InferenceEndpoints still exist. Finish uninstalling before init.')
    namespaces = [item for item in items(context, 'namespaces') if item['metadata']['name'] == namespace]
    require(not any(item['metadata'].get('deletionTimestamp') for item in namespaces),
            'The saved namespace is terminating.')
    nodes = {item['metadata']['name']: item for item in items(context, 'nodes')}
    for role, name in config['nodes'].items():
        require(name in nodes and eligible(nodes[name]), 'Saved node is unavailable: ' + name)
        require(nodes[name]['metadata']['labels'].get('kubernetes.io/hostname') == name,
                'Saved node hostname no longer matches placement: ' + name)
        if role in ('leader', 'worker'):
            require(int(nodes[name]['status'].get('allocatable', {}).get('nvidia.com/gpu', 0)) >= 1,
                    'Saved model node has no advertised GPU: ' + name)
    pods = items(context, 'pods', all_namespaces=True)
    require(not any(requests_gpu(pod) and pod.get('spec', {}).get('nodeName') in
                    (config['nodes']['leader'], config['nodes']['worker']) for pod in pods),
            'A saved model GPU is occupied.')
    if not namespaces:
        require(not crds, 'Retained CRD without the saved namespace requires ownership review.')
        return
    require(not items(context, 'deployments,statefulsets,daemonsets,jobs,cronjobs,pods,services,ingresses', namespace=namespace),
            'Workloads still exist in the saved namespace. Finish uninstalling before init.')
    claims = items(context, 'persistentvolumeclaims', namespace=namespace)
    volumes = {item['metadata']['name']: item for item in items(context, 'persistentvolumes')} if claims else {}
    expected = {glm + '-artifacts': (glm, 'leader'), glm + '-rpc-cache': (glm, 'worker'),
                prefix + '-monitoring-metrics': (prefix + '-monitoring', 'control')}
    for claim in claims:
        name = claim['metadata']['name']
        require(name in expected, 'Unexpected retained PVC: ' + name)
        release, role = expected[name]
        owned(claim, release)
        spec = claim['spec']
        require(claim.get('status', {}).get('phase') == 'Bound'
                and spec.get('storageClassName') == config['storageClass']
                and spec.get('accessModes') == ['ReadWriteOnce']
                and spec.get('volumeMode', 'Filesystem') == 'Filesystem',
                'Retained PVC is not compatible with saved storage: ' + name)
        volume = volumes.get(spec.get('volumeName'), {})
        ref = volume.get('spec', {}).get('claimRef', {})
        require(not volume.get('metadata', {}).get('deletionTimestamp')
                and ref.get('uid') == claim['metadata']['uid']
                and ref.get('name') == name and ref.get('namespace') == namespace,
                'Retained PVC binding changed: ' + name)
        selected = claim['metadata'].get('annotations', {}).get('volume.kubernetes.io/selected-node')
        require(not selected or selected == config['nodes'][role], 'Retained PVC placement changed: ' + name)
        terms = volume.get('spec', {}).get('nodeAffinity', {}).get('required', {}).get('nodeSelectorTerms')
        if terms is not None:
            # Fail closed for affinity shapes that this K3s recipe cannot validate.
            require(any(not term.get('matchFields') and term.get('matchExpressions') and
                        all(expr.get('key') == 'kubernetes.io/hostname' and expr.get('operator') == 'In'
                            and config['nodes'][role] in expr.get('values', [])
                            for expr in term['matchExpressions']) for term in terms),
                    'Retained PV affinity does not match saved placement: ' + name)
    secrets_by_name = {operator + '-cluster-credential': operator, config['caConfigMap']: stack}
    for secret in items(context, 'secrets', namespace=namespace):
        name = secret['metadata']['name']
        require(name in secrets_by_name, 'Unexpected retained Secret requires review: ' + name)
        owned(secret, secrets_by_name[name])
    require(crds or claims, 'Existing namespace has no retained demo ownership evidence. Use a new namespace.')
