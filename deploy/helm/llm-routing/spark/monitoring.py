# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Metrics-only monitoring release and read-only verification for the Spark recipe."""
import base64
import binascii
import contextlib
import copy
import ipaddress
import json
import pathlib
import re
import secrets
import socket
import subprocess
import tarfile
import time
import urllib.parse
import urllib.request

from client import Client
import gateway_access

HERE = pathlib.Path(__file__).resolve().parent
CHART = HERE / 'charts/monitoring'
MAX_ARCHIVE_BYTES = 2 * 1024**3


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def settings(config):
    options = config.get('monitoring', {})
    require(isinstance(options, dict), 'monitoring must be an object.')
    require(isinstance(options.get('enabled', False), bool), 'monitoring.enabled must be boolean.')
    allowed = {'enabled', 'images', 'imagePullPolicy', 'retentionPeriod', 'storageSize', 'namespaces', 'extraTargets', 'networkPolicy', 'model'}
    require(not set(options) - allowed, 'Unknown monitoring setting: ' + ', '.join(sorted(set(options) - allowed)))
    if 'model' in options:
        require(isinstance(options['model'], str) and bool(options['model'].strip()), 'monitoring.model must be a nonempty model ID.')
    return options


def enabled(config):
    return settings(config).get('enabled', False)


def chart_values(recipe):
    options = settings(recipe.c)
    values = json.loads((CHART / 'values.yaml').read_text())
    values.update(enabled=enabled(recipe.c), nodeSelector={'kubernetes.io/hostname': recipe.c['nodes']['control']},
                  imagePullPolicy=options.get('imagePullPolicy', values['imagePullPolicy']))
    require(values['imagePullPolicy'] in ('Never', 'IfNotPresent', 'Always'), 'Invalid monitoring imagePullPolicy.')
    namespaces = options.get('namespaces', [recipe.c['namespace']])
    require(isinstance(namespaces, list) and namespaces and all(isinstance(n, str) and re.fullmatch(r'[a-z0-9]([-a-z0-9]*[a-z0-9])?', n) for n in namespaces),
            'monitoring.namespaces must list explicit Kubernetes namespace names.')
    require(recipe.c['namespace'] in namespaces, 'monitoring.namespaces must include the installation namespace.')
    values['namespaces'] = sorted(set(namespaces))
    policy = options.get('networkPolicy', {})
    require(isinstance(policy, dict) and set(policy) <= {'enabled', 'apiServerCIDRs'}, 'Invalid monitoring networkPolicy.')
    values['networkPolicy'].update(policy)
    policy = values['networkPolicy']
    require(isinstance(policy['enabled'], bool), 'networkPolicy.enabled must be boolean.')
    require(isinstance(policy['apiServerCIDRs'], list), 'networkPolicy.apiServerCIDRs must be a list.')
    for cidr in policy['apiServerCIDRs']:
        require(isinstance(cidr, str) and '/' in cidr, 'Use explicit API server host CIDRs.')
        try:
            network = ipaddress.ip_network(cidr)
        except ValueError:
            raise RuntimeError('Invalid API server host CIDR.') from None
        require(network.prefixlen == network.max_prefixlen, 'Use /32 or /128 API server host CIDRs.')
    require(not policy['enabled'] or policy['apiServerCIDRs'], 'Restricted monitoring needs API server host CIDRs.')
    images = options.get('images', {})
    require(isinstance(images, dict) and not set(images) - {'collector', 'victoriaMetrics', 'grafana'}, 'Unknown monitoring image component.')
    for component, image in images.items():
        require(isinstance(image, str) and re.fullmatch(r'[^\s]+(?::[A-Za-z0-9_.-]+|@sha256:[a-f0-9]{64})', image) and not image.endswith(':latest'),
                'Monitoring images must have an explicit version tag or digest.')
        values[component]['image'] = image
    values['victoriaMetrics']['retentionPeriod'] = options.get('retentionPeriod', '3d')
    require(re.fullmatch(r'[1-9][0-9]*[dhmy]', values['victoriaMetrics']['retentionPeriod']) is not None, 'Use an explicit retention duration such as 3d.')
    storage = values['victoriaMetrics']['storage']
    storage.update(storageClass=recipe.c['storageClass'], size=options.get('storageSize', '5Gi'))
    require(re.fullmatch(r'[1-9][0-9]*(Mi|Gi|Ti)', storage['size']) is not None, 'Use a storageSize such as 5Gi.')
    def target(name, selector, port_name, port=0):
        return {'name': name, 'selector': selector, 'portName': port_name, 'port': port}
    values['targets'] = [
        target('gateway', 'app.kubernetes.io/name=llm-api-gateway,app.kubernetes.io/instance='+recipe.stack, 'http', 9464),
        target('router', 'app.kubernetes.io/name=llm-request-router,app.kubernetes.io/instance='+recipe.stack, 'metrics'),
        target('operator', 'app.kubernetes.io/instance='+recipe.operator, 'metrics'),
        target('pylon', 'app.kubernetes.io/name=pylon,app.kubernetes.io/managed-by=pylon-operator', 'metrics')]
    extra = options.get('extraTargets', [])
    require(isinstance(extra, list), 'monitoring.extraTargets must be a list.')
    names = {t['name'] for t in values['targets']} | {'monitoring-storage', 'monitoring-grafana', 'monitoring-collector'}
    for t in extra:
        require(isinstance(t, dict) and set(t) <= {'name', 'selector', 'portName', 'port', 'path', 'runtime'}, 'Invalid extra monitoring target.')
        require(isinstance(t.get('name'), str) and re.fullmatch(r'[a-z][a-z0-9-]*', t['name']) and t['name'] not in names, 'Monitoring target names must be unique.')
        require(isinstance(t.get('selector'), str) and t['selector'].strip() and isinstance(t.get('portName'), str) and t['portName'], 'Monitoring targets need selector and portName.')
        require(type(t.get('port', 0)) is int and 0 <= t.get('port', 0) <= 65535, 'Invalid monitoring target port.')
        require(isinstance(t.get('path', '/metrics'), str) and t.get('path', '/metrics').startswith('/'), 'Invalid metrics path.')
        require('runtime' not in t or t['runtime'] == 'llama.cpp', 'Supported backend dashboard runtime: llama.cpp. Omit runtime for raw exporter metrics.')
        names.add(t['name'])
    values['targets'].extend(copy.deepcopy(extra))
    values['grafana']['adminSecret'] = recipe.c['releasePrefix']+'-monitoring-grafana-admin'
    return values


