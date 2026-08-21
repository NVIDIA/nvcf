// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverExcludesActiveSegmentForEachCaptureType(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{
		"request-trace.000000.jsonl.gz",
		"request-trace.000001.jsonl.gz",
		"request-audit.000007.jsonl.gz",
		"request-audit.000008.jsonl.gz",
		"unrelated.jsonl.gz",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", name, err)
		}
	}

	segments, err := Discover(directory, "request-trace", "request-audit")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("segments = %d, want 2: %#v", len(segments), segments)
	}
	got := map[CaptureType]int{}
	for _, item := range segments {
		got[item.CaptureType] = item.Index
	}
	if got[CaptureTypeTrace] != 0 || got[CaptureTypeAudit] != 7 {
		t.Fatalf("closed indexes = %#v, want trace=0 audit=7", got)
	}
}

func TestDiscoverLeavesOnlySegmentActive(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "request-trace.000000.jsonl.gz"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	segments, err := Discover(directory, "request-trace", "request-audit")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(segments) != 0 {
		t.Fatalf("segments = %#v, want none", segments)
	}
}
