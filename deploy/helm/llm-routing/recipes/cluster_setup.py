# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Discover fresh recipe installation settings without changing the cluster."""
import datetime
import json
import pathlib
import re
import secrets
import subprocess
import time

import sizing

HERE = pathlib.Path(__file__).resolve().parent
CONTROL_LABELS = ('node-role.kubernetes.io/control-plane', 'node-role.kubernetes.io/master')
PROBE_COMMAND = ("nvidia-smi --query-gpu=name,compute_cap,memory.total --format=csv,noheader,nounits"
                 " && grep '^MemTotal:' /proc/meminfo")
# nvidia-smi reports no dedicated GPU memory when the GPU shares system memory, as on GB10.
UNREPORTED_MEMORY = {'[N/A]', 'N/A', '[Not Supported]', 'Not Supported'}


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


def parse_probe(text):
    """Turn GPU probe output into the configuration's gpu section."""
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    meminfo = [line for line in lines if line.startswith('MemTotal:')]
    gpus = [line for line in lines if not line.startswith('MemTotal:')]
    require(len(meminfo) == 1, 'The GPU probe did not report host memory.')
    require(len(gpus) == 1, 'Expected one GPU per model node. The probe reported ' + str(len(gpus)) + '.')
    fields = [field.strip() for field in gpus[0].split(',')]
    require(len(fields) == 3, 'Unexpected nvidia-smi output: ' + gpus[0])
    name, capability, memory = fields
    unified = memory in UNREPORTED_MEMORY
    if unified:
        memory_gib = int(meminfo[0].split()[1]) / 1024**2
    else:
        require(memory.isdigit(), 'Unexpected GPU memory value from nvidia-smi: ' + memory)
        memory_gib = int(memory) / 1024
    gpu = {'name': name, 'computeCapability': capability, 'memoryGiB': round(memory_gib, 1),
           'unifiedMemory': unified, 'cudaArchitectures': None}
    try:
        sizing.check_gpu(gpu)
    except ValueError as error:
        raise ClusterSetupError('GPU probe: ' + str(error)) from None
    return gpu


def kubectl(context, *args, stdin=None, timeout=60):
    try:
        return subprocess.run(['kubectl', '--context', context, *args], input=stdin, text=True, check=True,
                              capture_output=True, timeout=timeout).stdout
    except (OSError, subprocess.SubprocessError) as error:
        detail = getattr(error, 'stderr', '') or str(error)
        raise ClusterSetupError('kubectl ' + ' '.join(args[:2]) + ' failed: ' + detail.strip()[-500:]) from None


def probe_gpus(context, names, image, runtime_class):
    """Run one short GPU pod per node in a temporary namespace, then delete the namespace."""
    namespace = 'llm-routing-probe-' + secrets.token_hex(3)
    kubectl(context, 'create', 'namespace', namespace)
    try:
        pods = {}
        for index, name in enumerate(names):
            pod = 'gpu-probe-' + str(index)
            manifest = {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': pod, 'namespace': namespace},
                        'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False,
                                 'runtimeClassName': runtime_class, 'nodeSelector': {'kubernetes.io/hostname': name},
                                 'securityContext': {'runAsUser': 1000, 'runAsGroup': 1000, 'runAsNonRoot': True,
                                                     'seccompProfile': {'type': 'RuntimeDefault'}},
                                 'containers': [{'name': 'probe', 'image': image, 'command': ['sh', '-c', PROBE_COMMAND],
                                                 'env': [{'name': 'NVIDIA_DRIVER_CAPABILITIES', 'value': 'compute,utility'}],
                                                 'resources': {'requests': {'cpu': '100m', 'memory': '128Mi', 'nvidia.com/gpu': 1},
                                                               'limits': {'cpu': '1', 'memory': '512Mi', 'nvidia.com/gpu': 1}},
                                                 'securityContext': {'allowPrivilegeEscalation': False, 'readOnlyRootFilesystem': True,
                                                                     'capabilities': {'drop': ['ALL']}}}]}}
            kubectl(context, 'create', '-f', '-', stdin=json.dumps(manifest))
            pods[name] = pod
        results = {}
        for name, pod in pods.items():
            # The first pull of the runtime image can take several minutes.
            kubectl(context, '-n', namespace, 'wait', 'pod/' + pod, '--for=jsonpath={.status.phase}=Succeeded',
                    '--timeout=20m', timeout=1260)
            results[name] = parse_probe(kubectl(context, '-n', namespace, 'logs', pod))
        return results
    finally:
        try:
            kubectl(context, 'delete', 'namespace', namespace, '--wait=true', '--timeout=3m', timeout=200)
        except ClusterSetupError:
            print('Warning: delete the temporary GPU probe namespace manually:', namespace)


