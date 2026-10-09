# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Cluster preparation scripts, run against stub node and ssh commands."""
import hashlib
import os
from pathlib import Path
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import unittest

CLUSTER = Path(__file__).resolve().parents[1] / 'cluster'
BASH = shutil.which('bash')
TOOLS = ('awk', 'cat', 'chmod', 'cp', 'dirname', 'grep', 'head', 'mkdir', 'mktemp', 'mv', 'paste',
         'rm', 'sed', 'seq', 'sh', 'sort', 'tail', 'tr', 'wc')
STATIC = '2: eth1    inet 192.0.2.10/24 brd 192.0.2.255 scope global noprefixroute eth1\\       valid_lft forever'
DYNAMIC = '2: eth1    inet 192.0.2.10/24 brd 192.0.2.255 scope global dynamic noprefixroute eth1\\       valid_lft 86000sec'
FAKE_INSTALLER = ('#!/bin/sh\n'
                  '{ echo "args: $*"; echo "version: $INSTALL_K3S_VERSION"; echo "token: ${K3S_TOKEN:-}"; } > "$STUB_RECORD"\n')
STUBS = {
    'uname': 'echo Linux',
    'hostname': 'echo node1',
    'id': 'echo "${STUB_UID:-0}"',
    'ip': 'printf "%s\\n" "$STUB_IP_ADDR"',
    'timedatectl': 'echo "${STUB_NTP:-yes}"',
    'systemctl': 'for s in $STUB_ACTIVE; do [ "$s" = "$3" ] && exit 0; done; exit 3',
    'nvidia-smi': '[ -n "$STUB_GPUS" ] || exit 9; printf "%b\\n" "$STUB_GPUS"',
    'nvidia-container-runtime': 'exit 0',
    'timeout': 'exit "${STUB_REACH:-0}"',
    'sleep': 'exit 0',
    'curl': 'while [ $# -gt 0 ]; do [ "$1" = -o ] && cp "$STUB_INSTALLER" "$2"; shift; done',
    'k3s': 'case "$1" in --version) echo "k3s version v1.36.5+k3s1 (stub)" ;; kubectl) echo True ;; esac',
}
SHA256SUM = (f'#!{sys.executable}\n'
             'import hashlib, sys\n'
             'expected, path = sys.stdin.read().split()\n'
             'sys.exit(0 if hashlib.sha256(open(path, "rb").read()).hexdigest() == expected else 1)\n')


@unittest.skipUnless(BASH, 'bash is required')
class ScriptTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.bin = self.work / 'bin'
        self.bin.mkdir()
        for tool in TOOLS:
            os.symlink(shutil.which(tool), self.bin / tool)
        os.symlink(BASH, self.bin / 'bash')

    def stub(self, name, body):
        path = self.bin / name
        path.write_text(body if body.startswith('#!') else '#!/bin/sh\n' + body + '\n')
        path.chmod(0o755)

    def run_script(self, script, *args, stdin='', **env):
        environment = {'PATH': str(self.bin), 'HOME': str(self.work), 'LC_ALL': 'C'}
        environment.update(env)
        return subprocess.run([BASH, str(CLUSTER / script), *args], env=environment, input=stdin,
                              capture_output=True, text=True, timeout=60)


