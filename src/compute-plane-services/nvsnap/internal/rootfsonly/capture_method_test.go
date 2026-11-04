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

package rootfsonly

import "testing"

// The capture-method stamp is the contract between capture and the restore
// webhook (mutate.go dispatches on manifest.CaptureMethod). These tests
// lock it so the cachedir/ember path can't silently regress to whole-rootfs
// — the exact drift that lost prewarm + gemm capture on the bench
// (cachedir_capture_method_bug, 2026-06-28).
func TestCaptureMethodFor(t *testing.T) {
	cases := []struct {
		name       string
		cacheDir   string
		wantMethod string
		wantDir    string
	}{
		{"cachedir mode (pod-cache-dir set)", "/opt/nvsnap", captureMethodCacheDir, "/opt/nvsnap"},
		{"cachedir mode (alt root)", "/nvsnap", captureMethodCacheDir, "/nvsnap"},
		{"whole-rootfs (no pod-cache-dir)", "", captureMethodRootfs, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, d := captureMethodFor(tc.cacheDir)
			if m != tc.wantMethod {
				t.Errorf("method = %q, want %q", m, tc.wantMethod)
			}
			if d != tc.wantDir {
				t.Errorf("dir = %q, want %q", d, tc.wantDir)
			}
		})
	}
}

// The stamp values must match exactly what the restore webhook keys on
// (internal/webhook/mutate.go: manifest.CaptureMethod == "cachedir" /
// "rootfs"). A rename here without the webhook is a silent dispatch break.
func TestCaptureMethodStampValues(t *testing.T) {
	if captureMethodCacheDir != "cachedir" {
		t.Errorf("captureMethodCacheDir = %q, want \"cachedir\" (webhook restore dispatch contract)", captureMethodCacheDir)
	}
	if captureMethodRootfs != "rootfs" {
		t.Errorf("captureMethodRootfs = %q, want \"rootfs\" (webhook restore dispatch contract)", captureMethodRootfs)
	}
}
