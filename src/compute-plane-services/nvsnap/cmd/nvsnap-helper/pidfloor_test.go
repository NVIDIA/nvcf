// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPIDFloorReached(t *testing.T) {
	f := filepath.Join(t.TempDir(), "ns_last_pid")
	if err := os.WriteFile(f, []byte("37\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if pidFloorReached(f, 100000, 100*time.Millisecond, 10*time.Millisecond) {
		t.Fatal("a low next pid is not the floor")
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(f, []byte("99999\n"), 0o644)
	}()
	if !pidFloorReached(f, 100000, 2*time.Second, 10*time.Millisecond) {
		t.Fatal("the floor raised by the agent was not seen")
	}
	if pidFloorReached(filepath.Join(t.TempDir(), "missing"), 100000, 50*time.Millisecond, 10*time.Millisecond) {
		t.Fatal("an unreadable file reached the floor")
	}
}
