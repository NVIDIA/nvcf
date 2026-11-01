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

package framework

import (
	"testing"
)

// TestBackendRoutes is a helper to test that all the given routes will
// route properly in the backend.
func TestBackendRoutes(t *testing.T, b *Backend, rs []string) {
	for _, r := range rs {
		if b.Route(r) == nil {
			t.Fatalf("bad route: %s", r)
		}
	}
}
