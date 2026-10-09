# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import re
import runpy
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import MagicMock, patch

HERE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('llm_preflight_tested', HERE / 'llm.py')
llm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(llm)


def node(name, arch='arm64', product='NVIDIA-GB10', gpus='1', labels=None, ready=True, images=(), **spec):
    node_labels = {'kubernetes.io/os': 'linux', 'kubernetes.io/arch': arch}
    if product:
        node_labels['nvidia.com/gpu.product'] = product
    node_labels.update(labels or {})
    status = {'conditions': [{'type': 'Ready', 'status': 'True' if ready else 'False'}],
              'allocatable': {'nvidia.com/gpu': gpus} if gpus is not None else {},
              'images': [{'names': list(names)} for names in images]}
    return {'metadata': {'name': name, 'labels': node_labels}, 'spec': spec, 'status': status}


class Cluster:
    def __init__(self):
        self.nodes = [node('spark-a')]
        self.runtime_classes = [{'metadata': {'name': 'nvidia'}}]
        self.storage_classes = [{'metadata': {'name': 'local-path'}, 'volumeBindingMode': 'WaitForFirstConsumer'}]
        self.versions = {'kubectl': 'Client Version: v1.31.0\nKustomize Version: v5.4.2\n', 'helm': 'v3.14.4+g81c902a\n'}
        self.can_i = 'yes\n'
        self.failures = {}
        self.commands = []

    def __call__(self, command, timeout=60):
        self.commands.append(command)
        if command[1] in ('version',):
            version = self.versions[command[0]]
            if isinstance(version, Exception):
                raise version
            return version
        if command == ['kubectl', 'config', 'current-context']:
            return 'target\n'
        if command[:3] != ['kubectl', '--context', 'target']:
            raise AssertionError('Unexpected command: ' + ' '.join(command))
        verb = command[3:]
        key = 'auth' if verb[0] == 'auth' else verb[1]
        if key in self.failures:
            raise self.failures[key]
        if verb == ['auth', 'can-i', 'create', 'customresourcedefinitions.apiextensions.k8s.io']:
            if self.can_i is None:
                raise RuntimeError('kubectl failed: ')
            return self.can_i
        if verb[0] != 'get' or verb[2:] != ['-o', 'json']:
            raise AssertionError('Preflight must only read cluster state: ' + ' '.join(command))
        items = {'nodes': self.nodes, 'runtimeclasses': self.runtime_classes, 'storageclasses': self.storage_classes}[verb[1]]
        return json.dumps({'items': items})


class PreflightTest(unittest.TestCase):
    def setUp(self):
        self.enterContext(patch.object(llm.subprocess, 'run', side_effect=AssertionError('Unexpected process')))
        self.enterContext(patch.object(llm.subprocess, 'Popen', side_effect=AssertionError('Unexpected port forward')))
        self.output = self.enterContext(contextlib.redirect_stdout(io.StringIO()))
        self.errors = self.enterContext(contextlib.redirect_stderr(io.StringIO()))
        self.cluster = Cluster()

    def preflight(self, *arguments):
        with patch.object(llm, 'run', side_effect=self.cluster):
            return llm.main(['--context', 'target', 'preflight', *arguments])

    def results(self, report):
        return {item['check']: item['result'] for item in report['checks']}

    def values_file(self, values):
        directory = self.enterContext(tempfile.TemporaryDirectory())
        path = Path(directory) / 'values.json'
        path.write_text(values if isinstance(values, str) else json.dumps(values))
        return path


