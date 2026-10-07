#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Install fresh independent SGLang releases and verify their shared gateway."""
import copy
import json
import pathlib
import re
import socket
import subprocess
import time
import uuid

import recipes
import verify

PHASE_TIMEOUTS = {'qualify': 660, 'download': 14520, 'serve': 7320}


class Deployment:
    def __init__(self, args):
        self.args = args
        self.work = pathlib.Path(args.work_dir).expanduser().resolve()
        recipes.require(not self.work.exists(), 'Use a new deployment work directory; existing progress is never overwritten.')
        recipes.require(not self.work.is_relative_to(recipes.HERE.parents[3]), 'Keep deployment artifacts outside the checkout.')
        self.stack = recipes.stack_api()
        self.connection = self.stack.load_connection(args.stack_connection)
        self.kc = ['kubectl', '--context', self.connection['context'], '-n', self.connection['namespace']]
        self.hm = ['helm', '--kube-context', self.connection['context'], '-n', self.connection['namespace']]
        self.sequence = 0
        self.previous = {}
        self.result = None
        self.reuse_caches = getattr(args, 'reuse_caches', False)
        self.caches = {}

    def save(self, name, value):
        path = self.work / name
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        with path.open('x') as stream:
            path.chmod(0o600)
            stream.write(value if isinstance(value, str) else json.dumps(value, indent=2) + '\n')
        return path

    def run(self, command, label, timeout=90):
        self.sequence += 1
        log = self.work / 'logs' / (f'{self.sequence:04d}-' + label + '.log')
        try:
            result = subprocess.run([str(value) for value in command], text=True, stdout=subprocess.PIPE,
                                    stderr=subprocess.STDOUT, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            output = error.stdout or ''
            self.save(log.relative_to(self.work), output.decode(errors='replace') if isinstance(output, bytes) else output)
            raise ValueError('Command timed out. Read ' + str(log)) from None
        self.save(log.relative_to(self.work), result.stdout)
        recipes.require(result.returncode == 0, 'Command failed. Read ' + str(log))
        return result.stdout

    def resources(self):
        return json.loads(self.run(self.kc + ['get', 'jobs,pods,deployments,pvc,configmaps,services,inferenceendpoints', '-o', 'json'], 'resources'))['items']

    def retained_caches(self, snapshot):
        namespace = self.connection['namespace']
        requested = {model.replace('.', '-'): model for model in self.args.model}
        claims = [item for item in self.resources() if item.get('kind') == 'PersistentVolumeClaim']
        records = {}
        for release, model in requested.items():
            selected = []
            for claim in claims:
                match = re.fullmatch(re.escape(release) + r'-cache-([0-9]+)', claim['metadata']['name'])
                if match:
                    recipes.require(claim['metadata']['name'] == release + '-cache-' + str(int(match[1])),
                                    'Retained cache rank name is ambiguous: ' + claim['metadata']['name'])
                    selected.append((int(match[1]), claim))
            selected.sort(key=lambda item: item[0])
            recipes.require(selected and [rank for rank, _ in selected] == list(range(len(selected))),
                            'Retained cache claims must cover every rank starting at zero: ' + release)
            for rank, claim in selected:
                meta, spec, status = claim['metadata'], claim.get('spec', {}), claim.get('status', {})
                name, owner = meta['name'], meta.get('annotations', {})
                recipes.require(meta.get('namespace') == namespace and meta.get('uid') and not meta.get('deletionTimestamp')
                                and owner.get('meta.helm.sh/release-name') == release
                                and owner.get('meta.helm.sh/release-namespace') == namespace
                                and owner.get('helm.sh/resource-policy') == 'keep'
                                and meta.get('labels', {}).get('app.kubernetes.io/managed-by') == 'Helm',
                                'Retained cache ownership differs: ' + name)
                recipes.require(status.get('phase') == 'Bound' and spec.get('volumeName')
                                and spec.get('storageClassName') == self.args.storage_class
                                and spec.get('accessModes') == ['ReadWriteOnce'] and spec.get('volumeMode', 'Filesystem') == 'Filesystem',
                                'Retained cache must be Bound with the requested storage class and filesystem access: ' + name)
                volume = json.loads(self.run(self.kc + ['get', 'pv', spec['volumeName'], '-o', 'json'], 'cache-volume-' + name))
                vm, vs = volume['metadata'], volume.get('spec', {})
                ref = vs.get('claimRef', {})
                recipes.require(vm.get('name') == spec['volumeName'] and vm.get('uid') and not vm.get('deletionTimestamp')
                                and volume.get('status', {}).get('phase') == 'Bound'
                                and ref.get('namespace') == namespace and ref.get('name') == name and ref.get('uid') == meta['uid']
                                and vs.get('storageClassName') == self.args.storage_class,
                                'Retained cache PV binding differs: ' + name)
                requested_size = recipes.quantity(spec.get('resources', {}).get('requests', {}).get('storage', 0))
                recipes.require(requested_size > 0 and recipes.quantity(status.get('capacity', {}).get('storage', 0)) >= requested_size
                                and recipes.quantity(vs.get('capacity', {}).get('storage', 0)) >= requested_size,
                                'Retained cache capacity is incomplete: ' + name)
                terms = vs.get('nodeAffinity', {}).get('required', {}).get('nodeSelectorTerms', [])
                recipes.require(len(terms) == 1 and not terms[0].get('matchFields'),
                                'Retained cache PV needs one unambiguous hostname affinity: ' + name)
                expressions = terms[0].get('matchExpressions', [])
                recipes.require(len(expressions) == 1 and expressions[0].get('key') == 'kubernetes.io/hostname'
                                and expressions[0].get('operator') == 'In' and len(expressions[0].get('values', [])) == 1,
                                'Retained cache PV needs one unambiguous hostname affinity: ' + name)
                hostname = expressions[0]['values'][0]
                nodes = [node for node in snapshot['nodes']
                         if node['metadata'].get('labels', {}).get('kubernetes.io/hostname') == hostname]
                recipes.require(len(nodes) == 1 and nodes[0]['metadata'].get('uid'),
                                'Retained cache PV does not identify one current node: ' + name)
                node = nodes[0]['metadata']
                recipes.require(not owner.get('volume.kubernetes.io/selected-node')
                                or owner['volume.kubernetes.io/selected-node'] == node['name'],
                                'Retained cache selected-node annotation differs from its PV: ' + name)
                records[name] = {'model': model, 'rank': rank, 'uid': meta['uid'], 'volume': vm['name'], 'volumeUID': vm['uid'],
                                 'node': node['name'], 'nodeUID': node['uid'], 'storageClass': self.args.storage_class,
                                 'requestedBytes': int(requested_size)}
        return records

    def verify_caches(self):
        if self.reuse_caches:
            current = self.retained_caches(recipes.inventory(self.connection['context']))
            recipes.require(current == self.caches, 'Retained cache claim, volume or node identity changed during deployment.')

    def require_fresh_releases(self):
        names = [release['name'] for release in self.result['releases']]
        pattern = '^(' + '|'.join(re.escape(name) for name in names) + ')$'
        found = json.loads(self.run(self.hm + ['list', '--deployed', '--failed', '--pending', '--superseded',
                                             '--uninstalled', '--uninstalling', '--filter', pattern, '-o', 'json'], 'release-check'))
        recipes.require(not found, 'A requested Helm release already exists. Existing releases are never adopted.')
        for item in self.resources():
            name = item['metadata']['name']
            if self.reuse_caches and item.get('kind') == 'PersistentVolumeClaim' and name in self.caches:
                recipes.require(item['metadata'].get('uid') == self.caches[name]['uid'], 'Retained cache identity changed: ' + name)
                continue
            owned = item['metadata'].get('annotations', {}).get('meta.helm.sh/release-name') in names
            conflicts = any(name in (release, release + '-runtime') or (self.reuse_caches and name.startswith(release + '-cache-')) or re.fullmatch(
                re.escape(release) + r'-(cache|qualify|download|serve)-[0-9]+' + (r'(-.+)?' if self.reuse_caches else ''), name) for release in names)
            recipes.require(not conflicts and not (self.reuse_caches and owned),
                            'Existing model resources or retained caches need explicit recovery: ' + name)

    def prepare(self):
        live = self.stack.inspect_connection(self.connection)
        args = self.args
        snapshot = recipes.inventory(self.connection['context'])
        capabilities = json.loads(pathlib.Path(args.capabilities).read_text()) if args.capabilities else {}
        requirements = json.loads(pathlib.Path(args.requirements).read_text()) if args.requirements else {}
        placements = None
        if self.reuse_caches:
            self.work.mkdir(parents=True, mode=0o700)
            self.caches = self.retained_caches(snapshot)
            placements = {model: [cache['node'] for cache in sorted(self.caches.values(), key=lambda item: item['rank'])
                                  if cache['model'] == model] for model in args.model}
        self.result = recipes.plan(snapshot, capabilities, args.model, self.connection['namespace'], args.storage_class,
                                   args.runtime_class, args.context_length, args.concurrency, not args.no_nvme_offload,
                                   args.preference, requirements, placements=placements)
        self.result['stackConnection'] = copy.deepcopy(self.connection)
        session = str(uuid.uuid4())
        for release in self.result['releases']:
            recipes.require(release['name'] not in (self.connection['stackRelease'], self.connection['operatorRelease']),
                            'Model and infrastructure release names must differ.')
            release['values']['deploymentSession'] = session
            if self.reuse_caches:
                release['values']['reuseCaches'] = True
                for rank, node in enumerate(release['values']['targets']):
                    cache = self.caches[release['name'] + '-cache-' + str(rank)]
                    recipes.require(cache['node'] == node['node'] and cache['nodeUID'] == node['uid']
                                    and cache['requestedBytes'] == release['values']['profile']['cacheGiB'] * recipes.GIB,
                                    'Retained cache placement or requested capacity differs from this profile: ' + release['name'])
        self.work.mkdir(parents=True, mode=0o700, exist_ok=self.reuse_caches)
        self.work.chmod(0o700)
        self.save('inventory.json', snapshot)
        self.save('plan.json', self.result)
        self.save('ca.crt', live['ca'])
        if self.reuse_caches:
            self.save('retained-caches.json', self.caches)
        self.require_fresh_releases()
        for release in self.result['releases']:
            print(release['model'] + ': ' + release['reason'] + ' Nodes: ' + ', '.join(release['nodes']), flush=True)
            values = self.save('render/' + release['name'] + '-values.json', release['values'])
            for phase in PHASE_TIMEOUTS:
                rendered = self.run(self.hm + ['template', release['name'], recipes.HERE / 'charts/sglang',
                                               '--values', values, '--set', 'phase=' + phase], 'render-' + phase)
                self.save('render/' + release['name'] + '-' + phase + '.yaml', rendered)
        print('Deployment evidence: ' + str(self.work), flush=True)

    def apply(self, phase):
        print(phase.capitalize() + ': checking capacity and applying model releases.', flush=True)
        self.verify_caches()
        # Check every placement before starting a batch: an unscheduled pod from the
        # same batch must not make its remaining placements look like new conflicts.
        for release in self.result['releases']:
            recipes.check_plan(self.result, release['name'])
        if phase == 'qualify':
            self.require_fresh_releases()
        for release in self.result['releases']:
            self.stack.inspect_connection(self.connection)
            name = release['name']
            if phase != 'qualify':
                current = json.loads(self.run(self.hm + ['get', 'values', name, '-o', 'json'], 'ownership-' + name))
                recipes.require(current == self.previous[name], 'Model Helm values changed during deployment: ' + name)
            command = 'install' if phase == 'qualify' else 'upgrade'
            self.run(self.hm + [command, name, recipes.HERE / 'charts/sglang', '--values',
                               self.work / 'render' / (name + '-values.json'), '--set', 'phase=' + phase,
                               '--timeout', '5m'], name + '-' + phase, timeout=330)
            self.previous[name] = dict(copy.deepcopy(release['values']), phase=phase)
            self.verify_caches()
        print(phase.capitalize() + ': waiting for ' + ', '.join(r['model'] for r in self.result['releases']), flush=True)

    def wait_jobs(self, phase):
        expected = {release['name'] + '-' + phase + '-' + str(rank): (release, rank)
                    for release in self.result['releases'] for rank in range(len(release['nodes']))}
        deadline = time.monotonic() + PHASE_TIMEOUTS[phase]
        next_progress = time.monotonic() + 60
        while time.monotonic() < deadline:
            resources = self.resources()
            jobs = {item['metadata']['name']: item for item in resources if item['kind'] == 'Job'}
            failed = [name for name in expected if any(c['type'] == 'Failed' and c['status'] == 'True'
                      for c in jobs.get(name, {}).get('status', {}).get('conditions', []))]
            if failed:
                self.save(phase + '-failed.json', resources)
                for name in failed:
                    self.run(self.kc + ['logs', 'job/' + name, '--all-containers=true'], name)
                raise ValueError('Job failed: ' + ', '.join(failed))
            if all(jobs.get(name, {}).get('status', {}).get('succeeded') == 1 for name in expected):
                for name, (release, rank) in expected.items():
                    output = self.run(self.kc + ['logs', 'job/' + name, '--all-containers=true'], name)
                    event = 'qualification_pass' if phase == 'qualify' else 'download_pass'
                    records = []
                    for line in output.splitlines():
                        try:
                            record = json.loads(line)
                        except ValueError:
                            continue
                        if isinstance(record, dict) and record.get('event') == event:
                            records.append(record)
                    if phase == 'qualify':
                        passed = any(r.get('rank') == rank and r.get('nodes') == len(release['nodes']) for r in records)
                    else:
                        passed = any(r.get('model') == release['model'] and r.get('revision') == release['values']['model']['revision']
                                     and (not self.reuse_caches or r.get('reused') is True) for r in records)
                    recipes.require(passed, 'Missing matching ' + event + ' marker in ' + name)
                self.save(phase + '-passed.json', resources)
                print(phase.capitalize() + ': passed for every selected model and rank.', flush=True)
                return
            if time.monotonic() >= next_progress:
                complete = sum(jobs.get(name, {}).get('status', {}).get('succeeded') == 1 for name in expected)
                print(f'{phase.capitalize()}: {complete}/{len(expected)} jobs complete. Logs: {self.work / "logs"}', flush=True)
                next_progress = time.monotonic() + 60
            time.sleep(5)
        raise ValueError(phase.capitalize() + ' jobs timed out. Read deployment logs.')

    def wait_serving(self):
        deadline = time.monotonic() + PHASE_TIMEOUTS['serve']
        next_progress = time.monotonic() + 60
        while time.monotonic() < deadline:
            resources = self.resources()
            endpoints = {item['metadata']['name']: item for item in resources if item['kind'] == 'InferenceEndpoint'}
            deployments = {item['metadata']['name']: item for item in resources if item['kind'] == 'Deployment'}
            ready = []
            for release in self.result['releases']:
                endpoint = endpoints.get(release['name'], {})
                conditions = {c['type']: c['status'] for c in endpoint.get('status', {}).get('conditions', [])}
                healthy = endpoint.get('spec', {}).get('modelName') == release['model'] and all(
                    conditions.get(c) == 'True' for c in ('Ready', 'TransportReady', 'Registered'))
                for rank in range(len(release['nodes'])):
                    deployment = deployments.get(release['name'] + '-serve-' + str(rank), {})
                    status = deployment.get('status', {})
                    healthy = healthy and status.get('observedGeneration', 0) >= deployment.get('metadata', {}).get('generation', 1) and status.get('readyReplicas', 0) == 1
                ready.append(healthy)
            if all(ready):
                self.save('serving-passed.json', resources)
                return
            if time.monotonic() >= next_progress:
                print(f'Serving: {sum(ready)}/{len(ready)} models ready. Logs: {self.work / "logs"}', flush=True)
                next_progress = time.monotonic() + 60
            time.sleep(5)
        raise ValueError('Models did not become Ready, TransportReady and Registered before timeout.')

    def verify_gateway(self):
        self.verify_caches()
        self.stack.inspect_connection(self.connection)
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', 0))
            port = probe.getsockname()[1]
        with self.stack.forward_connection(self.connection, self.work, port) as url:
            result = verify.verify(url, self.work / 'ca.crt', pathlib.Path(self.connection['apiKeyFile']).read_text().strip(),
                                   [release['model'] for release in self.result['releases']])
        recipes.require(result.get('passed'), 'Gateway verification did not pass.')
        self.save('verification.json', result)
        return result

    def execute(self):
        try:
            self.prepare()
            for phase in PHASE_TIMEOUTS:
                self.apply(phase)
                if phase == 'serve':
                    self.wait_serving()
                else:
                    self.wait_jobs(phase)
            result = self.verify_gateway()
            print('PASS: models are serving through one gateway; chat, streaming and authentication verified.', flush=True)
            print('Results: ' + str(self.work / 'verification.json'), flush=True)
            return result
        except (ValueError, OSError, KeyError, subprocess.SubprocessError) as error:
            if self.work.exists():
                self.save('failure.json', {'error': str(error), 'retained': 'Model releases, caches and logs remain for explicit recovery.'})
            detail = 'Resources and caches were retained. Evidence: ' + str(self.work) if self.work.exists() else 'No deployment artifacts were created.'
            raise ValueError(str(error) + '\nDeployment stopped. ' + detail) from error


def deploy(args):
    return Deployment(args).execute()
