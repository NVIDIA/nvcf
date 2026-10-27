// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestDiscoverExcludesActiveSegment(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{
		"request-trace.000000.jsonl.gz",
		"request-trace.000001.jsonl.gz",
		"request-trace.000002.jsonl.gz",
		"unrelated.jsonl.gz",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", name, err)
		}
	}

	segments, err := Discover(directory, "request-trace")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("segments = %d, want 2: %#v", len(segments), segments)
	}
	for _, item := range segments {
		if item.Index == 2 {
			t.Fatalf("active segment index 2 was returned: %#v", segments)
		}
	}
}

func TestDiscoverIgnoresOtherPrefixes(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{
		"request-trace.000000.jsonl.gz",
		"request-trace.000001.jsonl.gz",
		"request-audit.000007.jsonl.gz",
		"request-audit.000008.jsonl.gz",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", name, err)
		}
	}

	segments, err := Discover(directory, "request-trace")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(segments) != 1 || segments[0].Index != 0 {
		t.Fatalf("segments = %#v, want only request-trace index 0", segments)
	}
}

func TestDiscoverLeavesOnlySegmentActive(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "request-trace.000000.jsonl.gz"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	segments, err := Discover(directory, "request-trace")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(segments) != 0 {
		t.Fatalf("segments = %#v, want none", segments)
	}
}

func writeFiles(t *testing.T, directory string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", name, err)
		}
	}
}

func indexes(segments []Segment) []int {
	out := make([]int, 0, len(segments))
	for _, item := range segments {
		out = append(out, item.Index)
	}
	return out
}

func TestDiscoverFindsSevenDigitIndexAfterSixDigit(t *testing.T) {
	directory := t.TempDir()
	writeFiles(t, directory,
		"request-trace.999998.jsonl.gz",
		"request-trace.999999.jsonl.gz",
		"request-trace.1000000.jsonl.gz",
	)

	segments, err := Discover(directory, "request-trace")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got := indexes(segments); !slices.Equal(got, []int{999998, 999999}) {
		t.Fatalf("closed indexes = %v, want [999998 999999] with 1000000 active", got)
	}
}

func TestDiscoverOrdersByIndexNotModTime(t *testing.T) {
	directory := t.TempDir()
	writeFiles(t, directory,
		"request-trace.000001.jsonl.gz",
		"request-trace.000002.jsonl.gz",
		"request-trace.000003.jsonl.gz",
		"request-trace.000004.jsonl.gz",
	)
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for name, offset := range map[string]time.Duration{
		"request-trace.000001.jsonl.gz": 3 * time.Minute,
		"request-trace.000002.jsonl.gz": time.Minute,
		"request-trace.000003.jsonl.gz": 2 * time.Minute,
	} {
		if err := os.Chtimes(filepath.Join(directory, name), base.Add(offset), base.Add(offset)); err != nil {
			t.Fatal(err)
		}
	}

	segments, err := Discover(directory, "request-trace")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got := indexes(segments); !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("closed indexes = %v, want [1 2 3] in index order", got)
	}
}

func TestScanNameRules(t *testing.T) {
	tests := []struct {
		name      string
		closed    bool
		oddNameOK bool
	}{
		{name: "request-trace.000001.jsonl.gz", closed: true},
		{name: "request-trace.1234567.jsonl.gz", closed: true},
		{name: "request-trace.12345.jsonl.gz", oddNameOK: true},
		{name: "request-trace.00000a.jsonl.gz", oddNameOK: true},
		{name: "request-trace.000002.jsonl.gz.tmp", oddNameOK: true},
		{name: "request-trace.000003.jsonl", oddNameOK: true},
		{name: "request-trace.99999999999999999999.jsonl.gz", oddNameOK: true},
		{name: "request-trace-other.000004.jsonl.gz"},
		{name: "unrelated.000005.jsonl.gz"},
		{name: "notes.txt"},
	}
	directory := t.TempDir()
	wantOdd := 0
	for _, tt := range tests {
		writeFiles(t, directory, tt.name)
		if tt.oddNameOK {
			wantOdd++
		}
	}
	// The newest valid segment is active, so add one past every test name.
	writeFiles(t, directory, "request-trace.9999999.jsonl.gz")

	scan, err := ScanDir(directory, []string{"request-trace"})
	if err != nil {
		t.Fatalf("ScanDir() error = %v", err)
	}
	if got := indexes(scan.Closed); !slices.Equal(got, []int{1, 1234567}) {
		t.Errorf("closed indexes = %v, want [1 1234567]", got)
	}
	if scan.Skipped.OddNames != wantOdd {
		t.Errorf("odd names = %d, want %d", scan.Skipped.OddNames, wantOdd)
	}
	if scan.Skipped.NonRegular != 0 {
		t.Errorf("non-regular = %d, want 0", scan.Skipped.NonRegular)
	}
}