class InstallK3sTests(ScriptTest):
    def setUp(self):
        super().setUp()
        for name in ('uname', 'hostname', 'id', 'ip', 'timedatectl', 'systemctl', 'nvidia-smi',
                     'nvidia-container-runtime', 'timeout', 'sleep', 'curl'):
            self.stub(name, STUBS[name])
        self.stub('sha256sum', SHA256SUM)
        self.token = self.work / 'k3s-token'
        self.token.write_text('secret-join-token\n')
        self.token.chmod(0o600)
        self.record = self.work / 'installer-record'
        self.installer = self.work / 'fake-install.sh'
        self.installer.write_text(FAKE_INSTALLER)

    def install(self, *args, **env):
        defaults = {'STUB_IP_ADDR': STATIC, 'STUB_GPUS': 'NVIDIA GB10', 'STUB_RECORD': str(self.record),
                    'STUB_INSTALLER': str(self.installer)}
        defaults.update(env)
        return self.run_script('install-k3s.sh', *args, **defaults)

    def test_first_server_dry_run_prints_pinned_install_command(self):
        result = self.install('server', '--node-ip', '192.0.2.10', '--tls-san', 'k3s.example.com', '--dry-run')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('PASS  192.0.2.10 is a static address on eth1', result.stdout)
        self.assertIn('label nvidia.com/gpu.product=NVIDIA-GB10', result.stdout)
        for expected in ('INSTALL_K3S_VERSION=v1.36.5+k3s1 sh install.sh server --cluster-init',
                         '--node-ip 192.0.2.10 --node-external-ip 192.0.2.10 --flannel-iface eth1',
                         '--node-label nvidia.com/gpu.product=NVIDIA-GB10',
                         '--tls-san 192.0.2.10 --tls-san k3s.example.com',
                         'k3s-io/k3s/v1.36.5%2Bk3s1/install.sh',
                         'Dry run: nothing installed.'):
            self.assertIn(expected, result.stdout)
        self.assertNotIn('--write-kubeconfig-mode', result.stdout)
        self.assertFalse(self.record.exists())

    def test_dhcp_address_warns_to_reserve_it(self):
        result = self.install('server', '--node-ip', '192.0.2.10', '--dry-run', STUB_IP_ADDR=DYNAMIC)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('WARN  192.0.2.10 on eth1 is DHCP-assigned. Reserve it', result.stdout)

    def test_failed_checks_stop_before_install(self):
        (self.bin / 'nvidia-container-runtime').unlink()
        result = self.install('server', '--node-ip', '192.0.2.99', STUB_NTP='no', STUB_GPUS='')
        self.assertEqual(result.returncode, 1)
        for expected in ('FAIL  192.0.2.99 is not assigned to any interface',
                         'FAIL  clock not synchronized',
                         'FAIL  nvidia-smi found no GPU',
                         'FAIL  nvidia-container-runtime not found',
                         '1 passed, 0 warnings, 4 failed'):
            self.assertIn(expected, result.stdout)
        self.assertNotIn('Install command', result.stdout)
        self.assertNotIn('--flannel-iface', result.stdout)

    def test_mixed_gpu_models_fail(self):
        result = self.install('server', '--node-ip', '192.0.2.10', '--dry-run', STUB_GPUS='NVIDIA GB10\\nNVIDIA GB300')
        self.assertEqual(result.returncode, 1)
        self.assertIn('FAIL  GPU models differ on this node: NVIDIA GB10,NVIDIA GB300', result.stdout)

    def test_agent_dry_run_reads_token_without_printing_it(self):
        result = self.install('agent', '--join', '192.0.2.10', '--token-file', str(self.token),
                              '--node-ip', '192.0.2.10', '--dry-run')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('server 192.0.2.10 answers on 6443/tcp', result.stdout)
        self.assertIn(f'K3S_TOKEN=<from {self.token}> sh install.sh agent --server https://192.0.2.10:6443', result.stdout)
        self.assertNotIn('secret-join-token', result.stdout + result.stderr)
        self.assertNotIn('--cluster-init', result.stdout)
        self.assertNotIn('--tls-san', result.stdout)

    def test_unreachable_server_and_missing_token_fail(self):
        result = self.install('server', '--join', '192.0.2.10', '--token-file', str(self.work / 'missing'),
                              '--node-ip', '192.0.2.10', '--dry-run', STUB_REACH='1')
        self.assertEqual(result.returncode, 1)
        self.assertIn('FAIL  cannot read a join token from', result.stdout)
        self.assertIn('FAIL  cannot reach 192.0.2.10 on 6443/tcp', result.stdout)

    def test_active_firewall_warns_with_role_ports(self):
        self.stub('ufw', 'echo "Status: active"')
        result = self.install('agent', '--join', '192.0.2.10', '--token-file', str(self.token),
                              '--node-ip', '192.0.2.10', '--dry-run')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('WARN  ufw is active: allow 8472/udp and 10250/tcp between cluster nodes, and traffic from '
                      'the pod and service networks 10.42.0.0/16 and 10.43.0.0/16', result.stdout)

    def test_usage_errors(self):
        cases = {
            ('agent', '--node-ip', '192.0.2.10'): 'an agent needs --join',
            ('server', '--join', '192.0.2.10', '--node-ip', '192.0.2.10'): '--join needs --token-file',
            ('server', '--node-ip', 'node1'): '--node-ip must be an IPv4 address',
            ('server', '--node-ip', '192.0.2.10', '--k3s-version', 'v1.35.0+k3s1'): 'needs --installer-sha256',
            ('agent', '--join', 'x', '--token-file', 't', '--node-ip', '192.0.2.10', '--tls-san', 'a'): 'servers only',
            ('server', '--node-ip', '192.0.2.10', '--kubeconfig-mode', 'rw'): 'must be octal',
            ('server', '--node-ip', '192.0.2.10', '--kubeconfig-mode', '666'): 'must not let other users write',
            ('cluster',): 'must be server or agent',
        }
        for args, message in cases.items():
            with self.subTest(args=args):
                result = self.install(*args, '--dry-run')
                self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
                self.assertIn(message, result.stderr)

    def test_install_requires_root(self):
        result = self.install('server', '--node-ip', '192.0.2.10', STUB_UID='1000')
        self.assertEqual(result.returncode, 2)
        self.assertIn('run as root', result.stderr)

    def test_running_k3s_is_left_unchanged(self):
        self.stub('k3s', STUBS['k3s'])
        result = self.install('server', '--node-ip', '192.0.2.10', STUB_ACTIVE='k3s')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('k3s v1.36.5+k3s1 is already installed and k3s is running. No changes made.', result.stdout)
        self.assertNotIn('Checking', result.stdout)
        self.assertFalse(self.record.exists())

    def test_join_passes_token_only_through_the_environment(self):
        digest = hashlib.sha256(FAKE_INSTALLER.encode()).hexdigest()
        result = self.install('agent', '--join', '192.0.2.10', '--token-file', str(self.token),
                              '--node-ip', '192.0.2.10', '--installer-sha256', digest, STUB_ACTIVE='k3s-agent')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        record = self.record.read_text().splitlines()
        self.assertEqual(record[0], 'args: agent --server https://192.0.2.10:6443 --node-ip 192.0.2.10 '
                                    '--node-external-ip 192.0.2.10 --flannel-iface eth1 '
                                    '--node-label nvidia.com/gpu.product=NVIDIA-GB10')
        self.assertEqual(record[1:], ['version: v1.36.5+k3s1', 'token: secret-join-token'])
        self.assertNotIn('secret-join-token', result.stdout + result.stderr)
        self.assertIn('Agent installed.', result.stdout)

    def test_first_server_install_waits_for_ready_node(self):
        self.stub('k3s-after-install', STUBS['k3s'])
        installer = FAKE_INSTALLER + f'mv "{self.bin}/k3s-after-install" "{self.bin}/k3s"\n'
        self.installer.write_text(installer)
        digest = hashlib.sha256(installer.encode()).hexdigest()
        result = self.install('server', '--node-ip', '192.0.2.10', '--kubeconfig-mode', '644',
                              '--installer-sha256', digest)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        args, version, token = self.record.read_text().splitlines()
        self.assertIn('server --cluster-init --node-ip 192.0.2.10', args)
        self.assertIn('--write-kubeconfig-mode 644', args)
        self.assertEqual(token, 'token: ')
        self.assertIn('First server installed.', result.stdout)
        self.assertIn('--join 192.0.2.10', result.stdout)

    def test_server_join_dry_run_joins_existing_etcd(self):
        result = self.install('server', '--join', 'k3s.example.com', '--token-file', str(self.token),
                              '--node-ip', '192.0.2.10', '--tls-san', 'k3s.example.com', '--dry-run')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('sh install.sh server --server https://k3s.example.com:6443 --node-ip 192.0.2.10', result.stdout)
        self.assertIn('--tls-san 192.0.2.10 --tls-san k3s.example.com', result.stdout)
        self.assertNotIn('--cluster-init', result.stdout)
        self.assertNotIn('secret-join-token', result.stdout + result.stderr)

    def test_join_probe_connects_to_the_server_api_port(self):
        self.stub('timeout', 'shift; exec "$@"')
        listener = socket.socket()
        try:
            listener.bind(('127.0.0.1', 6443))
        except OSError:
            listener.close()
            self.skipTest('127.0.0.1:6443 is in use')
        listener.listen(1)
        args = ('agent', '--join', '127.0.0.1', '--token-file', str(self.token), '--node-ip', '192.0.2.10', '--dry-run')
        with listener:
            result = self.install(*args)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('PASS  server 127.0.0.1 answers on 6443/tcp', result.stdout)
        result = self.install(*args)
        self.assertEqual(result.returncode, 1)
        self.assertIn('FAIL  cannot reach 127.0.0.1 on 6443/tcp', result.stdout)

    def test_whitespace_only_token_fails(self):
        self.token.write_text(' \n\n')
        result = self.install('agent', '--join', '192.0.2.10', '--token-file', str(self.token),
                              '--node-ip', '192.0.2.10', '--dry-run')
        self.assertEqual(result.returncode, 1)
        self.assertIn(f'FAIL  cannot read a join token from {self.token}', result.stdout)

    def test_node_ip_inside_k3s_networks_fails(self):
        result = self.install('server', '--node-ip', '10.42.0.5', '--dry-run',
                              STUB_IP_ADDR=STATIC.replace('192.0.2.10', '10.42.0.5'))
        self.assertEqual(result.returncode, 1)
        self.assertIn('FAIL  10.42.0.5 is inside the k3s default pod (10.42.0.0/16) or service (10.43.0.0/16) network',
                      result.stdout)

    def test_gpu_label_matches_gpu_feature_discovery(self):
        result = self.install('server', '--node-ip', '192.0.2.10', '--dry-run', STUB_GPUS='NVIDIA  GB10 (x)')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('--node-label nvidia.com/gpu.product=NVIDIA-GB10-x', result.stdout)

    def test_running_k3s_with_another_role_is_an_error(self):
        self.stub('k3s', STUBS['k3s'])
        result = self.install('agent', '--join', '192.0.2.10', '--token-file', str(self.token),
                              '--node-ip', '192.0.2.10', STUB_ACTIVE='k3s')
        self.assertEqual(result.returncode, 1)
        self.assertIn('k3s is already running here as server (k3s), not agent. No changes made.', result.stderr)

    def test_node_not_ready_fails_install(self):
        self.stub('k3s-after-install', 'case "$1" in kubectl) echo False ;; esac')
        installer = FAKE_INSTALLER + f'mv "{self.bin}/k3s-after-install" "{self.bin}/k3s"\n'
        self.installer.write_text(installer)
        result = self.install('server', '--node-ip', '192.0.2.10',
                              '--installer-sha256', hashlib.sha256(installer.encode()).hexdigest())
        self.assertEqual(result.returncode, 1)
        self.assertIn('Node node1 is not Ready after 2 minutes', result.stderr)
        self.assertNotIn('First server installed.', result.stdout)

    def test_checksum_mismatch_does_not_run_installer(self):
        result = self.install('server', '--node-ip', '192.0.2.10')
        self.assertEqual(result.returncode, 1)
        self.assertIn('install.sh checksum mismatch. Not installing.', result.stderr)
        self.assertFalse(self.record.exists())


