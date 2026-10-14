// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	ok := driveGroupFreeze(ctx, http.DefaultClient, "s1", []freezeMember{
		{Base: b1, Namespace: "ns", Pod: "worker-0"}, {Base: b2, Namespace: "ns", Pod: "etcd-0"},
	}, 20*time.Millisecond, log)
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

// A member whose capture ends before the freeze point aborts the others.
func TestGroupFreeze_AbortsWhenAMemberEnds(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	a1, b1 := freezeServer(t)
	_, b2 := freezeServer(t)
	ctx := context.Background()
	errs := make(chan error, 1)
	go func() { errs <- a1.awaitGroupFreeze(ctx, "s2", "ns", "worker-0", log) }()
	ok := driveGroupFreeze(ctx, http.DefaultClient, "s2", []freezeMember{
		{Base: b1, Namespace: "ns", Pod: "worker-0"},
		{Base: b2, Namespace: "ns", Pod: "etcd-0", Done: func() bool { return true }},
	}, 20*time.Millisecond, log)
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
