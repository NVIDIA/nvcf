# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import copy
import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('chat_recipe', HERE/'spark.py')
spark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(spark)


class ChatTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        config = json.loads((HERE/'config.example.json').read_text())
        self.recipe = spark.Recipe(config, self.tmp.name)
        self.recipe.state = {'stack': {'apiKeyFile': None}, 'gateway': False}
        self.events = []

    @contextlib.contextmanager
    def forward(self, gateway, port):
        self.assertTrue(gateway)
        self.assertEqual(port, 18443)
        self.events.append('connected')
        try:
            yield
        finally:
            self.events.append('disconnected')

    @contextlib.contextmanager
    def key(self, recipe, url):
        self.assertIs(recipe, self.recipe)
        self.assertEqual(url, 'https://127.0.0.1:18443')
        self.assertEqual(self.events, ['connected'])
        self.events.append('registered')
        try:
            yield self.recipe.work/'temporary-gateway-key'
        finally:
            self.events.append('revoked')

    def test_custom_and_streaming_chat_use_temporary_key_then_revoke(self):
        for streaming in (False, True):
            self.events = []
            before = copy.deepcopy(self.recipe.state)
            with self.subTest(streaming=streaming), patch.object(self.recipe, 'bound_cluster') as bound, \
                 patch.object(self.recipe, 'forward', self.forward), \
                 patch.object(spark.gateway_access, 'temporary_gateway_key', self.key), \
                 patch.object(spark, 'run') as run:
                self.recipe.chat('How many planets are in the solar system?', streaming, 18443)
            bound.assert_called_once()
            command = run.call_args.args[0]
            self.assertEqual(command[command.index('--mode') + 1], 'chat')
            self.assertEqual(command[command.index('--url') + 1], 'https://127.0.0.1:18443')
            self.assertEqual(command[command.index('--ca-file') + 1], str(self.recipe.work/'ca.crt'))
            self.assertEqual(command[command.index('--api-key-file') + 1], str(self.recipe.work/'temporary-gateway-key'))
            self.assertEqual(command[-2:], ['--', 'How many planets are in the solar system?'])
            self.assertEqual('--stream' in command, streaming)
            self.assertEqual(self.events, ['connected', 'registered', 'revoked', 'disconnected'])
            self.assertEqual(self.recipe.state, before)
            self.assertFalse(self.recipe.state_path.exists())

    def test_existing_installer_key_is_supported_without_mutating_credentials(self):
        self.recipe.state['stack']['apiKeyFile'] = '/private/existing-key'
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'forward', self.forward), \
             patch.object(spark.gateway_access, 'temporary_gateway_key') as temporary, patch.object(spark, 'run') as run:
            self.recipe.chat('Hello', False, 18443)
        temporary.assert_not_called()
        command = run.call_args.args[0]
        self.assertEqual(command[command.index('--api-key-file') + 1], '/private/existing-key')
        self.assertEqual(self.events, ['connected', 'disconnected'])

    def test_request_failure_and_interrupt_revoke_before_closing_tunnel(self):
        for error in (RuntimeError('model request failed'), KeyboardInterrupt()):
            self.events = []
            with self.subTest(error=type(error).__name__), patch.object(self.recipe, 'bound_cluster'), \
                 patch.object(self.recipe, 'forward', self.forward), \
                 patch.object(spark.gateway_access, 'temporary_gateway_key', self.key), \
                 patch.object(spark, 'run', side_effect=error), self.assertRaises(type(error)):
                self.recipe.chat('Hello', False, 18443)
            self.assertEqual(self.events, ['connected', 'registered', 'revoked', 'disconnected'])
            self.assertFalse(self.recipe.state['gateway'])

    def test_changed_cluster_rejects_chat_before_connecting_or_issuing_key(self):
        with patch.object(self.recipe, 'bound_cluster', side_effect=RuntimeError('cluster changed')), \
             patch.object(self.recipe, 'forward') as forward, \
             patch.object(spark.gateway_access, 'temporary_gateway_key') as key, \
             self.assertRaisesRegex(RuntimeError, 'cluster changed'):
            self.recipe.chat('Hello', False, 18443)
        forward.assert_not_called()
        key.assert_not_called()

    def test_unattached_stack_rejects_chat_before_connecting(self):
        self.recipe.state = {}
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'forward') as forward, \
             self.assertRaisesRegex(RuntimeError, 'Attach to or deploy'):
            self.recipe.chat('Hello', False, 18443)
        forward.assert_not_called()

    def test_prompts_are_literal_arguments_not_options_or_shell(self):
        self.recipe.state['stack']['apiKeyFile'] = '/private/existing-key'
        prompt = '--stream $(echo example) `echo literal`'
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'forward', self.forward), \
             patch.object(spark, 'run') as run:
            self.recipe.chat(prompt, False, 18443)
        self.assertEqual(run.call_args.args[0][-2:], ['--', prompt])
        self.assertNotIn('shell', run.call_args.kwargs)

    def test_omitted_prompt_uses_client_default(self):
        self.recipe.state['stack']['apiKeyFile'] = '/private/existing-key'
        with patch.object(self.recipe, 'bound_cluster'), patch.object(self.recipe, 'forward', self.forward), \
             patch.object(spark, 'run') as run:
            self.recipe.chat(None, False, 18443)
        self.assertEqual(run.call_args.args[0][-2:], ['--api-key-file', '/private/existing-key'])

    def test_empty_prompt_rejects_before_cluster_access(self):
        for prompt in ('', '  ', 123):
            with patch.object(self.recipe, 'bound_cluster') as bound, self.assertRaisesRegex(RuntimeError, 'nonempty'):
                self.recipe.chat(prompt, False, 18443)
            bound.assert_not_called()


if __name__ == '__main__':
    unittest.main()
