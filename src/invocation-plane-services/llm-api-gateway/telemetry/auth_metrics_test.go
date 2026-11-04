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

package telemetry

import "testing"

func TestCanonicalGRPCStatusClampsUnknownCodes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"OK", "OK"},
		{"DeadlineExceeded", "DeadlineExceeded"},
		{"PermissionDenied", "PermissionDenied"},
		{"Unauthenticated", "Unauthenticated"},
		{"Unimplemented", "Unimplemented"},
		{"DataLoss", "DataLoss"},
		// "Other" is itself canonical (the fallback bucket) so it passes
		// through rather than being re-clamped.
		{"Other", "Other"},
		// Non-canonical status strings returned by status.Code(err).String()
		// for codes outside the standard set must collapse to "Other" so the
		// counter does not gain unbounded label cardinality.
		{"Code(42)", "Other"},
		{"Code(99)", "Other"},
		{"", "Other"},
		{"NotAGRPCStatus", "Other"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := canonicalGRPCStatus(tc.input); got != tc.want {
				t.Fatalf("canonicalGRPCStatus(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestAuthResultLabel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"OK", "ok"},
		{"DeadlineExceeded", "error"},
		{"Unknown", "error"},
		// The function maps purely on string identity, not on canonical-ness,
		// so an unmapped input still routes to "error". Callers should clamp
		// through canonicalGRPCStatus first.
		{"Code(42)", "error"},
		{"", "error"},
		// "Other" (the canonical fallback bucket) is also tagged as error.
		{"Other", "error"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := authResultLabel(tc.input); got != tc.want {
				t.Fatalf("authResultLabel(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestCanonicalGRPCStatusesCoversStandardSet(t *testing.T) {
	// Lock in the canonical set so future changes to the map are intentional.
	// The 17 standard gRPC codes plus the "Other" fallback bucket; "Other"
	// is required so preInitAuthMetrics emits a zero sample for it on cold
	// start (matching the runtime label canonicalGRPCStatus can produce).
	want := []string{
		"OK", "Canceled", "Unknown", "InvalidArgument", "DeadlineExceeded",
		"NotFound", "AlreadyExists", "PermissionDenied", "ResourceExhausted",
		"FailedPrecondition", "Aborted", "OutOfRange", "Unimplemented",
		"Internal", "Unavailable", "DataLoss", "Unauthenticated",
		"Other",
	}
	if got, expect := len(canonicalGRPCStatuses), len(want); got != expect {
		t.Fatalf("canonicalGRPCStatuses size = %d, want %d", got, expect)
	}
	for _, code := range want {
		if _, ok := canonicalGRPCStatuses[code]; !ok {
			t.Errorf("canonicalGRPCStatuses missing %q", code)
		}
	}
}
