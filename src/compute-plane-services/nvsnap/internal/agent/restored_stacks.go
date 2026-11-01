// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A restored engine that hangs (its processes run but never answer) leaves
// nothing to diagnose once the restored-pod watch ends it: its processes
// exit and its pod is deleted. So when a restored pod is still not Ready a
// while after its restore, and its engine still runs, the agent saves the
// restored processes' stacks once, to a file on the node: each process's
// and thread's state and kernel stack, and its Python stack from py-spy.
// py-spy runs with --nonblocking, which reads the process's memory without
// stopping it, so the dump never changes what it records.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

const (
	// restoredStacksAfter is how long after its restore a pod may stay not
	// Ready before its processes' stacks are saved: longer than a restored
	// engine takes to answer its probes (seconds), shorter than an engine
	// takes to give up on a peer that never answers (minutes).
	restoredStacksAfter = 90 * time.Second
	// restoredStacksMax bounds the processes one dump covers.
	restoredStacksMax = 128
	// diagnosticsDir, under the checkpoint root, holds the dumps. The
	// root's walkers skip dot-directories.
	diagnosticsDir = ".diagnostics"
)

// pySpyPath is py-spy in the agent image (Dockerfile.app); a variable for
// tests.
var pySpyPath = "/usr/local/bin/py-spy"

// considerRestoredStacks saves the stacks of a restored pod that is not
// Ready restoredStacksAfter after its restore, once per pod.
func (c *criuAutoRestorer) considerRestoredStacks(ctx context.Context, pod *corev1.Pod) {
	if !stacksDue(pod, time.Now()) {
		return
	}
	if _, done := c.attempted.LoadOrStore("stacks/"+string(pod.UID), struct{}{}); done {
		return
	}
	go func() {
		log := c.a.log.WithField("pod", pod.Namespace+"/"+pod.Name)
		ref, ok, err := c.restoredBy(ctx, pod)
		if err != nil || !ok {
			return // not a restore of this agent's records
		}
		info, err := c.a.runtime.FindContainerByPod(ctx, pod.Namespace, pod.Name, ref.container)
		if err != nil || info == nil || info.PID == 0 {
			log.WithError(err).Warn("CRIU restore: restored pod not Ready after the restore; cannot find its container, so its stacks are not saved")
			return
		}
		procBase := "/proc"
		if _, serr := os.Stat("/host/proc"); serr == nil {
			procBase = "/host/proc"
		}
		pids := restoredProcesses(procBase, int(info.PID))
		if !engineRuns(procBase, pids) {
			log.Warn("CRIU restore: restored pod not Ready after the restore; its engine is already gone, so no stacks are saved")
			return
		}
		dir := filepath.Join(c.a.config.CheckpointDir, diagnosticsDir)
		if err = os.MkdirAll(dir, 0o700); err != nil {
			log.WithError(err).Warn("CRIU restore: cannot save the stacks of a restored pod that is not Ready")
			return
		}
		path := filepath.Join(dir, fmt.Sprintf("%s_%s_%s.txt", pod.Namespace, pod.Name, time.Now().UTC().Format("20060102-150405")))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // agent-owned directory; names from the pod's namespace and name
		if err != nil {
			log.WithError(err).Warn("CRIU restore: cannot save the stacks of a restored pod that is not Ready")
			return
		}
		n := writeRestoredStacks(f, procBase, pids, func(pid int) ([]byte, error) { return pySpyDump(ctx, pid) })
		if err := f.Close(); err != nil {
			log.WithError(err).Warn("CRIU restore: cannot save the stacks of a restored pod that is not Ready")
			return
		}
		log.WithFields(map[string]interface{}{"file": path, "processes": n, "restoredAt": restoredAt(pod).UTC().Format(time.RFC3339)}).
			Warn("CRIU restore: restored pod not Ready after the restore; saved its processes' stacks")
	}()
}