def discover_config(context, namespace=None, recipe=None, probe=None):
    """Return fresh settings using eligible nodes, a GPU probe and the cluster defaults.

    probe(names) returns {node: gpu}; tests replace it to avoid touching a cluster.
    """
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
    require(idle, 'No idle Ready ARM64 GPU node found. Free a GPU or set placement explicitly in the configuration.')
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
    runtime_class = selected_runtime[0]['metadata']['name']
    # Prefer GPU nodes outside the control plane so the control plane can host routing.
    ordered = [node['metadata']['name'] for node in sorted(idle, key=lambda n: (control_plane(n), n['metadata']['name']))]
    probe = probe or (lambda names: probe_gpus(context, names, config['runtimeImage'], runtime_class))
    detected = probe(ordered[:max(sizing.MODEL_NODE_COUNTS)])
    gpu = detected[ordered[0]]
    try:
        count = sizing.model_node_count(recipe, gpu)
    except ValueError as error:
        raise ClusterSetupError(str(error)) from None
    require(len(ordered) >= count, recipe['name'] + ' needs ' + str(count) + ' idle ' + gpu['name'] +
            ' nodes. Found ' + str(len(ordered)) + '. Free GPUs or set placement explicitly in the configuration.')
    model = ordered[:count]
    require(all((detected[name]['name'], detected[name]['computeCapability']) == (gpu['name'], gpu['computeCapability'])
                for name in model),
            'Mixed GPU types are not supported: ' + ', '.join(n + '=' + detected[n]['name'] for n in model) +
            '. Set nodes.model explicitly to matching nodes.')
    others = sorted((node for node in candidates if node['metadata']['name'] not in model),
                    key=lambda n: (not control_plane(n), n['metadata']['name']))
    # With no spare node, routing shares the leader; it needs no GPU.
    control = others[0]['metadata']['name'] if others else model[0]
    config.update(context=context, namespace=namespace, clusterId=namespace, recipe=recipe['name'],
                  nodes={'control': control, 'model': model}, gpu=gpu,
                  storageClass=selected_storage[0]['metadata']['name'], runtimeClass=runtime_class)
    config['images'].update(prefix='localhost/' + namespace,
                            tag='dev-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d%H%M%S') + '-' + secrets.token_hex(3),
                            pullPolicy='Never', pullSecrets=[])
    config['containerd'] = {'socketPath': '/run/k3s/containerd/containerd.sock', 'archiveNode': control,
                           'runAsUser': 1000, 'nodeNames': sorted(node['metadata']['name'] for node in imports)}
    return config


def validate_reinitialization(config, backend):
    """Inspect an uninstalled demo before allowing local progress to be reset.

    backend is the model Helm release name chosen by the recipe tool.
    """
    context, namespace = config['context'], config['namespace']
    prefix = config['releasePrefix']
    releases = config.get('releases', {})
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
    model = config['nodes']['model']
    for name in sorted({config['nodes']['control'], *model}):
        require(name in nodes and eligible(nodes[name]), 'Saved node is unavailable: ' + name)
        require(nodes[name]['metadata']['labels'].get('kubernetes.io/hostname') == name,
                'Saved node hostname no longer matches placement: ' + name)
        if name in model:
            require(int(nodes[name]['status'].get('allocatable', {}).get('nvidia.com/gpu', 0)) >= 1,
                    'Saved model node has no advertised GPU: ' + name)
    pods = items(context, 'pods', all_namespaces=True)
    require(not any(requests_gpu(pod) and pod.get('spec', {}).get('nodeName') in model for pod in pods),
            'A saved model GPU is occupied.')
    if not namespaces:
        require(not crds, 'Retained CRD without the saved namespace requires ownership review.')
        return
    require(not items(context, 'deployments,statefulsets,daemonsets,jobs,cronjobs,pods,services,ingresses', namespace=namespace),
            'Workloads still exist in the saved namespace. Finish uninstalling before init.')
    claims = items(context, 'persistentvolumeclaims', namespace=namespace)
    volumes = {item['metadata']['name']: item for item in items(context, 'persistentvolumes')} if claims else {}
    # claim name -> (owning release, node it must stay on)
    expected = {backend + '-artifacts': (backend, model[0]),
                prefix + '-monitoring-metrics': (prefix + '-monitoring', config['nodes']['control'])}
    expected.update({backend + '-rpc-cache-n' + str(index): (backend, name) for index, name in enumerate(model) if index})
    for claim in claims:
        name = claim['metadata']['name']
        require(name in expected, 'Unexpected retained PVC: ' + name)
        release, node = expected[name]
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
        require(not selected or selected == node, 'Retained PVC placement changed: ' + name)
        terms = volume.get('spec', {}).get('nodeAffinity', {}).get('required', {}).get('nodeSelectorTerms')
        if terms is not None:
            # Fail closed for affinity shapes that this K3s recipe cannot validate.
            require(any(not term.get('matchFields') and term.get('matchExpressions') and
                        all(expr.get('key') == 'kubernetes.io/hostname' and expr.get('operator') == 'In'
                            and node in expr.get('values', [])
                            for expr in term['matchExpressions']) for term in terms),
                    'Retained PV affinity does not match saved placement: ' + name)
    secrets_by_name = {operator + '-cluster-credential': operator, config['caConfigMap']: stack}
    for secret in items(context, 'secrets', namespace=namespace):
        name = secret['metadata']['name']
        require(name in secrets_by_name, 'Unexpected retained Secret requires review: ' + name)
        owned(secret, secrets_by_name[name])
    require(crds or claims, 'Existing namespace has no retained demo ownership evidence. Use a new namespace.')
