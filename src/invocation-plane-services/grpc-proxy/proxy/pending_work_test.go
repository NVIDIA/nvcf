/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package proxy

import (
	"context"
	"errors"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"nvcf-grpc-proxy/proxy/worker"
	"time"

	"net"
	"nvcf-grpc-proxy/proxy/metrics"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-grpc-proxy/proxy/invocation"
)

// recordingPurger stands in for the function invoker, capturing which requests
// shutdown asked to drop.
type recordingPurger struct {
	mu     sync.Mutex
	purged map[uuid.UUID]string
	err    error
}

func newRecordingPurger() *recordingPurger {
	return &recordingPurger{purged: map[uuid.UUID]string{}}
}

func (p *recordingPurger) InvokeStatefulFunction(_ context.Context, _ net.Conn, _, _, _ string, _ *uuid.UUID, _ func(string, uuid.UUID, string, string)) (invocation.Result, context.CancelFunc, error) {
	return invocation.Result{}, nil, errors.New("not used")
}

func (p *recordingPurger) PurgePendingWork(_ context.Context, requestId uuid.UUID, functionVersionId string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.purged[requestId] = functionVersionId
	return nil
}

func (p *recordingPurger) purgedRequests() map[uuid.UUID]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[uuid.UUID]string{}
	for k, v := range p.purged {
		out[k] = v
	}
	return out
}

// A request whose worker never came back to CONNECT can never authenticate once
// this pod is gone, because the token only ever existed here. Shutdown has to
// take it out of the work queue, otherwise it is still pulled, still occupies a
// worker slot, and still fails.
func TestClosePurgesWorkForSessionsAwaitingConnect(t *testing.T) {
	purger := newRecordingPurger()
	director := NewStreamDirector(purger)

	awaiting := uuid.New()
	director.pendingWork.Set(awaiting, pendingWorkInfo{functionVersionId: "version-1"}, ttlcache.DefaultTTL)

	require.NoError(t, director.Close())

	purged := purger.purgedRequests()
	require.Len(t, purged, 1)
	assert.Equal(t, "version-1", purged[awaiting])
}

// A session with a worker already attached is not queued any more and can
// reattach through another pod. Purging it would sever a session that was going
// to survive the restart, so shutdown must leave it alone.
func TestClosePurgesNothingOnceTheWorkerHasConnected(t *testing.T) {
	purger := newRecordingPurger()
	director := NewStreamDirector(purger)

	connected := uuid.New()
	director.pendingWork.Set(connected, pendingWorkInfo{functionVersionId: "version-1"}, ttlcache.DefaultTTL)
	// what HijackHandler does once the worker's CONNECT is accepted
	director.pendingWork.Delete(connected)

	require.NoError(t, director.Close())

	assert.Empty(t, purger.purgedRequests())
}

// The purge is an optimisation. If this service has no rights on the work queue
// the calls fail, and shutdown still has to complete.
func TestClosePurgeFailureDoesNotBlockShutdown(t *testing.T) {
	purger := newRecordingPurger()
	purger.err = errors.New("nats: permissions violation for stream purge")
	director := NewStreamDirector(purger)

	director.pendingWork.Set(uuid.New(), pendingWorkInfo{functionVersionId: "version-1"}, ttlcache.DefaultTTL)

	require.NoError(t, director.Close())
}

// An invoker with no route to the work queue simply skips the purge rather than
// failing shutdown.
func TestClosePurgeSkippedWhenInvokerCannotPurge(t *testing.T) {
	director := NewStreamDirector((*mockInvoker)(&invocation.Result{}))
	director.pendingWork.Set(uuid.New(), pendingWorkInfo{functionVersionId: "version-1"}, ttlcache.DefaultTTL)

	require.NoError(t, director.Close())
}

// purgeCount reads one series of the purge counter. The counters are
// process-global, so every assertion below is a delta.
func purgeCount(t *testing.T, result, trigger string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.PendingWorkPurgedTotal.WithLabelValues(result, trigger))
}

