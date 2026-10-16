// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

func freezeServer(t *testing.T) (*Agent, string) {
	t.Helper()
	a := &Agent{}
	r := mux.NewRouter()
	r.HandleFunc("/v1/group-freeze/{session}/{namespace}/{pod}", a.freezeHandler).Methods("GET", "POST")
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return a, srv.URL
}

// Members ready at different times are released together, only once the
// last one waits.
func TestGroupFreeze_ReleasesTogether(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	a1, b1 := freezeServer(t)
	a2, b2 := freezeServer(t)
	ctx := context.Background()
	var released [2]atomic.Int64
	errs := make(chan error, 2)
	go func() {
		err := a1.awaitGroupFreeze(ctx, "s1", "ns", "worker-0", log)
		released[0].Store(time.Now().UnixNano())
		errs <- err
	}()
	go func() {
		time.Sleep(300 * time.Millisecond) // a slower member, e.g. a GPU rank's suspend
		err := a2.awaitGroupFreeze(ctx, "s1", "ns", "etcd-0", log)
		released[1].Store(time.Now().UnixNano())
		errs <- err
	}()
	ok := driveGroupFreeze(ctx, http.DefaultClient, "s1", "", []freezeMember{
		{Base: b1, Namespace: "ns", Pod: "worker-0"}, {Base: b2, Namespace: "ns", Pod: "etcd-0"},
	}, 20*time.Millisecond, log, nil)
	if !ok {
		t.Fatal("driver did not release the group")
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("member: %v", err)
		}
	}
	if d := time.Duration(released[0].Load() - released[1].Load()); d > 100*time.Millisecond || d < -100*time.Millisecond {
		t.Errorf("members released %s apart", d)
	}
}

// Every member at the freeze point is released only once the gate opens:
// the instance's pods are held.
func TestGroupFreeze_WaitsForTheGate(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	a1, b1 := freezeServer(t)
	ctx := context.Background()
	errs := make(chan error, 1)
	go func() { errs <- a1.awaitGroupFreeze(ctx, "s5", "ns", "worker-0", log) }()
	gate := make(chan struct{})
	opened := time.Now().Add(300 * time.Millisecond)
	time.AfterFunc(300*time.Millisecond, func() { close(gate) })
	ok := driveGroupFreeze(ctx, http.DefaultClient, "s5", "", []freezeMember{{Base: b1, Namespace: "ns", Pod: "worker-0"}}, 20*time.Millisecond, log, gate)
	if !ok || time.Now().Before(opened) {
		t.Fatalf("released before the gate opened (ok=%v)", ok)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

// A member whose capture ends before the freeze point aborts the others.
func TestGroupFreeze_AbortsWhenAMemberEnds(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	a1, b1 := freezeServer(t)
	_, b2 := freezeServer(t)
	ctx := context.Background()
	errs := make(chan error, 1)
	go func() { errs <- a1.awaitGroupFreeze(ctx, "s2", "ns", "worker-0", log) }()
	ok := driveGroupFreeze(ctx, http.DefaultClient, "s2", "", []freezeMember{
		{Base: b1, Namespace: "ns", Pod: "worker-0"},
		{Base: b2, Namespace: "ns", Pod: "etcd-0", Done: func() bool { return true }},
	}, 20*time.Millisecond, log, nil)
	if ok {
		t.Fatal("driver released a group with a failed member")
	}
	select {
	case err := <-errs:
		if err == nil {
			t.Error("the waiting member was not aborted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting member was never released")
	}
}

// A release that reaches a member's agent before the member does is kept.
func TestGroupFreeze_EarlyReleaseIsKept(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	a, base := freezeServer(t)
	resp, err := http.Post(base+"/v1/group-freeze/s3/ns/p?go=true", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.awaitGroupFreeze(ctx, "s3", "ns", "p", log); err != nil {
		t.Fatalf("awaitGroupFreeze = %v", err)
	}
}

// The post-dump hook passes other actions, reports the member dumped at
// post-dump and waits: exit 0 on go, non-zero on abort (criu then rolls
// the dump back).
func TestPostDumpScript(t *testing.T) {
	run := func(action string, release func(dir string)) (bool, error) {
		dir := t.TempDir()
		script, err := writePostDumpScript(dir)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(script)
		cmd.Env = append(os.Environ(), "CRTOOLS_SCRIPT_ACTION="+action)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		reached := false
		if release != nil {
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(filepath.Join(dir, "reached")); err == nil {
					reached = true
					break
				}
			}
			release(dir)
		}
		return reached, cmd.Wait()
	}
	if _, err := run("pre-dump", nil); err != nil {
		t.Errorf("pre-dump: %v, want a pass", err)
	}
	reached, err := run("post-dump", func(d string) { _ = os.WriteFile(filepath.Join(d, "go"), nil, 0o644) })
	if !reached || err != nil {
		t.Errorf("post-dump released: reached=%v err=%v, want reached and exit 0", reached, err)
	}
	reached, err = run("post-dump", func(d string) { _ = os.WriteFile(filepath.Join(d, "abort"), nil, 0o644) })
	if !reached || err == nil {
		t.Errorf("post-dump aborted: reached=%v err=%v, want reached and a failure", reached, err)
	}
}

// The post-dump phase: a member that reports itself past its dump and
// returns (no hook) does not abort the group.
func TestGroupFreeze_PostPhaseAcceptsAMemberWithoutHook(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	a, base := freezeServer(t)
	a.config.CheckpointDir = t.TempDir()
	markPostDumpReached(a.postDumpDir("s4", "ns", "etcd-0"), log)
	ok := driveGroupFreeze(context.Background(), http.DefaultClient, "s4", "post", []freezeMember{
		{Base: base, Namespace: "ns", Pod: "etcd-0", Done: func() bool { return true }},
	}, 20*time.Millisecond, log, nil)
	if !ok {
		t.Fatal("a member past its dump aborted the group")
	}
	if _, err := os.Stat(filepath.Join(a.postDumpDir("s4", "ns", "etcd-0"), "go")); err != nil {
		t.Errorf("member not released: %v", err)
	}
}
