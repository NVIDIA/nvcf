// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// The group freeze point. The members of a group capture are dumped by
// their own nodes' agents, each after its own preparation (a GPU rank's
// suspend takes minutes for a large model, a companion's none). Dumped as
// each was ready, they stopped seconds apart, and an instance that keeps
// time between its pods saw that gap as real: an etcd that kept counting
// while its client had already stopped expired the client's lease, and the
// restored client found its lease gone. So every member waits at a freeze
// point once ready, and the group's driver releases them together: their
// dumps start at the same moment.
//
// Starting together is not stopping together for long enough: a member
// dumped first resumed while another was still being dumped and talked to it
// (a NATS ping and its pong), so the two images held the same TCP connection
// at different points and the restored connection never resynchronised. So
// each member's criu also waits in its post-dump hook, its tree still
// stopped and its network still locked, until every member is dumped; then
// they all resume.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// groupFreezeWait bounds how long a member waits at the freeze point, and
// the driver for every member to reach it: the GPU ranks' suspend comes
// first.
const groupFreezeWait = 30 * time.Minute

// freezePoint is one member's place at its group's freeze point.
type freezePoint struct {
	reached chan struct{} // closed when the member waits at the point
	release chan bool     // true: dump now; false: the capture is aborted
	once    sync.Once
}

type freezePoints struct {
	mu sync.Mutex
	m  map[string]*freezePoint
}

func freezeKey(session, ns, pod string) string { return session + "/" + ns + "/" + pod }

func (f *freezePoints) get(key string) *freezePoint {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]*freezePoint{}
	}
	p := f.m[key]
	if p == nil {
		p = &freezePoint{reached: make(chan struct{}), release: make(chan bool, 1)}
		f.m[key] = p
	}
	return p
}

func (f *freezePoints) drop(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, key)
}

