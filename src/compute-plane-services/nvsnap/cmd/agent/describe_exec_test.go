// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An exec failure names the mode and owner of the binary and of every
// directory above it, so an EACCES says which of them refused it.
func TestDescribeExecPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "criu")
	if err := os.WriteFile(bin, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := describeExecPath(bin)
	if !strings.HasPrefix(got, bin+" 644 ") || !strings.Contains(got, "; "+dir+" ") {
		t.Errorf("describeExecPath = %q", got)
	}
	if !strings.Contains(got, "; / ") {
		t.Errorf("must walk up to /: %q", got)
	}
	if got := describeExecPath(filepath.Join(dir, "missing")); !strings.Contains(got, "no such file") {
		t.Errorf("a missing binary must say so: %q", got)
	}
}
