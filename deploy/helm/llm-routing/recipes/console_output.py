# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Concise phase results with private diagnostic logs."""
import contextlib
import contextvars
import os
import pathlib
import re
import subprocess
import sys
import tempfile
import traceback

_ACTIVE = contextvars.ContextVar('recipe_console', default=None)
PAYLOAD_PHASES = {'chat', 'paths', 'context', 'dashboard', 'monitoring-images'}
NEXT_CHECK = {
    'init': 'Check the saved configuration and retained-resource ownership',
    'inventory': 'Check Kubernetes access, node readiness and GPU allocation',
    'render': 'Check the chart and values errors in the log',
    'prepare': 'Check source compatibility and Helm dependency access',
    'build-images': 'Check Docker and the image build error in the log',
    'push-images': 'Check registry access and the image name',
    'export-images': 'Check local images and free disk space',
    'import-images': 'Check the archive and image-import Job evidence',
    'stack': 'Check Helm release status and the failing workload',
    'preflight': 'Check preflight Job logs and node GPU/runtime configuration',
    'build-runtime': 'Check build Job logs and artifact storage',
    'qualify': 'Check qualification evidence and the failing GPU/RPC check',
    'download': 'Check download Job logs, artifact storage and checksum results',
    'load': 'Check model/RPC pod logs and memory-guard evidence',
    'register': 'Check InferenceEndpoint conditions and operator logs',
    'verify-direct': 'Check direct request evidence and model pod logs',
    'verify-gateway': 'Check gateway request evidence, port-forward.log and key cleanup',
    'chat': 'Check gateway connectivity and port-forward.log',
    'attach-existing': 'Check deployment ownership and the saved configuration',
    'cleanup-key': 'Check the temporary key journal and gateway access',
    'recover': 'Check recovery evidence and model/RPC pod status',
    'retune': 'Check model/RPC pod logs and the tuning section of the configuration',
    'update': 'Check the update record and workload rollout status',
    'rollback': 'Check the selected update record and workload rollout status',
    'monitoring': 'Check the monitoring release and its workload logs',
    'dashboard': 'Check monitoring-port-forward.log and Grafana readiness',
    'verify-monitoring': 'Check evidence/monitoring.json and monitoring-port-forward.log',
    'uninstall-monitoring': 'Check the saved cluster identity and monitoring Helm release',
    'export-monitoring-images': 'Check pinned monitoring images and free disk space',
    'import-monitoring-images': 'Check the archive and image-import Job evidence',
}


def warn(message):
    console = _ACTIVE.get()
    if console:
        print(message, file=console.terminal_error, flush=True)
        print(message, file=console.log, flush=True)
    else:
        print(message, file=sys.stderr, flush=True)


def _record_error(error):
    console = _ACTIVE.get()
    if console:
        for value in (getattr(error, 'stdout', None), getattr(error, 'stderr', None)):
            if value:
                print(value.decode(errors='replace') if isinstance(value, bytes) else value, file=console.log)


def run(command, **kwargs):
    console = _ACTIVE.get()
    if console:
        console.log.flush()
        kwargs.setdefault('stdout', console.log)
        kwargs.setdefault('stderr', console.log)
    try:
        return subprocess.run([str(x) for x in command], check=True, **kwargs)
    except subprocess.SubprocessError as error:
        _record_error(error)
        raise


def output(command, **kwargs):
    console = _ACTIVE.get()
    if console:
        console.log.flush()
        kwargs.setdefault('stderr', console.log)
    try:
        return subprocess.check_output([str(x) for x in command], text=True, **kwargs)
    except subprocess.SubprocessError as error:
        _record_error(error)
        raise


class Console:
    def __init__(self):
        self.phase = 'command'
        self.log_path = None
        self.log = None
        self.terminal = sys.stdout
        self.terminal_error = sys.stderr

    def run(self, phase, work, action):
        self.phase = phase
        self.terminal, self.terminal_error = sys.stdout, sys.stderr
        if phase in PAYLOAD_PHASES:
            self.log_path = None
            return action()
        directory = pathlib.Path(work)/'evidence'
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd, name = tempfile.mkstemp(prefix=phase+'-', suffix='.log', dir=directory)
        self.log_path = pathlib.Path(name)
        with os.fdopen(fd, 'w') as self.log:
            token = _ACTIVE.set(self)
            try:
                with contextlib.redirect_stdout(self.log), contextlib.redirect_stderr(self.log):
                    result = action()
            except BaseException:
                traceback.print_exc(file=self.log)
                raise
            finally:
                _ACTIVE.reset(token)
        with self.log_path.open() as saved_log:
            lines = [line.rstrip() for line in saved_log
                     if line.startswith(('Helm lint/render passed for', 'Configuration created:', 'Reusing configuration:',
                                         'Previous progress archived:', 'Recipe:', 'GPU:', 'Model nodes:', 'Context:', 'Routing node:', 'Run render,',
                                         'Image archive:', 'Monitoring image archive:', 'Update recorded:')) or re.match(r'^(?:WARNING|Warning|warning)[: ]|^W[0-9]{4} ', line)]
        for line in lines:
            if re.match(r'^(?:WARNING|Warning|warning)[: ]|^W[0-9]{4} ', line):
                print(line, file=self.terminal_error)
        if phase == 'render':
            summary = next((line for line in reversed(lines) if line.startswith('Helm lint/render passed for')), None)
            print(summary or 'render passed.', file=self.terminal)
        else:
            print(phase + ' passed.', file=self.terminal)
        prefixes = {
            'init': ('Configuration created:', 'Reusing configuration:', 'Previous progress archived:',
                     'Recipe:', 'GPU:', 'Model nodes:', 'Context:', 'Routing node:', 'Run render,'),
            'export-images': ('Image archive:',),
            'export-monitoring-images': ('Monitoring image archive:',),
            'update': ('Update recorded:',),
        }.get(phase, ())
        for line in lines:
            if line.startswith(prefixes):
                print(line, file=self.terminal)
        return result

    def failure(self, error):
        if isinstance(error, subprocess.CalledProcessError):
            command = error.cmd
            executable = pathlib.Path(str(command[0] if isinstance(command, (list, tuple)) else command)).name
            cause = executable + ' exited with status ' + str(error.returncode)
            if self.log_path:
                with self.log_path.open() as saved_log:
                    details = [line.strip()[:400] for line in saved_log
                               if re.match(r'^(?:Error|error|RuntimeError|ValueError|fatal):', line)]
                if details:
                    cause += ': ' + details[-1]
        elif isinstance(error, subprocess.TimeoutExpired):
            cause = 'Command timed out after ' + str(error.timeout) + ' seconds'
        elif isinstance(error, KeyboardInterrupt):
            cause = 'Interrupted'
        elif isinstance(error, SystemExit):
            lines = self.log_path.read_text().splitlines() if self.log_path else []
            cause = next((line.removeprefix('error: ') for line in reversed(lines) if line.startswith('error: ')), 'Command exited before completion')
        else:
            cause = str(error) or type(error).__name__
        print(self.phase + ' failed: ' + cause, file=self.terminal_error)
        print(NEXT_CHECK.get(self.phase, 'Check the command arguments and diagnostic log') + '.', file=self.terminal_error)
        if self.log_path:
            print('Log:', self.log_path, file=self.terminal_error)
        return 1
