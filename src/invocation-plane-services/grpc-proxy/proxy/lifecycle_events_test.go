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
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-grpc-proxy/proxy/eventledger"
	"nvcf-grpc-proxy/proxy/invocation"
	"nvcf-grpc-proxy/proxy/worker"
)

const testWorkerToken = "worker-token"

// newRecordingLedger returns a publisher wired to a fake ledger. The events
// it received are complete once the publisher is closed.
func newRecordingLedger(t *testing.T) (*eventledger.Publisher, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var received []map[string]any
	ledger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&event))
		mu.Lock()
		received = append(received, event)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ledger.Close)

	secrets := filepath.Join(t.TempDir(), "secrets.json")
	require.NoError(t, os.WriteFile(secrets, []byte(`{"eventLedgerToken":"test-token"}`), 0o600))
	publisher, err := eventledger.New(ledger.URL, secrets, "")
	require.NoError(t, err)
	return publisher, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return received
	}
}

// newEventDirector returns a director whose invoker records ncaId on the
// worker token, as the real invoker does after authenticating the client.
func newEventDirector(t *testing.T, publisher *eventledger.Publisher) *StreamDirector {
	t.Helper()
	director := NewStreamDirector(FunctionInvokerFunc(func(_ context.Context, _ net.Conn, _, _, _ string, requestId *uuid.UUID, onWorkerAuth func(string, uuid.UUID, string, string, string)) (invocation.Result, context.CancelFunc, error) {
		result := invocation.Result{RequestId: uuid.New(), WorkerAuthorizationToken: testWorkerToken}
		if requestId != nil {
			result.RequestId = *requestId
		}
		onWorkerAuth(result.WorkerAuthorizationToken, result.RequestId, testFunctionId, testFunctionVersionId, "nca-1")
		return result, nil, nil
	}))
	director.eventLedger = publisher
	t.Cleanup(func() { _ = director.Close() })
	return director
}

// setUpSession runs the client side of session setup and returns the slot and
// the token record a worker CONNECT would be checked against.
func setUpSession(t *testing.T, director *StreamDirector, requestId *uuid.UUID) (*worker.WorkerConnection, workerAuthInfo) {
	t.Helper()
	client, _ := net.Pipe()
	wc, err := director.getAndInitWorkerConnection(t.Context(), worker.NewConnectionTrackingConn(client), "client-auth", testFunctionId, "", requestId)
	require.NoError(t, err)
	token := director.workerAuth.Get(testWorkerToken)
	require.NotNil(t, token)
	return wc, token.Value()
}

func TestRegisterWorkerPublishesAttachmentEstablished(t *testing.T) {
	existing := uuid.New()
	tests := []struct {
		name      string
		requestId *uuid.UUID
		isRejoin  bool
	}{
		{name: "new session"},
		{name: "rejoin", requestId: &existing, isRejoin: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			publisher, received := newRecordingLedger(t)
			director := newEventDirector(t, publisher)
			wc, info := setUpSession(t, director, tc.requestId)
			// Setup in a test takes under a millisecond, so backdate it to make
			// the reported latency distinguishable from zero.
			info.setupStartedAt = info.setupStartedAt.Add(-1500 * time.Millisecond)

			workerLink, _ := net.Pipe()
			require.NoError(t, director.RegisterWorker(testWorkerToken, info, workerLink))
			publisher.Close()

			events := received()
			require.Len(t, events, 1)
			event := events[0]
			assert.Equal(t, "GrpcProxy.Attachment.Established", event["type"])
			assert.Equal(t, "grpc-proxy", event["source"])
			assert.Equal(t, testFunctionVersionId, event["functionversionid"])
			assert.Equal(t, testFunctionId, event["functionid"])
			assert.Equal(t, wc.RequestId.String(), event["resourceid"])
			assert.Equal(t, "nca-1", event["ncaid"])
			connectedAt, err := time.Parse(time.RFC3339Nano, event["time"].(string))
			require.NoError(t, err)
			assert.Equal(t, map[string]any{
				"schema_version":   float64(1),
				"request_id":       wc.RequestId.String(),
				"attachment_id":    wc.AttachmentId.String(),
				"is_rejoin":        tc.isRejoin,
				"setup_latency_ms": float64(connectedAt.Sub(info.setupStartedAt).Milliseconds()),
			}, event["data"])
		})
	}
}

func TestRegisterWorkerPublishesOnceWhenWorkersRace(t *testing.T) {
	publisher, received := newRecordingLedger(t)
	director := newEventDirector(t, publisher)
	_, info := setUpSession(t, director, nil)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerLink, _ := net.Pipe()
			errs[i] = director.RegisterWorker(testWorkerToken, info, workerLink)
		}()
	}
	wg.Wait()
	publisher.Close()

	assert.ElementsMatch(t, []bool{true, false}, []bool{errs[0] == nil, errs[1] == nil})
	assert.Len(t, received(), 1)
}
