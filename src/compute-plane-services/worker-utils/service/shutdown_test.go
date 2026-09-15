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

package service

import (
	"errors"
	"testing"
)

// TestIsFatalRunError_SIGTERM is the regression test for Run() panicking on a
// normal container stop. Kubernetes stops containers with SIGTERM, which the
// servers run group surfaces as "received signal terminated"; only the SIGINT
// wording used to be excused, so every graceful shutdown panicked.
func TestIsFatalRunError_SIGTERM(t *testing.T) {
	if isFatalRunError(errors.New("received signal terminated")) {
		t.Fatal("SIGTERM shutdown must not be treated as a fatal run error")
	}
}

func TestIsFatalRunError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "clean exit", err: nil, want: false},
		{name: "sigterm", err: errors.New("received signal terminated"), want: false},
		{name: "sigint", err: errors.New("received signal interrupt"), want: false},
		{name: "startup failure", err: errors.New("inference container not ready"), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFatalRunError(tt.err); got != tt.want {
				t.Fatalf("isFatalRunError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