def image_list(recipe):
    values = chart_values(recipe)
    return [values[k]['image'] for k in ('collector', 'victoriaMetrics', 'grafana')]


def gateway_response(client, path, payload=None):
    """Read a bounded authenticated response over the client's verified TLS connection."""
    require(client.context and client.key, 'Traffic verification requires verified HTTPS and a caller key.')
    connection = client.connect()
    connection.timeout = 180
    deadline = time.monotonic() + 180
    try:
        headers = {'Authorization': 'Bearer ' + client.key}
        if payload is not None:
            headers['Content-Type'] = 'application/json'
        connection.request('POST' if payload is not None else 'GET', path,
                           json.dumps(payload) if payload is not None else None, headers)
        response = connection.getresponse()
        require(response.status == 200, 'Gateway monitoring request failed with HTTP ' + str(response.status) + ': ' + path)
        body = bytearray()
        while True:
            remaining = deadline - time.monotonic()
            require(remaining > 0, 'Gateway monitoring response exceeded 180 seconds.')
            if connection.sock:
                connection.sock.settimeout(remaining)
            chunk = response.read1(min(65536, 524289-len(body)))
            if not chunk:
                break
            body.extend(chunk)
            require(len(body) <= 524288, 'Gateway monitoring response exceeded 512 KiB.')
        return bytes(body)
    finally:
        connection.close()


def select_model(client, requested=None):
    require(requested is None or (isinstance(requested, str) and bool(requested.strip())), 'Provide a nonempty model ID.')
    listing = json.loads(gateway_response(client, '/v1/models'))
    require(isinstance(listing, dict) and listing.get('object') == 'list' and isinstance(listing.get('data'), list),
            'Gateway model discovery returned an invalid model list.')
    entries = listing['data']
    require(entries and all(isinstance(item, dict) and isinstance(item.get('id'), str) and item['id'].strip() for item in entries),
            'Gateway model discovery returned no models or an invalid model ID.')
    models = sorted({item['id'] for item in entries})
    require(requested is None or requested in models, 'Requested monitoring model is absent from gateway discovery: ' + str(requested))
    return requested if requested is not None else models[0]