class PreflightClusterTests(PreflightTest):
    def test_ready_cluster_passes_every_check_and_lists_matching_recipes(self):
        report = self.preflight()
        self.assertEqual(report['status'], 'ready')
        self.assertEqual(set(self.results(report).values()), {'PASS'})
        self.assertEqual(list(self.results(report)), ['Python', 'kubectl', 'Helm', 'Cluster access', 'Permissions',
                                                      'GPU nodes', 'Recipe hardware', 'RuntimeClass', 'StorageClass'])
        self.assertEqual(report['nodes'][0]['name'], 'spark-a')
        self.assertIn('qwen3.8-27b', report['nodes'][0]['recipes'])
        self.assertEqual(report['nodes'][0]['problems'], [])
        output = self.output.getvalue()
        self.assertIn('Cluster: target', output)
        self.assertIn('Prerequisites met. Continue with README.md#1-install-shared-infrastructure.', output)
        self.assertIn('Outbound access from nodes', output)

    def test_spaced_gpu_product_label_matches_like_the_planner(self):
        self.cluster.nodes = [node('spark-a', product='NVIDIA GB10')]
        report = self.preflight()
        self.assertIn('qwen3.8-27b', report['nodes'][0]['recipes'])
        self.assertEqual(report['status'], 'ready')

    def test_gpu_node_problems_block_recipe_hardware(self):
        cases = {
            'no nvidia.com/gpu.product label': {'product': None},
            'MIG enabled': {'labels': {'nvidia.com/mig.capable': 'true', 'nvidia.com/mig.strategy': 'single'}},
            'GPU shared by time-slicing or MPS': {'labels': {'nvidia.com/gpu.sharing-strategy': 'time-slicing'}},
            'NoSchedule or NoExecute taint': {'taints': [{'key': 'gpu', 'effect': 'NoSchedule'}]},
            'not Ready or cordoned': {'ready': False},
        }
        for problem, fields in cases.items():
            with self.subTest(problem=problem):
                self.cluster.nodes = [node('spark-a', **fields)]
                report = self.preflight()
                self.assertEqual(report['status'], 'blocked')
                self.assertEqual(self.results(report)['Recipe hardware'], 'FAIL')
                self.assertIn(problem, report['nodes'][0]['problems'])
        for variant in ({'labels': {'nvidia.com/gpu.replicas': '4'}}, {'unschedulable': True}):
            with self.subTest(variant=variant):
                self.cluster.nodes = [node('spark-a', **variant)]
                self.assertEqual(self.preflight()['status'], 'blocked')

    def test_mig_resources_block_recipe_hardware(self):
        self.cluster.nodes = [node('spark-a')]
        self.cluster.nodes[0]['status']['allocatable']['nvidia.com/mig-1g.10gb'] = '7'
        report = self.preflight()
        self.assertIn('MIG enabled', report['nodes'][0]['problems'])
        self.assertEqual(report['status'], 'blocked')

    def test_partially_usable_gpu_nodes_warn_without_blocking(self):
        self.cluster.nodes = [node('spark-a'), node('spark-b', product=None)]
        report = self.preflight()
        self.assertEqual(self.results(report)['Recipe hardware'], 'WARN')
        self.assertEqual(report['status'], 'ready')
        self.assertIn('1 of 2 GPU node(s)', report['checks'][6]['detail'])

    def test_unmatched_gpu_product_architecture_or_count_has_no_recipes(self):
        for fields in ({'product': 'NVIDIA-GB300'}, {'arch': 'amd64'}, {'gpus': '4'}):
            with self.subTest(fields=fields):
                self.cluster.nodes = [node('station-a', **fields)]
                report = self.preflight()
                self.assertEqual(report['nodes'][0]['recipes'], [])
                self.assertEqual(self.results(report)['Recipe hardware'], 'FAIL')

    def test_cluster_without_gpu_nodes_fails_and_skips_recipe_hardware(self):
        self.cluster.nodes = [node('cpu-a', gpus=None), node('cpu-b', gpus='0')]
        report = self.preflight()
        self.assertEqual(self.results(report)['GPU nodes'], 'FAIL')
        self.assertNotIn('Recipe hardware', self.results(report))
        self.assertEqual(report['nodes'], [])
        self.assertIn('Install the NVIDIA device plugin', self.output.getvalue())
        self.assertIn('Fix each FAIL above', self.output.getvalue())

    def test_runtime_class_and_storage_class_requirements(self):
        cases = [
            ({'runtime_classes': []}, 'RuntimeClass', 'is not installed'),
            ({'storage_classes': []}, 'StorageClass', 'is not installed'),
            ({'storage_classes': [{'metadata': {'name': 'local-path'}}]}, 'StorageClass', 'uses Immediate binding'),
            ({'storage_classes': [{'metadata': {'name': 'local-path'}, 'volumeBindingMode': 'Immediate'}]},
             'StorageClass', 'uses Immediate binding'),
            ({'storage_classes': [{'metadata': {'name': 'local-path'}, 'volumeBindingMode': 'WaitForFirstConsumer',
                                   'allowedTopologies': [{'matchLabelExpressions': []}]}]},
             'StorageClass', 'with allowedTopologies'),
        ]
        for fields, check, detail in cases:
            with self.subTest(fields=fields):
                self.cluster = Cluster()
                for key, value in fields.items():
                    setattr(self.cluster, key, value)
                report = self.preflight()
                self.assertEqual(report['status'], 'blocked')
                failed = next(item for item in report['checks'] if item['check'] == check)
                self.assertEqual(failed['result'], 'FAIL')
                self.assertIn(detail, failed['detail'])

    def test_selected_runtime_and_storage_class_names_are_checked(self):
        self.cluster.runtime_classes = [{'metadata': {'name': 'nvidia-cdi'}}]
        self.cluster.storage_classes = [{'metadata': {'name': 'nvme'}, 'volumeBindingMode': 'WaitForFirstConsumer'}]
        self.assertEqual(self.preflight()['status'], 'blocked')
        self.assertEqual(self.preflight('--runtime-class', 'nvidia-cdi', '--storage-class', 'nvme')['status'], 'ready')

    def test_invalid_class_names_fail_before_cluster_access(self):
        for arguments in (['--runtime-class', 'Bad_Name'], ['--storage-class', '-x']):
            with self.subTest(arguments=arguments), self.assertRaises(SystemExit):
                self.preflight(*arguments)
        self.assertEqual(self.cluster.commands, [])

    def test_listing_failures_are_reported_as_failures(self):
        self.cluster.failures = {'runtimeclasses': RuntimeError('kubectl failed: forbidden'),
                                 'storageclasses': subprocess.TimeoutExpired('kubectl', 60)}
        report = self.preflight()
        self.assertEqual(report['status'], 'blocked')
        details = {item['check']: item['detail'] for item in report['checks']}
        self.assertIn('Cannot list runtimeclasses: kubectl failed: forbidden', details['RuntimeClass'])
        self.assertIn('Cannot list storageclasses', details['StorageClass'])

    def test_crd_permission_denied_fails_without_crashing(self):
        for answer in (None, 'no\n'):
            with self.subTest(answer=answer):
                self.cluster.can_i = answer
                report = self.preflight()
                self.assertEqual(self.results(report)['Permissions'], 'FAIL')
                self.assertEqual(report['status'], 'blocked')
                self.assertIn('operator.installCRDs=false', self.output.getvalue())

    def test_context_failure_stops_before_cluster_checks(self):
        self.cluster.failures = {'nodes': RuntimeError('kubectl failed: error: context "target" does not exist')}
        report = self.preflight()
        self.assertEqual(report['status'], 'blocked')
        self.assertEqual(list(self.results(report))[-1], 'Cluster access')
        self.assertEqual(self.results(report)['Cluster access'], 'FAIL')
        self.assertIn('does not exist', report['checks'][-1]['detail'])
        self.assertEqual([command[3:5] for command in self.cluster.commands if command[1] == '--context'], [['get', 'nodes']])
        with patch.object(llm, 'run', side_effect=self.cluster), \
                patch.object(llm, 'selected_context', side_effect=ValueError('Select a Kubernetes context')):
            report = llm.main(['preflight'])
        self.assertEqual(report['status'], 'blocked')
        self.assertIsNone(report['context'])
        self.assertIn('Select a Kubernetes context', report['checks'][-1]['detail'])

    def test_tool_versions(self):
        cases = [
            ('helm', 'v3.9.4+gdbc6d8e\n', 'FAIL'),
            ('helm', 'v4.2.4+g3900f43\n', 'PASS'),
            ('helm', FileNotFoundError(2, 'No such file or directory', 'helm'), 'FAIL'),
            ('helm', 'unparseable\n', 'FAIL'),
            ('kubectl', RuntimeError('kubectl failed: broken'), 'FAIL'),
        ]
        for tool, version, result in cases:
            with self.subTest(tool=tool, version=version):
                self.cluster = Cluster()
                self.cluster.versions[tool] = version
                report = self.preflight()
                self.assertEqual(self.results(report)['Helm' if tool == 'helm' else 'kubectl'], result)
                self.assertEqual(report['status'], 'ready' if result == 'PASS' else 'blocked')
        with patch.object(llm.sys, 'version_info', (3, 10, 14)):
            report = self.preflight()
        self.assertEqual(self.results(report)['Python'], 'FAIL')
        self.assertIn('Python 3.10.14', report['checks'][0]['detail'])

    def test_json_output_is_the_complete_report(self):
        report = self.preflight('--json')
        self.assertEqual(json.loads(self.output.getvalue()), report)

    def test_script_exits_two_when_a_prerequisite_fails(self):
        self.cluster.nodes = [node('cpu-a', gpus=None)]

        def process(command, **kwargs):
            return subprocess.CompletedProcess(command, 0, self.cluster(command), '')

        with patch.object(llm.sys, 'argv', ['llm.py', '--context', 'target', 'preflight']), \
                patch.object(llm.subprocess, 'run', side_effect=process), patch.object(llm.signal, 'signal'), \
                self.assertRaises(SystemExit) as stopped:
            runpy.run_path(str(HERE / 'llm.py'), run_name='__main__')
        self.assertEqual(stopped.exception.code, 2)
        self.cluster.nodes = [node('spark-a')]
        with patch.object(llm.sys, 'argv', ['llm.py', '--context', 'target', 'preflight']), \
                patch.object(llm.subprocess, 'run', side_effect=process), patch.object(llm.signal, 'signal'):
            runpy.run_path(str(HERE / 'llm.py'), run_name='__main__')


