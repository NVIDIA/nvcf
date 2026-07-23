/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNvSnapFunctionStateDeepCopy(t *testing.T) {
	now := metav1.NewTime(time.Now())
	in := &NvSnapFunctionState{
		ObjectMeta: metav1.ObjectMeta{
			Name: "fv-uuid-1",
		},
		Spec: NvSnapFunctionStateSpec{
			FunctionVersionID:            "fv-uuid-1",
			OptOut:                       false,
			WarmupTimeoutSecondsOverride: 900,
		},
		Status: NvSnapFunctionStateStatus{
			CheckpointHash:  "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			CapturedHere:    true,
			CapturedAt:      &now,
			LocalCacheState: LocalCacheStateWarm,
			AttemptCount:    1,
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "CaptureCommitted", Message: "ok"},
			},
		},
	}

	out := in.DeepCopy()
	if out == in {
		t.Fatal("DeepCopy returned the same pointer")
	}
	if out.Spec != in.Spec {
		t.Errorf("Spec contents differ: in=%+v out=%+v", in.Spec, out.Spec)
	}
	if out.Status.CheckpointHash != in.Status.CheckpointHash {
		t.Errorf("Status.CheckpointHash differ")
	}

	// Mutate the copy; original must not change.
	out.Status.CheckpointHash = "mutated"
	if in.Status.CheckpointHash == "mutated" {
		t.Error("mutation on DeepCopy leaked into input")
	}

	// Conditions slice must be independent.
	out.Status.Conditions[0].Message = "changed"
	if in.Status.Conditions[0].Message == "changed" {
		t.Error("Conditions slice was shared between in and out")
	}

	// Time pointer must be independent.
	if out.Status.CapturedAt == in.Status.CapturedAt {
		t.Error("CapturedAt pointer was shared")
	}
}

func TestNvSnapFunctionStateListDeepCopy(t *testing.T) {
	in := &NvSnapFunctionStateList{
		Items: []NvSnapFunctionState{
			{ObjectMeta: metav1.ObjectMeta{Name: "fv-1"}, Spec: NvSnapFunctionStateSpec{FunctionVersionID: "fv-1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "fv-2"}, Spec: NvSnapFunctionStateSpec{FunctionVersionID: "fv-2", OptOut: true}},
		},
	}
	out := in.DeepCopy()
	if len(out.Items) != 2 {
		t.Fatalf("Items len = %d, want 2", len(out.Items))
	}
	out.Items[0].Spec.OptOut = true
	if in.Items[0].Spec.OptOut {
		t.Error("Items slice was shared between in and out")
	}
}

func TestSchemeGroupVersionStable(t *testing.T) {
	// Pinned: NVCA convention is *.nvcf.nvidia.io, and v1alpha1 means
	// breaking changes are allowed. Tightening this test forces a
	// conscious decision when bumping versions.
	if SchemeGroupVersion.Group != "nvsnap.nvcf.nvidia.io" {
		t.Errorf("Group = %q, want nvsnap.nvcf.nvidia.io", SchemeGroupVersion.Group)
	}
	if SchemeGroupVersion.Version != "v1alpha1" {
		t.Errorf("Version = %q, want v1alpha1", SchemeGroupVersion.Version)
	}
}

func TestResourceGroupQualified(t *testing.T) {
	gr := Resource("nvsnapfunctionstates")
	if gr.Group != SchemeGroupVersion.Group {
		t.Errorf("Group = %q, want %q", gr.Group, SchemeGroupVersion.Group)
	}
	if gr.Resource != "nvsnapfunctionstates" {
		t.Errorf("Resource = %q", gr.Resource)
	}
}

func TestLocalCacheStateValues(t *testing.T) {
	// Stable string values — NVCA agents in different versions may
	// read the same status object, so changing these is a wire-
	// protocol break. Guard with a test that fails loudly on rename.
	want := map[NvSnapFunctionStateLocalCacheState]string{
		LocalCacheStateCold:     "Cold",
		LocalCacheStateFetching: "Fetching",
		LocalCacheStateWarm:     "Warm",
		LocalCacheStateFailed:   "Failed",
	}
	for state, expected := range want {
		if string(state) != expected {
			t.Errorf("%v = %q, want %q", state, string(state), expected)
		}
	}
}
