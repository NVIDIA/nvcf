// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// The stacks are due only for a running restored pod that is still not
// Ready restoredStacksAfter after its restore.
func TestStacksDue(t *testing.T) {
	now := time.Date(2026, 10, 22, 22, 40, 0, 0, time.UTC)
	pod := func(restoredAgo time.Duration, ready bool, phase corev1.PodPhase) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}, Status: corev1.PodStatus{Phase: phase}}
		if restoredAgo >= 0 {
			p.Annotations[webhook.CRIURestoredAnnotation] = now.Add(-restoredAgo).Format(time.RFC3339)
		}
		st := corev1.ConditionFalse
		if ready {
			st = corev1.ConditionTrue
		}
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}
		return p
	}
	deleting := pod(5*time.Minute, false, corev1.PodRunning)
	deleting.DeletionTimestamp = &metav1.Time{Time: now}
	for name, tc := range map[string]struct {
		pod  *corev1.Pod
		want bool
	}{
		"hung past the wait":    {pod(2*time.Minute, false, corev1.PodRunning), true},
		"still within the wait": {pod(30*time.Second, false, corev1.PodRunning), false},
		"ready":                 {pod(5*time.Minute, true, corev1.PodRunning), false},
		"never restored":        {pod(-1, false, corev1.PodRunning), false},
		"not running":           {pod(5*time.Minute, false, corev1.PodPending), false},
		"being deleted":         {deleting, false},
	} {
		if got := stacksDue(tc.pod, now); got != tc.want {
			t.Errorf("%s: due=%v, want %v", name, got, tc.want)
		}
	}
}

// fakeTasks gives pid a cmdline and one thread with a state, a wait
// channel and a kernel stack.
func fakeTasks(t *testing.T, root string, pid int, cmd string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	td := filepath.Join(dir, "task", strconv.Itoa(pid))
	if err := os.MkdirAll(td, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		filepath.Join(dir, "cmdline"): strings.ReplaceAll(cmd, " ", "\x00"),
		filepath.Join(td, "comm"):     "worker\n",
		filepath.Join(td, "status"):   "Name:\tworker\nState:\tS (sleeping)\n",
		filepath.Join(td, "wchan"):    "futex_wait_queue",
		filepath.Join(td, "stack"):    "[<0>] futex_wait_queue+0x60/0x90\n[<0>] do_futex+0x10/0x20\n",
	} {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Each live process of the tree gets its threads' states, wait channels and
// kernel stacks, then its Python stack or the reason py-spy gave none; a
// pid that exited meanwhile is skipped.
func TestWriteRestoredStacks(t *testing.T) {
	root := t.TempDir()
	fakeTasks(t, root, 100, "python3 -m engine")
	fakeTasks(t, root, 101, "VLLM::Worker_TP0")
	var buf bytes.Buffer
	n := writeRestoredStacks(&buf, root, []int{100, 101, 999}, func(pid int) ([]byte, error) {
		if pid == 101 {
			return nil, errors.New("not a python process")
		}
		return []byte("Thread 7 (idle): \"MainThread\"\n    dequeue (shm_broadcast.py:889)"), nil
	})
	out := buf.String()
	if n != 2 {
		t.Errorf("covered %d processes, want 2 (999 exited)", n)
	}
	for _, want := range []string{
		"=== pid 100: python3 -m engine",
		"tid 100 worker state=S (sleeping) wchan=futex_wait_queue",
		"futex_wait_queue+0x60/0x90",
		"dequeue (shm_broadcast.py:889)",
		"=== pid 101: VLLM::Worker_TP0",
		"py-spy: not a python process",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pid 999") {
		t.Error("a pid that exited was dumped")
	}
}

// A dump covers at most restoredStacksMax processes.
func TestWriteRestoredStacksBounded(t *testing.T) {
	root := t.TempDir()
	pids := make([]int, 0, restoredStacksMax+5)
	for i := 0; i < restoredStacksMax+5; i++ {
		fakeTasks(t, root, 1000+i, "p")
		pids = append(pids, 1000+i)
	}
	calls := 0
	n := writeRestoredStacks(&bytes.Buffer{}, root, pids, func(int) ([]byte, error) { calls++; return nil, nil })
	if n != restoredStacksMax || calls != restoredStacksMax {
		t.Errorf("covered %d processes with %d py-spy runs, want %d", n, calls, restoredStacksMax)
	}
}

// pidNS links pid's ns/pid to namespace id in a fake proc tree.
func pidNS(t *testing.T, root string, pid int, id string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid), "ns")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pid:["+id+"]", filepath.Join(dir, "pid")); err != nil {
		t.Fatal(err)
	}
}

// The restored processes are the placeholder's descendants and every
// process in its pid namespace: in a pod sharing one pid namespace, the
// restored engine hangs off the pod's pause process, not the placeholder
// (seen with Kimi-K3 on Dynamo, 2026-10-23).
func TestRestoredProcesses(t *testing.T) {
	root := fakeProcTree(t, map[int][2]string{
		50:  {"1", "pause"},
		100: {"40", "nvsnap-idle"},      // the placeholder, the container's pid 1
		101: {"100", "sleep"},           // its child
		102: {"100", "python3"},         // a descendant in a nested pid namespace
		200: {"50", "python3"},          // the restored engine, under pause
		201: {"200", "VLLM::EngineCor"}, // and its child
		300: {"1", "python3"},           // another pod's
	})
	for pid, ns := range map[int]string{50: "A", 100: "A", 101: "A", 102: "C", 200: "A", 201: "A", 300: "B"} {
		pidNS(t, root, pid, ns)
	}
	got := map[int]bool{}
	for _, p := range restoredProcesses(root, 100) {
		got[p] = true
	}
	for _, want := range []int{100, 101, 102, 200, 201} {
		if !got[want] {
			t.Errorf("pid %d missing from %v", want, got)
		}
	}
	if got[300] {
		t.Error("another pod's process was included")
	}
	if !engineRuns(root, []int{100, 101, 200}) {
		t.Error("the engine under pause was not seen running")
	}
	if engineRuns(root, []int{100, 101}) {
		t.Error("the placeholder alone was taken for a running engine")
	}
}
