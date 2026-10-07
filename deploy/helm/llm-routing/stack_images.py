# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Build and preload application images for a shared routing stack."""
import copy
import importlib.util
import json
import os
import pathlib
import re
import subprocess
import uuid

HERE = pathlib.Path(__file__).resolve().parent
REPO = HERE.parents[2]
spec = importlib.util.spec_from_file_location('routing_image_tools', HERE / 'recipes/image_tools.py')
image_tools = importlib.util.module_from_spec(spec)
spec.loader.exec_module(image_tools)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def save(path, value):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(value if isinstance(value, str) else json.dumps(value, indent=2) + '\n')


class Preparation:
    def __init__(self, config, work, allow):
        require(allow, 'Building and preloading images requires explicit containerd-import authorization.')
        self.config = copy.deepcopy(config)
        self.context = config.get('context')
        require(isinstance(self.context, str) and self.context and not self.context.startswith('-'), 'Set an explicit Kubernetes context.')
        self.platform = config.get('imagePlatform')
        require(self.platform in image_tools.PLATFORMS, 'Set imagePlatform to linux/arm64 or linux/amd64.')
        self.containerd = config.get('containerd') or {}
        socket = self.containerd.get('socketPath')
        require(isinstance(socket, str) and socket.startswith('/') and '..' not in pathlib.PurePath(socket).parts,
                'Configure containerd.socketPath for the selected cluster before preloading images.')
        self.nodes = self.containerd.get('nodeNames')
        uids = self.containerd.get('nodeUIDs')
        require(isinstance(self.nodes, list) and self.nodes and len(set(self.nodes)) == len(self.nodes)
                and all(isinstance(name, str) and re.fullmatch(r'[a-z0-9][a-z0-9.-]*', name) for name in self.nodes),
                'Configure distinct containerd.nodeNames.')
        require(isinstance(uids, dict) and set(uids) == set(self.nodes) and all(isinstance(uid, str) and uid for uid in uids.values()),
                'Bind every image preload node in containerd.nodeUIDs.')
        require(config.get('controlNode') in self.nodes and self.containerd.get('archiveNode', config['controlNode']) in self.nodes,
                'Control and archive nodes must be included in the image preload set.')
        user = self.containerd.get('runAsUser', 1000)
        require(type(user) is int and user > 0, 'containerd.runAsUser must be a positive user ID.')
        self.images = {}
        for component in image_tools.COMPONENTS:
            image = config.get('images', {}).get(component, {})
            repository, tag = image.get('repository'), image.get('tag')
            require(isinstance(repository, str) and '/' in repository and not repository.startswith('-') and not any(c.isspace() for c in repository),
                    'Set an image repository for ' + component)
            require(isinstance(tag, str) and re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', tag) and tag != 'latest',
                    'Set a pinned image tag for ' + component)
            require(image.get('pullPolicy', 'IfNotPresent') in ('Never', 'IfNotPresent'),
                    'Local image preload requires Never or IfNotPresent pull policy: ' + component)
            self.images[component] = repository + ':' + tag
        self.work = pathlib.Path(work).expanduser().resolve() / 'image-preparation'
        require(not self.work.is_relative_to(REPO), 'Keep image archives and evidence outside the checkout.')
        require(not self.work.is_symlink(), 'Image preparation directory must not be a symlink.')
        self.work.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.work.chmod(0o700)
        self.state_path = self.work / 'state.json'
        identity = {'context': self.context, 'stackNamespace': config['namespace'], 'platform': self.platform,
                    'images': self.images, 'containerd': self.containerd, 'controlNode': config['controlNode']}
        if self.state_path.exists():
            self.state = json.loads(self.state_path.read_text())
            require(self.state.get('identity') == identity, 'Image preparation configuration changed. Use its original configuration or a new work directory.')
        else:
            require(not any(self.work.iterdir()), 'Image preparation directory has unrelated files. Use a new work directory.')
            token = uuid.uuid4().hex
            self.state = {'schemaVersion': 1, 'identity': identity, 'session': token, 'namespace': 'llm-images-' + token[:16]}
            save(self.state_path, self.state)
        self.namespace = self.state['namespace']
        require(self.state.get('schemaVersion') == 1 and re.fullmatch('[a-f0-9]{32}', self.state.get('session', ''))
                and self.namespace == 'llm-images-' + self.state['session'][:16], 'Invalid image preparation checkpoint.')
        require(self.namespace != config['namespace'], 'Image preparation must use a separate namespace.')
        self.kc = ['kubectl', '--context', self.context, '-n', self.namespace]
        self.hm = ['helm', '--kube-context', self.context, '-n', self.namespace]
        self.log_path = self.work / 'commands.log'
        if not self.log_path.exists():
            save(self.log_path, '')

    def run(self, command, **kwargs):
        if command[:3] == ['docker', 'buildx', 'build']:
            print('Building ' + str(command[command.index('--tag') + 1]), flush=True)
        with self.log_path.open('a') as log:
            kwargs.setdefault('stdout', log)
            kwargs.setdefault('stderr', log)
            kwargs.setdefault('timeout', 3600)
            return subprocess.run([str(arg) for arg in command], check=True, **kwargs)

    def output(self, command, **kwargs):
        with self.log_path.open('a') as log:
            kwargs.setdefault('stderr', log)
            kwargs.setdefault('timeout', 120)
            return subprocess.check_output([str(arg) for arg in command], text=True, **kwargs)

    def check_nodes(self):
        nodes = json.loads(self.output(['kubectl', '--context', self.context, 'get', 'nodes', '-o', 'json']))['items']
        architecture = self.platform.split('/')[1]
        eligible = set()
        indexed = {node['metadata']['name']: node for node in nodes}
        for node in nodes:
            labels = node['metadata'].get('labels', {})
            conditions = {c['type']: c['status'] for c in node.get('status', {}).get('conditions', [])}
            schedulable = not node['metadata'].get('deletionTimestamp') and not node.get('spec', {}).get('unschedulable') and not any(
                taint.get('effect') in ('NoSchedule', 'NoExecute') for taint in node.get('spec', {}).get('taints', []))
            if labels.get('kubernetes.io/os') == 'linux' and labels.get('kubernetes.io/arch') == architecture and conditions.get('Ready') == 'True' and schedulable and not any(
                    conditions.get(condition) == 'True' for condition in ('MemoryPressure', 'DiskPressure', 'PIDPressure')):
                eligible.add(node['metadata']['name'])
        require(eligible <= set(self.nodes), 'New compatible nodes are missing from the bound image preload set. Run setup again.')
        for name in self.nodes:
            node = indexed.get(name)
            require(node and node['metadata']['uid'] == self.containerd['nodeUIDs'][name], 'Image preload node was replaced: ' + name)
            require(name in eligible, 'Image preload node is unschedulable, pressured, not Ready or does not match ' + self.platform + ': ' + name)
            require(node.get('status', {}).get('nodeInfo', {}).get('containerRuntimeVersion', '').startswith('containerd://'),
                    'Image preload requires containerd on node: ' + name)

    def namespace_info(self):
        value = self.output(['kubectl', '--context', self.context, 'get', 'namespace', self.namespace, '--ignore-not-found', '-o', 'json'])
        return json.loads(value) if value.strip() else None

    def check_namespace(self):
        namespace = self.namespace_info()
        require(namespace and namespace['metadata']['uid'] == self.state.get('namespaceUID') and not namespace['metadata'].get('deletionTimestamp'),
                'Image preparation namespace changed. Existing namespaces are not adopted.')

    def owned_release(self, release, chart):
        entries = json.loads(self.output(self.hm + ['list', '--deployed', '--failed', '--pending', '--uninstalled', '--superseded',
                                                   '--uninstalling', '--filter', '^' + re.escape(release) + '$', '-o', 'json']))
        require(len(entries) <= 1 and all(item['name'] == release and item['chart'].startswith(chart + '-') for item in entries),
                'Image preparation release belongs to another chart: ' + release)
        if entries:
            values = json.loads(self.output(self.hm + ['get', 'values', release, '-o', 'json']))
            require(values.get('preparationSession') == self.state['session'], 'Image preparation release ownership changed: ' + release)
        return bool(entries)

    def bootstrap(self):
        namespace = self.namespace_info()
        if namespace:
            if not self.state.get('namespaceUID'):
                require(self.owned_release('image-preparation', 'llm-image-preparation'), 'Image preparation namespace already exists without this attempt ownership.')
                self.state['namespaceUID'] = namespace['metadata']['uid']
                save(self.state_path, self.state)
            self.check_namespace()
        else:
            require(not self.state.get('namespaceUID'), 'Image preparation namespace was removed. Use a new work directory.')
        existing = self.owned_release('image-preparation', 'llm-image-preparation') if namespace else False
        values = self.work / 'bootstrap-values.json'
        save(values, {'preparationSession': self.state['session']})
        self.run(self.hm + [('upgrade' if existing else 'install'), 'image-preparation', HERE / 'charts/image-preparation',
                           '--create-namespace', '-f', values, '--wait', '--timeout', '2m'])
        namespace = self.namespace_info()
        require(namespace and not namespace['metadata'].get('deletionTimestamp'), 'Image preparation namespace did not become available.')
        if self.state.get('namespaceUID'):
            require(namespace['metadata']['uid'] == self.state['namespaceUID'], 'Image preparation namespace was replaced during bootstrap.')
        self.state['namespaceUID'] = namespace['metadata']['uid']
        save(self.state_path, self.state)
        self.check_namespace()

    def helm_apply(self, release, chart, values, wait=False):
        require(release == 'image-loader' and pathlib.Path(chart).resolve() == (HERE / 'recipes/charts/image-loader').resolve(),
                'Image preparation may apply only its loader chart.')
        self.check_nodes()
        self.check_namespace()
        exists = self.owned_release(release, 'pylon-image-loader')
        values = dict(values, preparationSession=self.state['session'])
        for field in ('clientImage', 'serverImage'):
            if field in self.containerd:
                values[field] = self.containerd[field]
        path = self.work / 'loader-values.json'
        save(path, values)
        command = self.hm + [('upgrade' if exists else 'install'), release, chart, '-f', path, '--timeout', '5m']
        if wait:
            command.append('--wait')
        self.run(command)

    def execute(self):
        try:
            self.check_nodes()
            if self.state.get('namespaceUID'):
                self.check_namespace()
            revision = self.output(['git', 'rev-parse', 'HEAD'], cwd=REPO).strip()
            if self.output(['git', 'status', '--porcelain'], cwd=REPO).strip():
                revision += '-dirty'
            image_tools.build_images(REPO, self.images, run=self.run, platform=self.platform, source_revision=revision)
            archive = image_tools.export_images(list(self.images.values()), self.work / 'images.tar', run=self.run)
            self.check_nodes()
            self.bootstrap()
            print('Preloading routing images on ' + ', '.join(self.nodes) + '. Logs: ' + str(self.log_path), flush=True)
            image_tools.import_images(archive, list(self.images.values()), context=self.context, namespace=self.namespace,
                release='image-loader', work=self.work, containerd=self.containerd, node_names=self.nodes,
                control_node=self.config['controlNode'], helm_apply=self.helm_apply, run=self.run, output=self.output,
                save=save, platform=self.platform, archive_name='images.tar')
            self.check_nodes()
            for release, chart in [('image-loader', 'pylon-image-loader'), ('image-preparation', 'llm-image-preparation')]:
                self.check_namespace()
                if self.owned_release(release, chart):
                    self.run(self.hm + ['uninstall', release, '--wait', '--timeout', '3m'])
            self.state['completed'] = True
            self.state['sourceRevision'] = revision
            self.state['retainedNamespace'] = 'Empty preparation namespace retained with its saved UID for guarded retries.'
            save(self.state_path, self.state)
            print('Shared routing images are built and preloaded. Evidence: ' + str(self.work), flush=True)
            return copy.deepcopy(self.state)
        except (RuntimeError, ValueError, OSError, subprocess.SubprocessError) as error:
            self.state['completed'] = False
            self.state['lastError'] = str(error)
            save(self.state_path, self.state)
            message = 'Command failed; details are in the log.' if isinstance(error, subprocess.SubprocessError) else str(error)
            raise ValueError('Image preparation stopped. Retry with the same configuration and work directory. Logs: ' + str(self.log_path) + '\n' + message) from error


def prepare(config, work, allow_containerd_import=True):
    return Preparation(config, work, allow_containerd_import).execute()
