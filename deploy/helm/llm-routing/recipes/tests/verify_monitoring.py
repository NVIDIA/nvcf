#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Integration checks for metrics and dashboard access on an installed monitoring release."""
import argparse
import contextlib
import json
import pathlib
import socket
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request

HERE = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(HERE.parent))
import llm


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


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
    def __init__(self, context, namespace, release, work, ca_configmap=None, api_key_file=None):
        self.context, self.namespace, self.release = context, namespace, release
        self.work = pathlib.Path(work)
        self.ca_configmap, self.api_key_file = ca_configmap, api_key_file
        self.kc = llm.kube(context, namespace)
        self.output = llm.run
        status = json.loads(llm.run(['helm', '--kube-context', context, '--namespace', namespace,
                                    'list', '-o', 'json']))
        require(any(r['name'] == release and r['chart'].startswith('llm-demo-monitoring-') and r['status'] == 'deployed'
                    for r in status), 'Select a deployed monitoring Helm release with --release.')
        self.values = json.loads(llm.run(['helm', '--kube-context', context, '--namespace', namespace,
                                         'get', 'values', release, '--all', '-o', 'json']))
        require(self.values.get('enabled') and namespace in self.values.get('namespaces', []),
                'Monitoring must be enabled and scrape the shared namespace.')

    @contextlib.contextmanager
    def forward(self, service, port, remote_port):
        r = self
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

    def grafana_path(self):
        ingress = self.values['grafana'].get('ingress', {})
        return ingress['path'] if ingress.get('enabled') else ''

    @contextlib.contextmanager
    def traffic_client(self, model=None):
        with llm.gateway(self.context, self.namespace, self.ca_configmap, self.api_key_file) as client:
            yield client, select_model(client, model)

    def verify(self, port, traffic=False, model=None):
        require(1 <= port <= 65533, 'Monitoring verification needs three consecutive local ports.')
        r = self
        values = self.values
        expected_pods = set()
        for namespace in values['namespaces']:
            for target in values['targets']:
                pods = json.loads(self.output(['kubectl', '--context', r.context, '-n', namespace, 'get', 'pods', '-l', target['selector'], '-o', 'json']))['items']
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
                with self.traffic_client(model) as (client, selected):
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
            request = urllib.request.Request('http://127.0.0.1:'+str(port+2)+self.grafana_path()+'/api/dashboards/uid/llm-demo')
            with urllib.request.urlopen(request, timeout=20) as response:
                dashboard = json.load(response)
            require(dashboard.get('dashboard', {}).get('panels'), 'Grafana demo dashboard is missing or empty.')
            require(all(dashboard.get('meta', {}).get(permission) is False for permission in ('canEdit', 'canSave', 'canAdmin')),
                    'Grafana anonymous access must have Viewer permissions.')
            report['dashboardUid'] = dashboard['dashboard']['uid']
            report['anonymousViewer'] = True
        print('Fresh metrics and provisioned dashboard verified:', ', '.join(sorted(expected)))
        if not traffic:
            print('Use --verify-traffic to check metric increases from real gateway requests.')
        return report


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


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--context')
    parser.add_argument('--namespace', default='llm-stack')
    parser.add_argument('--release', default='llm-monitoring')
    parser.add_argument('--ca-configmap')
    parser.add_argument('--api-key-file', type=pathlib.Path)
    parser.add_argument('--port', type=int, default=18428)
    parser.add_argument('--verify-traffic', action='store_true')
    parser.add_argument('--model')
    parser.add_argument('--output', type=pathlib.Path, required=True)
    args = parser.parse_args(argv)
    if args.output.exists():
        parser.error('Use a new output file.')
    context = llm.selected_context(args.context)
    with tempfile.TemporaryDirectory(prefix='llm-monitoring-test-') as work:
        monitor = Monitoring(context, args.namespace, args.release, work, args.ca_configmap, args.api_key_file)
        result = monitor.verify(args.port, args.verify_traffic, args.model)
    with args.output.open('x') as out:
        args.output.chmod(0o600)
        json.dump(result, out, indent=2)
        out.write('\n')
    print('Saved:', args.output)


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, ValueError, OSError, KeyError) as error:
        raise SystemExit(str(error)) from None
