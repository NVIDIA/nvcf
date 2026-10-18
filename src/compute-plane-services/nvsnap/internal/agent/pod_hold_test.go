// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// fakePodCgroup makes a pod cgroup with the given container cgroups, each
// reporting itself frozen as soon as asked (cgroup.events).
func fakePodCgroup(t *testing.T, containers ...string) string {
	t.Helper()
	pod := filepath.Join(t.TempDir(), "kubepods", "podabc")
	for _, c := range containers {
		d := filepath.Join(pod, c)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, v := range map[string]string{"cgroup.freeze": "0", "cgroup.events": "populated 1\nfrozen 1\n"} {
			if err := os.WriteFile(filepath.Join(d, name), []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Not a container cgroup: no freezer file.
	if err := os.MkdirAll(filepath.Join(pod, "misc"), 0o755); err != nil {
		t.Fatal(err)
	}
	return pod
}

func frozenState(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.freeze"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestPodHoldDirs(t *testing.T) {
	pod := fakePodCgroup(t, "cri-pause", "cri-main", "cri-sidecar")
	target := filepath.Join(pod, "cri-main")
	keep, err := podHoldDirs(target, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(pod, "cri-pause"), filepath.Join(pod, "cri-sidecar")}; !slices.Equal(keep, want) {
		t.Errorf("a GPU rank's hold: %v, want every container but the one it saves: %v", keep, want)
	}
	all, err := podHoldDirs(target, false)
	if err != nil || len(all) != 3 {
		t.Errorf("a companion's hold: %v (%v), want all three containers", all, err)
	}
}

// A companion's hold freezes its whole pod, thaws the container to dump
// just before the dump, and thaws the rest when released.
func TestPodHold_Companion(t *testing.T) {
	a := &Agent{config: Config{CheckpointDir: t.TempDir()}}
	log := logrus.NewEntry(logrus.New())
	pod := fakePodCgroup(t, "cri-pause", "cri-etcd")
	target := filepath.Join(pod, "cri-etcd")
	key := holdKey("s1", "ns", "etcd-0")
	if err := a.holdCgroups(key, target, false, log); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"cri-pause", "cri-etcd"} {
		if got := frozenState(t, filepath.Join(pod, c)); got != "1" {
			t.Errorf("%s: freeze = %s, want 1", c, got)
		}
	}
	if _, err := os.Stat(a.holdRecordPath(key)); err != nil {
		t.Errorf("the hold is not recorded: %v", err)
	}
	if a.heldTargetDir(key) != target {
		t.Errorf("held target %q, want %q: a dump from outside seizes it frozen", a.heldTargetDir(key), target)
	}
	a.thawHoldTarget(key, log)
	if a.heldTargetDir(key) != "" {
		t.Error("a thawed target is no longer held frozen")
	}
	if frozenState(t, target) != "0" || frozenState(t, filepath.Join(pod, "cri-pause")) != "1" {
		t.Error("before its dump only the companion's container is thawed")
	}
	a.releaseHold(key, log)
	if frozenState(t, filepath.Join(pod, "cri-pause")) != "0" {
		t.Error("a released hold leaves the pod frozen")
	}
	if _, err := os.Stat(a.holdRecordPath(key)); !os.IsNotExist(err) {
		t.Errorf("a released hold is still recorded: %v", err)
	}
	a.releaseHold(key, log) // twice: a no-op
}

// A GPU rank's hold leaves the container it saves running.
func TestPodHold_RankKeepsItsContainer(t *testing.T) {
	a := &Agent{config: Config{CheckpointDir: t.TempDir()}}
	log := logrus.NewEntry(logrus.New())
	pod := fakePodCgroup(t, "cri-main", "cri-gang-health")
	key := holdKey("s1", "ns", "worker-0")
	if err := a.holdCgroups(key, filepath.Join(pod, "cri-main"), true, log); err != nil {
		t.Fatal(err)
	}
	if frozenState(t, filepath.Join(pod, "cri-main")) != "0" || frozenState(t, filepath.Join(pod, "cri-gang-health")) != "1" {
		t.Error("a rank's hold must freeze its sidecar and keep its engine running")
	}
	if a.heldTargetDir(key) != "" {
		t.Error("a rank's engine container is not held frozen")
	}
	a.thawHoldTarget(key, log) // nothing to thaw
	if err := a.holdCgroups(key, filepath.Join(pod, "cri-main"), true, log); err != nil {
		t.Errorf("a second hold of one key: %v", err)
	}
	a.releaseHold(key, log)
	if frozenState(t, filepath.Join(pod, "cri-gang-health")) != "0" {
		t.Error("released hold left the sidecar frozen")
	}
}

// An agent that restarts thaws what its earlier run held.
func TestRecoverPodHolds(t *testing.T) {
	a := &Agent{config: Config{CheckpointDir: t.TempDir()}}
	log := logrus.NewEntry(logrus.New())
	pod := fakePodCgroup(t, "cri-a", "cri-b")
	if err := a.holdCgroups(holdKey("s1", "ns", "p"), filepath.Join(pod, "cri-a"), false, log); err != nil {
		t.Fatal(err)
	}
	restarted := &Agent{config: a.config}
	restarted.recoverPodHolds(log)
	for _, c := range []string{"cri-a", "cri-b"} {
		if frozenState(t, filepath.Join(pod, c)) != "0" {
			t.Errorf("%s still frozen after the agent restarted", c)
		}
	}
	if ents, _ := os.ReadDir(a.holdRecordDir()); len(ents) != 0 {
		t.Errorf("hold records left: %d", len(ents))
	}
}

func TestProcessCgroup(t *testing.T) {
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "42"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "42", "cgroup"), []byte("0::/kubepods/podabc/cri-main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := processCgroup(proc, 42); err != nil || got != "/kubepods/podabc/cri-main" {
		t.Errorf("got %q %v", got, err)
	}
	if _, err := processCgroup(proc, 43); err == nil {
		t.Error("a missing process has no cgroup")
	}
}

// A rank's container keeps running for its GPU suspend, but its processes
// without GPU state (the engine core, the Dynamo runtime) stop at the
// freeze point, in a frozen child cgroup, until the dump starts.
func TestPodHold_RankStopsItsProcessesWithoutGPU(t *testing.T) {
	gpu := map[int]bool{12: true}
	orig := usesGPU
	usesGPU = func(pid int) bool { return gpu[pid] }
	t.Cleanup(func() { usesGPU = orig })
	a := &Agent{config: Config{CheckpointDir: t.TempDir()}}
	log := logrus.NewEntry(logrus.New())
	pod := fakePodCgroup(t, "cri-main", "cri-gang-health")
	main := filepath.Join(pod, "cri-main")
	if err := os.WriteFile(filepath.Join(main, "cgroup.procs"), []byte("11\n12\n13\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	key := holdKey("s1", "ns", "worker-0")
	if err := a.holdCgroups(key, main, true, log); err != nil {
		t.Fatal(err)
	}
	q := filepath.Join(main, quietCgroup)
	if frozenState(t, main) != "0" || frozenState(t, q) != "1" {
		t.Fatal("want the container running and its quiet cgroup frozen")
	}
	if got, _ := cgroupPIDs(q); !slices.Equal(got, []int{11, 13}) {
		t.Fatalf("stopped %v, want the processes without GPU state [11 13]", got)
	}
	a.releaseQuiet(key, log) // the dump starts
	if frozenState(t, q) != "0" {
		t.Error("the stopped processes were not thawed as the dump starts")
	}
	if got, _ := cgroupPIDs(main); !slices.Equal(got, []int{11, 12, 13, 11, 13}) {
		t.Errorf("container procs written %v, want 11 and 13 moved back", got)
	}
	a.releaseHold(key, log)
}

// An agent that restarts puts a rank's stopped processes back.
func TestRecoverPodHolds_Quiet(t *testing.T) {
	orig := usesGPU
	usesGPU = func(int) bool { return false }
	t.Cleanup(func() { usesGPU = orig })
	a := &Agent{config: Config{CheckpointDir: t.TempDir()}}
	log := logrus.NewEntry(logrus.New())
	pod := fakePodCgroup(t, "cri-main")
	main := filepath.Join(pod, "cri-main")
	if err := os.WriteFile(filepath.Join(main, "cgroup.procs"), []byte("21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.holdCgroups(holdKey("s1", "ns", "w"), main, true, log); err != nil {
		t.Fatal(err)
	}
	(&Agent{config: a.config}).recoverPodHolds(log)
	if frozenState(t, filepath.Join(main, quietCgroup)) != "0" {
		t.Error("the stopped processes are still frozen after the agent restarted")
	}
	if got, _ := cgroupPIDs(main); !slices.Equal(got, []int{21, 21}) {
		t.Errorf("container procs written %v, want 21 moved back", got)
	}
}
