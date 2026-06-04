/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/creack/pty"
	"github.com/google/shlex"
)

// Result is what a single command execution produced. ExitCode is the
// shell exit status; Stdout and Stderr are captured streams. The
// non-zero exit case still returns a populated Result alongside the
// error so handlers can attach stderr to assertion failures.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// CommandRunner executes a shell command spelled as a single string
// and returns the captured Result. The handler that invokes Run is
// responsible for ${VAR} interpolation on the command text before
// calling here; the runner sees only the resolved argv.
type CommandRunner interface {
	Run(ctx context.Context, commandText string) (Result, error)
	// RunWithTTY runs commandText with stdin attached to a pseudo-terminal
	// so the child process sees a terminal on fd 0 (term.IsTerminal(0) is
	// true). It is for commands that gate interactive-only behavior on a
	// TTY: `nvcf-cli self-hosted up` mints its admin token at the auth-gate
	// only when stdin is a terminal, and refuses (rather than blocking on a
	// stdin read) otherwise. Stdout and stderr are still captured
	// separately; only stdin is wired to the pty, and no input is written.
	RunWithTTY(ctx context.Context, commandText string) (Result, error)
}

// execRunner is the default CommandRunner. The commandText is split
// into argv using POSIX shell tokenization (single and double quoting
// supported via google/shlex) so commands like `make VAR='a b'` round
// trip without surprise. Stdout, stderr, and the parsed argv are
// written to <logDir>/<seq>.{cmd,stdout,stderr} on every invocation
// when logDir is non-empty.
type execRunner struct {
	cwd    string
	logDir string
	mu     sync.Mutex
	seq    int
}

// NewCommandRunner returns a CommandRunner that executes inside cwd.
// When logDir is non-empty, every invocation writes its parsed argv,
// stdout, and stderr to numbered files under logDir.
func NewCommandRunner(cwd, logDir string) CommandRunner {
	return &execRunner{cwd: cwd, logDir: logDir}
}

// Run tokenizes commandText, executes via os/exec, captures the
// streams, and writes the trio of log files if logDir is configured.
// A non-zero exit code is reported through the returned error along
// with a populated Result.
func (r *execRunner) Run(ctx context.Context, commandText string) (Result, error) {
	return r.run(ctx, commandText, false)
}

// RunWithTTY is Run with the child's stdin attached to a pty slave so it
// observes a terminal on fd 0. See the CommandRunner interface comment.
func (r *execRunner) RunWithTTY(ctx context.Context, commandText string) (Result, error) {
	return r.run(ctx, commandText, true)
}

func (r *execRunner) run(ctx context.Context, commandText string, withTTY bool) (Result, error) {
	argv, err := shlex.Split(commandText)
	if err != nil {
		return Result{}, fmt.Errorf("parse command %q: %w", commandText, err)
	}
	if len(argv) == 0 {
		return Result{}, errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = r.cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var ptmx *os.File
	if withTTY {
		// Attach stdin to a pty slave so term.IsTerminal(0) is true in the
		// child. Keep the master open until the command exits so Linux keeps
		// fd 0 attached to a live terminal; callers use ctx to bound commands
		// that unexpectedly wait for stdin.
		var pts *os.File
		ptmxFile, ptsFile, ptyErr := pty.Open()
		if ptyErr != nil {
			return Result{}, fmt.Errorf("open pty for %q: %w", commandText, ptyErr)
		}
		ptmx = ptmxFile
		pts = ptsFile
		cmd.Stdin = pts
		defer func() {
			_ = pts.Close()
			if ptmx != nil {
				_ = ptmx.Close()
			}
		}()
	}
	runErr := cmd.Start()
	if runErr == nil {
		runErr = cmd.Wait()
	}
	result := Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		result.ExitCode = -1
	}
	r.writeLog(commandText, argv, result)
	if runErr != nil {
		return result, fmt.Errorf("command %q failed: %w", commandText, runErr)
	}
	return result, nil
}

func (r *execRunner) writeLog(commandText string, argv []string, result Result) {
	if r.logDir == "" {
		return
	}
	r.mu.Lock()
	r.seq++
	seq := r.seq
	r.mu.Unlock()
	if err := os.MkdirAll(r.logDir, 0o755); err != nil {
		return
	}
	prefix := filepath.Join(r.logDir, fmt.Sprintf("%04d", seq))
	cmdLog := fmt.Sprintf("%s\nargv: %s\n", commandText, strings.Join(argv, "\x1f"))
	_ = os.WriteFile(prefix+".cmd", []byte(cmdLog), 0o644)
	_ = os.WriteFile(prefix+".stdout", []byte(result.Stdout), 0o644)
	_ = os.WriteFile(prefix+".stderr", []byte(result.Stderr), 0o644)
}
