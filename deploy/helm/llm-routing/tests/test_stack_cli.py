# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import json
import pathlib
import tempfile
import unittest
from unittest.mock import Mock, patch

from test_stack import stack


class InstallFlowTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.work = pathlib.Path(self.directory.name)
        self.config = {'context': 'cluster', 'namespace': 'models'}
        self.source = self.work/'source.json'
        self.source.write_text(json.dumps(self.config))
        self.instance = Mock(config=self.config, work=self.work)
        self.arguments = ['--config', str(self.source), '--work-dir', str(self.work), 'install']

    def test_install_verifies_empty_gateway_after_saving_its_configuration(self):
        events = []
        self.instance.install.side_effect = lambda: events.append('installed')
        def verify(*args):
            self.assertEqual(events, ['installed'])
            self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)
            self.assertEqual(args, (True, None, 18477))
            events.append('verified')
        self.instance.verify.side_effect = verify
        with patch.object(stack, 'Stack', return_value=self.instance):
            stack.main(self.arguments)
        self.assertEqual(events, ['installed', 'verified'])

    def test_install_failure_does_not_report_or_run_gateway_verification(self):
        self.instance.install.side_effect = ValueError('installation failed')
        with patch.object(stack, 'Stack', return_value=self.instance), self.assertRaisesRegex(ValueError, 'installation failed'):
            stack.main(self.arguments)
        self.instance.verify.assert_not_called()
        self.assertFalse((self.work/'config.json').exists())

    def test_verification_failure_retains_config_for_diagnostics(self):
        self.instance.verify.side_effect = ValueError('gateway check failed')
        with patch.object(stack, 'Stack', return_value=self.instance), self.assertRaisesRegex(ValueError, 'gateway check failed'):
            stack.main(self.arguments)
        self.assertEqual(json.loads((self.work/'config.json').read_text()), self.config)

    def test_chat_uses_saved_config_and_explicit_model(self):
        (self.work/'config.json').write_text(json.dumps(self.config))
        with patch.object(stack, 'Stack', return_value=self.instance) as constructor:
            stack.main(['--work-dir', str(self.work), 'chat', '--model', 'selected-model', 'hello'])
        constructor.assert_called_once_with(self.config, self.work)
        self.instance.chat.assert_called_once_with('selected-model', 'hello', False, 18477)

    def test_missing_saved_config_fails_before_creating_stack(self):
        with patch.object(stack, 'Stack') as constructor, self.assertRaisesRegex(ValueError, 'Provide --config'):
            stack.main(['--work-dir', str(self.work), 'install'])
        constructor.assert_not_called()
