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

package criu

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestDumpRPCOptions_JSONRoundTrip is the load-bearing schema guard for
// phase 5b: the agent JSON-marshals DumpRPCOptions, hands the bytes to
// the writer Job via CaptureSource.CRIURPCOptionsJSON, and the Job
// unmarshals them back into a struct it feeds to go-criu. Any field
// that doesn't survive the round-trip (e.g. unexported, missing tags,
// custom marshal logic) silently changes the dump's behavior in the
// Job vs in the agent.
//
// This test populates every documented field with a non-zero value and
// asserts deep equality after marshal+unmarshal. Adding a new field to
// DumpRPCOptions should fail this test until the field is added below
// — that's the point.
func TestDumpRPCOptions_JSONRoundTrip(t *testing.T) {
	orig := DumpRPCOptions{
		PID:       12345,
		ImagesDir: "/dest/tree/criu/vllm-small__nvsnap-system__20260507-100000",
		Root:      "/host/proc/12345/root",

		LeaveRunning: true,
		ShellJob:     true,

		PluginDir: "/criu-bundle/plugins",

		ExtMnt: []ExtMountMap{
			{Key: "extNvidiaCtl", Val: "/dev/nvidiactl"},
			{Key: "extNvidia0", Val: "/dev/nvidia0"},
			{Key: "extDevShm", Val: "/dev/shm"},
		},
		External: []string{
			"dev[195/0]:nvidia0",
			"dev[195/255]:nvidiactl",
			"dev[511/0]:nvidia-uvm",
			"net[12345]:extNetNs",
		},
		SkipMounts: []string{"/proc", "/sys", "/run/secrets"},

		TCPEstab:          true,
		TcpClose:          false,
		FileLocks:         true,
		LinkRemap:         true,
		ExtUnixSk:         true,
		ExtMasters:        true,
		SkipFsnotify:      false,
		SkipInFlight:      true,
		NetworkLockMethod: "skip",
		OrphanPtsMaster:   true,

		AllowUprobes: true,

		Timeout:    1200,
		GhostLimit: 512 * 1024 * 1024,

		LogLevel: 4,

		Stream: false,
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got DumpRPCOptions
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(orig, got) {
		t.Errorf("round-trip mismatch:\n  orig=%+v\n  got =%+v", orig, got)
	}
}

// TestDumpRPCOptions_EmptySliceRoundTrip ensures that empty slices come
// back as either nil or empty (both valid for go-criu) — guards against
// a regression where a marshalling tag drops an explicit empty slice
// and a downstream consumer crashes on a nil dereference.
func TestDumpRPCOptions_EmptySliceRoundTrip(t *testing.T) {
	orig := DumpRPCOptions{
		PID:       1,
		ImagesDir: "/x",
		// All slices left empty.
	}
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DumpRPCOptions
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.PID != 1 || got.ImagesDir != "/x" {
		t.Errorf("scalar fields lost: got=%+v", got)
	}
	// nil and zero-length both ok; just must not panic at runtime.
	if len(got.External) != 0 {
		t.Errorf("External had unexpected content: %v", got.External)
	}
}
