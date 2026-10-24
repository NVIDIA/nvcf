// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A restored process's clocks continue from the values its dump recorded,
// from the moment CRIU creates its time namespace: before CRIU has forked
// the tasks and restored their memory. Everything CRIU does after that
// counts as time the tasks ran, and a task waiting on a timeout when it was
// dumped finds it expired at once. A Kimi-K3 rank holds a 600 GiB shared
// memory file whose restore takes minutes; its engine, waiting 300 s for a
// step, died in the first second after the restore (2026-10-19).
//
// CRIU's --timens-budget starts the restored clocks that far behind the
// dumped values and resumes the tasks only when they have reached them:
// the restore (up to the budget) does not count, and the clocks never read
// earlier than before the dump. The budget is what this checkpoint's CRIU
// restore took on this node the last time, with a margin; before its first
// restore, an estimate from the size of its images. Too large a budget only
// delays the resume; too small a one leaves the overrun counted.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// restoreClockRate is a conservative rate at which CRIU restores image
	// data: a Kimi-K3 rank restored 620 GiB in 135 s, 4.6 GiB/s.
	restoreClockRate = 3 << 30
	// restoreClockSlack covers CRIU's work that does not scale with the
	// images (forking, namespaces, files).
	restoreClockSlack = 5 * time.Second
	// restoreClockMax bounds the budget: a restore never waits longer.
	restoreClockMax = 15 * time.Minute
	// restoreClockFile, in a checkpoint's directory, holds what its last
	// CRIU restore on this node took, in milliseconds.
	restoreClockFile = "criu-restore-ms"
)

// restoreClockBudget is the --timens-budget for a restore of the checkpoint
// in dir: its last measured CRIU restore with a quarter more, else an
// estimate from its images.
func restoreClockBudget(dir string) time.Duration {
	if b, err := os.ReadFile(filepath.Join(dir, restoreClockFile)); err == nil {
		if ms, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && ms >= 0 {
			return min(time.Duration(ms)*time.Millisecond*5/4+restoreClockSlack, restoreClockMax)
		}
	}
	est := time.Duration(float64(criuImageBytes(dir)) / restoreClockRate * float64(time.Second))
	return min(est+restoreClockSlack, restoreClockMax)
}

// criuImageBytes is the size of the CRIU images in dir: the files at its
// top level, not the directories beside them (the GPU memory, which
// gpushare reloads after CRIU, the rootfs diff, the mounts).
func criuImageBytes(dir string) int64 {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range ents {
		if !e.Type().IsRegular() || !strings.Contains(e.Name(), ".img") && !strings.Contains(e.Name(), ".tar") {
			continue
		}
		if fi, err := e.Info(); err == nil {
			n += fi.Size()
		}
	}
	return n
}

var (
	// The restore log's lines start with the seconds since CRIU started.
	restoreLogTime = regexp.MustCompile(`^\((\d+\.\d+)\)`)
	// The restored clocks start ...
	restoreClockStart = regexp.MustCompile(`timens: (clocks start|monotonic )`)
	// ... and CRIU's work ends where it waits for them, or resumes.
	restoreClockEnd = regexp.MustCompile(`timens: (resuming the tasks in|the restore took)|Restore finished successfully`)
)

// restoreClockSpent reads from a CRIU restore log how long the restore ran
// with the restored clocks running, before any wait for them.
func restoreClockSpent(logPath string) (time.Duration, bool) {
	f, err := os.Open(logPath)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	start := -1.0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		m := restoreLogTime.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		if start < 0 && restoreClockStart.MatchString(line) {
			start = t
			continue
		}
		if start >= 0 && restoreClockEnd.MatchString(line) {
			return time.Duration((t - start) * float64(time.Second)), true
		}
	}
	return 0, false
}

// recordRestoreClock keeps what this restore of the checkpoint in dir took,
// for the budget of its next one.
func recordRestoreClock(dir string) (time.Duration, bool) {
	d, ok := restoreClockSpent(filepath.Join(dir, "restore.log"))
	if !ok {
		return 0, false
	}
	if err := os.WriteFile(filepath.Join(dir, restoreClockFile), []byte(strconv.FormatInt(d.Milliseconds(), 10)+"\n"), 0o644); err != nil {
		return d, false
	}
	return d, true
}


func criuSupportsTimensBudget(help string) bool {
	return strings.Contains(help, "--timens-budget")
}
