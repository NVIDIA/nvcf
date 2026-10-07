# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Read-only placement checks for an exclusive GPU and its model cache."""
import json
import os
import pathlib
import re
import ssl
import sys
import urllib.parse
import urllib.request

GIB = 1024 ** 3


def log(event, **fields):
    print(json.dumps({'event': event, **fields}), flush=True)


def storage_bytes(value):
    match = re.fullmatch(r'([0-9]+)(Ki|Mi|Gi|Ti)?', str(value))
    if not match:
        raise RuntimeError('Cache capacity must use a whole binary storage quantity')
    return int(match[1]) * {'': 1, 'Ki': 1024, 'Mi': 1024**2, 'Gi': GIB, 'Ti': 1024**4}[match[2] or '']


def validate_placement(config, node, claim):
    target, hardware = config['targets'][0]['node'], config['profile']['hardware']
    metadata, spec, status = node.get('metadata', {}), node.get('spec', {}), node.get('status', {})
    labels = metadata.get('labels', {})
    conditions = {item['type']: item['status'] for item in status.get('conditions', [])}
    if metadata.get('name') != target or not metadata.get('uid') or metadata.get('deletionTimestamp') or spec.get('unschedulable'):
        raise RuntimeError('The selected node is missing, terminating or unschedulable')
    if conditions.get('Ready') != 'True' or any(conditions.get(name) == 'True' for name in ('MemoryPressure', 'DiskPressure', 'PIDPressure')):
        raise RuntimeError('The selected node is not Ready or reports resource pressure')
    if any(taint.get('effect') in ('NoSchedule', 'NoExecute') for taint in spec.get('taints', [])):
        raise RuntimeError('The selected node has a scheduling or eviction taint')
    if labels.get('kubernetes.io/os') != hardware['os'] or labels.get('kubernetes.io/arch') != hardware['architecture']:
        raise RuntimeError('The selected node does not match the profile OS and architecture')
    product = labels.get('nvidia.com/gpu.product', '').replace(' ', '-')
    if product not in [name.replace(' ', '-') for name in hardware['gpuProducts']]:
        raise RuntimeError('The selected node GPU product does not match the profile')
    if labels.get('nvidia.com/gpu.sharing-strategy', 'none') != 'none' or labels.get('nvidia.com/gpu.replicas', '1') != '1':
        raise RuntimeError('Automatic recipes require an exclusive GPU without time-sharing or MPS')
    if (labels.get('nvidia.com/mig.capable') != 'false' and labels.get('nvidia.com/mig.strategy', 'none') != 'none') or any(
            name.startswith('nvidia.com/mig-') for name in status.get('allocatable', {})):
        raise RuntimeError('Automatic recipes require a full GPU with MIG disabled')
    if any(str(status.get(field, {}).get('nvidia.com/gpu')) != str(hardware['gpuCount']) for field in ('capacity', 'allocatable')):
        raise RuntimeError('The node GPU capacity differs from the exclusive hardware profile')
    metadata, spec = claim.get('metadata', {}), claim.get('spec', {})
    if (metadata.get('name') != config['claimName'] or metadata.get('namespace') != config['namespace'] or
            not metadata.get('uid') or metadata.get('deletionTimestamp')):
        raise RuntimeError('The model cache claim identity is missing or terminating')
    if claim.get('status', {}).get('phase') != 'Bound' or not spec.get('volumeName'):
        raise RuntimeError('The model cache claim is not Bound')
    if spec.get('storageClassName') != config['storageClassName'] or spec.get('accessModes') != ['ReadWriteOnce'] or spec.get('volumeMode', 'Filesystem') != 'Filesystem':
        raise RuntimeError('The cache claim must use the selected storage class and ReadWriteOnce filesystem access')
    selected = metadata.get('annotations', {}).get('volume.kubernetes.io/selected-node')
    if selected and selected != target:
        raise RuntimeError('The retained cache claim belongs to another node')
    minimum = config['profile']['cacheGiB'] * GIB
    if storage_bytes(claim.get('status', {}).get('capacity', {}).get('storage', 0)) < minimum:
        raise RuntimeError('The cache claim capacity is smaller than the pinned profile')
    return {'node': target, 'nodeUID': node['metadata']['uid'], 'claim': metadata['name'], 'claimUID': metadata['uid']}


def cluster_object(path, credentials=pathlib.Path('/var/run/recipe-cluster')):
    host = os.environ.get('KUBERNETES_SERVICE_HOST')
    if not host:
        raise RuntimeError('Kubernetes API service address is unavailable')
    if ':' in host:
        host = '[' + host + ']'
    context = ssl.create_default_context(cafile=str(credentials / 'ca.crt'))
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
    request = urllib.request.Request('https://' + host + ':' + os.environ.get('KUBERNETES_SERVICE_PORT_HTTPS', '443') + path,
                                     headers={'Authorization': 'Bearer ' + (credentials / 'token').read_text().strip()})
    with opener.open(request, timeout=30) as response:
        return json.load(response)


def preflight(config):
    target = config['targets'][0]['node']
    if os.environ.get('POD_NODE_NAME') != target:
        raise RuntimeError('The pod was placed on a different node')
    ssl.create_default_context(cafile='/shared-ca/ca.crt')
    node = cluster_object('/api/v1/nodes/' + urllib.parse.quote(target, safe=''))
    claim = cluster_object('/api/v1/namespaces/' + urllib.parse.quote(config['namespace'], safe='') +
                           '/persistentvolumeclaims/' + urllib.parse.quote(config['claimName'], safe=''))
    log('placement_pass', **validate_placement(config, node, claim))
    return 0


if __name__ == '__main__':
    try:
        config = json.loads(pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else '/recipe/config.json').read_text())
        sys.exit(preflight(config))
    except Exception as error:
        log('placement_failed', reason=str(error))
        sys.exit(1)
