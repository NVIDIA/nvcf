// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package foreign

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

// isolatePatterns gives a test an empty pattern registry.
func isolatePatterns(t *testing.T) {
	t.Helper()
	patternMu.Lock()
	saved := patterns
	patterns = nil
	patternMu.Unlock()
	t.Cleanup(func() {
		patternMu.Lock()
		patterns = saved
		patternMu.Unlock()
	})
}

var segmentName = regexp.MustCompile(`^request-trace\.\d{6}\.jsonl\.gz$`)

// protectSpool protects segments and the uploader's own directories, as
// integration does.
func protectSpool(name string) bool {
	return segmentName.MatchString(name) || name == "request-trace-uploader-state"
}

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func registerGenericPatterns() {
	Register(Pattern{Kind: "job_id", Glob: "*.other-id", Action: Remove})
	Register(Pattern{Kind: "staging", Glob: ".other-upload", Action: Remove})
	Register(Pattern{Kind: "tmp", Glob: "*.tmp", Action: Remove})
	Register(Pattern{Kind: "failed", Glob: ".other-failed", Action: Keep})
}

func ownerConfig() Config { return DefaultConfig() }

func TestCleanRemovesLeftoversAndCountsFilesByKind(t *testing.T) {
	isolatePatterns(t)
	registerGenericPatterns()
	root := t.TempDir()
	segments := []string{"request-trace.000001.jsonl.gz", "request-trace.000002.jsonl.gz"}
	for _, name := range segments {
		write(t, filepath.Join(root, name))
	}
	write(t, filepath.Join(root, "request-trace-uploader-state", "journal.json"))
	write(t, filepath.Join(root, "request-trace.000001.jsonl.gz.other-id"))
	write(t, filepath.Join(root, ".other-upload", "a.parquet"))
	write(t, filepath.Join(root, ".other-upload", "b.parquet"))
	write(t, filepath.Join(root, ".other-upload", "batch", "c.parquet"))
	write(t, filepath.Join(root, "stray.tmp"))
	for _, name := range []string{"x.jsonl.gz", "y.jsonl.gz", "z.jsonl.gz"} {
		write(t, filepath.Join(root, ".other-failed", name))
	}

	report, err := ownerConfig().Clean(root, protectSpool)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}

	wantRemoved := map[string]int{"job_id": 1, "staging": 3, "tmp": 1}
	wantKept := map[string]int{"failed": 3}
	if !reflect.DeepEqual(report.Removed, wantRemoved) {
		t.Errorf("Removed = %v, want %v", report.Removed, wantRemoved)
	}
	if !reflect.DeepEqual(report.Kept, wantKept) {
		t.Errorf("Kept = %v, want %v", report.Kept, wantKept)
	}
	for _, name := range append(segments, "request-trace-uploader-state/journal.json", ".other-failed/x.jsonl.gz") {
		if !exists(filepath.Join(root, name)) {
			t.Errorf("%s was removed, want it kept", name)
		}
	}
	for _, name := range []string{"request-trace.000001.jsonl.gz.other-id", ".other-upload", "stray.tmp"} {
		if exists(filepath.Join(root, name)) {
			t.Errorf("%s still exists, want it removed", name)
		}
	}
}

func TestCleanNeverTouchesProtectedEntries(t *testing.T) {
	isolatePatterns(t)
	Register(Pattern{Kind: "everything", Glob: "*", Action: Remove})
	root := t.TempDir()
	write(t, filepath.Join(root, "request-trace.000001.jsonl.gz"))
	write(t, filepath.Join(root, "request-trace-uploader-state", "journal.json"))
	write(t, filepath.Join(root, "leftover"))

	report, err := ownerConfig().Clean(root, protectSpool)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if !exists(filepath.Join(root, "request-trace.000001.jsonl.gz")) {
		t.Fatal("a protected segment was removed")
	}
	if !exists(filepath.Join(root, "request-trace-uploader-state", "journal.json")) {
		t.Fatal("the uploader's own state was removed")
	}
	if report.Removed["everything"] != 1 {
		t.Fatalf("Removed = %v, want only the one unprotected entry", report.Removed)
	}
}

func TestCleanKeepWinsOverRemove(t *testing.T) {
	isolatePatterns(t)
	Register(Pattern{Kind: "tmp", Glob: "*.tmp", Action: Remove})
	Register(Pattern{Kind: "held", Glob: "held.*", Action: Keep})
	root := t.TempDir()
	write(t, filepath.Join(root, "held.tmp"))

	report, err := ownerConfig().Clean(root, protectSpool)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if !exists(filepath.Join(root, "held.tmp")) {
		t.Fatal("held.tmp was removed, want a keep pattern to win")
	}
	if report.Kept["held"] != 1 || len(report.Removed) != 0 {
		t.Fatalf("report = %+v, want held.tmp kept and nothing removed", report)
	}
}

func TestCleanDoesNotFollowSymlinks(t *testing.T) {
	isolatePatterns(t)
	registerGenericPatterns()
	root := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(outside, "precious.jsonl.gz"))
	if err := os.Symlink(outside, filepath.Join(root, ".other-upload")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	report, err := ownerConfig().Clean(root, protectSpool)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if exists(filepath.Join(root, ".other-upload")) {
		t.Fatal("the symlink was not removed")
	}
	if !exists(filepath.Join(outside, "precious.jsonl.gz")) {
		t.Fatal("a file outside the spool was removed through a symlink")
	}
	if report.Removed["staging"] != 1 {
		t.Fatalf("Removed = %v, want the link counted once", report.Removed)
	}
}

func TestCleanContinuesPastARemovalFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions enforced for the current user")
	}
	isolatePatterns(t)
	registerGenericPatterns()
	root := t.TempDir()
	locked := filepath.Join(root, ".other-upload", "locked")
	write(t, filepath.Join(locked, "a.parquet"))
	write(t, filepath.Join(root, "stray.tmp"))
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o750) })

	report, err := ownerConfig().Clean(root, protectSpool)
	if err == nil {
		t.Fatal("Clean() error = nil, want the removal failure reported")
	}
	if report.Removed["tmp"] != 1 || exists(filepath.Join(root, "stray.tmp")) {
		t.Fatalf("report = %+v, want stray.tmp still removed after the failure", report)
	}
	if _, counted := report.Removed["staging"]; counted {
		t.Fatalf("report = %+v, want the failed removal not counted", report)
	}
}

func TestCleanRejectsBadArguments(t *testing.T) {
	isolatePatterns(t)
	registerGenericPatterns()
	if _, err := ownerConfig().Clean(t.TempDir(), nil); err == nil {
		t.Fatal("Clean() error = nil without a protected predicate, want error")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := ownerConfig().Clean(missing, protectSpool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Clean() error = %v for a missing spool, want os.ErrNotExist", err)
	}
}

func TestRegisterPanicsOnWiringMistakes(t *testing.T) {
	tests := []struct {
		name    string
		pattern Pattern
	}{
		{"empty kind", Pattern{Glob: "*.x", Action: Remove}},
		{"kind not a label token", Pattern{Kind: "Other Kind", Glob: "*.x", Action: Remove}},
		{"empty glob", Pattern{Kind: "x", Action: Remove}},
		{"bad glob", Pattern{Kind: "x", Glob: "[", Action: Remove}},
		{"glob with a separator", Pattern{Kind: "x", Glob: "dir/*.x", Action: Remove}},
		{"no action", Pattern{Kind: "x", Glob: "*.x"}},
		{"duplicate glob", Pattern{Kind: "y", Glob: "*.taken", Action: Remove}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolatePatterns(t)
			Register(Pattern{Kind: "taken", Glob: "*.taken", Action: Remove})
			defer func() {
				if recover() == nil {
					t.Fatalf("Register(%+v) did not panic", tt.pattern)
				}
			}()
			Register(tt.pattern)
		})
	}
}

func TestShadowModeDeletesNothing(t *testing.T) {
	isolatePatterns(t)
	registerGenericPatterns()
	root := t.TempDir()
	write(t, filepath.Join(root, "stray.tmp"))
	write(t, filepath.Join(root, ".other-upload", "a.parquet"))
	cfg := Config{Mode: ModeShadow, Destination: "shadow_table"}

	report, err := cfg.Clean(root, protectSpool)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if !exists(filepath.Join(root, "stray.tmp")) || !exists(filepath.Join(root, ".other-upload", "a.parquet")) {
		t.Fatal("shadow mode removed a file")
	}
	if len(report.Removed) != 0 || len(report.Kept) != 0 {
		t.Fatalf("report = %+v, want empty in shadow mode", report)
	}
	if cfg.DeletesSource() {
		t.Fatal("DeletesSource() = true in shadow mode")
	}
	if !ownerConfig().DeletesSource() {
		t.Fatal("DeletesSource() = false in owner mode")
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"default owner", DefaultConfig(), false},
		{"shadow with destination", Config{Mode: ModeShadow, Destination: "shadow_table"}, false},
		{"shadow without destination", Config{Mode: ModeShadow}, true},
		{"shadow with blank destination", Config{Mode: ModeShadow, Destination: "  "}, true},
		{"owner with destination", Config{Mode: ModeOwner, Destination: "shadow_table"}, true},
		{"empty mode", Config{}, true},
		{"unknown mode", Config{Mode: "mirror"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCleanRejectsInvalidConfig(t *testing.T) {
	isolatePatterns(t)
	if _, err := (Config{Mode: ModeShadow}).Clean(t.TempDir(), protectSpool); err == nil {
		t.Fatal("Clean() error = nil for an invalid config, want error")
	}
}

func TestRecordPresetsRegisteredKindsAndAddsCounts(t *testing.T) {
	isolatePatterns(t)
	registerGenericPatterns()
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatalf("NewMemory() error = %v", err)
	}

	Record(metrics, Report{Removed: map[string]int{"tmp": 2}, Kept: map[string]int{"failed": 3}})

	checks := []struct {
		name string
		kind string
		want float64
	}{
		{telemetry.LeftoversRemoved, "tmp", 2},
		{telemetry.LeftoversRemoved, "job_id", 0},
		{telemetry.LeftoversRemoved, "staging", 0},
		{telemetry.LeftoversKept, "failed", 3},
	}
	for _, check := range checks {
		got, ok := metrics.Value(check.name, telemetry.Labels{"kind": check.kind})
		if !ok || got != check.want {
			t.Errorf("%s{kind=%s} = %v, %v; want %v, true", check.name, check.kind, got, ok, check.want)
		}
	}
	if _, ok := metrics.Value(telemetry.LeftoversKept, telemetry.Labels{"kind": "tmp"}); ok {
		t.Error("leftovers_kept{kind=tmp} exists, want only keep kinds under leftovers_kept")
	}
	if metrics.Dropped() != 0 {
		t.Errorf("Dropped() = %d, want 0", metrics.Dropped())
	}
}
