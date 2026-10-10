// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// fakeFabricMember plays one node's tool for driveFabric.
type fakeFabricMember struct {
	name string
	mu   sync.Mutex
	st   fabricState
	reg  bool
	// verdicts and ins record what the driver sent.
	verdicts []string
	ins      []string
	unlocks  []string
}

func (f *fakeFabricMember) Name() string { return f.name }
func (f *fakeFabricMember) State(context.Context) (fabricState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st, f.reg, nil
}
func (f *fakeFabricMember) Verdict(_ context.Context, attempt int, verdict string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verdicts = append(f.verdicts, fmt.Sprintf("%d %s", attempt, verdict))
	return nil
}
func (f *fakeFabricMember) PutIn(_ context.Context, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ins = append(f.ins, string(data))
	f.st.HaveIn = true
	return nil
}
func (f *fakeFabricMember) Unlock(_ context.Context, verdict string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlocks = append(f.unlocks, verdict)
	return nil
}
func (f *fakeFabricMember) restored() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reg = true
	f.st.Restored = true
}
func (f *fakeFabricMember) unlocked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.unlocks...)
}
func (f *fakeFabricMember) vote(attempt int, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reg = true
	f.st.Vote = &fabricVote{Attempt: attempt, OK: ok}
}
func (f *fakeFabricMember) out(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.Out = &s
}
func (f *fakeFabricMember) sent() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.verdicts...), append([]string(nil), f.ins...)
}

