// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// The restored tree's root: the nested namespace's init for a whole-
// namespace capture, the task at its dumped pid in the placeholder's own
// namespace for a subtree.
func TestRestoredRootHostPID(t *testing.T) {
	base := fakeProc(t, map[int]procEntry{
		5000: {ns: "pid:[111]", nspid: "1"},      // the pod's pause
		5001: {ns: "pid:[111]", nspid: "100003"}, // the placeholder
		5002: {ns: "pid:[111]", nspid: "64"},     // the restored root
		5003: {ns: "pid:[222]", nspid: "64"},     // another pod's pid 64
	})
	got, err := restoredRootHostPID(base, 5001, pidNSCapture{Subtree: true, RootNSPID: 64})
	if err != nil || got != 5002 {
		t.Fatalf("subtree root = %d, %v; want 5002", got, err)
	}
	if _, err := restoredRootHostPID(base, 5001, pidNSCapture{Subtree: true, RootNSPID: 72}); err == nil {
		t.Fatal("a root that was not restored was found")
	}
}

// The marker says which kind of dump a checkpoint holds; one written before
// subtrees reads as a whole namespace.
func TestPIDNSMarkerSubtree(t *testing.T) {
	dir := t.TempDir()
	if err := writePIDNSMarker(dir, pidNSCapture{Mounts: []string{"/m"}, Subtree: true, RootNSPID: 64}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := readPIDNSMarker(dir)
	if err != nil || !ok || !c.Subtree || c.RootNSPID != 64 {
		t.Fatalf("marker %+v %v %v", c, ok, err)
	}
	if err := os.WriteFile(filepath.Join(dir, pidNSMarkerFile), []byte(`{"mounts":["/m"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := readPIDNSMarker(dir); c.Subtree || c.RootNSPID != 0 {
		t.Fatalf("an older marker read as a subtree: %+v", c)
	}
	if rootNSPIDOf(false, 1) != 0 || rootNSPIDOf(true, 64) != 64 {
		t.Fatal("rootNSPIDOf")
	}
}

// A subtree dump targets the container's pid in the pod's namespace and
// reaches its root through the node's /proc.
func TestPIDNSDumpArgsSubtree(t *testing.T) {
	args := strings.Join(pidNSDumpArgs(64, "/host/proc/4242/root", "/ck/x", "/criu-bundle/plugins", 4026538391,
		nil, nil, nil, dumpLeaveRunning, true, false, defaultGhostLimit), " ")
	if !strings.Contains(args, "-t 64 ") || !strings.Contains(args, "--root /host/proc/4242/root ") {
		t.Fatalf("argv %s", args)
	}
}

// In a pod that shares one pid namespace, the namespace also holds the
// pause process and the other containers, each the child of its own shim.
// Only this container's exec sessions are ended: never the pause process
// or a sidecar.
func TestExecSessions_SharedPIDNamespace(t *testing.T) {
	const ns = 4026532001
	main := "0::/kubepods/pod1/main\n"
	base := sessionProc(t, map[int]sessionProcEntry{
		100: {ns: 1, ppid: 0},    // the shims, outside the namespace
		200: {ns: ns, ppid: 100}, // pause
		300: {ns: ns, ppid: 100}, // a sidecar
		400: {ns: ns, ppid: 100}, // the container's own process
		401: {ns: ns, ppid: 400}, // its child
		500: {ns: ns, ppid: 100}, // an exec session into the container
		600: {ns: ns, ppid: 100}, // an exec session into the sidecar
	})
	for pid, nspid := range map[int]int{200: 1, 300: 7, 400: 64, 401: 65, 500: 90, 600: 91} {
		d := filepath.Join(base, strconv.Itoa(pid))
		status := fmt.Sprintf("Name:\tx\nState:\tS (sleeping)\nPPid:\t%d\nNSpid:\t%d\t%d\n", map[int]int{200: 100, 300: 100, 400: 100, 401: 400, 500: 100, 600: 100}[pid], pid, nspid)
		if err := os.WriteFile(filepath.Join(d, "status"), []byte(status), 0o644); err != nil {
			t.Fatal(err)
		}
		cg := main
		switch pid {
		case 200:
			cg = "0::/kubepods/pod1/pause\n"
		case 300, 600:
			cg = "0::/kubepods/pod1/sidecar\n"
		}
		if err := os.WriteFile(filepath.Join(d, "cgroup"), []byte(cg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := execSessions(base, 400)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{500}) {
		t.Fatalf("exec sessions %v, want [500]: only the container's own", got)
	}
}

func TestAwaitPlaceholderPIDReservation(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	log.Logger.SetOutput(io.Discard)

	t.Run("returns as soon as the reservation is visible", func(t *testing.T) {
		base := fakeProc(t, map[int]procEntry{
			5000: {ns: "pid:[111]", nspid: "1"},
			5001: {ns: "pid:[111]", nspid: "100002"},
		})
		got, err := awaitPlaceholderPIDReservationFor(base, 5000, 5*time.Second, nil, log)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 100002 {
			t.Errorf("got %d, want 100002", got)
		}
	})

	t.Run("errors when the reservation never lands", func(t *testing.T) {
		base := fakeProc(t, map[int]procEntry{
			5000: {ns: "pid:[111]", nspid: "1"},
			5001: {ns: "pid:[111]", nspid: "363"},
		})
		start := time.Now()
		got, err := awaitPlaceholderPIDReservationFor(base, 5000, 1200*time.Millisecond, nil, log)
		if err == nil {
			t.Fatalf("expected an error, got max=%d", got)
		}
		// The highest pid seen belongs in the message: it is what tells an
		// operator the bump did not run, rather than that it ran and was low.
		if !strings.Contains(err.Error(), "363") {
			t.Errorf("error should report the highest pid seen, got: %v", err)
		}
		if elapsed := time.Since(start); elapsed < 1200*time.Millisecond {
			t.Errorf("returned after %s, should have waited the full timeout", elapsed)
		}
	})

	t.Run("does not fail the restore when the namespace cannot be read", func(t *testing.T) {
		// A procfs read error is ambiguous, not proof of a missing reservation.
		_, err := awaitPlaceholderPIDReservationFor(t.TempDir(), 5000, 600*time.Millisecond, nil, log)
		if err == nil {
			t.Fatal("expected an error describing the unreadable namespace")
		}
		if !strings.Contains(err.Error(), "could not read") {
			t.Errorf("want a read-failure message distinct from a missing reservation, got: %v", err)
		}
	})
}

// fakeThreads adds task entries (thread id -> NSpid) under a fake process.
func fakeThreads(t *testing.T, base string, pid int, threads map[int]string) {
	t.Helper()
	for tid, nspid := range threads {
		dir := filepath.Join(base, strconv.Itoa(pid), "task", strconv.Itoa(tid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\tx\nNSpid:\t"+nspid+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A task left below the floor (a child started before the bump, or a thread
// of a multi-threaded init) holds a pid CRIU may recreate: the reservation is
// not complete until it is gone, even with a task already above the floor.
func TestAwaitPlaceholderPIDReservation_LowTaskBlocks(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	log.Logger.SetOutput(io.Discard)

	base := fakeProc(t, map[int]procEntry{
		5000: {ns: "pid:[111]", nspid: "1"},
		5001: {ns: "pid:[111]", nspid: "100002"},
	})
	fakeThreads(t, base, 5000, map[int]string{5000: "1", 5003: "9"})
	fakeThreads(t, base, 5001, map[int]string{5001: "100002"})
	if highest, low, err := placeholderPIDs(base, 5000); err != nil || highest != 100002 || low != 9 {
		t.Fatalf("placeholderPIDs = %d, %d, %v; want 100002, 9 (the init's thread)", highest, low, err)
	}
	_, err := awaitPlaceholderPIDReservationFor(base, 5000, 600*time.Millisecond, nil, log)
	if err == nil || !strings.Contains(err.Error(), "still holds pid 9") {
		t.Fatalf("a low thread must block the restore with its pid named, got %v", err)
	}

	// Single-threaded init, nothing else below the floor: ready.
	if err := os.RemoveAll(filepath.Join(base, "5000", "task", "5003")); err != nil {
		t.Fatal(err)
	}
	if got, err := awaitPlaceholderPIDReservationFor(base, 5000, 600*time.Millisecond, nil, log); err != nil || got != 100002 {
		t.Fatalf("got %d, %v; want 100002", got, err)
	}
}

// In a pod whose containers share one pid namespace, a sidecar started
// before the placeholder holds a low pid. Only a pid the checkpoint
// restores blocks the restore.
func TestAwaitPlaceholderPIDReservation_ExactClash(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	log.Logger.SetOutput(io.Discard)
	base := fakeProc(t, map[int]procEntry{
		5000: {ns: "pid:[111]", nspid: "1"},
		5001: {ns: "pid:[111]", nspid: "37"}, // a native sidecar
		5002: {ns: "pid:[111]", nspid: "100002"},
	})
	if got, err := awaitPlaceholderPIDReservationFor(base, 5000, 2*time.Second, map[int]bool{45: true, 591: true}, log); err != nil || got != 100002 {
		t.Fatalf("a sidecar on a pid the dump does not use blocked the restore: %d %v", got, err)
	}
	_, err := awaitPlaceholderPIDReservationFor(base, 5000, 600*time.Millisecond, map[int]bool{37: true, 45: true}, log)
	if err == nil || !strings.Contains(err.Error(), "pid 37, which the checkpoint restores") {
		t.Fatalf("a held pid the dump restores: %v", err)
	}
	if _, err := awaitPlaceholderPIDReservationFor(base, 5000, 600*time.Millisecond, nil, log); err == nil {
		t.Fatal("without the dumped ids, any low pid must block the restore")
	}
}
