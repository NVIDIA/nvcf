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

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

// freezeHandler serves a member's freeze point to its group's driver:
// GET reports whether the member waits there, POST ?go=true|false releases
// or aborts it.
func (a *Agent) freezeHandler(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
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

// driveGroupFreeze releases every member from the freeze point once all
// wait there, or aborts them all when one ends before it, or none of
// that happens within groupFreezeWait.
func driveGroupFreeze(ctx context.Context, client *http.Client, session string, members []freezeMember, poll time.Duration, log *logrus.Entry) bool {
	deadline := time.Now().Add(groupFreezeWait)
	release := func(ok bool) {
		var wg sync.WaitGroup
		for _, m := range members {
			wg.Add(1)
			go func(m freezeMember) {
				defer wg.Done()
				u := fmt.Sprintf("%s/v1/group-freeze/%s/%s/%s?go=%t", m.Base, url.PathEscape(session), url.PathEscape(m.Namespace), url.PathEscape(m.Pod), ok)
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
			if m.Done != nil && m.Done() {
				log.WithField("pod", m.Namespace+"/"+m.Pod).Warn("group capture: a member ended before the freeze point; aborting the others")
				release(false)
				return false
			}
			if !freezeReached(ctx, client, m.Base, session, m.Namespace, m.Pod) {
				all = false
			}
		}
		if all {
			release(true)
			log.WithField("members", len(members)).Info("group capture: every member at the freeze point; released together")
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

func freezeReached(ctx context.Context, client *http.Client, base, session, ns, pod string) bool {
	u := fmt.Sprintf("%s/v1/group-freeze/%s/%s/%s", base, url.PathEscape(session), url.PathEscape(ns), url.PathEscape(pod))
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
