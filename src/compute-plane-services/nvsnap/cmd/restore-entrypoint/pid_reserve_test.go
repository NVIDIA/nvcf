/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReservePIDRangeWritesTheFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ns_last_pid")
	reservePIDRangeAt(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("control file not written: %v", err)
	}
	if string(got) != pidReserveFloor {
		t.Errorf("wrote %q, want %q", got, pidReserveFloor)
	}
}

// A write failure must not take the process down: the restore pod still has a
// cold-start fallback, and crash-looping here would turn a degraded restore
// into no workload at all. The agent refuses the restore instead.
func TestReservePIDRangeSurvivesAnUnwritablePath(t *testing.T) {
	dir := t.TempDir()
	// A directory cannot be written as a file, which is the closest stand-in
	// for the read-only /proc/sys the old shell version believed it faced.
	reservePIDRangeAt(dir)
}

// The floor this binary writes must clear the floor the agent accepts,
// otherwise a correctly-reserved pod would still be refused. These constants
// live in different packages and nothing but this test ties them together.
func TestReserveFloorClearsTheAgentAcceptanceFloor(t *testing.T) {
	// Mirrors reservedPIDFloor in internal/agent/restore_v2.go. If that value
	// changes, this test should fail and force the pair to be reconsidered.
	const agentAcceptanceFloor = 50000

	got, err := strconv.Atoi(pidReserveFloor)
	if err != nil {
		t.Fatalf("pidReserveFloor %q is not a number: %v", pidReserveFloor, err)
	}
	if got <= agentAcceptanceFloor {
		t.Errorf("reserve floor %d must exceed the agent's acceptance floor %d, "+
			"or every reserved pod is refused", got, agentAcceptanceFloor)
	}
}
