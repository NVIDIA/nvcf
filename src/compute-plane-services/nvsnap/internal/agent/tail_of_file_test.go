// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A failed CRIU run is reported with its first errors, which name the
// cause, as well as the tail, which only says it gave up.
func TestTailOfFileReportsFirstErrors(t *testing.T) {
	log := []string{"(00.1) start", "(00.2) Error (criu/sk-inet.c:1): bind failed", "(00.3) more"}
	for i := 0; i < 20; i++ {
		log = append(log, "(00.4) Unseizing")
	}
	log = append(log, "(00.9) Error (criu/cr-dump.c:2040): Dumping FAILED.")
	p := filepath.Join(t.TempDir(), "dump.log")
	if err := os.WriteFile(p, []byte(strings.Join(log, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tailOfFile(p, 6)
	if !strings.HasPrefix(got, "first errors: (00.2) Error (criu/sk-inet.c:1): bind failed ... ") || !strings.HasSuffix(got, "Dumping FAILED.") {
		t.Errorf("tailOfFile = %q", got)
	}
	if got := tailOfFile(filepath.Join(t.TempDir(), "restore.log"), 6); got != "(no restore.log)" {
		t.Errorf("missing file: %q", got)
	}
}
