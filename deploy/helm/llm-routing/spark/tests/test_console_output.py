# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import io
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(HERE))
import console_output
import spark


class ConsoleOutputTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.work = pathlib.Path(self.temporary.name)
        self.console = console_output.Console()
        self.stdout = io.StringIO()
        self.stderr = io.StringIO()

    @contextlib.contextmanager
    def captured(self):
        with contextlib.redirect_stdout(self.stdout), contextlib.redirect_stderr(self.stderr):
            yield

    def log(self):
        return pathlib.Path(self.console.log_path).read_text()

    def test_success_reports_result_and_keeps_chatter_in_private_log(self):
        def action():
            print('Helm release revision details')
            console_output.run([sys.executable, '-c', 'print("subprocess success chatter")'])
            return 'action result'

        with self.captured():
            result = self.console.run('inventory', self.work, action)

        self.assertEqual(result, 'action result')
        self.assertEqual(self.stdout.getvalue(), 'inventory passed.\n')
        self.assertEqual(self.stderr.getvalue(), '')
        self.assertIn('Helm release revision details', self.log())
        self.assertIn('subprocess success chatter', self.log())
        self.assertEqual(pathlib.Path(self.console.log_path).stat().st_mode & 0o777, 0o600)

    def test_partial_action_failure_never_reports_success(self):
        def action():
            print('first operation completed')
            raise RuntimeError('model registration failed')

        with self.captured():
            with self.assertRaisesRegex(RuntimeError, 'model registration failed') as error:
                self.console.run('register', self.work, action)
            status = self.console.failure(error.exception)

        self.assertEqual(status, 1)
        displayed = self.stdout.getvalue() + self.stderr.getvalue()
        self.assertIn('model registration failed', displayed)
        self.assertNotIn('passed.', displayed)
        self.assertNotIn('Traceback', displayed)
        self.assertIn('first operation completed', self.log())

    def test_failed_subprocess_diagnostics_remain_in_log(self):
        def action():
            console_output.run([sys.executable, '-c',
                                'import sys; print("last build step"); '
                                'print("specific build failure", file=sys.stderr); sys.exit(7)'])

        with self.captured():
            with self.assertRaises(subprocess.CalledProcessError) as error:
                self.console.run('build-images', self.work, action)
            status = self.console.failure(error.exception)

        self.assertEqual(status, 1)
        self.assertIn('last build step', self.log())
        self.assertIn('specific build failure', self.log())
        self.assertNotIn('passed.', self.stdout.getvalue() + self.stderr.getvalue())

    def test_output_wrapper_preserves_parser_payload(self):
        def action():
            payload = console_output.output([sys.executable, '-c', 'print("{\\"items\\": []}")'])
            self.assertEqual(payload, '{"items": []}\n')
            return payload

        with self.captured():
            result = self.console.run('inventory', self.work, action)

        self.assertEqual(result, '{"items": []}\n')
        self.assertEqual(self.stdout.getvalue(), 'inventory passed.\n')

    def test_needed_warning_is_visible_during_quiet_success(self):
        warning = 'Image updates require a compatible source revision.'
        with self.captured():
            self.console.run('attach-existing', self.work, lambda: console_output.warn(warning))

        self.assertIn(warning, self.stdout.getvalue() + self.stderr.getvalue())
        self.assertIn('attach-existing passed.', self.stdout.getvalue())

    def test_payload_commands_keep_their_output_without_status_suffix(self):
        cases = [('chat', 'The answer is 42.'), ('context', 'team-context'),
                 ('paths', '{"workDir": "/private/work"}')]
        for phase, payload in cases:
            with self.subTest(phase=phase):
                self.stdout = io.StringIO()
                self.stderr = io.StringIO()
                with self.captured():
                    result = self.console.run(phase, self.work, lambda: print(payload))
                self.assertIsNone(result)
                self.assertEqual(self.stdout.getvalue(), payload + '\n')
                self.assertEqual(self.stderr.getvalue(), '')

    def test_render_keeps_configuration_count(self):
        with self.captured():
            self.console.run('render', self.work,
                             lambda: print('Helm lint/render passed for 8 configurations.'))

        self.assertIn('Helm lint/render passed for 8 configurations.', self.stdout.getvalue())
        self.assertNotIn('render passed.\n', self.stdout.getvalue())

    def cli_arguments(self):
        config = json.loads((HERE/'config.example.json').read_text())
        config['context'] = 'console-test'
        config_path = self.work/'config.json'
        config_path.write_text(json.dumps(config))
        return ['--context', 'console-test', '--config', str(config_path),
                '--work-dir', str(self.work), 'inventory']

    def test_cli_entrypoint_reports_phase_failure_without_traceback(self):
        arguments = self.cli_arguments()

        def fail():
            print('node inspection completed')
            raise RuntimeError('Selected model GPU is occupied: another-model')

        with self.captured(), patch.object(spark, 'Recipe') as recipe:
            recipe.return_value.inventory.side_effect = fail
            status = spark.cli(arguments)

        self.assertEqual(status, 1)
        recipe.return_value.inventory.assert_called_once_with()
        displayed = self.stdout.getvalue() + self.stderr.getvalue()
        self.assertIn('inventory failed: Selected model GPU is occupied: another-model', displayed)
        self.assertNotIn('passed.', displayed)
        self.assertNotIn('Traceback', displayed)
        self.assertNotIn('node inspection completed', displayed)
        log_path, = (self.work/'evidence').glob('inventory-*.log')
        self.assertIn('node inspection completed', log_path.read_text())
        self.assertIn('Selected model GPU is occupied: another-model', log_path.read_text())

    def test_cli_entrypoint_reports_single_success_after_action_completes(self):
        arguments = self.cli_arguments()
        with self.captured(), patch.object(spark, 'Recipe') as recipe:
            recipe.return_value.inventory.side_effect = lambda: print('Inventory passed.')
            status = spark.cli(arguments)

        self.assertEqual(status, 0)
        recipe.return_value.inventory.assert_called_once_with()
        self.assertEqual(self.stdout.getvalue(), 'inventory passed.\n')
        self.assertEqual(self.stderr.getvalue(), '')


if __name__ == '__main__':
    unittest.main()