K3S_KUBECONFIG = """apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: Y2E=
    server: https://127.0.0.1:6443
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    client-certificate-data: Y2VydA==
    client-key-data: a2V5
"""


@unittest.skipUnless(shutil.which('kubectl'), 'kubectl is required')
class FetchKubeconfigTests(ScriptTest):
    def setUp(self):
        super().setUp()
        os.symlink(shutil.which('kubectl'), self.bin / 'kubectl')
        self.remote = self.work / 'remote-k3s.yaml'
        self.remote.write_text(K3S_KUBECONFIG)
        self.calls = self.work / 'ssh-calls'
        self.stub('ssh', f'echo "$@" >> "{self.calls}"\n'
                         'case "$*" in\n'
                         '  *"sudo -S"*) IFS= read -r password; [ "$password" = "$STUB_PASSWORD" ] && cat "$STUB_REMOTE" ;;\n'
                         '  *) [ -n "$STUB_READABLE" ] && cat "$STUB_REMOTE" ;;\n'
                         'esac')
        self.output = self.work / 'my-cluster.yaml'

    def fetch(self, *args, context='my-cluster', readable='1', stdin=''):
        return self.run_script('fetch-kubeconfig.sh', '--no-check', '--output', str(self.output), *args,
                               'node1', 'k3s.example.com', context, stdin=stdin, STUB_REMOTE=str(self.remote),
                               STUB_READABLE=readable, STUB_PASSWORD='correct horse')

    def kubeconfig(self, jsonpath):
        return subprocess.run(['kubectl', '--kubeconfig', str(self.output), 'config', 'view', '-o',
                               f'jsonpath={jsonpath}'], capture_output=True, text=True, check=True).stdout

    def test_renames_context_and_points_at_api_host(self):
        result = self.fetch()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(stat.S_IMODE(self.output.stat().st_mode), 0o600)
        self.assertEqual(self.kubeconfig('{.current-context}'), 'my-cluster')
        self.assertEqual(self.kubeconfig('{.clusters[0].name} {.users[0].name}'), 'my-cluster my-cluster')
        self.assertEqual(self.kubeconfig('{.clusters[0].cluster.server}'), 'https://k3s.example.com:6443')
        self.assertIn('sudo -n cat /etc/rancher/k3s/k3s.yaml', self.calls.read_text())
        self.assertEqual(sorted(path.name for path in self.work.iterdir() if path.name.startswith('.')), [])

    def test_existing_output_needs_force(self):
        self.output.write_text('keep me')
        result = self.fetch()
        self.assertEqual(result.returncode, 2)
        self.assertEqual(self.output.read_text(), 'keep me')
        self.assertEqual(self.fetch('--force').returncode, 0)
        self.assertEqual(self.kubeconfig('{.current-context}'), 'my-cluster')

    def test_asks_for_the_sudo_password_when_the_file_is_private(self):
        result = self.fetch(readable='', stdin='correct horse\n')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('sudo password on node1:', result.stderr)
        self.assertNotIn('correct horse', result.stdout + result.stderr + self.calls.read_text())
        self.assertEqual(self.kubeconfig('{.current-context}'), 'my-cluster')

    def test_wrong_or_missing_sudo_password_fails(self):
        for stdin in ('wrong\n', ''):
            with self.subTest(stdin=stdin):
                result = self.fetch(readable='', stdin=stdin)
                self.assertEqual(result.returncode, 2)
                self.assertIn('could not read /etc/rancher/k3s/k3s.yaml on node1', result.stderr)
                self.assertFalse(self.output.exists())

    def test_rejects_default_context(self):
        result = self.fetch(context='default')
        self.assertEqual(result.returncode, 2)
        self.assertIn("not 'default'", result.stderr)

    def test_rejects_a_file_that_is_not_a_k3s_server_kubeconfig(self):
        self.remote.write_text(K3S_KUBECONFIG.replace('https://127.0.0.1:6443', 'https://203.0.113.5:6443'))
        result = self.fetch()
        self.assertEqual(result.returncode, 2)
        self.assertIn('does not look like a k3s server kubeconfig', result.stderr)
        self.assertFalse(self.output.exists())


class ValidateClusterTests(ScriptTest):
    def test_context_and_on_are_exclusive(self):
        result = self.run_script('validate-cluster.sh', '--context', 'my-cluster', '--on', 'node1')
        self.assertEqual(result.returncode, 2)
        self.assertIn('--context and --on cannot be combined', result.stderr)

    def test_context_is_validated(self):
        result = self.run_script('validate-cluster.sh', '--context', 'my cluster')
        self.assertEqual(result.returncode, 2)
        self.assertIn('invalid --context', result.stderr)


class ScriptHygieneTests(unittest.TestCase):
    def test_scripts_are_executable_and_ascii(self):
        for path in CLUSTER.iterdir():
            with self.subTest(path=path.name):
                path.read_bytes().decode('ascii')
                if path.suffix == '.sh':
                    self.assertTrue(os.access(path, os.X_OK))


if __name__ == '__main__':
    unittest.main()
