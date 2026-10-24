// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Restored clocks through a GPU reload (docs/design/RESTORE-CLOCK-GPU-RELOAD.md).
//
// restore_clock.go keeps CRIU's restore out of the restored clocks. With
// gpushare the restore goes on after CRIU: the GPU processes stay held
// while their memory is loaded, the fabric remapped and their GPUs resumed,
// and their clocks ran all that time. A 2-node Kimi-K3 restore resumed its
// processes 97 s after CRIU finished; every one of them saw that time pass
// at once, and NCCL's RAS thread declared its peers dead.
//
// So with a GPU reload the budget covers the whole restore up to the GPU
// resume, and nothing runs before the clocks reach the dumped values:
//   - CRIU starts the clocks the budget behind and leaves every task
//     stopped (--leave-stopped --timens-no-wait), writing the tasks that ran
//     at the dump to timens-resume-pids;
//   - nvsnap-gpu-suspend resume --clock-at waits for the clocks before it
//     thaws the GPU processes;
//   - the agent then resumes the other tasks of that list.
// The budget is this checkpoint's last whole restore on the node, with a
// margin; before its first, an estimate from its CRIU images and its GPU
// memory. Too large a budget delays the resume; too small a one leaves the
// overrun counted.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// restoreSpanFile, in a checkpoint's directory, holds what its last
	// restore with a GPU reload took on this node, from CRIU's start to the
	// GPU resume, in milliseconds.
	restoreSpanFile = "restore-span-ms"
	// timensResumePIDsFile is CRIU's list of the tasks that ran at the
	// dump, which the agent resumes (--timens-no-wait).
	timensResumePIDsFile = "timens-resume-pids"
	// restoreGPULoadRate is a conservative rate for a node's GPU memory
	// reload: a Kimi-K3 rank's node loaded 4 x 200 GiB in 36 s, 22 GiB/s.
	restoreGPULoadRate = 16 << 30
	// restoreGPUSlack covers what a GPU resume does besides the load: the
	// driver restore, the fabric remap (45-60 s for 2,152 handles across 2
	// nodes) and the resume.
	restoreGPUSlack = 60 * time.Second
)

// restoreSpanBudget is the --timens-budget for a restore of the checkpoint
// in dir that reloads GPU memory: its last whole restore's work (without
// its wait for the clocks) with a tenth more, else an estimate. Restores of
// a checkpoint take about the same time (97-98 s for a 2-node Kimi-K3
// rank); a budget too large is waited out at every resume, a restore
// slower than its budget sees only its overrun.
func restoreSpanBudget(dir string) time.Duration {
	if b, err := os.ReadFile(filepath.Join(dir, restoreSpanFile)); err == nil {
		if ms, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && ms >= 0 {
			return min(time.Duration(ms)*time.Millisecond*11/10+restoreClockSlack, restoreClockMax)
		}
	}
	gpu := time.Duration(float64(gpuStoreBytes(dir)) / restoreGPULoadRate * float64(time.Second))
	return min(restoreClockBudget(dir)+gpu+restoreGPUSlack, restoreClockMax)
}

// restoreClockArgs is what CRIU is told about the restored clocks, and
// the budget: nothing without --timens-budget; CRIU's own restore as the
// budget, which CRIU waits out; or, through a GPU reload, the whole restore,
// with every task left stopped for the GPU resume.
func restoreClockArgs(dir string, budgetSupported, throughGPU bool) ([]string, time.Duration) {
	switch {
	case !budgetSupported:
		return nil, 0
	case throughGPU:
		b := restoreSpanBudget(dir)
		return []string{"--timens-budget", strconv.FormatInt(b.Milliseconds(), 10), "--leave-stopped", "--timens-no-wait"}, b
	default:
		b := restoreClockBudget(dir)
		return []string{"--timens-budget", strconv.FormatInt(b.Milliseconds(), 10)}, b
	}
}