// restoredProcesses returns the processes of the restored container whose
// pid 1 is hostPID: its descendants, and every process in its pid
// namespace. A restored tree need not hang off the placeholder: in a pod
// whose containers share one pid namespace it is reparented to the pod's
// pause process.
func restoredProcesses(procBase string, hostPID int) []int {
	seen := map[int]bool{}
	var out []int
	add := func(p int) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range processTree(procBase, hostPID) {
		add(p)
	}
	ns, err := os.Readlink(filepath.Join(procBase, strconv.Itoa(hostPID), "ns", "pid"))
	if err != nil {
		return out
	}
	ents, _ := os.ReadDir(procBase)
	for _, e := range ents {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if pns, err := os.Readlink(filepath.Join(procBase, e.Name(), "ns", "pid")); err == nil && pns == ns {
			add(p)
		}
	}
	return out
}

// engineRuns reports whether pids hold a process other than the
// placeholder's own (placeholderComms).
func engineRuns(procBase string, pids []int) bool {
	for _, p := range pids {
		comm, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(p), "comm")) //nolint:gosec // a pid under the proc root
		if err == nil && !placeholderComms[strings.TrimSpace(string(comm))] {
			return true
		}
	}
	return false
}

// stacksDue reports whether pod is a restored pod, running and not Ready
// restoredStacksAfter after its restore.
func stacksDue(pod *corev1.Pod, now time.Time) bool {
	if pod.Annotations[webhook.CRIURestoredAnnotation] == "" || pod.DeletionTimestamp != nil ||
		pod.Status.Phase != corev1.PodRunning || rootfsonly.IsPodReady(pod) {
		return false
	}
	at := restoredAt(pod)
	return !at.IsZero() && now.Sub(at.Time) >= restoredStacksAfter
}

// writeRestoredStacks writes, for each of pids (at most restoredStacksMax),
// its command, its threads' states, wait channels and kernel stacks, and
// what pySpy returns for it. It returns how many processes it covered.
func writeRestoredStacks(w io.Writer, procBase string, pids []int, pySpy func(pid int) ([]byte, error)) int {
	if len(pids) > restoredStacksMax {
		pids = pids[:restoredStacksMax]
	}
	n := 0
	for _, pid := range pids {
		dir := filepath.Join(procBase, strconv.Itoa(pid))
		cmd, err := os.ReadFile(filepath.Join(dir, "cmdline")) //nolint:gosec // a pid of the restored tree under the proc root
		if err != nil {
			continue // exited meanwhile
		}
		n++
		_, _ = fmt.Fprintf(w, "=== pid %d: %s\n", pid, strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " ")))
		tasks, _ := os.ReadDir(filepath.Join(dir, "task"))
		for _, t := range tasks {
			td := filepath.Join(dir, "task", t.Name())
			_, _ = fmt.Fprintf(w, "  tid %s %s state=%s wchan=%s\n", t.Name(), readTrim(filepath.Join(td, "comm")),
				procState(filepath.Join(td, "status")), readTrim(filepath.Join(td, "wchan")))
			if ks := readTrim(filepath.Join(td, "stack")); ks != "" {
				for _, l := range strings.Split(ks, "\n") {
					_, _ = fmt.Fprintf(w, "    %s\n", l)
				}
			}
		}
		out, err := pySpy(pid)
		if err != nil {
			_, _ = fmt.Fprintf(w, "  py-spy: %v\n", err)
		}
		if len(out) > 0 {
			_, _ = fmt.Fprintf(w, "  py-spy:\n%s\n", out)
		}
	}
	return n
}

func pySpyDump(ctx context.Context, pid int) ([]byte, error) {
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(dctx, pySpyPath, "dump", "--pid", strconv.Itoa(pid), "--nonblocking").CombinedOutput() //nolint:gosec // fixed binary; the pid comes from the proc tree
}

func readTrim(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // proc files of the restored tree
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// procState returns the State field of a proc status file.
func procState(path string) string {
	for _, l := range strings.Split(readTrim(path), "\n") {
		if v, ok := strings.CutPrefix(l, "State:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
