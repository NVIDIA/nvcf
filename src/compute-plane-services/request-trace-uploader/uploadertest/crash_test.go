// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package uploadertest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

const envCrashDir = "UPLOADERTEST_CRASH_DIR"

// TestCrashChildFixture is the child side of the crash tests. It does work on
// both sides of one crash point so the parent can see where it stopped.
func TestCrashChildFixture(t *testing.T) {
	if !uploadertest.CrashChild() {
		t.Skip("runs only as a crash child")
	}
	dir := os.Getenv(envCrashDir)
	if err := os.WriteFile(filepath.Join(dir, "before"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	uploadertest.CrashPoint("after-before")
	if err := os.WriteFile(filepath.Join(dir, "after"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunCrashStopsChildAtNamedPoint(t *testing.T) {
	cases := []struct {
		name       string
		point      string
		wantKilled bool
		wantAfter  bool
	}{
		{name: "point reached", point: "after-before", wantKilled: true, wantAfter: false},
		{name: "point never reached", point: "not-a-point", wantKilled: false, wantAfter: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			result := uploadertest.RunCrash(t, "TestCrashChildFixture", tc.point, envCrashDir+"="+dir)

			if result.Killed != tc.wantKilled {
				t.Fatalf("killed = %v, want %v\n%s", result.Killed, tc.wantKilled, result.Output)
			}
			if tc.wantKilled && result.Point != tc.point {
				t.Errorf("stopped at %q, want %q", result.Point, tc.point)
			}
			if _, err := os.Stat(filepath.Join(dir, "before")); err != nil {
				t.Errorf("work before the point is missing: %v", err)
			}
			_, err := os.Stat(filepath.Join(dir, "after"))
			if gotAfter := err == nil; gotAfter != tc.wantAfter {
				t.Errorf("work after the point present = %v, want %v", gotAfter, tc.wantAfter)
			}
		})
	}
}

// fatalRecorder stands in for a test so RunCrash's own failures can be
// observed instead of failing this test.
type fatalRecorder struct {
	testing.TB
	message string
}

func (f *fatalRecorder) Helper() {}

func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func TestRunCrashFailsWhenTheChildRunsNoTest(t *testing.T) {
	recorder := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		uploadertest.RunCrash(recorder, "TestNoSuchCrashChild", "after-before")
	}()
	<-done

	if !strings.Contains(recorder.message, "TestNoSuchCrashChild") {
		t.Errorf("failure = %q, want it to name the missing test", recorder.message)
	}
}