def sample_completion(client, model, stream):
    payload = {'model': model, 'messages': [{'role': 'user', 'content': 'What is 2 plus 2? Answer briefly.'}],
               'max_tokens': 512, 'stream': stream}
    if stream:
        payload['stream_options'] = {'include_usage': True}
    body = gateway_response(client, '/v1/chat/completions', payload)
    if stream:
        events = []
        done = False
        for line in body.decode().splitlines():
            if not line.startswith('data:'):
                continue
            data = line[5:].strip()
            if data == '[DONE]':
                done = True
                break
            events.append(json.loads(data))
        require(done and events, 'Monitoring streaming request did not finish with an SSE completion marker.')
    else:
        events = [json.loads(body)]
    require(all(isinstance(event, dict) and not event.get('error') for event in events), 'Monitoring completion returned an error.')
    require(all(isinstance(event.get('choices', []), list) for event in events), 'Monitoring completion returned invalid choices.')
    choices = [choice for event in events for choice in event.get('choices', [])]
    require(choices and all(isinstance(choice, dict) for choice in choices), 'Monitoring completion returned no valid choices.')
    require(any(choice.get('finish_reason') in ('stop', 'length') for choice in choices), 'Monitoring completion has no successful finish reason.')
    output = [choice.get('delta' if stream else 'message', {}) for choice in choices]
    require(any(isinstance(item, dict) and any(isinstance(item.get(key), str) and item[key].strip()
                for key in ('content', 'reasoning_content', 'reasoning')) for item in output),
            'Monitoring completion returned no content or reasoning.')
    usage = next((event['usage'] for event in reversed(events) if event.get('usage')), None)
    require(isinstance(usage, dict) and all(type(usage.get(key)) is int and usage[key] > 0 for key in ('prompt_tokens', 'completion_tokens')),
            'Monitoring completion did not report positive prompt and completion token usage.')
    return {'model': model, 'stream': stream, 'status': 200,
            'promptTokens': usage['prompt_tokens'], 'completionTokens': usage['completion_tokens']}


