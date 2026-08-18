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

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// The backend chain (Local -> ConfigMap -> PerCapturePVC) answers Stat from
// the first tier that claims a hash, so the tiers can disagree. An L2 PVC left
// behind by an earlier capture claims a hash whose manifest tier is gone, and
// the manifest it returns describes no content. Skipping the capture on that
// claim leaves the pod recorded as captured but unrestorable -- observed on a
// 70B TP=4 workload, where the agent logged "capture skipped: hash already
// exists" then "capture committed for pod files=0 size_mib=0" and the e2e sat
// for an hour waiting for a manifest that was never written.
func TestUsableCapture(t *testing.T) {
	cases := []struct {
		name string
		m    checkpointstore.Manifest
		want bool
	}{
		{
			name: "real capture",
			m:    checkpointstore.Manifest{FileCount: 465, TotalSizeBytes: 141_733_920_768},
			want: true,
		},
		{
			name: "stale L2 claim: no files, no bytes",
			m:    checkpointstore.Manifest{},
			want: false,
		},
		{
			name: "files but no bytes",
			m:    checkpointstore.Manifest{FileCount: 12},
			want: false,
		},
		{
			name: "bytes but no files",
			m:    checkpointstore.Manifest{TotalSizeBytes: 4096},
			want: false,
		},
		{
			name: "single small file is still a capture",
			m:    checkpointstore.Manifest{FileCount: 1, TotalSizeBytes: 1},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := usableCapture(tc.m); got != tc.want {
				t.Errorf("usableCapture(files=%d bytes=%d) = %v, want %v",
					tc.m.FileCount, tc.m.TotalSizeBytes, got, tc.want)
			}
		})
	}
}

// A manifest describing content must short-circuit the capture; one describing
// nothing must not. Capture() itself needs a live pod to go further, so this
// asserts the decision usableCapture drives rather than re-running the walk --
// the decision is the whole behaviour change.
func TestEmptyManifestIsNotTreatedAsExisting(t *testing.T) {
	stale := checkpointstore.Manifest{CapturedOnNodes: []string{"node-a"}}
	if usableCapture(stale) {
		t.Fatal("a manifest with no files and no bytes must not count as an existing capture; " +
			"skipping on it records the pod as captured while leaving it unrestorable")
	}

	real := checkpointstore.Manifest{FileCount: 1, TotalSizeBytes: 1, CapturedOnNodes: []string{"node-a"}}
	if !usableCapture(real) {
		t.Fatal("a manifest describing content must still short-circuit; " +
			"re-capturing every pass would undo the idempotence the check exists for")
	}
}
