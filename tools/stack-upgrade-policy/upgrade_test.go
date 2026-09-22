// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// receiptsIn is the release that first ships the receipt writer. Everything
// before it is indistinguishable from everything else before it, so this is
// the one version the rule has to special-case.
const receiptsIn = "1.0.1"

func TestUpgradePathDecisionTable(t *testing.T) {
	cases := []struct {
		name      string
		installed string // "" means the cluster has no receipt at all
		target    string
		allowed   bool
		mentions  string // the version the refusal must point the customer at
	}{
		// The path this whole design exists to make safe.
		{"1.0.1 to 2.0.0 is one major hop", "1.0.1", "2.0.0", true, ""},
		{"2.0.0 to 3.0.4 is one major hop", "2.0.0", "3.0.4", true, ""},

		// Within a line there is no boundary to respect.
		{"1.0.1 to 1.9.0 stays in the line", "1.0.1", "1.9.0", true, ""},
		{"2.0.0 to 2.5.3 stays in the line", "2.0.0", "2.5.3", true, ""},

		// Skipping a major is the thing being prevented.
		{"1.0.1 to 3.0.0 skips the 2.x line", "1.0.1", "3.0.0", false, "2."},
		{"1.0.1 to 4.0.0 skips two lines", "1.0.1", "4.0.0", false, "2."},

		// A cluster with no receipt predates the writer. Sending it to 1.x is
		// safe and is what makes every later hop decidable; sending it past
		// 1.x is the case that has to be refused with a followable
		// instruction, because 0.x and 1.0.0 look identical from here.
		{"no receipt may reach the receipt-writing line", "", "1.2.0", true, ""},
		{"no receipt may not skip to 2.x", "", "2.0.0", false, "1."},
		{"no receipt may not skip to 3.x", "", "3.0.0", false, "1."},

		// Downgrades are not an upgrade path.
		{"2.0.0 back to 1.9.0 is refused", "2.0.0", "1.9.0", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanUpgrade(tc.installed, tc.target, receiptsIn)
			if err != nil {
				t.Fatalf("CanUpgrade(%q, %q): %v", tc.installed, tc.target, err)
			}
			if got.Allowed != tc.allowed {
				t.Fatalf("Allowed = %v, want %v (reason: %s)", got.Allowed, tc.allowed, got.Reason)
			}
			if tc.allowed {
				return
			}
			if got.Reason == "" {
				t.Fatal("refusal carries no reason; the customer needs the next step")
			}
			if tc.mentions != "" && !strings.Contains(got.Reason, tc.mentions) {
				t.Fatalf("refusal does not point at %q: %s", tc.mentions, got.Reason)
			}
		})
	}
}

// A refusal that does not name the next version to install is a dead end: the
// customer cannot act on it, which is what made fail-closed unusable before
// the writer was backported.
func TestRefusalNamesTheNextHop(t *testing.T) {
	got, err := CanUpgrade("1.0.1", "4.0.0", receiptsIn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Reason, "2.") {
		t.Fatalf("refusal should send a 1.x cluster to 2.x, got: %s", got.Reason)
	}
}

func TestCanUpgradeRejectsUnparseableVersions(t *testing.T) {
	if _, err := CanUpgrade("1.0.1", "not-a-version", receiptsIn); err == nil {
		t.Fatal("err = nil, want an error rather than a silent allow")
	}
}