// gpuStoreBytes is the size of the GPU memory a restore of the checkpoint
// in dir loads: its gpushare chunk store.
func gpuStoreBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(filepath.Join(dir, "gpushare"), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// recordRestoreSpan keeps what this restore of the checkpoint in dir took
// up to its GPU resume, without its wait for the clocks, for the budget of
// its next one: with the wait, each budget would grow from the last.
func recordRestoreSpan(dir string, d time.Duration) error {
	return os.WriteFile(filepath.Join(dir, restoreSpanFile), []byte(strconv.FormatInt(d.Milliseconds(), 10)+"\n"), 0o644) //nolint:gosec // dir is the agent's own checkpoint directory
}

// dumpedMonotonic reads, from the timens image of the checkpoint in dir,
// the CLOCK_MONOTONIC its tasks read when they were dumped, in nanoseconds.
func dumpedMonotonic(ctx context.Context, dir string) (int64, error) {
	imgs, err := filepath.Glob(filepath.Join(dir, "timens-*.img"))
	if err != nil || len(imgs) == 0 {
		return 0, fmt.Errorf("no timens image in %s", dir)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "crit", "decode", "-i", imgs[0]).Output() //nolint:gosec // the image path is the agent's own
	if err != nil {
		return 0, fmt.Errorf("crit decode %s: %w", imgs[0], err)
	}
	return parseTimensMonotonic(out)
}

// parseTimensMonotonic reads the monotonic clock out of crit's JSON of a
// timens image.
func parseTimensMonotonic(b []byte) (int64, error) {
	var doc struct {
		Entries []struct {
			Monotonic *struct {
				Sec  int64 `json:"tv_sec"`
				Nsec int64 `json:"tv_nsec"`
			} `json:"monotonic"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return 0, fmt.Errorf("parse timens: %w", err)
	}
	if len(doc.Entries) == 0 || doc.Entries[0].Monotonic == nil {
		return 0, fmt.Errorf("timens image has no monotonic clock")
	}
	m := doc.Entries[0].Monotonic
	if m.Sec < 0 || m.Nsec < 0 || m.Nsec >= int64(time.Second) {
		return 0, fmt.Errorf("timens image has an invalid monotonic clock %d.%09d", m.Sec, m.Nsec)
	}
	return m.Sec*int64(time.Second) + m.Nsec, nil
}

// readTimensResumePIDs reads CRIU's list of the tasks to resume, pids in
// the pid namespace CRIU restored them in.
func readTimensResumePIDs(dir string) ([]int, error) {
	b, err := os.ReadFile(filepath.Join(dir, timensResumePIDsFile))
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		n, err := strconv.Atoi(f)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%s: bad pid %q", timensResumePIDsFile, f)
		}
		pids = append(pids, n)
	}
	return pids, nil
}

// continueRestoredTasks resumes the tasks pids name: pids in the pid
// namespace of nsRef, the one CRIU restored them in, leaving out skip (the
// GPU processes, which their resume thawed). It returns how many it
// resumed, and fails when a task of the list is not there.
func continueRestoredTasks(procBase string, nsRef int, pids, skip []int) (int, error) {
	ns, err := os.Readlink(filepath.Join(procBase, strconv.Itoa(nsRef), "ns", "pid"))
	if err != nil {
		return 0, fmt.Errorf("pid namespace of %d: %w", nsRef, err)
	}
	want := map[int]bool{}
	for _, p := range pids {
		want[p] = true
	}
	for _, p := range skip {
		delete(want, p)
	}
	ents, err := os.ReadDir(procBase)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		host, aerr := strconv.Atoi(e.Name())
		if aerr != nil || len(want) == 0 {
			continue
		}
		if l, lerr := os.Readlink(filepath.Join(procBase, e.Name(), "ns", "pid")); lerr != nil || l != ns {
			continue
		}
		ids, nerr := nsPIDs(procBase, host)
		if nerr != nil || len(ids) == 0 || !want[ids[len(ids)-1]] {
			continue
		}
		if kerr := syscall.Kill(host, syscall.SIGCONT); kerr != nil && kerr != syscall.ESRCH {
			return n, fmt.Errorf("resume task %d (pid %d in its namespace): %w", host, ids[len(ids)-1], kerr)
		}
		delete(want, ids[len(ids)-1])
		n++
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for p := range want {
			missing = append(missing, strconv.Itoa(p))
		}
		return n, fmt.Errorf("restored tasks %s not found to resume", strings.Join(missing, ","))
	}
	return n, nil
}

var (
	bundledClockAtOnce sync.Once
	bundledClockAt     bool
)

// restoreClockThroughGPU reports whether a restore that reloads GPU memory
// keeps the reload out of the restored clocks: the bundled CRIU has
// --timens-no-wait and the bundled nvsnap-gpu-suspend has --clock-at.
// Without either, CRIU's own budget applies (restore_clock.go).
func restoreClockThroughGPU() bool {
	bundledClockAtOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		criuHelp, _ := exec.CommandContext(ctx, v2BinDirInContainer+"/criu", "--help").CombinedOutput()
		// The tool prints its usage, with its options, when given no action.
		var toolHelp bytes.Buffer
		cmd := exec.CommandContext(ctx, v2BinDirInContainer+"/"+gpushareToolName) //nolint:gosec // fixed bundled binary
		cmd.Stdout, cmd.Stderr = &toolHelp, &toolHelp
		_ = cmd.Run()
		bundledClockAt = clockThroughGPUSupported(string(criuHelp), toolHelp.String())
	})
	return bundledClockAt
}

func clockThroughGPUSupported(criuHelp, toolUsage string) bool {
	return criuSupportsTimensBudget(criuHelp) && strings.Contains(criuHelp, "--timens-no-wait") &&
		strings.Contains(toolUsage, "--clock-at")
}
