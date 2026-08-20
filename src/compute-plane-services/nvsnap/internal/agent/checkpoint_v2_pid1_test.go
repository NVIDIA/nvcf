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
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu"
)

func argsString(args []string) string { return strings.Join(args, " ") }

// hasFlagPair reports whether args contains flag followed immediately by value.
func hasFlagPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// The whole point of this path is that criu is NOT a member of the pid
// namespace it has to record. If an nsenter or -p ever appears here, criu
// stops writing a pid namespace and restore refuses the tree with
// "This process tree can only be restored in a new pid namespace".
func TestNamespaceRootDumpArgsStaysOutsideTheTargetNamespace(t *testing.T) {
	args := namespaceRootDumpArgs(4242, "/some/root", "/imgs", "/plugins", nil, nil, false)

	if got := args[0]; got != "dump" {
		t.Errorf("argv should start with the criu subcommand, got %q", got)
	}
	for _, forbidden := range []string{"nsenter", "-p"} {
		for _, a := range args {
			if a == forbidden {
				t.Errorf("argv must not contain %q: %s", forbidden, argsString(args))
			}
		}
	}
	if !hasFlagPair(args, "-t", "4242") {
		t.Errorf("target must be the HOST pid, got: %s", argsString(args))
	}
	if !hasFlagPair(args, "--root", "/some/root") {
		t.Errorf("--root is how criu reaches the container from outside: %s", argsString(args))
	}
}

// Kubernetes owns the pod network via CNI. Recording it makes restore try to
// recreate eth0, whose veth peer lives on the host, and fail with
// "net: Unknown peer net namespace".
func TestNamespaceRootDumpArgsDoesNotRecordTheNetNamespace(t *testing.T) {
	args := namespaceRootDumpArgs(1, "/r", "/imgs", "/plugins", nil, nil, false)
	if !hasFlagPair(args, "--empty-ns", "net") {
		t.Errorf("--empty-ns net missing: %s", argsString(args))
	}
}

// Restore rebuilds its mappings keyed by mountpoint (generateExtMountMaps uses
// Key: mountpoint, Val: mountpoint). A cookie that is anything else -- a
// mangled or generated name -- never resolves, and the restore dies with
// "No mapping for <id>:(null) mountpoint".
func TestNamespaceRootDumpArgsKeysExternalMountsByMountpoint(t *testing.T) {
	ext := []criu.ExtMountMap{
		{Key: "/run/nvidia/driver/lib/firmware/nvidia/gsp_ga10x.bin", Val: "/run/nvidia/driver/lib/firmware/nvidia/gsp_ga10x.bin"},
		{Key: "/dev/shm", Val: "/dev/shm"},
	}
	args := namespaceRootDumpArgs(1, "/r", "/imgs", "/plugins", ext, nil, false)

	for _, m := range ext {
		want := "mnt[" + m.Key + "]:" + m.Val
		if !hasFlagPair(args, "--external", want) {
			t.Errorf("missing external mount mapping %q in: %s", want, argsString(args))
		}
	}
}

// Device externals are passed through unchanged; they are built elsewhere and
// this function must not reorder or rewrite them.
func TestNamespaceRootDumpArgsPassesDeviceExternalsThrough(t *testing.T) {
	devs := []string{"--external", "dev[195/255]:nvidiactl", "--external", "dev[508/0]:nvidia-uvm"}
	args := namespaceRootDumpArgs(1, "/r", "/imgs", "/plugins", nil, devs, false)

	if !strings.HasSuffix(argsString(args), argsString(devs)) {
		t.Errorf("device externals should be appended verbatim, got: %s", argsString(args))
	}
}

func TestNamespaceRootDumpArgsLeaveRunning(t *testing.T) {
	for _, leave := range []bool{true, false} {
		args := namespaceRootDumpArgs(1, "/r", "/imgs", "/plugins", nil, nil, leave)
		found := false
		for _, a := range args {
			if a == "--leave-running" {
				found = true
			}
		}
		if found != leave {
			t.Errorf("leaveRunning=%v produced --leave-running=%v", leave, found)
		}
	}
}
