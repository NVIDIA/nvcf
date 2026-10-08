# SPDX-License-Identifier: Apache-2.0
"""Read-only Helm discovery, verification and monitoring chart behavior."""
import base64
import contextlib
import copy
import json
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

import yaml

HERE = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(HERE))
import monitoring


def installation():
    values = {'operator': {'watchNamespaces': ['llm-stack']},
              'gatewayStack': {'llm-api-gateway': {'llmApiGateway': {'metrics': {'enabled': True, 'port': 9464}}}}}
    deployments = []
    for name in ('llm-api-gateway', 'llm-request-router', 'operator'):
        labels = {'app.kubernetes.io/name': name, 'app.kubernetes.io/instance': 'llm-stack'}
        deployments.append({'metadata': {'name': name, 'labels': labels,
                                        'annotations': {'meta.helm.sh/release-name': 'llm-stack', 'meta.helm.sh/release-namespace': 'llm-stack'}},
                            'spec': {'selector': {'matchLabels': labels}}})
    return [values, {'items': deployments}]


def monitoring_values():
    with patch.object(monitoring.llm, 'run', side_effect=[json.dumps(x) for x in installation()]):
        return monitoring.chart_values('test-context', 'llm-stack')


class MonitoringTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.monitor = monitoring.Monitoring.__new__(monitoring.Monitoring)
        self.monitor.context, self.monitor.namespace, self.monitor.release = 'test-context', 'llm-stack', 'llm-monitoring'
        self.monitor.work = pathlib.Path(self.tmp.name)
        self.monitor.values = monitoring_values()
        self.monitor.kc = monitoring.llm.kube('test-context', 'llm-stack')
        self.monitor.output = self.output = Mock()
        self.monitor.ca_configmap, self.monitor.api_key_file = None, None

    def test_values_discover_one_shared_release_and_unique_component_selectors(self):
        fixture = installation()
        with patch.object(monitoring.llm, 'run', side_effect=[json.dumps(x) for x in fixture]) as run:
            values = monitoring.chart_values('test-context', 'llm-stack')
        self.assertEqual(values['namespaces'], ['llm-stack'])
        self.assertEqual(len(values['targets']), 4)
        for target, deployment in zip(values['targets'], fixture[1]['items']):
            selector = dict(pair.split('=', 1) for pair in target['selector'].split(','))
            matches = [d for d in fixture[1]['items'] if all(d['metadata']['labels'].get(k) == v for k, v in selector.items())]
            self.assertEqual(matches, [deployment])
        self.assertEqual(values['grafana']['adminPassword'], '')
        for call in run.call_args_list:
            command = call.args[0]
            self.assertFalse(set(command) & {'upgrade', 'install', 'apply', 'patch', 'delete'})
            self.assertIn('test-context', command)

    def test_missing_shared_release_or_wrong_namespace_is_rejected(self):
        with patch.object(monitoring.llm, 'run', side_effect=RuntimeError('release not found')), self.assertRaisesRegex(RuntimeError, 'release not found'):
            monitoring.shared_stack('test-context', 'llm-stack')
        values = installation()[0]
        values['operator']['watchNamespaces'] = ['other']
        with patch.object(monitoring.llm, 'run', return_value=json.dumps(values)), self.assertRaisesRegex(RuntimeError, 'watch the selected namespace'):
            monitoring.shared_stack('test-context', 'llm-stack')

    def test_missing_component_or_foreign_owner_is_rejected(self):
        for mutate in ('missing', 'foreign'):
            fixture = installation()
            if mutate == 'missing':
                fixture[1]['items'].pop()
            else:
                fixture[1]['items'][2]['metadata']['annotations']['meta.helm.sh/release-name'] = 'old-operator'
            with patch.object(monitoring.llm, 'run', side_effect=[json.dumps(x) for x in fixture]), self.assertRaisesRegex(RuntimeError, 'Pylon|pylon-operator'):
                monitoring.shared_stack('test-context', 'llm-stack')

    def test_custom_gateway_metrics_port_comes_from_shared_values(self):
        for enabled, port, expected in ((True, 9500, 9500), (False, 9500, 9464)):
            fixture = installation()
            fixture[0]['gatewayStack']['llm-api-gateway']['llmApiGateway']['metrics'] = {'enabled': enabled, 'port': port}
            with patch.object(monitoring.llm, 'run', side_effect=[json.dumps(x) for x in fixture]):
                self.assertEqual(monitoring.chart_values('test-context', 'llm-stack')['targets'][0]['port'], expected)

    def test_traffic_reuses_scoped_gateway_client_without_issuing_credentials(self):
        monitor = monitoring.Monitoring.__new__(monitoring.Monitoring)
        monitor.context, monitor.namespace = 'test-context', 'llm-stack'
        monitor.ca_configmap, monitor.api_key_file = None, None
        client = Mock()
        with patch.object(monitoring.llm, 'gateway', return_value=contextlib.nullcontext(client)) as gateway, \
             patch.object(monitoring, 'select_model', return_value='model-a') as select:
            with monitor.traffic_client('model-a') as actual:
                self.assertEqual(actual, (client, 'model-a'))
        gateway.assert_called_once_with('test-context', 'llm-stack', None, None)
        select.assert_called_once_with(client, 'model-a')

    def test_model_selection_rejects_missing_and_empty_registries(self):
        for listing, requested in (({'object': 'list', 'data': []}, None),
                                   ({'object': 'list', 'data': [{'id': 'model-a'}]}, 'absent')):
            with patch.object(monitoring, 'gateway_response', return_value=json.dumps(listing).encode()), self.assertRaises(RuntimeError):
                monitoring.select_model(Mock(), requested)

    def test_scrape_validation_rejects_missing_stale_and_unhealthy_targets(self):
        series = [{'metric': {'component': 'gateway', 'namespace': 'llm-stack', 'pod': 'gateway-0'}, 'value': [1, '1']}]
        self.assertTrue(monitoring.validate_scrapes({'status': 'success', 'data': {'result': series}}, {'gateway'})['passed'])
        cases = [([], {'gateway'}, set()), (series, {'router'}, set()),
                 (series, {'gateway'}, {('gateway', 'llm-stack', 'other-pod')}),
                 ([dict(series[0], value=[1, '0'])], {'gateway'}, set())]
        for result, expected, pods in cases:
            with self.subTest(result=result, expected=expected), self.assertRaises(RuntimeError):
                monitoring.validate_scrapes({'status': 'success', 'data': {'result': result}}, expected, pods)


    def test_model_discovery_is_generic_deterministic_and_preserves_ids(self):
        client = Mock()
        for ids, requested, expected in [(['org/zeta', 'org/alpha'], None, 'org/alpha'),
                                          (['org/zeta', 'org/alpha'], 'org/zeta', 'org/zeta'),
                                          (['a"b\\c\nmodel'], None, 'a"b\\c\nmodel')]:
            with self.subTest(ids=ids, requested=requested):
                listing = {'object': 'list', 'data': [{'id': name} for name in ids]}
                with patch.object(monitoring, 'gateway_response', return_value=json.dumps(listing).encode()) as response:
                    self.assertEqual(monitoring.select_model(client, requested), expected)
                    response.assert_called_once_with(client, '/v1/models')
        for listing, requested in [({'object': 'list', 'data': []}, None),
                                    ({'object': 'list', 'data': [{'id': ''}]}, None),
                                    ({'object': 'list', 'data': [{'id': 2}]}, None),
                                    ({'object': 'list', 'data': [{'id': 'real'}]}, 'missing'),
                                    ({'object': 'list', 'data': [{'id': 'real'}]}, ''),
                                    ({'data': [{'id': 'real'}]}, None)]:
            with self.subTest(listing=listing, requested=requested):
                with patch.object(monitoring, 'gateway_response', return_value=json.dumps(listing).encode()):
                    with self.assertRaises(RuntimeError):
                        monitoring.select_model(client, requested)

    def test_generic_chat_requests_accept_bounded_reasoning_and_length_finish(self):
        client = Mock()
        model = 'other-vendor/reasoning-model'
        usage = {'prompt_tokens': 7, 'completion_tokens': 12}
        reply = {'model': model, 'choices': [{'message': {'reasoning_content': 'Thinking'}, 'finish_reason': 'length'}], 'usage': usage}
        events = [dict(choices=[{'delta': {'reasoning_content': 'Thinking'}, 'finish_reason': None}]),
                  dict(choices=[{'delta': {}, 'finish_reason': 'length'}], usage=usage)]
        for stream, body in [(False, json.dumps(reply).encode()),
                             (True, ('\n\n'.join('data: '+json.dumps(e) for e in events)+'\n\ndata: [DONE]\n\n').encode())]:
            with self.subTest(stream=stream):
                with patch.object(monitoring, 'gateway_response', return_value=body) as request:
                    record = monitoring.sample_completion(client, model, stream)
                payload = request.call_args.args[2]
                self.assertEqual(payload['model'], model)
                self.assertEqual(payload['max_tokens'], 512)
                self.assertEqual(set(payload), {'model', 'messages', 'max_tokens', 'stream'} | ({'stream_options'} if stream else set()))
                self.assertEqual(record, {'model': model, 'stream': stream, 'status': 200, 'promptTokens': 7, 'completionTokens': 12})
                self.assertNotIn('Thinking', json.dumps(record))
        for invalid in [dict(reply, usage={}), dict(reply, choices=[]),
                        dict(reply, choices=[{'message': {}, 'finish_reason': 'stop'}])]:
            with patch.object(monitoring, 'gateway_response', return_value=json.dumps(invalid).encode()):
                with self.assertRaises(RuntimeError):
                    monitoring.sample_completion(client, model, False)
        with patch.object(monitoring, 'gateway_response', return_value=b'data: {}\n'):
            with self.assertRaisesRegex(RuntimeError, 'completion marker'):
                monitoring.sample_completion(client, model, True)

    def test_gateway_transport_uses_auth_tls_and_bounded_reads_and_closes(self):
        client = Mock(key='private-caller-key', context=True)
        connection = client.connect.return_value
        response = connection.getresponse.return_value
        response.status = 200
        response.read1.side_effect = [b'{"object":"list"}', b'']
        self.assertEqual(monitoring.gateway_response(client, '/v1/models'), b'{"object":"list"}')
        self.assertEqual(connection.request.call_args.args, ('GET', '/v1/models', None, {'Authorization': 'Bearer private-caller-key'}))
        self.assertEqual(connection.timeout, 180)
        connection.close.assert_called_once()
        connection.close.reset_mock()
        response.read1.side_effect = [b'x' * 524289]
        with self.assertRaisesRegex(RuntimeError, '512 KiB'):
            monitoring.gateway_response(client, '/v1/models')
        connection.close.assert_called_once()
        response.status = 401
        with self.assertRaisesRegex(RuntimeError, 'HTTP 401'):
            monitoring.gateway_response(client, '/v1/models')
        client.context = None
        with self.assertRaisesRegex(RuntimeError, 'verified HTTPS'):
            monitoring.gateway_response(client, '/v1/models')

    def test_verifier_detects_missing_pylon_replica(self):
        result = {'status': 'success', 'data': {'result': [
            {'metric': {'component': 'pylon', 'namespace': 'models', 'pod': 'pylon-a'}, 'value': [0, '1']}]}}
        with self.assertRaisesRegex(RuntimeError, 'Running pods missing'):
            monitoring.validate_scrapes(result, {'pylon'}, {('pylon', 'models', 'pylon-a'), ('pylon', 'models', 'pylon-b')})

    def test_verifier_waits_for_initial_collection_before_checking_dashboard(self):
        import contextlib
        import io
        self.output.return_value = '{"items": []}'
        components = {'gateway', 'router', 'operator', 'pylon',
                      'monitoring-storage', 'monitoring-grafana', 'monitoring-collector'}
        responses = [
            {'status': 'success', 'data': {'result': []}},
            {'status': 'success', 'data': {'result': [
                {'metric': {'component': component}, 'value': [0, '1']} for component in components]}},
            {'dashboard': {'uid': 'llm-demo', 'panels': [{'id': 1}]}, 'meta': {'canEdit': False, 'canSave': False, 'canAdmin': False}}]
        with patch.object(self.monitor, 'forward', side_effect=lambda *args: contextlib.nullcontext()), \
             patch.object(monitoring.urllib.request, 'urlopen', side_effect=[io.StringIO(json.dumps(r)) for r in responses]) as request, \
             patch.object(monitoring.time, 'monotonic', side_effect=[0, 0]), \
             patch.object(monitoring.time, 'sleep') as sleep:
            report = self.monitor.verify(18000)

        self.assertEqual(request.call_count, 3)
        self.assertFalse(request.call_args.args[0].has_header('Authorization'))
        self.assertFalse((self.monitor.work/'grafana-admin-password').exists())
        sleep.assert_called_once_with(2)
        self.assertTrue(report['passed'])
        self.assertEqual(report['dashboardUid'], 'llm-demo')
        self.assertTrue(report['anonymousViewer'])
        self.assertEqual({s['metric']['component'] for s in report['targets']}, components)

    def test_verifier_bounds_collection_wait_and_preserves_final_failure(self):
        import contextlib
        import io
        self.output.return_value = '{"items": []}'
        components = {'gateway', 'router', 'operator', 'pylon',
                      'monitoring-storage', 'monitoring-grafana', 'monitoring-collector'}
        missing = {'status': 'success', 'data': {'result': []}}
        failed = {'status': 'success', 'data': {'result': [
            {'metric': {'component': component}, 'value': [0, '0' if component == 'gateway' else '1']}
            for component in components]}}
        with patch.object(self.monitor, 'forward', side_effect=lambda *args: contextlib.nullcontext()) as forward, \
             patch.object(monitoring.urllib.request, 'urlopen', side_effect=[io.StringIO(json.dumps(r)) for r in (missing, missing, failed)]) as request, \
             patch.object(monitoring.time, 'monotonic', side_effect=[0, 0, 74, 75]), \
             patch.object(monitoring.time, 'sleep') as sleep:
            with self.assertRaisesRegex(RuntimeError, 'Failed scrape targets:.*gateway'):
                self.monitor.verify(18000, traffic=True)

        self.assertEqual(request.call_count, 3)
        self.assertEqual([call.args for call in sleep.call_args_list], [(2,), (1,)])
        forward.assert_called_once_with('victoria-metrics', 18000, 8428)

    def test_dashboard_viewer_does_not_read_or_display_credentials(self):
        import contextlib
        import io
        stream = io.StringIO()
        proc = Mock()
        proc.poll.return_value = None
        with patch.object(self.monitor, 'forward', return_value=contextlib.nullcontext(proc)), \
             patch.object(monitoring.dashboard_login, 'open_dashboard') as login, \
             patch.object(monitoring.time, 'sleep', side_effect=KeyboardInterrupt), contextlib.redirect_stdout(stream):
            self.monitor.dashboard(13000)
        login.assert_not_called()
        self.output.assert_not_called()
        self.assertIn('/d/llm-demo', stream.getvalue())
        self.assertIn('No login required', stream.getvalue())
        self.assertNotIn('Password', stream.getvalue())

    def admin_secret(self):
        return {'metadata': {'annotations': {'meta.helm.sh/release-name': self.monitor.release,
                 'meta.helm.sh/release-namespace': self.monitor.namespace}},
                'data': {key: base64.b64encode(value.encode()).decode()
                         for key, value in [('admin-user', 'admin'), ('admin-password', 'current-password')]}}

    def test_admin_access_rejects_foreign_or_malformed_credentials_before_tunneling(self):
        import contextlib
        import io
        for problem in ('owner', 'namespace', 'missing', 'base64', 'utf8', 'empty', 'control'):
            secret = self.admin_secret()
            if problem in ('owner', 'namespace'):
                key = 'meta.helm.sh/release-'+('name' if problem == 'owner' else 'namespace')
                secret['metadata']['annotations'][key] = 'other'
            elif problem == 'missing':
                del secret['data']['admin-password']
            else:
                secret['data']['admin-password'] = {'base64': '***', 'utf8': '/w==', 'empty': '',
                                                    'control': base64.b64encode(b'password\n').decode()}[problem]
            self.output.return_value = json.dumps(secret)
            stream = io.StringIO()
            with self.subTest(problem=problem), patch.object(self.monitor, 'forward') as forward, \
                 contextlib.redirect_stdout(stream), self.assertRaisesRegex(RuntimeError, 'another installation|malformed'):
                self.monitor.dashboard(13000, admin=True)
            forward.assert_not_called()
            self.assertEqual(stream.getvalue(), '')

    def test_admin_bind_failure_does_not_print_credentials(self):
        import contextlib
        import io
        self.output.return_value = json.dumps(self.admin_secret())
        stream = io.StringIO()
        with patch.object(self.monitor, 'forward', side_effect=OSError('Address already in use')), \
             contextlib.redirect_stdout(stream), self.assertRaises(OSError):
            self.monitor.dashboard(13000, admin=True)
        self.assertEqual(stream.getvalue(), '')

    def test_dashboard_reports_a_lost_tunnel(self):
        import contextlib
        proc = Mock()
        proc.poll.return_value = 1
        with patch.object(self.monitor, 'forward', return_value=contextlib.nullcontext(proc)):
            with self.assertRaisesRegex(RuntimeError, 'Grafana tunnel disconnected'):
                self.monitor.dashboard(13000)

    def test_traffic_verification_requires_latency_and_both_token_types_in_each_mode(self):
        import contextlib
        import io
        metrics = {
            'requests': ('llm_api_gateway_http_requests_total',),
            'durationCount': ('llm_api_gateway_http_request_duration_seconds_count',),
            'durationSeconds': ('llm_api_gateway_http_request_duration_seconds_sum',),
            'firstToken': ('llm_api_gateway_stream_first_token_seconds_count',),
            'firstTokenSeconds': ('llm_api_gateway_stream_first_token_seconds_sum',),
            'streamPromptTokens': ('llm_api_gateway_llm_tokens_total', 'token_type="prompt"', 'stream="true"'),
            'nonstreamPromptTokens': ('llm_api_gateway_llm_tokens_total', 'token_type="prompt"', 'stream="false"'),
            'streamTokens': ('llm_api_gateway_llm_tokens_total', 'token_type="completion"', 'stream="true"'),
            'nonstreamTokens': ('llm_api_gateway_llm_tokens_total', 'token_type="completion"', 'stream="false"')}
        for stalled_metric in (None, *metrics):
            with self.subTest(stalled_metric=stalled_metric):
                self.output.return_value = '{"items": []}'
                self.output.side_effect = None
                traffic_sent = False
                selected = 'vendor/model"quoted\\path\nname\U0001f680'
                client = Mock()
                def send_traffic(_client, model, stream):
                    nonlocal traffic_sent
                    self.assertIs(_client, client)
                    self.assertEqual(model, selected)
                    traffic_sent = stream
                    return {'stream': stream, 'status': 200}
                components = {'gateway','router','operator','pylon','monitoring-storage','monitoring-grafana','monitoring-collector'}
                def response(request, timeout):
                    url = request if isinstance(request, str) else request.full_url
                    if '/api/dashboards/' in url:
                        return io.StringIO(json.dumps({'dashboard': {'uid': 'llm-demo', 'panels': [{'id': 1}]}, 'meta': {'canEdit': False, 'canSave': False, 'canAdmin': False}}))
                    query = monitoring.urllib.parse.parse_qs(monitoring.urllib.parse.urlparse(url).query)['query'][0]
                    if query.startswith('up{'):
                        data = [{'metric': {'component': c}, 'value': [0,'1']} for c in components]
                    else:
                        self.assertIn('monitoring_release="'+self.monitor.release+'"', query)
                        self.assertIn('model='+json.dumps(selected, ensure_ascii=False), query)
                        stalled = stalled_metric and all(part in query for part in metrics[stalled_metric])
                        increased = traffic_sent and not stalled
                        data = [{'value': [0, '2' if increased else '1']}]
                    return io.StringIO(json.dumps({'status': 'success','data': {'result': data}}))
                with patch.object(self.monitor, 'forward', side_effect=lambda *args: contextlib.nullcontext()), \
                     patch.object(self.monitor, 'traffic_client', side_effect=lambda *args: contextlib.nullcontext((client, selected))), \
                     patch.object(monitoring, 'sample_completion', side_effect=send_traffic) as completion, \
                     patch.object(monitoring.urllib.request, 'urlopen', side_effect=response), \
                     patch.object(monitoring.time, 'monotonic', side_effect=[0, 0, 100]):
                    if stalled_metric is None:
                        report = self.monitor.verify(18000, traffic=True)
                    else:
                        with self.assertRaisesRegex(RuntimeError, 'did not increase'):
                            report = self.monitor.verify(18000, traffic=True)
                if stalled_metric is None:
                    self.assertEqual(report['traffic']['before'], dict.fromkeys(metrics, 1))
                    self.assertEqual(report['traffic']['after'], dict.fromkeys(metrics, 2))
                    self.assertEqual(report['traffic']['model'], selected)
                    self.assertEqual(report['dashboardUid'], 'llm-demo')
                self.assertEqual([call.args[2] for call in completion.call_args_list], [False, True])


