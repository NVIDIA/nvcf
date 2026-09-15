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

package webhook

import "testing"

// vol/mount/env element helpers (map shape, like auto-inject emits).
func vol(name string) map[string]any {
	return map[string]any{"name": name, "emptyDir": map[string]any{}}
}
func mount(name string) map[string]any { return map[string]any{"name": name, "mountPath": "/" + name} }

func countOps(ps []PatchOp, path string) int {
	n := 0
	for _, p := range ps {
		if p.Path == path {
			n++
		}
	}
	return n
}

// The core #93 case: auto-inject bootstraps /spec/volumes with nvsnap-lib,
// then a restore builder (computed from the original empty pod) bootstraps
// it again. Without normalization the second add REPLACES the first and
// nvsnap-lib is lost. After merge: ONE bootstrap (nvsnap-lib) + appends.
func TestMergePatchPlan_NoClobberOnEmptyArrays(t *testing.T) {
	in := []PatchOp{
		{Op: "add", Path: "/spec/volumes", Value: []any{vol("nvsnap-lib")}}, // auto-inject
		{Op: "add", Path: "/spec/volumes", Value: []any{vol("nvsnap-rox")}}, // restore (clobbers!)
		{Op: "add", Path: "/spec/containers/0/volumeMounts", Value: []any{mount("nvsnap-lib")}},
		{Op: "add", Path: "/spec/containers/0/volumeMounts", Value: []any{mount("nvsnap-rox")}},
	}
	out := mergePatchPlan(in)

	if got := countOps(out, "/spec/volumes"); got != 1 {
		t.Errorf("/spec/volumes bootstrap count = %d, want 1 (no clobber)", got)
	}
	// The surviving bootstrap must be nvsnap-lib (auto-inject), and nvsnap-rox
	// must appear as an append.
	var sawLibBootstrap, sawRoxAppend bool
	for _, p := range out {
		if p.Path == "/spec/volumes" {
			if patchElementName(p.Value.([]any)[0]) == "nvsnap-lib" {
				sawLibBootstrap = true
			}
		}
		if p.Path == "/spec/volumes/-" && patchElementName(p.Value) == "nvsnap-rox" {
			sawRoxAppend = true
		}
	}
	if !sawLibBootstrap {
		t.Error("nvsnap-lib bootstrap not preserved")
	}
	if !sawRoxAppend {
		t.Error("nvsnap-rox not converted to an append")
	}
}

// Duplicate nvsnap-lib (auto-inject + restore bundle) collapses to one.
func TestMergePatchPlan_DedupeDuplicateElement(t *testing.T) {
	in := []PatchOp{
		{Op: "add", Path: "/spec/volumes", Value: []any{vol("nvsnap-lib")}},
		{Op: "add", Path: "/spec/volumes/-", Value: vol("nvsnap-lib")}, // restore bundle re-adds
		{Op: "add", Path: "/spec/volumes/-", Value: vol("nvsnap-tools")},
	}
	out := mergePatchPlan(in)
	libs := 0
	for _, p := range out {
		if (p.Path == "/spec/volumes" && patchElementName(p.Value.([]any)[0]) == "nvsnap-lib") ||
			(p.Path == "/spec/volumes/-" && patchElementName(p.Value) == "nvsnap-lib") {
			libs++
		}
	}
	if libs != 1 {
		t.Errorf("nvsnap-lib emitted %d times, want exactly 1", libs)
	}
}

// Non-empty original arrays: both sides already use /- appends; dedupe only.
func TestMergePatchPlan_AppendsPreservedAndDeduped(t *testing.T) {
	in := []PatchOp{
		{Op: "add", Path: "/spec/volumes/-", Value: vol("nvsnap-lib")},
		{Op: "add", Path: "/spec/volumes/-", Value: vol("nvsnap-rox")},
		{Op: "add", Path: "/spec/volumes/-", Value: vol("nvsnap-rox")}, // dup
	}
	out := mergePatchPlan(in)
	if got := countOps(out, "/spec/volumes/-"); got != 2 {
		t.Errorf("appends = %d, want 2 (one nvsnap-rox dropped)", got)
	}
}

// Non-mergeable paths and non-add ops pass through untouched.
func TestMergePatchPlan_PassThrough(t *testing.T) {
	in := []PatchOp{
		{Op: "add", Path: "/spec/containers/0/command", Value: []any{"vllm", "serve"}},
		{Op: "replace", Path: "/spec/volumes", Value: []any{vol("x")}},
		{Op: "add", Path: "/spec/containers/0/securityContext/privileged", Value: true},
	}
	out := mergePatchPlan(in)
	if len(out) != 3 {
		t.Fatalf("pass-through changed op count: got %d, want 3", len(out))
	}
	if out[0].Path != "/spec/containers/0/command" || len(out[0].Value.([]any)) != 2 {
		t.Error("command (slice-valued, non-array) must pass through unchanged")
	}
}
