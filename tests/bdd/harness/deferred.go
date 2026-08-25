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
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DeferredCommands stores explicit scenario compensation commands and runs
// them in reverse registration order. It continues after failures so one
// failed compensation does not prevent later recovery work.
type DeferredCommands struct {
	commands []deferredCommand
}

type deferredCommand struct {
	text    string
	timeout time.Duration
}

// NewDeferredCommands returns an empty scenario compensation stack.
func NewDeferredCommands() *DeferredCommands {
	return &DeferredCommands{}
}

// Add registers one already-interpolated command and its execution bound for
// scenario cleanup.
func (d *DeferredCommands) Add(command, timeoutText string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("deferred command must not be empty")
	}
	timeout, err := positiveDuration("deferred command timeout", timeoutText)
	if err != nil {
		return err
	}
	d.commands = append(d.commands, deferredCommand{text: command, timeout: timeout})
	return nil
}

// Reset discards commands left from a prior scenario.
func (d *DeferredCommands) Reset() {
	d.commands = nil
}

// Run executes every registered command in reverse order and clears the
// stack. Failure messages point to command logs without repeating command
// text, which may contain target-specific values.
func (d *DeferredCommands) Run(ctx context.Context, runner CommandRunner, commandLogDir string) error {
	commands := d.commands
	d.commands = nil
	var errs []error
	for index := len(commands) - 1; index >= 0; index-- {
		commandCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commands[index].timeout)
		result, err := runner.Run(commandCtx, commands[index].text)
		cancel()
		if err == nil && result.ExitCode == 0 {
			continue
		}
		position := len(commands) - index
		if err != nil && result.ExitCode <= 0 {
			errs = append(errs, fmt.Errorf(
				"deferred command %d of %d did not execute; see %s",
				position, len(commands), commandLogDir,
			))
			continue
		}
		errs = append(errs, fmt.Errorf(
			"deferred command %d of %d failed with exit code %d; see %s",
			position, len(commands), result.ExitCode, commandLogDir,
		))
	}
	return errors.Join(errs...)
}
