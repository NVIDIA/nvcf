// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package uploadertest

import (
	"os"
	"path/filepath"
	"testing"
)

const envInertDir = "UPLOADERTEST_INERT_DIR"

// TestCrashChildOutsideTestBinary hits a crash point the way a non-test
// binary would. A Go build tool is not available under Bazel, so the child is
// still a test binary and the seam stands in for testing.Testing.
func TestCrashChildOutsideTestBinary(t *testing.T) {
	if !CrashChild() {
		t.Skip("runs only as a crash child")
	}
	crashAt("armed", false)
	if err := os.WriteFile(filepath.Join(os.Getenv(envInertDir), "after"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCrashPointIsInertOutsideATestBinary(t *testing.T) {
	dir := t.TempDir()
	result := RunCrash(t, "TestCrashChildOutsideTestBinary", "armed", envInertDir+"="+dir)

	if result.Killed {
		t.Fatalf("a non-test binary was killed at an armed crash point\n%s", result.Output)
	}
	if _, err := os.Stat(filepath.Join(dir, "after")); err != nil {
		t.Errorf("work after the crash point did not run: %v", err)
	}
}