class PreflightImageTests(PreflightTest):
    values = {
        'gatewayStack': {'llm-api-gateway': {'llmApiGateway': {
            'image': {'repository': 'localhost/llm-stack/gateway', 'tag': 'dev-1', 'pullPolicy': 'Never'},
            'nodeSelector': {'kubernetes.io/os': 'linux', 'kubernetes.io/arch': 'arm64'}}}},
        'operator': {'pylon': {'image': {'repository': 'python', 'tag': '3.12-alpine', 'pullPolicy': 'Never'}}},
        'verification': {'image': {'repository': 'python', 'tag': '3.13-alpine', 'pullPolicy': 'IfNotPresent'}},
    }
    gateway = 'localhost/llm-stack/gateway:dev-1'
    python = ('docker.io/library/python:3.12-alpine',)

    def image_checks(self, report):
        return [item for item in report['checks'] if item['check'] in ('Image', 'Images')]

    def test_never_pull_images_listed_on_every_eligible_node_pass(self):
        self.cluster.nodes = [node('spark-a', images=[(self.gateway,), self.python]),
                              node('spark-b', images=[(self.gateway, 'localhost/llm-stack/gateway@sha256:1'), self.python]),
                              node('amd64-a', arch='amd64', gpus=None), node('spark-down', ready=False)]
        report = self.preflight('--values', str(self.values_file(self.values)))
        checks = self.image_checks(report)
        self.assertEqual([item['result'] for item in checks], ['PASS', 'PASS'])
        self.assertIn('localhost/llm-stack/gateway:dev-1: listed on 2 of 2 eligible node(s).', checks[0]['detail'])
        self.assertIn('python:3.12-alpine: listed on 2 of 2', checks[1]['detail'])
        self.assertFalse(any('3.13' in item['detail'] for item in checks))
        self.assertEqual(report['status'], 'ready')

    def test_images_missing_on_some_nodes_warn_and_on_all_nodes_fail(self):
        self.cluster.nodes = [node('spark-a', images=[(self.gateway,)]), node('spark-b')]
        report = self.preflight('--values', str(self.values_file(self.values)))
        checks = self.image_checks(report)
        self.assertEqual([item['result'] for item in checks], ['WARN', 'FAIL'])
        self.assertIn('missing on spark-b. Kubelet lists at most 50 images per node.', checks[0]['detail'])
        self.assertIn('listed on 0 of 2', checks[1]['detail'])
        self.assertEqual(report['status'], 'blocked')

    def test_values_without_architecture_check_every_ready_node(self):
        values = {'image': {'repository': 'localhost/llm-stack/gateway', 'tag': 'dev-1', 'pullPolicy': 'Never'}}
        self.cluster.nodes = [node('spark-a', images=[(self.gateway,)]), node('amd64-a', arch='amd64', gpus=None)]
        report = self.preflight('--values', str(self.values_file(values)))
        self.assertIn('listed on 1 of 2 eligible node(s); missing on amd64-a', self.image_checks(report)[0]['detail'])

    def test_no_ready_node_with_the_selected_architecture_fails(self):
        self.cluster.nodes = [node('spark-a', ready=False)]
        report = self.preflight('--values', str(self.values_file(self.values)))
        checks = self.image_checks(report)
        self.assertEqual([(item['check'], item['result']) for item in checks], [('Images', 'FAIL')])
        self.assertIn('No Ready node matches the architecture', checks[0]['detail'])

    def test_yaml_values_warn_and_missing_values_fail(self):
        report = self.preflight('--values', str(self.values_file('image:\n  pullPolicy: Never\n')))
        self.assertEqual([(item['check'], item['result']) for item in self.image_checks(report)], [('Images', 'WARN')])
        self.assertIn('JSON values files only', self.image_checks(report)[0]['detail'])
        self.assertEqual(report['status'], 'ready')
        report = self.preflight('--values', '/nonexistent/llm-values.json')
        self.assertEqual([(item['check'], item['result']) for item in self.image_checks(report)], [('Images', 'FAIL')])
        self.assertIn('Cannot read values file', self.image_checks(report)[0]['detail'])
        self.assertEqual(report['status'], 'blocked')


