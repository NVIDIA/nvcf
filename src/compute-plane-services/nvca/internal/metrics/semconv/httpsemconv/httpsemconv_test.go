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

package httpsemconv

import "testing"

// TestNormalizeMethod pins http.request.method as a closed set. semconv requires
// unknown methods to collapse to _OTHER so a malformed or attacker-influenced
// method cannot mint an unbounded number of time series.
func TestNormalizeMethod(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"known upper", "GET", "GET"},
		{"known upper post", "POST", "POST"},
		{"known lowercase canonicalised", "get", "GET"},
		{"known mixed case canonicalised", "PaTcH", "PATCH"},
		{"query is known", "QUERY", "QUERY"},
		{"custom method collapses", "FROBNICATE", MethodOther},
		{"garbage collapses", "'; DROP TABLE --", MethodOther},
		{"empty stays empty so the attribute is omitted", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeMethod(tt.in); got != tt.want {
				t.Errorf("NormalizeMethod(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestClientAttrsNormalisesMethod checks the normalisation is actually applied
// on the path callers use, not just exposed as a helper.
func TestClientAttrsNormalisesMethod(t *testing.T) {
	attrs := ClientAttrs("icms", "FROBNICATE", "host", "", 200, "")
	var found bool
	for _, a := range attrs {
		if a.Key == RequestMethodKey {
			found = true
			if a.Value.AsString() != MethodOther {
				t.Errorf("http.request.method = %q, want %q", a.Value.AsString(), MethodOther)
			}
		}
	}
	if !found {
		t.Fatal("expected http.request.method to be present")
	}
}

// TestClientAttrsOmitsEmptyValues pins the documented contract: every attribute
// is omitted when unset, so successful calls carry no error.type and transport
// failures carry no status code.
func TestClientAttrsOmitsEmptyValues(t *testing.T) {
	attrs := ClientAttrs("", "", "", "", 0, "")
	if len(attrs) != 0 {
		t.Fatalf("expected no attributes for an all-empty call, got %v", attrs)
	}

	success := ClientAttrs("icms", "GET", "host", "v1/x/{id}", 200, "")
	for _, a := range success {
		if string(a.Key) == "error.type" {
			t.Errorf("successful call must not carry error.type")
		}
	}

	failure := ClientAttrs("icms", "GET", "host", "v1/x/{id}", 0, "timeout")
	for _, a := range failure {
		if a.Key == ResponseStatusCodeKey {
			t.Errorf("transport failure must not carry http.response.status_code")
		}
	}
}