class Monitoring:
    def __init__(self, recipe, run, output, save):
        self.recipe = recipe
        self.run, self.output, self.save = run, output, save
        self.release = recipe.c['releasePrefix']+'-monitoring'

    def install(self):
        r = self.recipe
        require(enabled(r.c), 'Set monitoring.enabled=true in the saved configuration.')
        r.bound_cluster()
        values = chart_values(r)
        existing = json.loads(self.output(r.hm+['list', '--deployed', '--failed', '--pending', '--uninstalled', '--superseded', '--uninstalling', '--filter', '^'+re.escape(self.release)+'$', '-o', 'json']))
        require(all(item['chart'].startswith('llm-demo-monitoring-') for item in existing), 'Monitoring release belongs to another chart.')
        secret = json.loads(self.output(r.kc+['get', 'secret', values['grafana']['adminSecret'], '--ignore-not-found', '-o', 'json']) or '{}')
        if secret:
            owner = secret['metadata'].get('annotations', {})
            require(owner.get('meta.helm.sh/release-name') == self.release and owner.get('meta.helm.sh/release-namespace') == r.c['namespace'], 'Grafana credential Secret belongs to another installation.')
            password = base64.b64decode(secret['data']['admin-password']).decode()
        else:
            require(not existing, 'Existing monitoring release has lost its credential Secret. Restore it before upgrading.')
            password = secrets.token_urlsafe(36)
        values['grafana']['adminPassword'] = password
        r.helm_apply(self.release, CHART, values)
        r.stamp('monitoring', {'release': self.release})
        print('Monitoring installed.')

    def export_images(self, archive):
        images = image_list(self.recipe)
        require(all('@' not in image for image in images), 'Offline Docker export requires tagged images. Use version tags for offline distribution.')
        for image in images:
            self.run(['docker', 'pull', '--platform', 'linux/arm64', image])
        archive = pathlib.Path(archive).resolve()
        command = ['docker', 'save']
        if '--platform' in self.output(['docker', 'save', '--help']):
            command += ['--platform', 'linux/arm64']
        self.run(command + ['-o', archive] + images)
        require(archive.stat().st_size < MAX_ARCHIVE_BYTES, 'Monitoring archive exceeds the importer 2 GiB limit.')
        with tarfile.open(archive) as tar:
            manifest = json.load(tar.extractfile('manifest.json'))
            tags = {tag for entry in manifest for tag in entry.get('RepoTags', [])}
            for image in images:
                require(bool({image, image.removeprefix('docker.io/')} & tags), 'Exported archive is missing '+image)
            for entry in manifest:
                config = json.load(tar.extractfile(entry['Config']))
                require(config.get('architecture') == 'arm64' and config.get('os') == 'linux',
                        'Exported archive contains a non-ARM64 image.')
        print('Monitoring image archive:', archive)

    @contextlib.contextmanager
    def forward(self, service, port, remote_port):
        r = self.recipe
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', port))
        with (r.work/'monitoring-port-forward.log').open('a') as log:
            proc = subprocess.Popen(r.kc+['port-forward', 'svc/'+self.release+'-'+service, str(port)+':'+str(remote_port), '--address', '127.0.0.1'], stdout=log, stderr=log)
            try:
                for _ in range(100):
                    require(proc.poll() is None, 'Monitoring port-forward exited. Read monitoring-port-forward.log.')
                    try:
                        with socket.create_connection(('127.0.0.1', port), timeout=0.2):
                            break
                    except OSError:
                        time.sleep(0.2)
                else:
                    raise RuntimeError('Monitoring port-forward did not become available.')
                yield proc
            finally:
                proc.terminate()
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()

    def admin_credentials(self):
        r = self.recipe
        name = self.release+'-grafana-admin'
        secret = json.loads(self.output(r.kc+['get', 'secret', name, '-o', 'json']))
        owner = secret.get('metadata', {}).get('annotations', {})
        require(owner.get('meta.helm.sh/release-name') == self.release and
                owner.get('meta.helm.sh/release-namespace') == r.c['namespace'],
                'Grafana credential Secret belongs to another installation.')
        try:
            credentials = tuple(base64.b64decode(secret['data'][key], validate=True).decode()
                                for key in ('admin-user', 'admin-password'))
        except (KeyError, TypeError, ValueError, binascii.Error):
            raise RuntimeError('Grafana admin Secret is malformed.') from None
        require(all(value and value.isprintable() for value in credentials), 'Grafana admin Secret is malformed.')
        return credentials

    def dashboard(self, port, admin=False):
        self.recipe.bound_cluster()
        credentials = self.admin_credentials() if admin else None
        with self.forward('grafana', port, 3000) as proc:
            path = '/login' if admin else '/d/llm-demo'
            print('Dashboard: http://127.0.0.1:'+str(port)+path, flush=True)
            if credentials:
                print('User:', credentials[0], flush=True)
                print('Password:', credentials[1], flush=True)
            else:
                print('Viewer access. No login required.', flush=True)
            print('Press Ctrl-C to close the tunnel.', flush=True)
            try:
                while True:
                    require(proc.poll() is None, 'Grafana tunnel disconnected. Rerun dashboard after restoring access.')
                    time.sleep(1)
            except KeyboardInterrupt:
                pass

    @contextlib.contextmanager
    def traffic_client(self, port, model=None):
        r = self.recipe
        require(r.state.get('stack'), 'Deploy or attach to the routing stack before verifying traffic.')
        requested = model if model is not None else settings(r.c).get('model')
        require(requested is None or (isinstance(requested, str) and bool(requested.strip())), 'Provide a nonempty model ID.')
        configured_key = r.c.get('apiKeyFile')
        if configured_key is not None:
            require(isinstance(configured_key, str) and bool(configured_key.strip()), 'apiKeyFile must name a caller-key file.')
            existing = str(pathlib.Path(configured_key).expanduser().resolve(strict=True))
        else:
            existing = r.state['stack'].get('apiKeyFile')
        url = 'https://127.0.0.1:' + str(port)
        with r.forward(True, port):
            access = contextlib.nullcontext(existing) if existing else gateway_access.temporary_gateway_key(r, url)
            with access as key:
                client = Client(url, r.work/'ca.crt', key)
                yield client, select_model(client, requested)

    def verify(self, port, traffic=False, model=None):
        require(1 <= port <= 65533, 'Monitoring verification needs three consecutive local ports.')
        r = self.recipe
        r.bound_cluster()
        self.save(r.work/'evidence/monitoring.json', {'passed': False, 'startedAt': time.time()})
        values = chart_values(r)
        expected_pods = set()
        for namespace in values['namespaces']:
            for target in values['targets']:
                pods = json.loads(self.output(['kubectl', '--context', r.c['context'], '-n', namespace, 'get', 'pods', '-l', target['selector'], '-o', 'json']))['items']
                for pod in pods:
                    if pod.get('status', {}).get('phase') == 'Running' and not pod['metadata'].get('deletionTimestamp'):
                        ports = [p for c in pod['spec']['containers'] for p in c.get('ports', []) if p['name'] == target['portName']]
                        require(len(ports) == 1, 'Expected one scrape port on '+pod['metadata']['name'])
                        expected_pods.add((target['name'], namespace, pod['metadata']['name']))
        expected = {t['name'] for t in values['targets']} | {'monitoring-storage', 'monitoring-grafana', 'monitoring-collector'}
        query = 'up{monitoring_release="'+self.release+'"} and (time() - timestamp(up{monitoring_release="'+self.release+'"}) < 45)'
        with self.forward('victoria-metrics', port, 8428):
            def query_metrics(expression):
                query_url = 'http://127.0.0.1:'+str(port)+'/api/v1/query?'+urllib.parse.urlencode({'query': expression})
                with urllib.request.urlopen(query_url, timeout=20) as response:
                    data = json.load(response)
                require(data.get('status') == 'success', 'VictoriaMetrics query failed.')
                return data
            deadline = time.monotonic()+75
            while True:
                samples = query_metrics(query)
                try:
                    report = validate_scrapes(samples, expected, expected_pods)
                    break
                except RuntimeError:
                    remaining = deadline-time.monotonic()
                    if remaining <= 0:
                        raise
                    time.sleep(min(2, remaining))
            if traffic:
                with self.traffic_client(port+1, model) as (client, selected):
                    selector = '{monitoring_release='+json.dumps(self.release)+',model='+json.dumps(selected, ensure_ascii=False)+'}'
                    expressions = {
                        'requests': 'sum(llm_api_gateway_http_requests_total'+selector+')',
                        'durationCount': 'sum(llm_api_gateway_http_request_duration_seconds_count'+selector+')',
                        'durationSeconds': 'sum(llm_api_gateway_http_request_duration_seconds_sum'+selector+')',
                        'firstToken': 'sum(llm_api_gateway_stream_first_token_seconds_count'+selector+')',
                        'firstTokenSeconds': 'sum(llm_api_gateway_stream_first_token_seconds_sum'+selector+')',
                        'streamPromptTokens': 'sum(llm_api_gateway_llm_tokens_total'+selector[:-1]+',token_type="prompt",stream="true"})',
                        'nonstreamPromptTokens': 'sum(llm_api_gateway_llm_tokens_total'+selector[:-1]+',token_type="prompt",stream="false"})',
                        'streamTokens': 'sum(llm_api_gateway_llm_tokens_total'+selector[:-1]+',token_type="completion",stream="true"})',
                        'nonstreamTokens': 'sum(llm_api_gateway_llm_tokens_total'+selector[:-1]+',token_type="completion",stream="false"})'}
                    def counters():
                        return {name: sum(float(s['value'][1]) for s in query_metrics(expr)['data']['result']) for name, expr in expressions.items()}
                    before = counters()
                    requests = [sample_completion(client, selected, stream) for stream in (False, True)]
                    deadline = time.monotonic()+75
                    while True:
                        after = counters()
                        if all(after[name] > before[name] for name in expressions):
                            break
                        require(time.monotonic() < deadline, 'Gateway request, response-duration, TTFT or prompt/completion token metrics did not increase: '+json.dumps({'before': before, 'after': after}))
                        time.sleep(2)
                    report['traffic'] = {'model': selected, 'requests': requests, 'before': before, 'after': after}
        with self.forward('grafana', port+2, 3000):
            request = urllib.request.Request('http://127.0.0.1:'+str(port+2)+'/api/dashboards/uid/llm-demo')
            with urllib.request.urlopen(request, timeout=20) as response:
                dashboard = json.load(response)
            require(dashboard.get('dashboard', {}).get('panels'), 'Grafana demo dashboard is missing or empty.')
            require(all(dashboard.get('meta', {}).get(permission) is False for permission in ('canEdit', 'canSave', 'canAdmin')),
                    'Grafana anonymous access must have Viewer permissions.')
            report['dashboardUid'] = dashboard['dashboard']['uid']
            report['anonymousViewer'] = True
        self.save(r.work/'evidence/monitoring.json', report)
        print('Fresh metrics and provisioned dashboard verified:', ', '.join(sorted(expected)))
        if not traffic:
            print('Use --verify-traffic to check metric increases from real gateway requests.')


def validate_scrapes(response, expected, expected_pods=None):
    require(response.get('status') == 'success', 'VictoriaMetrics query failed.')
    series = response.get('data', {}).get('result', [])
    found = {s.get('metric', {}).get('component') for s in series}
    require(not expected - found, 'Missing or stale scrape targets: '+', '.join(sorted(expected-found)))
    found_pods = {(s['metric'].get('component'), s['metric'].get('namespace'), s['metric'].get('pod')) for s in series}
    missing_pods = (expected_pods or set()) - found_pods
    require(not missing_pods, 'Running pods missing from collection: '+repr(sorted(missing_pods)))
    failed = [s['metric'] for s in series if s.get('value', [0, '0'])[1] != '1']
    require(not failed, 'Failed scrape targets: '+json.dumps(failed, sort_keys=True))
    return {'passed': True, 'verifiedAt': time.time(), 'targets': series}