class ConnectTests(unittest.TestCase):
    secret = 'sensitive-caller-value'

    def setUp(self):
        self.enterContext(patch.object(llm.subprocess, 'run', side_effect=AssertionError('Unexpected process')))
        self.output = self.enterContext(contextlib.redirect_stdout(io.StringIO()))
        self.errors = self.enterContext(contextlib.redirect_stderr(io.StringIO()))
        self.enterContext(patch.object(llm, 'access_material', return_value=('public-ca', self.secret)))
        self.process = MagicMock()
        self.process.poll.return_value = None
        self.commands = []
        self.files = []

    def start(self, port='18443'):
        def popen(command, **kwargs):
            self.commands.append(command)
            kwargs['stdout'].write(f'Forwarding from 127.0.0.1:{port} -> 8080\n')
            kwargs['stdout'].flush()
            return self.process
        return popen

    def held(self, outcome):
        def wait(timeout=None):
            if timeout is not None:
                return 0
            ca = Path(re.search(r"export LLM_GATEWAY_CA=(\S+)", self.output.getvalue())[1].strip("'"))
            key = ca.parent / 'api-key'
            self.files.extend((ca, key))
            self.assertEqual((ca.read_text(), key.read_text()), ('public-ca', self.secret))
            for path in (ca, key):
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            if outcome == 'exit':
                self.process.poll.return_value = 1
                return 1
            if outcome == 'sigterm':
                llm.terminate(signal.SIGTERM, None)
            raise KeyboardInterrupt
        return wait

    def connect(self, *arguments, port='18443'):
        with patch.object(llm.subprocess, 'Popen', side_effect=self.start(port)):
            return llm.main(['--context', 'target', 'connect', *arguments])

    def test_connect_forwards_fixed_port_and_prints_paths_without_the_secret(self):
        self.process.wait.side_effect = self.held('interrupt')
        with self.assertRaises(KeyboardInterrupt):
            self.connect()
        self.assertEqual(self.commands, [['kubectl', '--context', 'target', '--namespace', 'llm-stack', 'port-forward',
                                          'svc/llm-api-gateway', '18443:8080', '--address', '127.0.0.1']])
        output = self.output.getvalue()
        self.assertIn('Gateway for context target is open at https://127.0.0.1:18443/v1.', output)
        self.assertIn('export LLM_GATEWAY_URL=https://127.0.0.1:18443/v1\n', output)
        self.assertRegex(output, r'export LLM_API_KEY="\$\(cat \S+/api-key\)"')
        self.assertNotIn(self.secret, output + self.errors.getvalue())
        self.assertEqual(len(self.files), 2)
        self.assertFalse(any(path.exists() or path.parent.exists() for path in self.files))
        self.process.terminate.assert_called_once()

    def test_closed_port_forward_reports_and_removes_credentials(self):
        self.process.wait.side_effect = self.held('exit')
        with self.assertRaisesRegex(RuntimeError, 'Gateway connection closed: Forwarding from'):
            self.connect()
        self.assertFalse(any(path.exists() or path.parent.exists() for path in self.files))
        self.process.terminate.assert_not_called()

    def test_sigterm_removes_credentials(self):
        self.process.wait.side_effect = self.held('sigterm')
        with self.assertRaises(SystemExit) as stopped:
            self.connect()
        self.assertEqual(stopped.exception.code, 143)
        self.assertEqual(len(self.files), 2)
        self.assertFalse(any(path.exists() or path.parent.exists() for path in self.files))
        self.process.terminate.assert_called_once()

    def test_custom_port_and_port_validation(self):
        self.process.wait.side_effect = self.held('interrupt')
        with self.assertRaises(KeyboardInterrupt):
            self.connect('--port', '19000', port='19000')
        self.assertEqual(self.commands[0][7], '19000:8080')
        for port in ('0', '65536', '-1'):
            with self.subTest(port=port), patch.object(llm, 'selected_context') as context, self.assertRaises(SystemExit):
                self.connect('--port', port)
            context.assert_not_called()


if __name__ == '__main__':
    unittest.main()
