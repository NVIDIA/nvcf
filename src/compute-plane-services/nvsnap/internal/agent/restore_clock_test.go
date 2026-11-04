// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sizedFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil { // sparse: the size is what counts
		t.Fatal(err)
	}
	_ = f.Close()
}

// Before its first restore, a checkpoint's budget comes from its CRIU
// images; the GPU memory beside them is gpushare's, reloaded after CRIU.
func TestRestoreClockBudget_FromImages(t *testing.T) {
	dir := t.TempDir()
	sizedFile(t, filepath.Join(dir, "pages-1.img"), 4<<30)
	sizedFile(t, filepath.Join(dir, "ghost-file-1.img"), 2<<30)
	sizedFile(t, filepath.Join(dir, "tmpfs-dev-12.tar.gz.img"), 3<<30)
	sizedFile(t, filepath.Join(dir, "gpushare", "chunk-0"), 900<<30)
	sizedFile(t, filepath.Join(dir, "metadata.json"), 1<<30)
	if got, want := restoreClockBudget(dir), 3*time.Second+restoreClockSlack; got != want {
		t.Errorf("budget = %s, want %s (9 GiB of images at 3 GiB/s, plus slack)", got, want)
	}
}

// After a restore, the next one's budget is what it took, with a margin.
func TestRestoreClockBudget_FromLastRestore(t *testing.T) {
	dir := t.TempDir()
	sizedFile(t, filepath.Join(dir, "pages-1.img"), 600<<30)
	if err := os.WriteFile(filepath.Join(dir, restoreClockFile), []byte("135000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := restoreClockBudget(dir), 135*time.Second*5/4+restoreClockSlack; got != want {
		t.Errorf("budget = %s, want %s", got, want)
	}
}

func TestRestoreClockBudget_Bounded(t *testing.T) {
	dir := t.TempDir()
	sizedFile(t, filepath.Join(dir, "pages-1.img"), 8<<40)
	if got := restoreClockBudget(dir); got != restoreClockMax {
		t.Errorf("budget = %s, want the bound %s", got, restoreClockMax)
	}
}

// What the restore took is read from CRIU's log: from the moment the
// restored clocks start to the moment CRIU waits for them or resumes.
func TestRestoreClockSpent(t *testing.T) {
	cases := []struct {
		name, log string
		want      time.Duration
	}{
		{"budgeted", "(00.050000) Version: 4.2.1\n" +
			"(00.100000)      1: timens: clocks start 173750 ms behind the dumped values\n" +
			"(00.100100)      1: timens: monotonic 856528 129531436\n" +
			"(135.100000) timens: resuming the tasks in 38600 ms, when their clocks reach the dumped values\n" +
			"(173.700000) Restore finished successfully. Tasks resumed.\n", 135 * time.Second},
		{"overrun", "(00.100000)      1: timens: clocks start 5000 ms behind the dumped values\n" +
			"(07.600000) timens: the restore took 2500 ms longer than its budget; the tasks see it\n", 7500 * time.Millisecond},
		// A CRIU without the budget (the Kimi-K3 stock restore's log).
		{"unbudgeted", "(00.112586)      1: timens: monotonic 856528 129531436\n" +
			"(00.112607)      1: timens: boottime 856528 129509772\n" +
			"(03.260923) Run late stage hook from criu master for external devices\n" +
			"(03.264355) Restore finished successfully. Tasks resumed.\n", 3151769 * time.Microsecond},
	}
	for _, c := range cases {
		p := filepath.Join(t.TempDir(), "restore.log")
		if err := os.WriteFile(p, []byte(c.log), 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := restoreClockSpent(p)
		if !ok || (got-c.want).Abs() > time.Millisecond {
			t.Errorf("%s: spent = %s, %v; want %s", c.name, got, ok, c.want)
		}
	}
	p := filepath.Join(t.TempDir(), "restore.log")
	_ = os.WriteFile(p, []byte("(00.1) Restoring FAILED.\n"), 0o644)
	if _, ok := restoreClockSpent(p); ok {
		t.Error("a log without the clocks: measured")
	}
}

func TestRecordRestoreClock(t *testing.T) {
	dir := t.TempDir()
	log := "(00.500000)      1: timens: clocks start 9000 ms behind the dumped values\n(04.500000) timens: resuming the tasks in 5000 ms\n"
	if err := os.WriteFile(filepath.Join(dir, "restore.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, ok := recordRestoreClock(dir); !ok || d != 4*time.Second {
		t.Fatalf("record = %s, %v", d, ok)
	}
	if got, want := restoreClockBudget(dir), 5*time.Second+restoreClockSlack; got != want {
		t.Errorf("next budget = %s, want %s", got, want)
	}
}

func TestCRIUSupportsTimensBudget(t *testing.T) {
	if !criuSupportsTimensBudget("  --timens-budget MS    restore: start the restored clocks") {
		t.Error("patched criu: not detected")
	}
	if criuSupportsTimensBudget("  --timeout NUM         a timeout") {
		t.Error("stock criu: detected")
	}
}
