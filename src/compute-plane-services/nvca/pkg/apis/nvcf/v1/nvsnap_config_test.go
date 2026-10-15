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

package v1

import "testing"

func TestNvSnapConfigCompleteNil(t *testing.T) {
	var c *NvSnapConfig
	got := c.Complete(EnvTypeStage)
	if got == nil {
		t.Fatal("Complete(nil) returned nil; should return defaulted NvSnapConfig")
	}
	if got.IntegrationEnabled || got.RestoreEnabled {
		t.Errorf("nil → defaults should leave both disabled, got %+v", got)
	}
	if got.ServerURL != DefaultNvSnapServerURL {
		t.Errorf("ServerURL = %q, want %q", got.ServerURL, DefaultNvSnapServerURL)
	}
	if got.WarmupTimeoutSeconds != DefaultNvSnapWarmupTimeoutSeconds {
		t.Errorf("WarmupTimeoutSeconds = %d, want %d", got.WarmupTimeoutSeconds, DefaultNvSnapWarmupTimeoutSeconds)
	}
	if got.WarmupBufferSeconds != DefaultNvSnapWarmupBufferSeconds {
		t.Errorf("WarmupBufferSeconds = %d, want %d", got.WarmupBufferSeconds, DefaultNvSnapWarmupBufferSeconds)
	}
}

func TestNvSnapConfigCompletePreservesOverrides(t *testing.T) {
	in := &NvSnapConfig{
		IntegrationEnabled:   true,
		RestoreEnabled:       true,
		ServerURL:            "http://my-nvsnap.example.com:8080",
		WarmupTimeoutSeconds: 600,
		WarmupBufferSeconds:  30,
	}
	got := in.Complete(EnvTypeProd)
	if !got.IntegrationEnabled || !got.RestoreEnabled {
		t.Errorf("Complete should preserve enable bits; got %+v", got)
	}
	if got.ServerURL != "http://my-nvsnap.example.com:8080" {
		t.Errorf("ServerURL override not preserved: %q", got.ServerURL)
	}
	if got.WarmupTimeoutSeconds != 600 || got.WarmupBufferSeconds != 30 {
		t.Errorf("warmup overrides not preserved: %+v", got)
	}
}

func TestNvSnapConfigCompletePartialOverride(t *testing.T) {
	// Enabled but no URL set — defaults should fill the URL.
	in := &NvSnapConfig{IntegrationEnabled: true}
	got := in.Complete(EnvTypeStage)
	if !got.IntegrationEnabled {
		t.Errorf("IntegrationEnabled lost across Complete")
	}
	if got.ServerURL != DefaultNvSnapServerURL {
		t.Errorf("ServerURL should default when empty, got %q", got.ServerURL)
	}
}

func TestNvSnapConfigCompleteReturnsCopy(t *testing.T) {
	in := &NvSnapConfig{IntegrationEnabled: true}
	got := in.Complete(EnvTypeStage)
	got.IntegrationEnabled = false
	if !in.IntegrationEnabled {
		t.Error("Complete returned a reference to input — should be a deep copy")
	}
}

// v0.0.49 / rootfs-everywhere: DefaultNvSnapWarmupBufferSeconds flipped
// from 10 → 0, AND Complete() no longer treats 0 as a sentinel — the
// value the operator sets is the value used. This test pins both
// behaviors so a future reviewer can't silently re-introduce a
// default dwell time.
func TestNvSnapConfigCompleteWarmupBufferIsLiteralZero(t *testing.T) {
	if DefaultNvSnapWarmupBufferSeconds != 0 {
		t.Fatalf("DefaultNvSnapWarmupBufferSeconds = %d; v0.0.49 design pins it to 0", DefaultNvSnapWarmupBufferSeconds)
	}
	// Explicit zero must survive Complete() unchanged.
	in := &NvSnapConfig{IntegrationEnabled: true, WarmupBufferSeconds: 0}
	got := in.Complete(EnvTypeStage)
	if got.WarmupBufferSeconds != 0 {
		t.Errorf("Complete() must preserve explicit 0; got %d", got.WarmupBufferSeconds)
	}
	// And an explicit non-zero override still works.
	in2 := &NvSnapConfig{IntegrationEnabled: true, WarmupBufferSeconds: 5}
	got2 := in2.Complete(EnvTypeStage)
	if got2.WarmupBufferSeconds != 5 {
		t.Errorf("Complete() must preserve explicit non-zero; got %d", got2.WarmupBufferSeconds)
	}
}

func TestNvSnapConfigIsEnabled(t *testing.T) {
	cases := []struct {
		name string
		c    *NvSnapConfig
		want bool
	}{
		{"nil", nil, false},
		{"zero", &NvSnapConfig{}, false},
		{"checkpoint only", &NvSnapConfig{IntegrationEnabled: true}, true},
		{"restore only", &NvSnapConfig{RestoreEnabled: true}, true},
		{"both", &NvSnapConfig{IntegrationEnabled: true, RestoreEnabled: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.IsEnabled(); got != tc.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNvSnapConfigDeepCopy(t *testing.T) {
	in := &NvSnapConfig{
		IntegrationEnabled:   true,
		RestoreEnabled:       false,
		ServerURL:            "http://x:1",
		WarmupTimeoutSeconds: 900,
		WarmupBufferSeconds:  10,
	}
	out := in.DeepCopy()
	if out == in {
		t.Fatal("DeepCopy returned the same pointer")
	}
	if *out != *in {
		t.Errorf("DeepCopy contents differ: in=%+v out=%+v", in, out)
	}
	out.ServerURL = "mutated"
	if in.ServerURL == "mutated" {
		t.Error("mutation on DeepCopy leaked into input")
	}
}

func TestClusterConfigDeepCopyIncludesNvSnap(t *testing.T) {
	in := &ClusterConfig{
		ClusterName: "c1",
		NvSnap: &NvSnapConfig{
			IntegrationEnabled: true,
			ServerURL:          "http://x:1",
		},
	}
	out := in.DeepCopy()
	if out.NvSnap == in.NvSnap {
		t.Fatal("ClusterConfig.DeepCopy shared the NvSnap pointer with input")
	}
	out.NvSnap.ServerURL = "mutated"
	if in.NvSnap.ServerURL == "mutated" {
		t.Error("mutation on copied NvSnap leaked into input")
	}
}
