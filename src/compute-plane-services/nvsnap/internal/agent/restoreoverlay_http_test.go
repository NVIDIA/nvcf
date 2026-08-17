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

import "testing"

// The lowerDir hint arrives over the agent's HTTP API and is handed to
// OverlayManager.Prepare as the lower layer of an overlay this privileged
// agent mounts into a pod. Anything outside the cache root must be refused.
func TestUnderRootConfinesLowerDirHint(t *testing.T) {
	root := "/var/lib/nvsnap/cache"
	inside := []string{
		"/var/lib/nvsnap/cache",
		"/var/lib/nvsnap/cache/abc123/tree",
		"/var/lib/nvsnap/cache/abc/../abc/tree",
	}
	for _, p := range inside {
		if !underRoot(root, p) {
			t.Errorf("underRoot(%q, %q) = false, want true", root, p)
		}
	}
	outside := []string{
		"/etc",
		"/etc/shadow",
		"/var/lib/nvsnap/cache/../../../etc",
		"/var/lib/nvsnap/cachemore", // prefix match must not count
		"relative/path",             // must be absolute
		"",
	}
	for _, p := range outside {
		if underRoot(root, p) {
			t.Errorf("underRoot(%q, %q) = true, want false", root, p)
		}
	}
}
