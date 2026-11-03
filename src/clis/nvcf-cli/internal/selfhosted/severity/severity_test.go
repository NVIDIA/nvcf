/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package severity

import "testing"

// A miss fails closed: only the two named non-blocking severities warn.
func TestOf_FailsClosed(t *testing.T) {
	for _, tc := range []struct {
		passed bool
		sev    Severity
		want   Grade
	}{
		{true, Error, Pass},
		{true, "", Pass},
		{false, Info, Warn},
		{false, Warning, Warn},
		{false, Error, Fail},
		{false, "Error", Fail},
		{false, "warn", Fail},
		{false, "", Fail},
	} {
		if got := Of(tc.passed, tc.sev); got != tc.want {
			t.Errorf("Of(%v, %q) = %v, want %v", tc.passed, tc.sev, got, tc.want)
		}
	}
}
