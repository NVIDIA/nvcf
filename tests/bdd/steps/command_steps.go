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

package steps

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
	"nvcf-bdd/harness"
)

// registerCommandSteps hooks the run/has-succeeded/export handlers.
// The run forms (single-line and docstring) route through runCommand
// so the recording and caching paths are identical.
func registerCommandSteps(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Step(`^I run command "([^"]*)"$`, sc.iRunCommandLine)
	ctx.Step(`^I run command:$`, sc.iRunCommandDoc)
	ctx.Step(`^I run command with a terminal:$`, sc.iRunCommandWithTTYDoc)
	ctx.Step(`^command has succeeded:$`, sc.commandHasSucceededDoc)
	ctx.Step(`^I export command output to environment variable "([^"]*)"$`, sc.iExportCommandOutputToEnv)
}

func (sc *ScenarioContext) iRunCommandLine(ctx context.Context, commandText string) error {
	return sc.runAndRecord(ctx, commandText)
}

func (sc *ScenarioContext) iRunCommandDoc(ctx context.Context, doc *godog.DocString) error {
	return sc.runAndRecord(ctx, doc.Content)
}

// iRunCommandWithTTYDoc runs the docstring command with stdin attached to
// a pseudo-terminal (Suite.Runner.RunWithTTY). It exists for commands that
// only do the right thing when stdin is a terminal, such as
// `nvcf-cli self-hosted up`, whose auth-gate mints the admin token after
// installing the control plane only when a TTY is present.
func (sc *ScenarioContext) iRunCommandWithTTYDoc(ctx context.Context, doc *godog.DocString) error {
	return sc.runAndRecordWith(ctx, doc.Content, sc.Suite.Runner.RunWithTTY)
}

// commandHasSucceededDoc delegates to the shared cachedRun primitive
// on ScenarioContext so the "Given command has succeeded:" semantic
// stays in lockstep with the bootstrap Givens that use the same
// caching path.
func (sc *ScenarioContext) commandHasSucceededDoc(ctx context.Context, doc *godog.DocString) error {
	return sc.cachedRun(ctx, doc.Content)
}

// runAndRecord is used by the When-form steps. It interpolates,
// executes, and stores the Result on the ScenarioContext so later
// assertions can read it.
//
// A non-zero exit code is NOT a step failure here; the `the command
// exit code should be N` assertion that typically follows decides
// whether the recorded exit code passes the scenario (negative
// prechecks rely on this: they assert `should be 1` against a `k3d
// cluster get <absent-cluster>` that exits 1 by design). But a runner
// error that prevented the command from producing a meaningful exit
// (shlex parse failure, empty command, binary not found) is a step
// failure: returning nil would leave LastResult.ExitCode == 0 and the
// next "should be 0" assertion would silently pass against a phantom
// run. We surface those by returning err when ExitCode <= 0; real
// non-zero exits (ExitCode > 0) ran the process and are left for the
// assertion to inspect.
func (sc *ScenarioContext) runAndRecord(ctx context.Context, commandText string) error {
	return sc.runAndRecordWith(ctx, commandText, sc.Suite.Runner.Run)
}

// runAndRecordWith interpolates, executes via the supplied runner method
// (Run or RunWithTTY), and stores the Result on the ScenarioContext so
// later assertions can read it. The non-zero-exit-is-not-a-failure
// contract documented on the When-form steps applies regardless of which
// runner method is used.
func (sc *ScenarioContext) runAndRecordWith(ctx context.Context, commandText string, run func(context.Context, string) (harness.Result, error)) error {
	resolved := strings.TrimSpace(dsl.Interpolate(commandText))
	result, err := run(ctx, resolved)
	sc.LastResult = result
	sc.LastErr = err
	sc.LastCommand = resolved
	if err != nil && result.ExitCode <= 0 {
		return fmt.Errorf("command did not execute: %w", err)
	}
	return nil
}

// combinedOutput concatenates stdout and stderr of the last run.
// Feature files express substring assertions against the combined
// stream without distinguishing the two.
func combinedOutput(r harness.Result) string {
	return r.Stdout + r.Stderr
}

// iExportCommandOutputToEnv records the previous command's trimmed
// stdout under the named env var. Strict gates:
//
//   - LastResult.ExitCode must be 0. A non-zero exit on the prior step
//     should already have failed an explicit "Then the command exit code
//     should be 0" assertion, so reaching this step with a non-zero
//     exit means the feature author forgot to assert success; surface
//     it rather than exporting garbage.
//   - The trimmed stdout must be non-empty. Exporting an empty string
//     would silently allow downstream "${VAR}" interpolation to expand
//     to "" and produce malformed kubectl URLs (-n -o instead of
//     -n my-ns -o), so the failure must be loud.
//
// On success the EnvLedger snapshots the prior value first, then
// os.Setenv writes the new one. Suite teardown restores the original.
func (sc *ScenarioContext) iExportCommandOutputToEnv(_ context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("export: env var name must be non-empty")
	}
	if sc.LastResult.ExitCode != 0 {
		return fmt.Errorf("export to %s: prior command exited %d (want 0)", name, sc.LastResult.ExitCode)
	}
	value := strings.TrimSpace(sc.LastResult.Stdout)
	if value == "" {
		return fmt.Errorf("export to %s: prior command produced empty stdout", name)
	}
	if err := sc.Suite.EnvLedger.Snapshot(name); err != nil {
		return fmt.Errorf("export to %s: snapshot: %w", name, err)
	}
	if err := os.Setenv(name, value); err != nil {
		return fmt.Errorf("export to %s: setenv: %w", name, err)
	}
	return nil
}
