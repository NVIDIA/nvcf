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
	"context"
	"strings"
	"testing"
)

// Whole-rootfs capture must not start by accident. It succeeds quietly --
// producing a capture that restores -- so a cluster whose cachedir setting was
// dropped keeps running while diverging from every workload and benchmark that
// assumes cachedir. The agent refuses at startup instead.
func TestStartRootfsCaptureRefusesWholeRootfs(t *testing.T) {
	a := &Agent{}
	_, err := a.startRootfsCapture(context.Background(), RootfsCaptureConfig{
		Enabled: true, // no PodCacheDir, no override
	})
	if err == nil {
		t.Fatal("expected refusal when --pod-cache-dir is unset; whole-rootfs must be opt-in")
	}
	for _, want := range []string{"pod-cache-dir", "allow-whole-rootfs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should tell the operator about %q, got: %v", want, err)
		}
	}
}

// The override exists so an operator can still run that path deliberately.
// It must get past the guard -- failing later for an unrelated reason (no
// kube client in a unit test) is fine; failing *at the guard* is not.
func TestStartRootfsCaptureAllowsExplicitOptIn(t *testing.T) {
	a := &Agent{}
	_, err := a.startRootfsCapture(context.Background(), RootfsCaptureConfig{
		Enabled:          true,
		AllowWholeRootfs: true,
	})
	if err != nil && strings.Contains(err.Error(), "whole-rootfs capture is not supported") {
		t.Fatalf("explicit opt-in must pass the guard, got: %v", err)
	}
}

// Disabled stays a clean no-op: the guard must not turn "capture off" into an
// error for every agent that does not run capture at all.
func TestStartRootfsCaptureDisabledIsNoop(t *testing.T) {
	a := &Agent{}
	b, err := a.startRootfsCapture(context.Background(), RootfsCaptureConfig{Enabled: false})
	if err != nil || b != nil {
		t.Fatalf("disabled capture should be a no-op, got backend=%v err=%v", b, err)
	}
}