func quietFabricLog() *logrus.Entry {
	l := logrus.New()
	l.SetOutput(nopWriter{})
	return logrus.NewEntry(l)
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// eventually polls cond for up to 3 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// doneSet is the per-member "checkpoint returned" flag driveFabric reads.
type doneSet struct {
	mu sync.Mutex
	d  map[int]bool
}

func (d *doneSet) set(i int) { d.mu.Lock(); d.d[i] = true; d.mu.Unlock() }
func (d *doneSet) get(i int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.d[i]
}

func startDrive(members []fabricMember) (*doneSet, chan fabricOutcome, context.CancelFunc) {
	return startDriveOpts(members, fabricDriveOpts{Vote: true})
}

func startDriveOpts(members []fabricMember, opts fabricDriveOpts) (*doneSet, chan fabricOutcome, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ds := &doneSet{d: map[int]bool{}}
	res := make(chan fabricOutcome, 1)
	go func() { res <- driveFabric(ctx, members, ds.get, opts, quietFabricLog()) }()
	return ds, res, cancel
}

func TestDriveFabric_GoOnceAllQuiescedThenExchangeInMemberOrder(t *testing.T) {
	a, b := &fakeFabricMember{name: "a"}, &fakeFabricMember{name: "b"}
	ds, res, cancel := startDrive([]fabricMember{a, b})
	defer cancel()

	a.vote(0, true)
	time.Sleep(3 * fabricPollInterval)
	if v, _ := a.sent(); len(v) != 0 {
		t.Fatalf("a verdict before every member voted: %v", v)
	}
	b.vote(0, true)
	eventually(t, "go to both", func() bool {
		va, _ := a.sent()
		vb, _ := b.sent()
		return len(va) == 1 && len(vb) == 1
	})
	for _, m := range []*fakeFabricMember{a, b} {
		if v, _ := m.sent(); v[0] != "0 go" {
			t.Errorf("%s verdict = %v, want [0 go]", m.name, v)
		}
	}

	b.out("hb1 hb1'\n")
	a.out("ha1 ha1'\nha2 ha2'") // no trailing newline: the merge adds one
	eventually(t, "handle lists", func() bool {
		_, ia := a.sent()
		_, ib := b.sent()
		return len(ia) == 1 && len(ib) == 1
	})
	want := "ha1 ha1'\nha2 ha2'\nhb1 hb1'\n"
	for _, m := range []*fakeFabricMember{a, b} {
		if _, in := m.sent(); in[0] != want {
			t.Errorf("%s in = %q, want %q (member order, one line per handle)", m.name, in[0], want)
		}
	}
	ds.set(0)
	ds.set(1)
	out := <-res
	if !out.Went || !out.Exchanged || out.Attempts != 1 || out.Handles != 3 {
		t.Errorf("outcome = %+v, want went, exchanged, 1 attempt, 3 handles", out)
	}
	if _, in := a.sent(); len(in) != 1 {
		t.Errorf("handle list delivered %d times, want once", len(in))
	}
}

func TestDriveFabric_RetryWhileAnyMemberIsBusy(t *testing.T) {
	a, b := &fakeFabricMember{name: "a"}, &fakeFabricMember{name: "b"}
	ds, res, cancel := startDrive([]fabricMember{a, b})
	defer cancel()

	a.vote(0, true)
	b.vote(0, false)
	eventually(t, "retry", func() bool { v, _ := b.sent(); return len(v) == 1 })
	a.vote(1, true)
	b.vote(1, true)
	eventually(t, "go", func() bool { v, _ := b.sent(); return len(v) == 2 })
	for _, m := range []*fakeFabricMember{a, b} {
		if v, _ := m.sent(); !reflect.DeepEqual(v, []string{"0 retry", "1 go"}) {
			t.Errorf("%s verdicts = %v, want [0 retry, 1 go]", m.name, v)
		}
	}
	ds.set(0)
	ds.set(1)
	if out := <-res; out.Attempts != 2 || !out.Went {
		t.Errorf("outcome = %+v, want 2 attempts and went", out)
	}
}

func TestDriveFabric_AbortsWhenAMemberEndsBeforeTheGo(t *testing.T) {
	a, b, c := &fakeFabricMember{name: "a"}, &fakeFabricMember{name: "b"}, &fakeFabricMember{name: "c"}
	ds, res, cancel := startDrive([]fabricMember{a, b, c})
	defer cancel()

	a.vote(0, true)
	b.vote(0, true)
	ds.set(2) // c's checkpoint failed before it could vote
	eventually(t, "abort", func() bool {
		va, _ := a.sent()
		vb, _ := b.sent()
		return len(va) == 1 && len(vb) == 1
	})
	// The tools keep voting until their own timeout; every vote is aborted.
	a.vote(1, true)
	eventually(t, "second abort", func() bool { v, _ := a.sent(); return len(v) == 2 })
	if v, _ := a.sent(); !reflect.DeepEqual(v, []string{"0 abort", "1 abort"}) {
		t.Errorf("a verdicts = %v, want aborts only", v)
	}
	ds.set(0)
	ds.set(1)
	if out := <-res; out.Went {
		t.Errorf("outcome = %+v: the session must not go without every member", out)
	}
}

func TestDriveFabric_ExchangeLeavesOutMembersThatEnded(t *testing.T) {
	a, b, c := &fakeFabricMember{name: "a"}, &fakeFabricMember{name: "b"}, &fakeFabricMember{name: "c"}
	ds, res, cancel := startDrive([]fabricMember{a, b, c})
	defer cancel()

	a.vote(0, true)
	b.vote(0, true)
	c.vote(0, true)
	eventually(t, "go", func() bool { v, _ := c.sent(); return len(v) == 1 })
	ds.set(2) // c failed after the go and will not resume
	a.out("ha\n")
	b.out("hb\n")
	eventually(t, "exchange", func() bool { _, in := b.sent(); return len(in) == 1 })
	if _, in := a.sent(); in[0] != "ha\nhb\n" {
		t.Errorf("in = %q, want the running members' lists", in[0])
	}
	if _, in := c.sent(); len(in) != 0 {
		t.Errorf("ended member got a list: %v", in)
	}
	ds.set(0)
	ds.set(1)
	<-res
}

func TestParseFabricVote(t *testing.T) {
	for in, want := range map[string]*fabricVote{"0 ok\n": {0, true}, "12 busy": {12, false}} {
		got, err := parseFabricVote(in)
		if err != nil || *got != *want {
			t.Errorf("parseFabricVote(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ok", "1 go", "-1 ok", "x ok", "1 ok extra"} {
		if _, err := parseFabricVote(bad); err == nil {
			t.Errorf("parseFabricVote(%q) accepted", bad)
		}
	}
}

func TestFabricSessionNames(t *testing.T) {
	if s := newFabricSession(); !validFabricSession(s) {
		t.Errorf("newFabricSession() = %q is not valid", s)
	}
	for _, bad := range []string{"", "../x", "a/b", "UPPER", strings.Repeat("a", 64)} {
		if validFabricSession(bad) {
			t.Errorf("validFabricSession(%q) = true", bad)
		}
	}
	if got := fabricContainerDir("s1"); got != "/nvsnap-gpushare/.fabric-s1" {
		t.Errorf("fabricContainerDir = %q", got)
	}
}

func TestGPUShareArgs_FabricMap(t *testing.T) {
	got := gpushareSuspendArgs("/nvsnap-gpushare", "/nvsnap-gpushare/.fabric-s1", []int{7, 8})
	want := []string{"--timeout-ms", "120000", "--store", "/nvsnap-gpushare", "--ckpt-dir", "/nvsnap-gpushare",
		"--fabric-map", "/nvsnap-gpushare/.fabric-s1", "suspend", "7", "8"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("suspend args = %v, want %v", got, want)
	}
	if got := gpushareSuspendArgs("/s", "", []int{7}); strings.Contains(strings.Join(got, " "), "fabric") {
		t.Errorf("single-node suspend carries --fabric-map: %v", got)
	}
	got = gpushareResumeArgs("/s/gpus", "/f", []int{7})
	want = []string{"--gpu-map", "/s/gpus", "--fabric-map", "/f", "resume", "7"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resume args = %v, want %v", got, want)
	}
	if got := gpushareResumeArgs("", "", []int{7}); !reflect.DeepEqual(got, []string{"resume", "7"}) {
		t.Errorf("plain resume args = %v", got)
	}
}

// fabricTestAgent serves the fabric routes over a temp checkpoint dir.
func fabricTestAgent(t *testing.T) (*Agent, *httptest.Server) {
	t.Helper()
	a := &Agent{config: Config{CheckpointDir: t.TempDir()}}
	r := mux.NewRouter()
	r.HandleFunc("/v1/gpushare/fabric/{session}/{namespace}/{pod}", a.fabricStateHandler).Methods("GET")
	r.HandleFunc("/v1/gpushare/fabric/{session}/{namespace}/{pod}/verdict", a.fabricVerdictHandler).Methods("PUT")
	r.HandleFunc("/v1/gpushare/fabric/{session}/{namespace}/{pod}/in", a.fabricInHandler).Methods("PUT")
	r.HandleFunc("/v1/gpushare/fabric/{session}/{namespace}/{pod}/unlock", a.fabricUnlockHandler).Methods("PUT")
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return a, srv
}

// simulateTool follows nvsnap-gpu-suspend's file protocol in dir: vote
// until "go", then write out and wait for in, as node_vote and
// fabric_exchange do. busyFirst makes its first vote "busy".
func simulateTool(dir, out string, busyFirst bool) (string, error) {
	write := func(name, s string) error {
		if err := os.WriteFile(filepath.Join(dir, name+".tmp"), []byte(s), 0o644); err != nil {
			return err
		}
		return os.Rename(filepath.Join(dir, name+".tmp"), filepath.Join(dir, name))
	}
	deadline := time.Now().Add(5 * time.Second)
	for attempt := 0; ; attempt++ {
		ok := "ok"
		if busyFirst && attempt == 0 {
			ok = "busy"
		}
		if err := write("q", fmt.Sprintf("%d %s\n", attempt, ok)); err != nil {
			return "", err
		}
		for {
			if time.Now().After(deadline) {
				return "", fmt.Errorf("no verdict for attempt %d", attempt)
			}
			var a int
			var v string
			if b, err := os.ReadFile(filepath.Join(dir, "d")); err == nil {
				if _, err := fmt.Sscanf(string(b), "%d %s", &a, &v); err == nil && a == attempt {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "d"))
		if strings.Contains(string(b), " go") {
			break
		}
	}
	if err := write("out", out); err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "in")); err == nil {
			return string(b), nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no handle list")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Two nodes' agents, each serving one pod's session, driven over HTTP the
// way groupCheckpoint does, with each tool simulated on its directory.
func TestFabricSession_EndToEndOverHTTP(t *testing.T) {
	type node struct {
		a      *Agent
		srv    *httptest.Server
		dir    string
		closeF func()
	}
	session := newFabricSession()
	nodes := make([]node, 2)
	for i := range nodes {
		a, srv := fabricTestAgent(t)
		cdir, closeF, err := a.openFabricSession(session, "ns", fmt.Sprintf("worker-%d", i), fmt.Sprintf("uid-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if cdir != fabricContainerDir(session) {
			t.Fatalf("container dir = %q", cdir)
		}
		nodes[i] = node{a: a, srv: srv, dir: a.fabricHostDir(fmt.Sprintf("uid-%d", i), session), closeF: closeF}
	}

	members := make([]fabricMember, len(nodes))
	for i, n := range nodes {
		members[i] = &httpFabricMember{client: http.DefaultClient, base: n.srv.URL, session: session, ns: "ns", pod: fmt.Sprintf("worker-%d", i)}
	}
	ds, res, cancel := startDrive(members)
	defer cancel()

	ins := make([]string, len(nodes))
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, dir string) {
			defer wg.Done()
			ins[i], errs[i] = simulateTool(dir, fmt.Sprintf("old%d new%d\n", i, i), i == 1)
			ds.set(i)
		}(i, n.dir)
	}
	wg.Wait()
	out := <-res
	for i := range nodes {
		if errs[i] != nil {
			t.Fatalf("tool %d: %v", i, errs[i])
		}
		if ins[i] != "old0 new0\nold1 new1\n" {
			t.Errorf("tool %d got in = %q", i, ins[i])
		}
	}
	if !out.Went || !out.Exchanged || out.Attempts != 2 || out.Handles != 2 {
		t.Errorf("outcome = %+v, want went after a retried vote and 2 handles exchanged", out)
	}

	// Closing a session removes its directory and its route.
	nodes[0].closeF()
	if _, err := os.Stat(nodes[0].dir); !os.IsNotExist(err) {
		t.Errorf("fabric dir survives close: %v", err)
	}
	if _, ok, err := members[0].State(context.Background()); ok || err != nil {
		t.Errorf("closed session still served: ok=%v err=%v", ok, err)
	}
}

func TestFabricHandlers_RejectBadInput(t *testing.T) {
	a, srv := fabricTestAgent(t)
	if _, _, err := a.openFabricSession("s1", "ns", "p", "uid"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.openFabricSession("../x", "ns", "p", "uid"); err == nil {
		t.Error("openFabricSession accepted a path as a session")
	}
	if _, _, err := a.openFabricSession("s2", "ns", "p", ""); err == nil {
		t.Error("openFabricSession accepted an empty pod uid")
	}
	for _, q := range []string{"attempt=-1&verdict=go", "attempt=0&verdict=maybe", "verdict=go"} {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/gpushare/fabric/s1/ns/p/verdict?"+q, http.NoBody)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("verdict %q: status %d, want 400", q, resp.StatusCode)
		}
	}
	resp, err := http.Get(srv.URL + "/v1/gpushare/fabric/s1/ns/other")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unregistered pod: status %d, want 404", resp.StatusCode)
	}
}

func TestGroupCheckpoint_RejectsUnsafeRequests(t *testing.T) {
	a := &Agent{}
	for name, req := range map[string]GroupCheckpointRequest{
		"one member":     {Members: []CheckpointRequest{{Namespace: "n", PodName: "a"}}, LeaveRunning: true},
		"no kube client": {Members: []CheckpointRequest{{Namespace: "n", PodName: "a"}, {Namespace: "n", PodName: "b"}}},
	} {
		if _, err := a.groupCheckpoint(context.Background(), req, quietFabricLog()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDriveFabric_UnlockOnceAllRestoredThenExchange(t *testing.T) {
	a, b := &fakeFabricMember{name: "a"}, &fakeFabricMember{name: "b"}
	ds, res, cancel := startDriveOpts([]fabricMember{a, b}, fabricDriveOpts{Unlock: true})
	defer cancel()

	a.restored()
	a.out("ha\n") // a list before the unlock must wait for it
	time.Sleep(3 * fabricPollInterval)
	if u := a.unlocked(); len(u) != 0 {
		t.Fatalf("unlocked before every pod was restored: %v", u)
	}
	if _, in := a.sent(); len(in) != 0 {
		t.Fatalf("handle list delivered before the unlock: %v", in)
	}
	b.restored()
	eventually(t, "unlock", func() bool { return len(b.unlocked()) == 1 })
	for _, m := range []*fakeFabricMember{a, b} {
		if u := m.unlocked(); !reflect.DeepEqual(u, []string{"unlock"}) {
			t.Errorf("%s unlocks = %v, want [unlock]", m.name, u)
		}
		if v, _ := m.sent(); len(v) != 0 {
			t.Errorf("%s got a quiesce verdict in a restore: %v", m.name, v)
		}
	}
	b.out("hb\n")
	eventually(t, "exchange", func() bool { _, in := b.sent(); return len(in) == 1 })
	ds.set(0)
	ds.set(1)
	if out := <-res; !out.Unlocked || !out.Exchanged || out.Handles != 2 {
		t.Errorf("outcome = %+v, want unlocked, exchanged, 2 handles", out)
	}
}

func TestDriveFabric_AbortRestoreWhenAMemberFails(t *testing.T) {
	a, b := &fakeFabricMember{name: "a"}, &fakeFabricMember{name: "b"}
	ds, res, cancel := startDriveOpts([]fabricMember{a, b}, fabricDriveOpts{Unlock: true})
	defer cancel()

	a.restored()
	ds.set(1) // b's CRIU restore failed
	eventually(t, "abort", func() bool { return len(a.unlocked()) == 1 })
	if u := a.unlocked(); u[0] != fabricVerdictAbort {
		t.Errorf("a got %v, want abort", u)
	}
	if u := b.unlocked(); len(u) != 0 {
		t.Errorf("failed member got %v", u)
	}
	ds.set(0)
	if out := <-res; out.Unlocked {
		t.Errorf("outcome = %+v: must not unlock without every member", out)
	}
}

func TestPlanGroupRestore(t *testing.T) {
	g := func(i int) *GPUShareGroupInfo { return &GPUShareGroupInfo{Session: "s1", Index: i, Size: 2} }
	got, err := planGroupRestore([]groupRestoreEntry{
		{Name: "b", OldIP: "10.0.0.2", NewIP: "10.1.0.20", Group: g(1)},
		{Name: "a", OldIP: "10.0.0.1", NewIP: "10.1.0.10", Group: g(0)},
	})
	if err != nil || got != "10.0.0.1=10.1.0.10,10.0.0.2=10.1.0.20" {
		t.Fatalf("plan = %q, %v; want both pods, in index order", got, err)
	}
	if got, err := planGroupRestore([]groupRestoreEntry{
		{Name: "a", OldIP: "fd00::1", NewIP: "fd00::a", Group: g(0)},
		{Name: "b", OldIP: "fd00::2", NewIP: "fd00::b", Group: g(1)},
	}); err != nil || got != "fd00::1=fd00::a,fd00::2=fd00::b" {
		t.Errorf("IPv6 plan = %q, %v", got, err)
	}
	for name, entries := range map[string][]groupRestoreEntry{
		"one member":     {{Name: "a", OldIP: "10.0.0.1", NewIP: "10.1.0.1", Group: g(0)}},
		"no group":       {{Name: "a", OldIP: "10.0.0.1", NewIP: "10.1.0.1"}, {Name: "b", OldIP: "10.0.0.2", NewIP: "10.1.0.2", Group: g(1)}},
		"mixed sessions": {{Name: "a", OldIP: "10.0.0.1", NewIP: "10.1.0.1", Group: g(0)}, {Name: "b", OldIP: "10.0.0.2", NewIP: "10.1.0.2", Group: &GPUShareGroupInfo{Session: "s2", Index: 1, Size: 2}}},
		"missing pod":    {{Name: "a", OldIP: "10.0.0.1", NewIP: "10.1.0.1", Group: &GPUShareGroupInfo{Session: "s1", Index: 0, Size: 3}}, {Name: "b", OldIP: "10.0.0.2", NewIP: "10.1.0.2", Group: &GPUShareGroupInfo{Session: "s1", Index: 1, Size: 3}}},
		"same index":     {{Name: "a", OldIP: "10.0.0.1", NewIP: "10.1.0.1", Group: g(0)}, {Name: "b", OldIP: "10.0.0.2", NewIP: "10.1.0.2", Group: g(0)}},
		"no old ip":      {{Name: "a", OldIP: "", NewIP: "10.1.0.1", Group: g(0)}, {Name: "b", OldIP: "10.0.0.2", NewIP: "10.1.0.2", Group: g(1)}},
		"same new ip":    {{Name: "a", OldIP: "10.0.0.1", NewIP: "10.1.0.1", Group: g(0)}, {Name: "b", OldIP: "10.0.0.2", NewIP: "10.1.0.1", Group: g(1)}},
		"mixed family":   {{Name: "a", OldIP: "10.0.0.1", NewIP: "fd00::1", Group: g(0)}, {Name: "b", OldIP: "10.0.0.2", NewIP: "10.1.0.2", Group: g(1)}},
	} {
		if got, err := planGroupRestore(entries); err == nil {
			t.Errorf("%s: accepted, map %q", name, got)
		}
	}
}

func TestRestoreV2Args_GroupRestore(t *testing.T) {
	got := strings.Join(restoreV2Args(10, "/checkpoints/x", true, false, "10.0.0.1=10.1.0.1,10.0.0.2=10.1.0.2"), " ")
	if !strings.Contains(got, "--inet-addr-map 10.0.0.1=10.1.0.1,10.0.0.2=10.1.0.2 --keep-network-lock") {
		t.Errorf("group restore args lack the map and the held lock: %s", got)
	}
	if plain := strings.Join(restoreV2Args(10, "/checkpoints/x", true, false, ""), " "); strings.Contains(plain, "inet-addr-map") || strings.Contains(plain, "keep-network-lock") {
		t.Errorf("single-pod restore carries group flags: %s", plain)
	}
}

func TestNetUnlockArgs(t *testing.T) {
	want := []string{"-t", "42", "-n", "--", "/criu-bundle/criu", "net-unlock", "-D", "/var/lib/nvsnap/checkpoints/c1", "-o", "net-unlock.log"}
	if got := netUnlockArgs(42, "/var/lib/nvsnap/checkpoints/c1"); !reflect.DeepEqual(got, want) {
		t.Errorf("netUnlockArgs = %v, want %v (network namespace only; the agent's CRIU and images)", got, want)
	}
	if !criuSupportsNetMigration("  --inet-addr-map OLD=NEW\n  net-unlock  release a kept lock\n") {
		t.Error("probe misses a CRIU with both")
	}
	if criuSupportsNetMigration("  --image-io-mode direct\n") {
		t.Error("probe accepts a CRIU without them")
	}
}

// A restore member reports itself restored, waits, and gets the driver's
// unlock over HTTP; with gpushare its session lives in the checkpoint's
// store, where the placeholder sees it at the store path.
func TestRestoreSession_UnlockOverHTTP(t *testing.T) {
	a, srv := fabricTestAgent(t)
	ckpt := t.TempDir()
	sess, err := a.openRestoreSession(restoreV2Group{Session: "s1", Namespace: "ns", Pod: "p0"}, ckpt, true)
	if err != nil {
		t.Fatal(err)
	}
	if sess.hostDir != filepath.Join(ckpt, GPUShareCheckpointSubdir, ".fabric-s1") || sess.containerDir != "/nvsnap-gpushare/.fabric-s1" {
		t.Fatalf("session dirs = %q, %q", sess.hostDir, sess.containerDir)
	}
	verdict := make(chan string, 1)
	go func() {
		v, err := sess.awaitUnlock(context.Background(), quietFabricLog())
		if err != nil {
			v = "error: " + err.Error()
		}
		verdict <- v
	}()
	m := &httpFabricMember{client: http.DefaultClient, base: srv.URL, session: "s1", ns: "ns", pod: "p0"}
	eventually(t, "restored", func() bool { st, ok, _ := m.State(context.Background()); return ok && st.Restored })
	if err := m.Unlock(context.Background(), "unlock"); err != nil {
		t.Fatal(err)
	}
	if v := <-verdict; v != "unlock" {
		t.Errorf("verdict = %q", v)
	}
	sess.close()
	if _, err := os.Stat(sess.hostDir); !os.IsNotExist(err) {
		t.Errorf("session dir survives close: %v", err)
	}
}
