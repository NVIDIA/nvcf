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

package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeProc builds a procfs stand-in. Each entry is hostPID -> (nsLink, NSpid
// line); a process is "in" the placeholder's namespace when its ns/pid symlink
// target matches.
func fakeProc(t *testing.T, procs map[int]struct {
	ns    string
	nspid string
},
) string {
	t.Helper()
	base := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(base, strconv.Itoa(pid))
		if err := os.MkdirAll(filepath.Join(dir, "ns"), 0o755); err != nil {
			t.Fatalf("mkdir %d: %v", pid, err)
		}
		// The real procfs uses magic symlinks; a plain symlink reproduces what
		// the code actually does with them (Readlink, compare strings).
		if err := os.Symlink(p.ns, filepath.Join(dir, "ns", "pid")); err != nil {
			t.Fatalf("symlink %d: %v", pid, err)
		}
		status := "Name:\tsh\nState:\tS (sleeping)\nNSpid:\t" + p.nspid + "\n"
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600); err != nil {
			t.Fatalf("status %d: %v", pid, err)
		}
	}
	// Non-pid entries must be skipped rather than error the walk.
	if err := os.WriteFile(filepath.Join(base, "meminfo"), []byte("MemTotal: 1 kB\n"), 0o600); err != nil {
		t.Fatalf("meminfo: %v", err)
	}
	return base
}

type procEntry = struct {
	ns    string
	nspid string
}

func TestPlaceholderMaxNSPID(t *testing.T) {
	tests := []struct {
		name    string
		procs   map[int]procEntry
		hostPID int
		want    int
		wantErr bool
	}{
		{
			// A placeholder that ran the ns_last_pid bump: its helpers sit
			// above the dumped range, so restore is safe.
			name: "reserved placeholder reports the high pid",
			procs: map[int]procEntry{
				5000: {ns: "pid:[111]", nspid: "1"},
				5001: {ns: "pid:[111]", nspid: "100001"},
				5002: {ns: "pid:[111]", nspid: "100002"},
			},
			hostPID: 5000,
			want:    100002,
		},
		{
			// The regression this guard exists for: the bump is missing, so a
			// long-lived tail sits at 363, inside the range CRIU must recreate.
			name: "unreserved placeholder reports the low pid",
			procs: map[int]procEntry{
				5000: {ns: "pid:[111]", nspid: "1"},
				5001: {ns: "pid:[111]", nspid: "363"},
				5002: {ns: "pid:[111]", nspid: "372"},
			},
			hostPID: 5000,
			want:    372,
		},
		{
			// Processes outside the placeholder's namespace must not count --
			// the agent's own pids are far higher and would mask the problem.
			name: "ignores processes in other namespaces",
			procs: map[int]procEntry{
				5000: {ns: "pid:[111]", nspid: "1"},
				5001: {ns: "pid:[111]", nspid: "363"},
				9000: {ns: "pid:[999]", nspid: "987654"},
			},
			hostPID: 5000,
			want:    363,
		},
		{
			name:    "missing placeholder is an error, not zero",
			procs:   map[int]procEntry{},
			hostPID: 5000,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := fakeProc(t, tt.procs)
			got, err := placeholderMaxNSPID(base, tt.hostPID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got max=%d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("max NSpid = %d, want %d", got, tt.want)
			}
		})
	}
}

// The floor must sit clear of both shapes we actually produce: a bumped
// placeholder (100000+) passes, an unbumped one (hundreds) fails. A floor that
// admitted the unbumped case would restore the silent 79% failure rate.
func TestReservedPIDFloorSeparatesBothShapes(t *testing.T) {
	const bumped, unbumped = 100001, 372
	if bumped < reservedPIDFloor {
		t.Errorf("a bumped placeholder (%d) must clear the floor (%d)", bumped, reservedPIDFloor)
	}
	if unbumped >= reservedPIDFloor {
		t.Errorf("an unbumped placeholder (%d) must fail the floor (%d)", unbumped, reservedPIDFloor)
	}
}

func TestNSPIDOfUsesInnermostNamespace(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "42")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Nested namespaces list outermost first; the placeholder's own view is
	// the last field, and taking the first would report the host pid.
	status := "Name:\tbash\nNSpid:\t42\t7\t3\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := nsPIDOf(base, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 3 {
		t.Errorf("nsPIDOf = %d, want 3 (innermost)", got)
	}
}