// The departure trigger is the one the evidence points at: work whose client
// stopped waiting is still delivered, occupies a worker slot, and is rejected
// for nobody. Purging it must be attributed separately from the shutdown
// trigger so a single deployment can say which one actually did the work.
func TestClientDepartureTriggerPurgesAndIsAttributed(t *testing.T) {
	reqID := uuid.New()
	purger := newRecordingPurger()
	s := &StreamDirector{
		functionInvoker: purger,
		pendingWork:     ttlcache.New[uuid.UUID, pendingWorkInfo](),
		shuttingDown:    &atomic.Bool{},
		departurePurges: make(chan struct{}, departurePurgeConcurrency),
	}
	s.pendingWork.Set(reqID, pendingWorkInfo{functionVersionId: "ver"}, ttlcache.NoTTL)

	before := purgeCount(t, metrics.PurgeSucceeded, metrics.PurgeTriggerClientDeparted)
	shutdownBefore := purgeCount(t, metrics.PurgeSucceeded, metrics.PurgeTriggerShutdown)

	s.purgeDepartedClientWork(reqID, "ver")

	if got := purger.purgedRequests(); len(got) != 1 || got[reqID] != "ver" {
		t.Fatalf("expected the departed client's work to be purged, got %v", got)
	}
	if got := purgeCount(t, metrics.PurgeSucceeded, metrics.PurgeTriggerClientDeparted) - before; got != 1 {
		t.Errorf("client_departed purges moved by %v, want 1", got)
	}
	if got := purgeCount(t, metrics.PurgeSucceeded, metrics.PurgeTriggerShutdown) - shutdownBefore; got != 0 {
		t.Errorf("shutdown trigger moved by %v on a departure purge, want 0", got)
	}
	// Dropped from the cache so a later shutdown cannot purge it again and
	// double count the same work.
	if s.pendingWork.Get(reqID) != nil {
		t.Error("pending work entry survived the departure purge")
	}
}

// A failed purge must leave the pending entry in place. Deleting it would make
// the failure permanently untracked, so the shutdown purge could never retry
// it and the stale queued work this change removes would survive.
func TestFailedDeparturePurgeKeepsTheEntryForShutdown(t *testing.T) {
	reqID := uuid.New()
	purger := newRecordingPurger()
	purger.err = errors.New("jetstream unavailable")
	s := &StreamDirector{
		functionInvoker: purger,
		pendingWork:     ttlcache.New[uuid.UUID, pendingWorkInfo](),
		shuttingDown:    &atomic.Bool{},
		departurePurges: make(chan struct{}, departurePurgeConcurrency),
	}
	s.pendingWork.Set(reqID, pendingWorkInfo{functionVersionId: "ver"}, ttlcache.NoTTL)

	s.purgeDepartedClientWork(reqID, "ver")

	if s.pendingWork.Get(reqID) == nil {
		t.Fatal("a failed purge dropped the entry, so shutdown can never retry it")
	}
}

// The remedy must not amplify the event it exists to fix. Abandonment peaks
// exactly when the work queue is most strained, so departure purges are
// bounded and shed rather than queue.
func TestDeparturePurgeIsBounded(t *testing.T) {
	purger := newRecordingPurger()
	s := &StreamDirector{
		functionInvoker: purger,
		pendingWork:     ttlcache.New[uuid.UUID, pendingWorkInfo](),
		shuttingDown:    &atomic.Bool{},
		departurePurges: make(chan struct{}, 1),
	}
	// Occupy the only slot, as an in-flight purge would.
	s.departurePurges <- struct{}{}

	reqID := uuid.New()
	s.pendingWork.Set(reqID, pendingWorkInfo{functionVersionId: "ver"}, ttlcache.NoTTL)
	before := skipCount(t, metrics.PurgeSkipBudgetExhausted)

	s.purgeDepartedClientWork(reqID, "ver")

	if len(purger.purgedRequests()) != 0 {
		t.Error("purged despite an exhausted budget")
	}
	if got := skipCount(t, metrics.PurgeSkipBudgetExhausted) - before; got != 1 {
		t.Errorf("budget_exhausted skips moved by %v, want 1", got)
	}
	if s.pendingWork.Get(reqID) == nil {
		t.Error("a shed purge dropped the entry, so shutdown can never retry it")
	}
}

