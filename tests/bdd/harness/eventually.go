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
	"fmt"
	"time"
)

// RunUntilSuccess runs a visible command until it exits zero or the timeout
// expires. Runner failures that did not produce a process exit code stop the
// retry immediately. Product-level nonzero exits remain retryable.
func RunUntilSuccess(
	ctx context.Context,
	runner CommandRunner,
	command string,
	timeoutText string,
	intervalText string,
) (Result, error) {
	timeout, err := positiveDuration("timeout", timeoutText)
	if err != nil {
		return Result{}, err
	}
	interval, err := positiveDuration("interval", intervalText)
	if err != nil {
		return Result{}, err
	}

	retryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last Result
	for {
		result, runErr := runner.Run(retryCtx, command)
		last = result
		if runErr == nil && result.ExitCode == 0 {
			return result, nil
		}
		if retryCtx.Err() != nil {
			return last, fmt.Errorf("command did not succeed within %s", timeout)
		}
		if runErr != nil && result.ExitCode <= 0 {
			return result, fmt.Errorf("command did not execute: %w", runErr)
		}

		timer := time.NewTimer(interval)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return last, fmt.Errorf("command did not succeed within %s", timeout)
		case <-timer.C:
		}
	}
}

func positiveDuration(name, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a duration: %w", name, value, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %s", name, duration)
	}
	return duration, nil
}