func TestScanSkipsNonRegularAndSubdirectoryEntries(t *testing.T) {
	directory := t.TempDir()
	writeFiles(t, directory, "request-trace.000001.jsonl.gz", "request-trace.000009.jsonl.gz")
	if err := os.Mkdir(filepath.Join(directory, "request-trace.000002.jsonl.gz"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "request-trace.000001.jsonl.gz"), filepath.Join(directory, "request-trace.000003.jsonl.gz")); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(directory, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, nested, "request-trace.000004.jsonl.gz", "request-trace.000005.jsonl.gz")

	scan, err := ScanDir(directory, []string{"request-trace"})
	if err != nil {
		t.Fatalf("ScanDir() error = %v", err)
	}
	if got := indexes(scan.Closed); !slices.Equal(got, []int{1}) {
		t.Errorf("closed indexes = %v, want [1]", got)
	}
	if scan.Skipped.NonRegular != 2 {
		t.Errorf("non-regular = %d, want 2 (directory and symlink)", scan.Skipped.NonRegular)
	}
}

func TestScanIgnoresFileThatVanishesBeforeStat(t *testing.T) {
	directory := t.TempDir()
	writeFiles(t, directory,
		"request-trace.000001.jsonl.gz",
		"request-trace.000002.jsonl.gz",
		"request-trace.000003.jsonl.gz",
	)
	original := readDir
	t.Cleanup(func() { readDir = original })
	readDir = func(name string) ([]os.DirEntry, error) {
		entries, err := original(name)
		if err == nil {
			err = os.Remove(filepath.Join(directory, "request-trace.000001.jsonl.gz"))
		}
		return entries, err
	}

	scan, err := ScanDir(directory, []string{"request-trace"})
	if err != nil {
		t.Fatalf("ScanDir() error = %v, want the vanished file ignored", err)
	}
	if got := indexes(scan.Closed); !slices.Equal(got, []int{2}) {
		t.Errorf("closed indexes = %v, want [2]", got)
	}
}

func TestValidatePrefixes(t *testing.T) {
	tests := []struct {
		name     string
		prefixes []string
		wantErr  bool
	}{
		{name: "one prefix", prefixes: []string{"request-trace"}},
		{name: "two distinct prefixes", prefixes: []string{"request-trace", "request-audit"}},
		{name: "none", prefixes: nil, wantErr: true},
		{name: "empty", prefixes: []string{""}, wantErr: true},
		{name: "blank", prefixes: []string{" "}, wantErr: true},
		{name: "path separator", prefixes: []string{"traces/request-trace"}, wantErr: true},
		{name: "duplicate", prefixes: []string{"request-trace", "request-trace"}, wantErr: true},
		{name: "dotted extension", prefixes: []string{"request-trace", "request-trace.x"}, wantErr: true},
		{name: "dotted extension reversed", prefixes: []string{"request-trace.x", "request-trace"}, wantErr: true},
		{name: "plain string prefix is not an overlap", prefixes: []string{"request", "request-trace"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePrefixes(tt.prefixes)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidatePrefixes(%q) error = %v, wantErr %v", tt.prefixes, err, tt.wantErr)
			}
		})
	}
}

func TestScanRejectsOverlappingPrefixes(t *testing.T) {
	if _, err := ScanDir(t.TempDir(), []string{"request-trace", "request-trace.x"}); err == nil {
		t.Fatal("ScanDir() accepted overlapping prefixes")
	}
}
