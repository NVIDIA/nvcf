# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Discover retained recipe caches and measure already mounted model storage."""
from concurrent.futures import ThreadPoolExecutor
import json
import re
import subprocess


def collect(context, snapshot, run):
    """Read storage metadata and run du only inside existing running containers."""
    warnings = []

    def resources(kind):
        command = ['kubectl', '--context', context, 'get', kind, '-o', 'json']
        if kind == 'persistentvolumeclaims':
            command += ['--all-namespaces']
        try:
            return json.loads(run(command, timeout=15))['items']
        except (RuntimeError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
            warnings.append(f'{kind}: {error}')
            return []

    claims = resources('persistentvolumeclaims')
    volumes = {p['metadata']['name']: p for p in resources('persistentvolumes')} if claims else {}
    mounts, model_claims = {}, set()
    for pod in snapshot.get('pods', []):
        meta, spec = pod['metadata'], pod['spec']
        namespace = meta.get('namespace', 'default')
        gpu = any(float(c.get('resources', {}).get('requests', {}).get('nvidia.com/gpu',
                        c.get('resources', {}).get('limits', {}).get('nvidia.com/gpu', 0))) > 0
                  for c in spec.get('containers', []) + spec.get('initContainers', []))
        pvc_volumes = {v['name']: (namespace, v['persistentVolumeClaim']['claimName'])
                       for v in spec.get('volumes', []) if v.get('persistentVolumeClaim')}
        if gpu:
            model_claims.update(pvc_volumes.values())
        if pod.get('status', {}).get('phase') != 'Running' or meta.get('deletionTimestamp'):
            continue
        running = {c['name'] for c in pod.get('status', {}).get('containerStatuses', [])
                   if 'running' in c.get('state', {})}
        for container in spec.get('containers', []):
            if container['name'] not in running:
                continue
            for mount in container.get('volumeMounts', []):
                key = pvc_volumes.get(mount['name'])
                # A partial mount cannot establish the size of the entire cache.
                if key and not mount.get('subPath') and not mount.get('subPathExpr') and mount['mountPath'].startswith('/'):
                    mounts.setdefault(key, []).append((meta['name'], container['name'], mount['mountPath'], spec.get('nodeName')))

    rows = []
    for pvc in claims:
        meta = pvc['metadata']
        namespace, name = meta.get('namespace', 'default'), meta['name']
        annotations = meta.get('annotations', {})
        release = annotations.get('meta.helm.sh/release-name', '')
        recipe_claim = release and (name == release + '-artifacts' or
                                    re.fullmatch(re.escape(release) + r'-rpc-cache(?:-n\d+)?', name) or
                                    re.fullmatch(re.escape(release) + r'-cache-\d+', name))
        if not recipe_claim and (namespace, name) not in model_claims:
            continue
        volume_name = pvc.get('spec', {}).get('volumeName')
        volume = volumes.get(volume_name, {}).get('spec', {})
        nodes = set()
        for term in volume.get('nodeAffinity', {}).get('required', {}).get('nodeSelectorTerms', []):
            for expression in term.get('matchExpressions', []) + term.get('matchFields', []):
                if expression.get('key') in ('kubernetes.io/hostname', 'metadata.name') and expression.get('operator') == 'In':
                    nodes.update(expression.get('values', []))
        if annotations.get('volume.kubernetes.io/selected-node'):
            nodes.add(annotations['volume.kubernetes.io/selected-node'])
        for mount in mounts.get((namespace, name), []):
            if mount[3]:
                nodes.add(mount[3])
        rows.append({'namespace': namespace, 'claim': name, 'nodes': sorted(nodes),
                     'volume': volume_name, 'bytesUsed': None,
                     'usageStatus': 'not-mounted' if volume_name else 'not-provisioned'})

    def measure(row):
        for pod, container, path, _ in mounts.get((row['namespace'], row['claim']), []):
            command = ['kubectl', '--context', context, '--namespace', row['namespace'],
                       'exec', pod, '--container', container, '--', 'du', '-sk', path]
            try:
                output = run(command, timeout=10)
                lines = output.strip().splitlines()
                match = re.fullmatch(r'(\d+)\s+.+', lines[0]) if len(lines) == 1 else None
                if not match:
                    raise ValueError('du did not return one complete cache size')
                row.update(bytesUsed=int(match[1]) * 1024, usageStatus='measured')
                row.pop('measurementError', None)
                break
            except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as error:
                row.update(usageStatus='unavailable', measurementError=str(error))
        return row

    with ThreadPoolExecutor(max_workers=4) as executor:
        measured = list(executor.map(measure, rows))
    return {'caches': sorted(measured, key=lambda row: (row['namespace'], row['claim'])), 'warnings': warnings}
