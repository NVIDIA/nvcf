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
	"os"
	"strings"
)

// CleanupMode names a pre-suite destructive cleanup mode the operator
// opts into via BDD_CLEANUP_MODE. The empty string is the explicit
// no-cleanup default; every other value maps to one make target in
// either tools/ncp-local-cluster/Makefile or
// deploy/stacks/self-managed/Makefile. The strict enum exists so a
// typo in the env var (e.g. "stack_single" with an underscore) fails
// the suite immediately rather than silently downgrading to no-cleanup
// and leaving scenarios to run on top of state the operator intended
// to wipe.
type CleanupMode string

const (
	CleanupNone           CleanupMode = ""
	CleanupStackSingle    CleanupMode = "stack-single"
	CleanupStackMulti     CleanupMode = "stack-multi"
	CleanupTopologySingle CleanupMode = "topology-single"
	CleanupTopologyMulti  CleanupMode = "topology-multi"
)

func validCleanupModes() []CleanupMode {
	return []CleanupMode{
		CleanupNone, CleanupStackSingle, CleanupStackMulti,
		CleanupTopologySingle, CleanupTopologyMulti,
	}
}

// ResolveCleanupMode reads BDD_CLEANUP_MODE and returns the validated
// enum value. Any value outside the enumerated set returns an error;
// the unset (empty) case maps to CleanupNone with no error. Callers
// must not silently downgrade an invalid value: see the CleanupMode
// docstring for why.
func ResolveCleanupMode() (CleanupMode, error) {
	raw := strings.TrimSpace(os.Getenv("BDD_CLEANUP_MODE"))
	for _, candidate := range validCleanupModes() {
		if string(candidate) == raw {
			return candidate, nil
		}
	}
	quoted := make([]string, 0, len(validCleanupModes()))
	for _, c := range validCleanupModes() {
		quoted = append(quoted, fmt.Sprintf("%q", string(c)))
	}
	return CleanupNone, fmt.Errorf(
		"BDD_CLEANUP_MODE=%q is not a valid cleanup mode; expected one of %s",
		raw, strings.Join(quoted, ", "),
	)
}

// cleanupCommandFor maps a CleanupMode to the make command the suite
// shells out to. Single source of truth so the unit test asserts
// command text exactly. CleanupNone returns (empty, false) because
// callers must not invoke the runner for it.
func cleanupCommandFor(mode CleanupMode) (string, bool) {
	switch mode {
	case CleanupTopologySingle:
		return "make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local", true
	case CleanupTopologyMulti:
		return "make -C tools/ncp-local-cluster destroy-all-ncp-local SHELL=/bin/bash", true
	case CleanupStackSingle:
		return "tests/bdd/scripts/destroy-stack.sh single", true
	case CleanupStackMulti:
		return "tests/bdd/scripts/destroy-stack.sh multi", true
	}
	return "", false
}

// RunPreSuiteCleanup invokes the make target corresponding to mode
// through the suite's CommandRunner. Logging, argv recording, and
// shlex parsing flow through the same pipe scenario commands use; a
// post-mortem `ls out/<run-id>/logs/` shows the cleanup alongside
// scenario steps. CleanupNone is a no-op.
//
// Failure aborts the suite. A half-cleaned cluster would leave
// scenarios running on top of stale state, exactly the case this hook
// exists to prevent.
func (s *Suite) RunPreSuiteCleanup(ctx context.Context, mode CleanupMode) error {
	if mode == CleanupNone {
		return nil
	}
	cmd, ok := cleanupCommandFor(mode)
	if !ok {
		return fmt.Errorf("no cleanup command for mode %q", mode)
	}
	result, err := s.Runner.Run(ctx, cmd)
	if err != nil {
		return fmt.Errorf("pre-suite cleanup (%s): %w", mode, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf(
			"pre-suite cleanup (%s) exit=%d (see %s for stdout/stderr)",
			mode, result.ExitCode, s.Config.CommandLogDir,
		)
	}
	return nil
}
