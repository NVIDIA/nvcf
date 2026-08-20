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
	"net"
	"sync"
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
