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

package v1

import "testing"

func TestCryoConfigCompleteNil(t *testing.T) {
	var c *CryoConfig
	got := c.Complete(EnvTypeStage)
	if got == nil {
		t.Fatal("Complete(nil) returned nil; should return defaulted CryoConfig")
	}
	if got.IntegrationEnabled || got.RestoreEnabled {
		t.Errorf("nil → defaults should leave both disabled, got %+v", got)
	}
	if got.ServerURL != DefaultCryoServerURL {
		t.Errorf("ServerURL = %q, want %q", got.ServerURL, DefaultCryoServerURL)
	}
	if got.WarmupTimeoutSeconds != DefaultCryoWarmupTimeoutSeconds {
		t.Errorf("WarmupTimeoutSeconds = %d, want %d", got.WarmupTimeoutSeconds, DefaultCryoWarmupTimeoutSeconds)
	}
	if got.WarmupBufferSeconds != DefaultCryoWarmupBufferSeconds {
		t.Errorf("WarmupBufferSeconds = %d, want %d", got.WarmupBufferSeconds, DefaultCryoWarmupBufferSeconds)
	}
}

func TestCryoConfigCompletePreservesOverrides(t *testing.T) {
	in := &CryoConfig{
		IntegrationEnabled:   true,
		RestoreEnabled:       true,
		ServerURL:            "http://my-cryo.example.com:8080",
		WarmupTimeoutSeconds: 600,
		WarmupBufferSeconds:  30,
	}
	got := in.Complete(EnvTypeProd)
	if !got.IntegrationEnabled || !got.RestoreEnabled {
		t.Errorf("Complete should preserve enable bits; got %+v", got)
	}
	if got.ServerURL != "http://my-cryo.example.com:8080" {
		t.Errorf("ServerURL override not preserved: %q", got.ServerURL)
	}
	if got.WarmupTimeoutSeconds != 600 || got.WarmupBufferSeconds != 30 {
		t.Errorf("warmup overrides not preserved: %+v", got)
	}
}

func TestCryoConfigCompletePartialOverride(t *testing.T) {
	// Enabled but no URL set — defaults should fill the URL.
	in := &CryoConfig{IntegrationEnabled: true}
	got := in.Complete(EnvTypeStage)
	if !got.IntegrationEnabled {
		t.Errorf("IntegrationEnabled lost across Complete")
	}
	if got.ServerURL != DefaultCryoServerURL {
		t.Errorf("ServerURL should default when empty, got %q", got.ServerURL)
	}
}

func TestCryoConfigCompleteReturnsCopy(t *testing.T) {
	in := &CryoConfig{IntegrationEnabled: true}
	got := in.Complete(EnvTypeStage)
	got.IntegrationEnabled = false
	if !in.IntegrationEnabled {
		t.Error("Complete returned a reference to input — should be a deep copy")
	}
}

func TestCryoConfigIsEnabled(t *testing.T) {
	cases := []struct {
		name string
		c    *CryoConfig
		want bool
	}{
		{"nil", nil, false},
		{"zero", &CryoConfig{}, false},
		{"checkpoint only", &CryoConfig{IntegrationEnabled: true}, true},
		{"restore only", &CryoConfig{RestoreEnabled: true}, true},
		{"both", &CryoConfig{IntegrationEnabled: true, RestoreEnabled: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.IsEnabled(); got != tc.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCryoConfigDeepCopy(t *testing.T) {
	in := &CryoConfig{
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

func TestClusterConfigDeepCopyIncludesCryo(t *testing.T) {
	in := &ClusterConfig{
		ClusterName: "c1",
		Cryo: &CryoConfig{
			IntegrationEnabled: true,
			ServerURL:          "http://x:1",
		},
	}
	out := in.DeepCopy()
	if out.Cryo == in.Cryo {
		t.Fatal("ClusterConfig.DeepCopy shared the Cryo pointer with input")
	}
	out.Cryo.ServerURL = "mutated"
	if in.Cryo.ServerURL == "mutated" {
		t.Error("mutation on copied Cryo leaked into input")
	}
}
