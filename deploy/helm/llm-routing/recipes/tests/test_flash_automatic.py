# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Automatic Flash-Next startup gates and rank lifecycle regressions."""
import json
import os
import pathlib
import socket
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch

import test_helm_sglang as helm_tests
from test_recipes import runtime


class FlashHelmTests(unittest.TestCase):
    render = helm_tests.HelmSGLangTests.render
    configuration = helm_tests.HelmSGLangTests.configuration

    def values(self, tp2=False):
        result = {'recipe': 'qwen3.8-flash-next', 'profileName': 'spark-nvfp4-nvme',
                  'nodes': ['gpu-node-1'], 'nodeCapabilities': {'gpu-node-1': {'localNvme': True}}}
        if tp2:
            result.update(profileName='spark-nvfp4-tp2', nodes=['gpu-node-1', 'gpu-node-2'],
                          nodeCapabilities={f'gpu-node-{rank+1}': {'fabric': 'test', 'interface': 'eth1',
                            'address': f'192.0.2.{rank+1}', 'linkGbps': 200} for rank in range(2)})
        return result

    def test_nvme_automatic_installs_pins_and_reserves_disposable_offload(self):
        docs = self.render(self.values())
        config = self.configuration(docs)
        self.assertEqual(config['profile']['id'], 'spark-nvfp4-nvme')
        pod = next(d for d in docs if d['kind'] == 'Deployment')['spec']['template']['spec']
        self.assertEqual(pod['containers'][0]['command'][-2:], ['/recipe/runtime.py', '0'])
        self.assertEqual(pod['containers'][0]['resources']['requests']['ephemeral-storage'], '56Gi')
        self.assertEqual(next(v for v in pod['volumes'] if v['name'] == 'ple')['emptyDir'], {'sizeLimit': '56Gi'})
        self.assertEqual(config['targets'], [{'node': 'gpu-node-1', 'localNvme': True}])
        self.assertFalse(any(d['kind'] == 'Job' for d in docs))

    def test_tp2_places_and_checks_each_rank_own_retained_cache(self):
        values = self.values(True)
        values['cache'] = {'existingClaims': ['retained-one', 'retained-two']}
        docs = self.render(values)
        self.assertFalse(any(d['kind'] == 'PersistentVolumeClaim' for d in docs))
        cm = next(d for d in docs if d['kind'] == 'ConfigMap')['data']
        config = self.configuration(docs)
        self.assertEqual(config['claimNames'], ['retained-one', 'retained-two'])
        self.assertEqual(config['release'], 'test-stack/test-model')
        for rank, doc in enumerate(d for d in docs if d['kind'] == 'Deployment'):
            pod = doc['spec']['template']['spec']
            self.assertTrue(pod['hostNetwork'])
            env = {entry['name']: entry.get('value') for entry in pod['containers'][0]['env']}
            self.assertEqual(env['NCCL_SOCKET_IFNAME'], '=eth1')
            if rank == 1:
                self.assertEqual(pod['containers'][0]['readinessProbe']['httpGet']['host'], config['targets'][rank]['address'])
            self.assertEqual(pod['containers'][0]['command'][-2:], ['/recipe/runtime.py', str(rank)])
            self.assertEqual(pod['initContainers'][0]['command'][-1], f'/recipe/placement-{rank}.json')
            placement = json.loads(cm[f'placement-{rank}.json'])
            self.assertEqual(placement['targets'], [config['targets'][rank]])
            self.assertEqual(placement['claimName'], config['claimNames'][rank])
            self.assertEqual(next(v for v in pod['volumes'] if v['name'] == 'cache')['persistentVolumeClaim']['claimName'], placement['claimName'])
            self.assertNotIn('cluster-access', [v['name'] for v in pod['containers'][0]['volumeMounts']])
        node_role = next(d for d in docs if d['kind'] == 'ClusterRole')['rules']
        cache_role = next(d for d in docs if d['kind'] == 'Role')['rules']
        self.assertEqual(node_role, [{'apiGroups': [''], 'resources': ['nodes'], 'resourceNames': values['nodes'], 'verbs': ['get']}])
        self.assertEqual(cache_role, [{'apiGroups': [''], 'resources': ['persistentvolumeclaims'], 'resourceNames': config['claimNames'], 'verbs': ['get']}])
        self.assertEqual(next(d for d in docs if d['kind'] == 'Service')['spec']['selector']['recipe-rank'], '0')

    def test_missing_or_incompatible_site_capabilities_are_rejected(self):
        cases = [(False, lambda v: v.pop('profileName'), 'profileName is required'),
                 (False, lambda v: v['nodeCapabilities'].clear(), 'localNvme=true'),
                 (False, lambda v: v['nodeCapabilities']['gpu-node-1'].update(localNvme='true'), 'localNvme=true'),
                 (True, lambda v: v['nodes'].__setitem__(1, 'gpu-node-1'), 'distinct node'),
                 (True, lambda v: v['nodeCapabilities']['gpu-node-2'].update(fabric='other'), 'same verified fabric'),
                 (True, lambda v: v['nodeCapabilities']['gpu-node-2'].update(linkGbps=100), 'profile minimum'),
                 (True, lambda v: v['nodeCapabilities']['gpu-node-2'].update(interface='../eth1'), 'valid nodeCapabilities.interface'),
                 (True, lambda v: v.update(cache={'existingClaim': 'shared'}), 'existingClaims'),
                 (True, lambda v: v.update(cache={'existingClaims': ['same', 'same']}), 'distinct cache'),
                 (True, lambda v: v.update(cache={'existingClaims': ['one']}), 'number of ranks'),
                 (True, lambda v: v.update(ports={'bootstrap': 34000}), 'ports must be distinct'),
                 (True, lambda v: v.update(ports={'bootstrap': 65536}), 'between 1 and 65535')]
        for tp2, mutate, message in cases:
            with self.subTest(message=message):
                values = self.values(tp2)
                mutate(values)
                self.assertIn(message, self.render(values, success=False))

    def test_tp2_suspend_withdraws_endpoint_and_preserves_rank_caches(self):
        docs = self.render(dict(self.values(True), suspended=True))
        self.assertEqual(len([d for d in docs if d['kind'] == 'PersistentVolumeClaim']), 2)
        self.assertTrue(all(d['metadata']['annotations']['helm.sh/resource-policy'] == 'keep' for d in docs if d['kind'] == 'PersistentVolumeClaim'))
        self.assertTrue(all(d['spec']['replicas'] == 0 for d in docs if d['kind'] == 'Deployment'))
        self.assertFalse(any(d['kind'] == 'InferenceEndpoint' for d in docs))


