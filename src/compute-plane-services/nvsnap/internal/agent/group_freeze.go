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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	steps := int(postDumpWait / (50 * time.Millisecond))
	script := fmt.Sprintf(`#!/bin/sh
[ "$CRTOOLS_SCRIPT_ACTION" = post-dump ] || exit 0
D=%q
: > "$D/reached"
i=0
while [ ! -e "$D/go" ]; do
	[ -e "$D/abort" ] && exit 1
	i=$((i+1)); [ "$i" -gt %d ] && exit 1
	sleep 0.05
done
`, dir, steps)
	path := filepath.Join(dir, "post-dump.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return "", fmt.Errorf("post-dump barrier: %w", err)
	}
	return path, nil
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

// freezeHandler serves a member's freeze point to its group's driver:
// GET reports whether the member waits there, POST ?go=true|false releases
// or aborts it. ?phase=post is the member's post-dump barrier.
func (a *Agent) freezeHandler(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"reached": err == nil})
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
func driveGroupFreeze(ctx context.Context, client *http.Client, session, phase string, members []freezeMember, poll time.Duration, log *logrus.Entry) bool {
	wait := groupFreezeWait
	if phase == "post" {
		wait = postDumpWait
	}
	deadline := time.Now().Add(wait)
	release := func(ok bool) {
		var wg sync.WaitGroup
		for _, m := range members {
			wg.Add(1)
			go func(m freezeMember) {
				defer wg.Done()
				u := fmt.Sprintf("%s/v1/group-freeze/%s/%s/%s?go=%t&phase=%s", m.Base, url.PathEscape(session), url.PathEscape(m.Namespace), url.PathEscape(m.Pod), ok, url.QueryEscape(phase))
				req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodPost, u, nil)
				if err != nil {
					return
				}
				if resp, err := client.Do(req); err == nil {
					resp.Body.Close()
				} else {
					log.WithError(err).WithField("pod", m.Namespace+"/"+m.Pod).Warn("group capture: cannot reach a member's freeze point")
				}
			}(m)
		}
		wg.Wait()
	}
	for {
		all := true
		for _, m := range members {
			// Read the end before the point: a member without a post-dump
			// hook reports it and returns at once.
			done := m.Done != nil && m.Done()
			if freezeReached(ctx, client, m.Base, session, phase, m.Namespace, m.Pod) {
				continue
			}
			if done {
				log.WithFields(logrus.Fields{"pod": m.Namespace + "/" + m.Pod, "phase": phaseName(phase)}).Warn("group capture: a member ended before the freeze point; aborting the others")
				release(false)
				return false
			}
			all = false
		}
		if all {
			release(true)
			log.WithFields(logrus.Fields{"members": len(members), "phase": phaseName(phase)}).Info("group capture: every member at the freeze point; released together")
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			log.Warn("group capture: not every member reached the freeze point; aborting")
			release(false)
			return false
		}
		time.Sleep(poll)
	}
}

func phaseName(phase string) string {
	if phase == "" {
		return "before the dump"
	}
	return strings.TrimSpace(phase) + "-dump"
}

func freezeReached(ctx context.Context, client *http.Client, base, session, phase, ns, pod string) bool {
	u := fmt.Sprintf("%s/v1/group-freeze/%s/%s/%s?phase=%s", base, url.PathEscape(session), url.PathEscape(ns), url.PathEscape(pod), url.QueryEscape(phase))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out map[string]bool
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&out) == nil && out["reached"]
}
