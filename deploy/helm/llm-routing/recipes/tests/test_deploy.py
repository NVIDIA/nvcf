# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import io
import json
import unittest
from unittest.mock import patch

import test_stack_binding as binding

spark = binding.spark


class DeployTests(unittest.TestCase):
    setUp = binding.StackBindingTests.setUp
    output = binding.StackBindingTests.output
    attach = binding.StackBindingTests.attach

    @contextlib.contextmanager
    def phase_actions(self, failure=None):
        events = []
        def phase(name, checkpoint):
            events.append(name)
            if name == failure:
                raise RuntimeError('fixture failure')
            self.recipe.stamp(checkpoint)
        with contextlib.ExitStack() as stack:
            stack.enter_context(patch.object(self.recipe, 'inventory', side_effect=lambda: phase('inventory', 'inventory')))
            stack.enter_context(patch.object(self.recipe, 'backend_phase', side_effect=lambda name: phase(name, name)))
            stack.enter_context(patch.object(self.recipe, 'verify', side_effect=lambda gateway, port:
                phase('verify-gateway' if gateway else 'verify-direct', 'gateway' if gateway else 'direct')))
            stack.enter_context(patch.object(self.recipe, 'register', side_effect=lambda: phase('register', 'registered')))
            yield events

    def test_deploy_orders_model_phases_and_verifies_shared_gateway(self):
        self.attach()
        with self.phase_actions() as events, patch.object(spark, 'run') as run, patch.object(spark, 'output') as output, \
             patch.object(self.recipe, 'deploy_stack') as infrastructure, contextlib.redirect_stdout(io.StringIO()):
            self.recipe.deploy(18443)
        self.assertEqual(events, ['inventory', 'preflight', 'build', 'qualify', 'download', 'serve',
                                  'verify-direct', 'register', 'verify-gateway'])
        self.assertEqual(self.recipe.state['deploy'], {'phase': 'verify-gateway', 'status': 'complete'})
        self.assertTrue(self.recipe.state['gateway'])
        self.assertTrue(self.recipe.state['registered'])
        run.assert_not_called()
        output.assert_not_called()
        infrastructure.assert_not_called()

    def test_failure_preserves_checkpoints_and_stops_before_later_phases(self):
        self.attach()
        with self.phase_actions(failure='download') as events, self.assertRaisesRegex(RuntimeError, 'stopped during download.*Progress is saved.*ADVANCED.md'):
            self.recipe.deploy(18443)
        self.assertEqual(events, ['inventory', 'preflight', 'build', 'qualify', 'download'])
        self.assertTrue(self.recipe.state['qualify'])
        self.assertNotIn('download', self.recipe.state)
        self.assertNotIn('serve', self.recipe.state)
        self.assertEqual(json.loads(self.recipe.state_path.read_text()), self.recipe.state)
        before = self.recipe.state_path.read_bytes()
        with patch.object(self.recipe, 'inventory') as inventory, self.assertRaisesRegex(RuntimeError, 'individual phases'):
            self.recipe.deploy(18443)
        inventory.assert_not_called()
        self.assertEqual(self.recipe.state_path.read_bytes(), before)

    def test_started_or_loaded_model_is_rejected_before_inventory(self):
        self.attach()
        original = dict(self.recipe.state)
        for checkpoint in ('preflight', 'build', 'runtimeSha256', 'qualify', 'download', 'serve', 'direct', 'registered', 'gateway'):
            self.recipe.state = dict(original, **{checkpoint: False})
            with self.subTest(checkpoint=checkpoint), patch.object(self.recipe, 'inventory') as inventory, \
                 self.assertRaisesRegex(RuntimeError, 'already started'):
                self.recipe.deploy(18443)
            inventory.assert_not_called()

    def test_previously_inventoried_model_is_checked_for_collisions_again(self):
        self.attach()
        self.recipe.stamp('inventory', {'nodes': {node['metadata']['name']: node['metadata']['uid'] for node in self.nodes}})
        before = self.recipe.state_path.read_bytes()
        with patch.object(self.recipe, 'bound_cluster') as bound, \
             patch.object(self.recipe, 'check_model_absent', side_effect=RuntimeError('Model already exists')) as absent, \
             patch.object(self.recipe, 'inventory') as inventory, self.assertRaisesRegex(RuntimeError, 'already exists'):
            self.recipe.deploy(18443)
        bound.assert_called_once()
        absent.assert_called_once()
        inventory.assert_not_called()
        self.assertEqual(self.recipe.state_path.read_bytes(), before)

    def test_deploy_requires_external_binding_and_unchanged_saved_model_configuration(self):
        with patch.object(self.recipe, 'inventory') as inventory, self.assertRaisesRegex(RuntimeError, 'requires a shared stack connection'):
            self.recipe.deploy(18443)
        inventory.assert_not_called()
        self.attach()
        (self.work/'config.json').write_text('{}')
        with patch.object(self.recipe, 'inventory') as inventory, self.assertRaisesRegex(RuntimeError, 'Saved model configuration differs'):
            self.recipe.deploy(18443)
        inventory.assert_not_called()

    def test_cli_fresh_deploy_attaches_then_runs_all_model_steps(self):
        path = self.root/'model.json'
        path.write_text(json.dumps(self.model))
        with patch.object(spark.stack_binding, 'load_connection', return_value=self.connection), \
             patch.object(spark.stack_binding, 'inspect_connection', return_value=self.inspection), \
             patch.object(spark, 'output', side_effect=self.output), \
             patch.object(spark.Recipe, 'deploy', autospec=True) as deploy, contextlib.redirect_stdout(io.StringIO()):
            spark.main(['--config', str(path), '--work-dir', str(self.work), 'deploy', '--stack-connection', str(self.root/'connection.json'), '--port', '19443'])
        instance, port = deploy.call_args.args
        self.assertEqual(port, 19443)
        self.assertTrue(instance.state['attachedStack'])
        self.assertNotIn('serve', instance.state)
        self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)
        self.assertEqual(json.loads(path.read_text()), self.model)

    def test_cli_existing_attached_workdir_does_not_reattach_or_discover(self):
        self.attach()
        with patch.object(spark.Recipe, 'attach_stack') as attach, patch.object(spark.Recipe, 'deploy') as deploy, \
             patch.object(spark, 'discover_config') as discover:
            spark.main(['--work-dir', str(self.work), 'deploy'])
        deploy.assert_called_once_with(18443)
        attach.assert_not_called()
        discover.assert_not_called()

    def test_cli_deploy_without_configuration_explains_required_connection(self):
        with patch.dict(spark.os.environ, {'LLM_ROUTING_CONTEXT': 'shared-context'}), \
             patch.object(spark, 'discover_config') as discover, self.assertRaisesRegex(RuntimeError, '--stack-connection'):
            spark.main(['--work-dir', str(self.work), 'deploy'])
        discover.assert_not_called()


if __name__ == '__main__':
    unittest.main()
