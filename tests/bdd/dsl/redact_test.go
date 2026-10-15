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

package dsl

import (
	"strings"
	"testing"
)

func TestRedactSecretsMasksAPIKeysAndJWTs(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"
	tests := []struct {
		name, in, want string
	}{
		{"key in a response", `{"id":"k1","value":"nvapi-AbC_123-xyz.q"}`, `{"id":"k1","value":"[redacted]"}`},
		{"jwt", "Bearer " + jwt + " rejected", "Bearer [redacted] rejected"},
		{"two keys", "nvapi-a nvapi-b", "[redacted] [redacted]"},
		{"no secret", `{"error":"bad request"}`, `{"error":"bad request"}`},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedactSecrets(tt.in); got != tt.want {
				t.Fatalf("RedactSecrets(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	if strings.Contains(RedactSecrets("x nvapi-secretvalue y"), "secretvalue") {
		t.Fatal("key value leaked")
	}
}