// Shutdown purges everything still pending. Starting more work here would
// duplicate that and race it to the same subjects.
func TestDeparturePurgeStopsOnceShuttingDown(t *testing.T) {
	purger := newRecordingPurger()
	sd := &atomic.Bool{}
	sd.Store(true)
	s := &StreamDirector{
		functionInvoker: purger,
		pendingWork:     ttlcache.New[uuid.UUID, pendingWorkInfo](),
		shuttingDown:    sd,
		departurePurges: make(chan struct{}, departurePurgeConcurrency),
	}
	reqID := uuid.New()
	s.pendingWork.Set(reqID, pendingWorkInfo{functionVersionId: "ver"}, ttlcache.NoTTL)
	before := skipCount(t, metrics.PurgeSkipShuttingDown)

	s.purgeDepartedClientWork(reqID, "ver")

	if len(purger.purgedRequests()) != 0 {
		t.Error("purged while shutting down, racing the shutdown purge")
	}
	if got := skipCount(t, metrics.PurgeSkipShuttingDown) - before; got != 1 {
		t.Errorf("shutting_down skips moved by %v, want 1", got)
	}
}

func skipCount(t *testing.T, reason string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.PendingWorkPurgeSkippedTotal.WithLabelValues(reason))
}

// A worker request is shared by every RPC on a connection with the same
// function routing, so one RPC being cancelled says nothing about whether
// anyone is still waiting. Purging on that signal destroys the shared work and
// leaves the surviving RPC unable to ever get a worker.
//
// Requested in review on NVIDIA/nvcf#1031.
func TestCancellingOneRpcDoesNotPurgeWorkAnotherIsWaitingOn(t *testing.T) {
	purger := newRecordingPurger()
	s := &StreamDirector{
		functionInvoker: purger,
		pendingWork:     ttlcache.New[uuid.UUID, pendingWorkInfo](),
		shuttingDown:    &atomic.Bool{},
		departurePurges: make(chan struct{}, departurePurgeConcurrency),
	}
	reqID := uuid.New()
	key := workerConnectionKey{requestId: reqID, functionId: "fn", functionVersionId: "ver"}
	wc := worker.NewWorkerConnection(reqID, "fn", "ver", func() {}, func() {})

	// Mirrors production: the purge is driven by eviction of the shared entry,
	// never by a single request's context.
	evicted := make(chan struct{})
	workers := ttlcache.New[workerConnectionKey, *worker.WorkerConnection]()
	workers.OnEviction(func(_ context.Context, _ ttlcache.EvictionReason, i *ttlcache.Item[workerConnectionKey, *worker.WorkerConnection]) {
		if !i.Value().EverConnected() {
			s.purgeDepartedClientWork(i.Key().requestId, i.Key().functionVersionId)
		}
		close(evicted)
	})
	workers.Set(key, wc, ttlcache.NoTTL)
	s.pendingWork.Set(reqID, pendingWorkInfo{functionVersionId: "ver"}, ttlcache.NoTTL)

	// Two RPCs waiting on the one shared connection, each with its own context.
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()

	firstDone := make(chan bool, 1)
	go func() { _, ok := wc.WaitForConnection(firstCtx); firstDone <- ok }()
	go func() { _, _ = wc.WaitForConnection(secondCtx) }()

	// Cancel only the first. This is the case that used to purge.
	cancelFirst()
	if ok := <-firstDone; ok {
		t.Fatal("precondition: the cancelled RPC should not have connected")
	}

	if got := purger.purgedRequests(); len(got) != 0 {
		t.Fatalf("cancelling one RPC purged work the other is waiting on: %v", got)
	}
	if s.pendingWork.Get(reqID) == nil {
		t.Error("the shared pending entry was dropped while an RPC was still waiting")
	}

	// Once the entry is evicted, which in production means its TTL ran out and
	// the client has been sent a gateway timeout, the work is genuinely
	// abandoned and is purged exactly once.
	workers.Delete(key)
	select {
	case <-evicted:
	case <-time.After(2 * time.Second):
		t.Fatal("eviction callback never ran")
	}
	if got := purger.purgedRequests(); len(got) != 1 || got[reqID] != "ver" {
		t.Errorf("eviction should have purged the abandoned work, got %v", got)
	}
}

// Eviction must not purge work a worker already took: that work has left the
// queue, and the session can still reattach through another pod.
func TestEvictionDoesNotPurgeOnceAWorkerAttached(t *testing.T) {
	wc := worker.NewWorkerConnection(uuid.New(), "fn", "ver", func() {}, func() {})
	if wc.EverConnected() {
		t.Fatal("a fresh worker connection reports as connected")
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if err := wc.SetConnection(server); err != nil {
		t.Fatalf("SetConnection: %v", err)
	}
	if !wc.EverConnected() {
		t.Error("a worker attached but EverConnected reports otherwise, so its work would be purged")
	}
}