// awaitGroupFreeze waits at the freeze point of group session until the
// driver releases the member.
func (a *Agent) awaitGroupFreeze(ctx context.Context, session, ns, pod string, log *logrus.Entry) error {
	key := freezeKey(session, ns, pod)
	p := a.freezes.get(key)
	defer a.freezes.drop(key)
	p.once.Do(func() { close(p.reached) })
	log.Info("group capture: at the freeze point; waiting for every member")
	t0 := time.Now()
	select {
	case ok := <-p.release:
		if !ok {
			return fmt.Errorf("group capture %s: aborted at the freeze point", session)
		}
		log.WithField("waited", time.Since(t0).Round(time.Millisecond).String()).Info("group capture: released; dumping")
		return nil
	case <-time.After(groupFreezeWait):
		return fmt.Errorf("group capture %s: not released from the freeze point within %s", session, groupFreezeWait)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// postDumpWait bounds how long a dumped member waits, stopped, for the
// others: the slowest member's dump.
const postDumpWait = 10 * time.Minute

// postDumpDir is the post-dump barrier of a member of group session: its
// hook writes "reached" there and waits for "go" or "abort".
func (a *Agent) postDumpDir(session, ns, pod string) string {
	return filepath.Join(a.config.CheckpointDir, ".group-freeze", session, ns+"_"+pod)
}

// writePostDumpScript writes the member's criu action script into dir: at
// post-dump (the tree dumped, still stopped, the network still locked) it
// reports the member dumped and waits for the driver. Any other action
// passes. A non-zero exit makes criu roll the dump back and resume the tree.
func writePostDumpScript(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("post-dump barrier: %w", err)
	}
	path := filepath.Join(dir, "post-dump.sh")
	if err := writeDumpActionScript(path, "", dir); err != nil {
		return "", fmt.Errorf("post-dump barrier: %w", err)
	}
	return path, nil
}

// postDumpBarrier is the action script's wait at a group's post-dump
// barrier in dir.
func postDumpBarrier(dir string) string {
	return fmt.Sprintf(`D=%q
: > "$D/reached"
i=0
while [ ! -e "$D/go" ]; do
	[ -e "$D/abort" ] && exit 1
	i=$((i+1)); [ "$i" -gt %d ] && exit 1
	sleep 0.05
done
`, dir, int(postDumpWait/(50*time.Millisecond)))
}

// markPostDumpReached reports a member that has no post-dump hook as past
// its dump.
func markPostDumpReached(dir string, log *logrus.Entry) {
	if err := os.MkdirAll(dir, 0o755); err == nil {
		err = os.WriteFile(filepath.Join(dir, "reached"), nil, 0o644)
		if err == nil {
			return
		}
	}
	log.Warn("group capture: cannot report the member dumped; its group's driver will abort the capture")
}

// markPostDumpFailed reports a member whose dump failed: its group's
// driver aborts the others at once instead of waiting out the barrier.
func markPostDumpFailed(dir string, log *logrus.Entry) {
	if err := os.MkdirAll(dir, 0o755); err == nil {
		err = os.WriteFile(filepath.Join(dir, "failed"), nil, 0o644)
		if err == nil {
			return
		}
	}
	log.Warn("group capture: cannot report the member's dump failed; its group's driver aborts the capture when the barrier times out")
}

// freezeHandler serves a member's freeze point to its group's driver:
// GET reports whether the member waits there, POST ?go=true|false releases
// or aborts it. ?phase=post is the member's post-dump barrier.
func (a *Agent) freezeHandler(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	switch r.URL.Query().Get("phase") {
	case "hold":
		// POST ?container=&keep=: freeze the member's pod (pod_hold.go).
		if r.Method != http.MethodPost {
			http.Error(w, "hold is POST", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		if err := a.holdMember(r.Context(), v["session"], v["namespace"], v["pod"], q.Get("container"), q.Get("keep") == "true"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	case "release":
		a.releaseHold(holdKey(v["session"], v["namespace"], v["pod"]), a.log.WithField("pod", v["namespace"]+"/"+v["pod"]))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Query().Get("phase") == "post" {
		dir := a.postDumpDir(v["session"], v["namespace"], v["pod"])
		if r.Method == http.MethodPost {
			name := "abort"
			if r.URL.Query().Get("go") == "true" {
				name = "go"
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, err := os.Stat(filepath.Join(dir, "reached"))
		_, ferr := os.Stat(filepath.Join(dir, "failed"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"reached": err == nil, "failed": ferr == nil})
		return
	}
	key := freezeKey(v["session"], v["namespace"], v["pod"])
	if r.Method == http.MethodPost {
		p := a.freezes.get(key)
		select {
		case p.release <- r.URL.Query().Get("go") == "true":
		default: // already released
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	reached := false
	a.freezes.mu.Lock()
	if p := a.freezes.m[key]; p != nil {
		select {
		case <-p.reached:
			reached = true
		default:
		}
	}
	a.freezes.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"reached": reached})
}

// freezeMember is one member of a group capture as its driver sees it.
type freezeMember struct {
	Base, Namespace, Pod string
	// Done reports that the member's capture ended (it failed before the
	// freeze point).
	Done func() bool
}

// driveGroupFreeze releases every member from the freeze point of phase
// ("" before the dump, "post" after it) once all wait there, or aborts them
// all when one ends before it, or none of that happens in time.
func driveGroupFreeze(ctx context.Context, client *http.Client, session, phase string, members []freezeMember, poll time.Duration, log *logrus.Entry, gate <-chan struct{}) bool {
	wait := groupFreezeWait
	if phase == "post" {
		wait = postDumpWait
	}
	deadline := time.Now().Add(wait)
	// release posts the decision of phase to every member and reports
	// whether each one acknowledged it. A member that did not (its agent
	// restarting, a dropped connection) is asked again briefly.
	release := func(ok bool, phase string) bool {
		var wg sync.WaitGroup
		var missed atomic.Int32
		for _, m := range members {
			wg.Add(1)
			go func(m freezeMember) {
				defer wg.Done()
				u := fmt.Sprintf("%s/v1/group-freeze/%s/%s/%s?go=%t&phase=%s", m.Base, url.PathEscape(session), url.PathEscape(m.Namespace), url.PathEscape(m.Pod), ok, url.QueryEscape(phase))
				var err error
				for attempt := 0; attempt < releaseAttempts; attempt++ {
					if attempt > 0 {
						time.Sleep(releaseRetry)
					}
					if err = postFreeze(context.WithoutCancel(ctx), client, u); err == nil {
						return
					}
				}
				missed.Add(1)
				log.WithError(err).WithField("pod", m.Namespace+"/"+m.Pod).Warn("group capture: a member did not get its freeze point's decision")
			}(m)
		}
		wg.Wait()
		return missed.Load() == 0
	}
	for {
		all := true
		for _, m := range members {
			// Read the end before the point: a member without a post-dump
			// hook reports it and returns at once.
			done := m.Done != nil && m.Done()
			reached, failed := freezeState(ctx, client, m.Base, session, phase, m.Namespace, m.Pod)
			if reached && !failed {
				continue
			}
			if done || failed {
				log.WithFields(logrus.Fields{"pod": m.Namespace + "/" + m.Pod, "phase": phaseName(phase)}).Warn("group capture: a member ended before the freeze point; aborting the others")
				release(false, phase)
				return false
			}
			all = false
		}
		if all {
			if gate != nil {
				// The rest of the instance is being held; bounded, a
				// hold that does not finish leaves its pod running.
				select {
				case <-gate:
				case <-time.After(holdGateWait):
					log.Warn("group capture: the instance's pods are not all held yet; releasing the members anyway")
				case <-ctx.Done():
				}
			}
			if !release(true, phase) {
				// The member that missed it stays at its freeze point
				// while the rest dump and then wait for it at the
				// post-dump barrier: end the capture now instead. The
				// post-dump abort is recorded ahead of the members that
				// already dump, and they stop on reaching the barrier.
				log.WithField("phase", phaseName(phase)).Warn("group capture: a member was not released; aborting the others")
				release(false, phase)
				if phase == "" {
					release(false, "post")
				}
				return false
			}
			log.WithFields(logrus.Fields{"members": len(members), "phase": phaseName(phase)}).Info("group capture: every member at the freeze point; released together")
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			log.Warn("group capture: not every member reached the freeze point; aborting")
			release(false, phase)
			return false
		}
		time.Sleep(poll)
	}
}

// releaseAttempts and releaseRetry bound how long the driver keeps asking a
// member that did not acknowledge its freeze point's decision.
const releaseAttempts = 3

var releaseRetry = 500 * time.Millisecond

// postFreeze posts to a member's freeze point; an error unless the member's
// agent accepted it.
func postFreeze(ctx context.Context, client *http.Client, u string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("status %s", resp.Status)
	}
	return nil
}

func phaseName(phase string) string {
	if phase == "" {
		return "before the dump"
	}
	return strings.TrimSpace(phase) + "-dump"
}

// freezeState reports whether a member waits at the freeze point of
// phase, and whether it failed before it (post-dump: its dump failed).
func freezeState(ctx context.Context, client *http.Client, base, session, phase, ns, pod string) (reached, failed bool) {
	u := fmt.Sprintf("%s/v1/group-freeze/%s/%s/%s?phase=%s", base, url.PathEscape(session), url.PathEscape(ns), url.PathEscape(pod), url.QueryEscape(phase))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	var out map[string]bool
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return false, false
	}
	return out["reached"], out["failed"]
}

// holdGateWait bounds the wait at the freeze point for the members' pods
// to be held.
const holdGateWait = 30 * time.Second

// holdMembers holds every member pod of capture session (pod_hold.go): a
// companion's whole pod, a GPU rank's but for the container it saves.
func holdMembers(ctx context.Context, client *http.Client, session string, members []CheckpointRequest, bases []string, log *logrus.Entry) {
	var wg sync.WaitGroup
	for i := range members {
		wg.Add(1)
		go func(base string, m *CheckpointRequest) {
			defer wg.Done()
			u := fmt.Sprintf("%s/v1/group-freeze/%s/%s/%s?phase=hold&container=%s&keep=%t", base, url.PathEscape(session),
				url.PathEscape(m.Namespace), url.PathEscape(m.PodName), url.QueryEscape(m.ContainerName), !m.Companion)
			if err := postNoContent(ctx, client, u); err != nil {
				log.WithError(err).WithField("pod", m.Namespace+"/"+m.PodName).Warn("group capture: cannot hold a member's pod; it keeps running during the capture")
			}
		}(bases[i], &members[i])
	}
	wg.Wait()
	log.WithField("members", len(members)).Info("group capture: the instance's pods are held")
}

// releaseMembers ends every member's hold; a member ends its own with its
// capture, so this only covers one that did not.
func releaseMembers(ctx context.Context, client *http.Client, session string, members []CheckpointRequest, bases []string, log *logrus.Entry) {
	for i := range members {
		m := &members[i]
		u := fmt.Sprintf("%s/v1/group-freeze/%s/%s/%s?phase=release", bases[i], url.PathEscape(session), url.PathEscape(m.Namespace), url.PathEscape(m.PodName))
		if err := postNoContent(context.WithoutCancel(ctx), client, u); err != nil {
			log.WithError(err).WithField("pod", m.Namespace+"/"+m.PodName).Warn("group capture: cannot release a member's hold; its agent thaws it on its own")
		}
	}
}

func postNoContent(ctx context.Context, client *http.Client, u string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}