class FlashRuntimeTests(unittest.TestCase):
    setUp = helm_tests.AutomaticRuntimeTests.setUp

    def tp2(self):
        self.config.update(release='models/flash', generation=1,
                           targets=[{'node': f'node-{i}', 'fabric': 'test', 'interface': 'eth1', 'address': f'192.0.2.{i+1}', 'linkGbps': 200} for i in range(2)],
                           ports={'http': 30000, 'bootstrap': 38000, 'rendezvous': 34000})
        self.config['profile'].update(nodes=2, minFabricGbps=200)
        return self.config

    def test_offload_requires_actual_nvme_before_cleaning_and_enough_reserved_space(self):
        self.config['profile'].update(offload=True, offloadGiB=56)
        with tempfile.TemporaryDirectory() as temp:
            directory = pathlib.Path(temp)
            stale = directory / 'old-ple'
            stale.write_text('old')
            with patch.object(runtime, 'nvme_device', return_value=False), self.assertRaisesRegex(RuntimeError, 'backed by'):
                runtime.prepare_offload(self.config, directory)
            self.assertTrue(stale.exists())
            with patch.object(runtime, 'nvme_device', return_value=True), patch.object(runtime.shutil, 'disk_usage', return_value=Mock(free=55*runtime.GIB)), self.assertRaisesRegex(RuntimeError, 'reservation'):
                runtime.prepare_offload(self.config, directory)
            self.assertFalse(stale.exists())
            with patch.object(runtime, 'nvme_device', return_value=True), patch.object(runtime.shutil, 'disk_usage', return_value=Mock(free=56*runtime.GIB)):
                runtime.prepare_offload(self.config, directory)

    def test_nvme_backing_resolves_partitions_and_fails_unknown_device(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            partition = root / 'devices/nvme0/nvme0n1/nvme0n1p1'
            partition.mkdir(parents=True)
            (root / 'devices/nvme0/transport').write_text('pcie\n')
            block = root / 'block'
            block.mkdir()
            (block / '259:1').symlink_to(partition)
            self.assertTrue(runtime.nvme_device('259:1', block))
            self.assertFalse(runtime.nvme_device('8:1', block))
            sata = root / 'devices/sda/sda1'
            sata.mkdir(parents=True)
            (block / '8:1').symlink_to(sata)
            self.assertFalse(runtime.nvme_device('8:1', block))

    def test_nvme_requires_local_pcie_transport_and_rejects_fabrics_or_unknown(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            controller = root / 'devices/virtual/nvme-fabrics/ctl/nvme4'
            namespace = controller / 'nvme4n1'
            namespace.mkdir(parents=True)
            block = root / 'block'
            block.mkdir()
            (block / '259:4').symlink_to(namespace)
            for transport in ('tcp', 'rdma', 'fc', 'loop', 'pci', '', 'unknown'):
                with self.subTest(transport=transport):
                    (controller / 'transport').write_text(transport + '\n')
                    self.assertFalse(runtime.nvme_device('259:4', block))
            (controller / 'transport').unlink()
            self.assertFalse(runtime.nvme_device('259:4', block))

    def test_device_mapper_requires_every_backing_device_to_be_local_nvme(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            block = root / 'block'
            block.mkdir()
            mapped = root / 'devices/virtual/block/dm-0'
            (mapped / 'slaves').mkdir(parents=True)
            (mapped / 'dev').write_text('253:0\n')
            (block / '253:0').symlink_to(mapped)
            for number, transport in ((0, 'pcie'), (1, 'tcp')):
                controller = root / f'devices/nvme{number}'
                namespace = controller / f'nvme{number}n1'
                namespace.mkdir(parents=True)
                (controller / 'transport').write_text(transport + '\n')
                (namespace / 'dev').write_text(f'259:{number}\n')
                (block / f'259:{number}').symlink_to(namespace)
            (mapped / 'slaves/nvme0n1').symlink_to(root / 'devices/nvme0/nvme0n1')
            self.assertTrue(runtime.nvme_device('253:0', block))
            (mapped / 'slaves/nvme1n1').symlink_to(root / 'devices/nvme1/nvme1n1')
            self.assertFalse(runtime.nvme_device('253:0', block))
            (mapped / 'slaves/nvme1n1').unlink()
            (mapped / 'slaves/dm-0').symlink_to(mapped)
            self.assertFalse(runtime.nvme_device('253:0', block))

    def test_fabric_checks_actual_interface_address_state_and_speed(self):
        self.tp2()
        with tempfile.TemporaryDirectory() as temp:
            net = pathlib.Path(temp)
            interface = net / 'eth1'
            interface.mkdir()
            (interface / 'speed').write_text('200000\n')
            (interface / 'operstate').write_text('up\n')
            address = b'\0' * 20 + socket.inet_aton('192.0.2.1')
            with patch.object(runtime.fcntl, 'ioctl', return_value=address):
                runtime.check_fabric(self.config, 0, net)
                (interface / 'speed').write_text('100000\n')
                with self.assertRaisesRegex(RuntimeError, 'bandwidth'):
                    runtime.check_fabric(self.config, 0, net)
                (interface / 'speed').write_text('200000\n')
                (interface / 'operstate').write_text('down\n')
                with self.assertRaisesRegex(RuntimeError, 'not up'):
                    runtime.check_fabric(self.config, 0, net)
            with patch.object(runtime.fcntl, 'ioctl', return_value=b'\0'*20 + socket.inet_aton('192.0.2.3')), self.assertRaisesRegex(RuntimeError, 'does not belong'):
                runtime.check_fabric(self.config, 0, net)

    def test_distributed_starts_only_after_cache_pair_qualification_and_offload_gates(self):
        self.tp2()
        events = []
        barrier = Mock(state={})
        for method in ('start', 'pair', 'qualified', 'close'):
            getattr(barrier, method).side_effect = lambda method=method: events.append(method)
        def serve(*args, **kwargs):
            self.assertEqual(kwargs['peer_guard'], barrier.guard)
            self.assertEqual(barrier.state['phase'], 'serving')
            self.assertEqual(os.environ['HF_HUB_OFFLINE'], '1')
            events.append('serve')
            return 0
        with patch.dict(os.environ), patch.dict('sys.modules', {'torch': Mock()}), patch.object(runtime, 'host_memory', return_value=8*runtime.GIB), \
             patch.object(runtime, 'check_fabric', side_effect=lambda *args: events.append('fabric')), patch.object(runtime, 'check_hardware'), \
             patch.object(runtime, 'StartupBarrier', return_value=barrier), patch.object(runtime, 'prepare_checkpoint', side_effect=lambda *args: events.append('cache') or self.fixture.snapshot), \
             patch.object(runtime, 'qualify', side_effect=lambda *args: events.append('qualify')), patch.object(runtime, 'prepare_offload', side_effect=lambda *args: events.append('offload')), \
             patch.object(runtime, 'supervise', side_effect=serve), patch.object(runtime, 'log'):
            self.assertEqual(runtime.automatic(self.config, 1, self.cache), 0)
        self.assertEqual(events, ['fabric', 'start', 'cache', 'pair', 'qualify', 'qualified', 'offload', 'serve', 'close'])

    def test_failed_rank_qualification_closes_barrier_without_starting_server(self):
        self.tp2()
        barrier = Mock()
        with patch.dict('sys.modules', {'torch': Mock()}), patch.object(runtime, 'host_memory', return_value=8*runtime.GIB), \
             patch.object(runtime, 'check_fabric'), patch.object(runtime, 'check_hardware'), patch.object(runtime, 'StartupBarrier', return_value=barrier), \
             patch.object(runtime, 'prepare_checkpoint', return_value=self.fixture.snapshot), patch.object(runtime, 'qualify', side_effect=RuntimeError('collective failed')), \
             patch.object(runtime, 'supervise') as serve, self.assertRaisesRegex(RuntimeError, 'collective failed'):
            runtime.automatic(self.config, 0, self.cache)
        barrier.close.assert_called_once_with()
        barrier.qualified.assert_not_called()
        serve.assert_not_called()

    def test_single_node_offload_failure_never_starts_server(self):
        with patch.dict('sys.modules', {'torch': Mock()}), patch.object(runtime, 'host_memory', return_value=8*runtime.GIB), \
             patch.object(runtime, 'qualify'), patch.object(runtime, 'prepare_checkpoint', return_value=self.fixture.snapshot), \
             patch.object(runtime, 'prepare_offload', side_effect=RuntimeError('offload rejected')), patch.object(runtime, 'supervise') as serve, self.assertRaisesRegex(RuntimeError, 'offload rejected'):
            runtime.automatic(self.config, 0, self.cache)
        serve.assert_not_called()

    def test_peer_identity_attempt_and_reciprocal_pair_are_checked(self):
        config = self.tp2()
        local, peer = runtime.StartupBarrier(config, 0), runtime.StartupBarrier(config, 1)
        response = Mock()
        local.opener = Mock()
        local.opener.open.return_value.__enter__ = Mock(return_value=response)
        local.opener.open.return_value.__exit__ = Mock(return_value=False)
        with patch.object(runtime.json, 'load', side_effect=lambda _: peer.state.copy()):
            self.assertEqual(local.peer()['attempt'], peer.state['attempt'])
            peer.state['identity'] = 'another-release'
            self.assertIsNone(local.peer())
            peer.state['identity'] = local.state['identity']
            local.peer_attempt = peer.state['attempt']
            self.assertIsNone(local.peer()['peer'])  # The other rank can still be entering pair().
            peer.state['peer'] = 'another-attempt'
            with self.assertRaisesRegex(RuntimeError, 'different startup'):
                local.peer()
            peer.state['peer'] = local.state['attempt']
            peer.state['attempt'] = 'b' * 32
            with self.assertRaisesRegex(RuntimeError, 'restarted'):
                local.peer()

    def test_barrier_handles_different_cache_completion_times_without_pairing_stale_rank(self):
        config = self.tp2()
        left, right = runtime.StartupBarrier(config, 0), runtime.StartupBarrier(config, 1)
        errors = []
        def coordinate(barrier):
            try:
                barrier.pair()
                barrier.qualified()
                barrier.state['phase'] = 'serving'
            except Exception as error:
                errors.append(error)
        with patch.object(left, 'peer', side_effect=lambda: right.state.copy()), patch.object(right, 'peer', side_effect=lambda: left.state.copy()):
            first = threading.Thread(target=coordinate, args=(left,), daemon=True)
            second = threading.Thread(target=coordinate, args=(right,), daemon=True)
            first.start()
            self.assertEqual(right.state['phase'], 'preparing')
            second.start()
            first.join(8)
            second.join(8)
        self.assertFalse(first.is_alive() or second.is_alive())
        self.assertEqual(errors, [])
        self.assertEqual(left.state['peer'], right.state['attempt'])
        self.assertEqual(right.state['peer'], left.state['attempt'])
        self.assertEqual((left.state['phase'], right.state['phase']), ('serving', 'serving'))

    def test_missing_peer_has_bounded_startup_wait_and_serving_grace(self):
        barrier = runtime.StartupBarrier(self.tp2(), 0)
        with patch.object(barrier, 'peer', return_value=None), patch.object(runtime.time, 'monotonic', side_effect=[0, 0, 2]), patch.object(runtime.time, 'sleep'), self.assertRaisesRegex(RuntimeError, 'Timed out'):
            barrier.wait(('ready',), timeout=1)
        barrier.last_peer = 0
        with patch.object(barrier, 'peer', return_value=None), patch.object(runtime.time, 'monotonic', return_value=31), self.assertRaisesRegex(RuntimeError, 'stopped responding'):
            barrier.guard()

    def test_truncated_peer_status_recovers_within_grace_and_stops_after_expiry(self):
        config = self.tp2()
        local, peer = runtime.StartupBarrier(config, 0), runtime.StartupBarrier(config, 1)
        local.peer_attempt = peer.state['attempt']
        local.last_peer = 0
        peer.state.update(peer=local.state['attempt'], phase='serving')
        response = Mock()
        response.read.side_effect = [runtime.http.client.IncompleteRead(b'{', 100),
                                     json.dumps(peer.state).encode()]
        local.opener = Mock()
        local.opener.open.return_value.__enter__ = Mock(return_value=response)
        local.opener.open.return_value.__exit__ = Mock(return_value=False)
        with patch.object(runtime.time, 'monotonic', return_value=10):
            local.guard()
            self.assertEqual(local.last_peer, 0)
            local.guard()
            self.assertEqual(local.last_peer, 10)
        response.read.side_effect = runtime.http.client.IncompleteRead(b'{', 100)
        with patch.object(runtime.time, 'monotonic', return_value=41), self.assertRaisesRegex(RuntimeError, 'stopped responding'):
            local.guard()

    def test_supervisor_stops_child_when_peer_guard_fails(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            (root / 'memory.swap.current').write_text('0')
            child = Mock()
            child.poll.return_value = None
            with patch.object(runtime.subprocess, 'Popen', return_value=child), patch.object(runtime.signal, 'signal'), patch.object(runtime, 'stop') as stop, \
                 self.assertRaisesRegex(RuntimeError, 'peer failed'):
                runtime.supervise(['model'], self.config, root, root, peer_guard=Mock(side_effect=RuntimeError('peer failed')))
            stop.assert_called_once_with(child)


if __name__ == '__main__':
    unittest.main()