@unittest.skipUnless(yaml and shutil.which('helm'), 'Install requirements-monitoring.txt and Helm for chart tests')
class MonitoringChartTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.tmp.cleanup)
        cls.work = pathlib.Path(cls.tmp.name)
        cls.values = monitoring_values()
        cls.values['nodeSelector'] = {'kubernetes.io/hostname': 'control-node'}
        path = cls.work/'values.json'
        path.write_text(json.dumps(cls.values))
        rendered = subprocess.check_output(['helm', 'template', 'llm-monitoring', str(monitoring.CHART), '-n', 'llm-stack', '-f', str(path)], text=True)
        cls.docs = [d for d in yaml.safe_load_all(rendered) if d]
        cls.config = yaml.safe_load(next(d for d in cls.docs if d['kind']=='ConfigMap' and d['metadata']['name'].endswith('-collector'))['data']['config.yaml'])

    def test_discovered_selectors_match_the_rendered_shared_stack(self):
        import importlib.util
        path = HERE.parent/'tests/test_helm_shared_stack.py'
        spec = importlib.util.spec_from_file_location('monitoring_shared_fixture', path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        fixture = module.SharedHelmTests
        fixture.setUpClass()
        try:
            resources = fixture().installed()
            deployments = [d for d in resources if d['kind'] == 'Deployment']
            values = {'operator': {'watchNamespaces': ['test-models']},
                      'gatewayStack': {'llm-api-gateway': {'llmApiGateway': {'metrics': {'enabled': True}}}}}
            for deployment in deployments:
                deployment['metadata']['annotations']['meta.helm.sh/release-name'] = 'llm-stack'
            reads = [values, {'items': deployments}]
            with patch.object(monitoring.llm, 'run', side_effect=[json.dumps(v) for v in reads]):
                generated = monitoring.chart_values('test-context', 'test-models')
            for target in generated['targets'][:3]:
                labels = dict(pair.split('=', 1) for pair in target['selector'].split(','))
                selected = [d for d in deployments if all(d['spec']['template']['metadata']['labels'].get(k) == v for k, v in labels.items())]
                self.assertEqual(len(selected), 1, target)
                ports = [p for c in selected[0]['spec']['template']['spec']['containers'] for p in c.get('ports', []) if p['name'] == target['portName']]
                self.assertEqual(len(ports), 1)
        finally:
            fixture.tearDownClass()

    def test_disabled_chart_creates_no_resources(self):
        result = subprocess.check_output(['helm', 'template', 'disabled', str(monitoring.CHART)], text=True)
        self.assertFalse([doc for doc in yaml.safe_load_all(result) if doc])

    def test_optional_egress_policy_selects_only_monitoring_and_allows_cluster_access(self):
        self.assertFalse(any(d['kind']=='NetworkPolicy' for d in self.docs))
        values = copy.deepcopy(self.values)
        values['networkPolicy'] = {'enabled': True, 'apiServerCIDRs': ['10.0.0.1/32']}
        path = self.work/'restricted.json'
        path.write_text(json.dumps(values))
        rendered = subprocess.check_output(['helm', 'template', 'restricted', str(monitoring.CHART), '-f', str(path)], text=True)
        policy = next(d for d in yaml.safe_load_all(rendered) if d and d['kind']=='NetworkPolicy')['spec']
        self.assertEqual(policy['podSelector'], {'matchLabels': {'app.kubernetes.io/instance': 'restricted'}})
        self.assertEqual(policy['policyTypes'], ['Egress'])
        self.assertEqual(policy['egress'], [{'to': [{'namespaceSelector': {}}]},
                         {'to': [{'ipBlock': {'cidr': '10.0.0.1/32'}}],
                          'ports': [{'protocol': 'TCP', 'port': 443}, {'protocol': 'TCP', 'port': 6443}]}])

    def test_collection_has_scoped_read_only_rbac_and_no_cluster_role(self):
        self.assertFalse(any(d['kind'].startswith('ClusterRole') for d in self.docs))
        roles = [d for d in self.docs if d['kind']=='Role']
        self.assertEqual([d['metadata']['namespace'] for d in roles], ['llm-stack'])
        self.assertEqual(roles[0]['rules'], [{'apiGroups': [''], 'resources': ['pods'], 'verbs': ['get','list','watch']}])

    def test_metrics_pipeline_and_discovery_include_all_components(self):
        config = self.config
        pipeline = config['service']['pipelines']['metrics']
        self.assertEqual(pipeline['exporters'], ['prometheusremotewrite'])
        jobs = config['receivers']['prometheus']['config']['scrape_configs']
        self.assertEqual({j['job_name'] for j in jobs}, {'gateway','router','operator','pylon','monitoring-storage','monitoring-grafana','monitoring-collector'})
        for job in jobs[:4]:
            self.assertEqual(job['kubernetes_sd_configs'][0]['namespaces']['names'], ['llm-stack'])
            self.assertEqual(job['relabel_configs'][1]['action'], 'keep')
        self.assertEqual(jobs[0]['relabel_configs'][2]['replacement'], '$${1}:9464')

    def test_workloads_have_limits_control_placement_and_private_access(self):
        for doc in self.docs:
            if doc['kind']=='Deployment':
                pod = doc['spec']['template']['spec']
                self.assertEqual(pod['nodeSelector']['kubernetes.io/hostname'], 'control-node')
                self.assertTrue(pod['securityContext']['runAsNonRoot'])
                self.assertTrue(pod['containers'][0]['resources']['limits']['memory'])
            if doc['kind']=='Service':
                self.assertEqual(doc['spec']['type'], 'ClusterIP')
        pvc = next(d for d in self.docs if d['kind']=='PersistentVolumeClaim')
        self.assertEqual(pvc['metadata']['annotations']['helm.sh/resource-policy'], 'keep')
        grafana = next(d for d in self.docs if d['kind']=='Deployment' and d['metadata']['name'].endswith('-grafana'))
        env = {e['name']: e for e in grafana['spec']['template']['spec']['containers'][0]['env']}
        self.assertEqual(env['GF_AUTH_ANONYMOUS_ENABLED']['value'], 'true')
        self.assertIn('secretKeyRef', env['GF_SECURITY_ADMIN_PASSWORD']['valueFrom'])

    def test_dashboard_provisioning_and_queries_cover_required_signals(self):
        config = next(d for d in self.docs if d['kind']=='ConfigMap' and d['metadata']['name'].endswith('-grafana'))['data']
        datasource = yaml.safe_load(config['datasource.yaml'])['datasources'][0]
        dashboard = json.loads(config['dashboard.json'])
        self.assertEqual(datasource['uid'], 'demo-metrics')
        self.assertEqual(dashboard['uid'], 'llm-demo')
        expressions = ' '.join(t['expr'] for p in dashboard['panels'] for t in p['targets'])
        self.assertEqual(len(dashboard['panels']), 17)
        for metric in ('stream_first_token_seconds_bucket','llm_tokens_total','pylon_reverse_tunnel_connected','nvcf_pylon_operator_registered','stargate_requests_total'):
            self.assertIn(metric, expressions)
        self.assertNotIn('llamacpp', expressions)
        self.assertNotIn('GLM', json.dumps(dashboard))
        self.assertNotIn('or vector(0)', expressions)
        self.assertIn('timestamp(up', expressions)
        model = next(variable for variable in dashboard['templating']['list'] if variable['name'] == 'model')
        self.assertIn('llm_api_gateway_http_requests_total|stargate_active_inference_servers', model['query'])
        self.assertTrue(model['multi'])
        self.assertEqual(model['current']['value'], ['$__all'])
        for panel in dashboard['panels']:
            self.assertEqual(panel['datasource']['uid'], datasource['uid'])
            self.assertEqual(panel['fieldConfig']['defaults']['noValue'], 'No data')

    def test_runtime_panels_and_labels_require_explicit_supported_target(self):
        for runtime in (None, 'llama.cpp'):
            with self.subTest(runtime=runtime):
                values = copy.deepcopy(self.values)
                target = {'name': 'custom-runtime', 'selector': 'app=external-backend', 'portName': 'metrics'}
                if runtime:
                    target['runtime'] = runtime
                values['targets'].append(target)
                path = self.work/'runtime-target.json'
                path.write_text(json.dumps(values))
                rendered = subprocess.check_output(['helm', 'template', 'optional-runtime', str(monitoring.CHART), '-f', str(path)], text=True)
                docs = [d for d in yaml.safe_load_all(rendered) if d]
                collector = yaml.safe_load(next(d for d in docs if d['kind'] == 'ConfigMap' and d['metadata']['name'].endswith('-collector'))['data']['config.yaml'])
                job = next(j for j in collector['receivers']['prometheus']['config']['scrape_configs'] if j['job_name'] == target['name'])
                labels = {rule.get('target_label'): rule.get('replacement') for rule in job['relabel_configs']}
                self.assertEqual(labels.get('backend_runtime'), runtime)
                self.assertNotIn('model', labels)
                dashboard = json.loads(next(d for d in docs if d['kind'] == 'ConfigMap' and d['metadata']['name'].endswith('-grafana'))['data']['dashboard.json'])
                self.assertEqual(len(dashboard['panels']), 20 if runtime else 17)
                self.assertEqual(len({p['id'] for p in dashboard['panels']}), len(dashboard['panels']))
                if runtime:
                    for panel in dashboard['panels'][17:]:
                        for query in panel['targets']:
                            self.assertIn('backend_runtime="llama.cpp"', query['expr'])


if __name__ == '__main__':
    unittest.main()
