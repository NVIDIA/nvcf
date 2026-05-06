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
	"net"
	"nvcf-grpc-proxy/proxy/consts"
	"nvcf-grpc-proxy/proxy/invocation"
	"nvcf-grpc-proxy/proxy/worker"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestWorkerConnectionCacheEvictionLogging verifies that TTL expiration triggers
// detailed logging with inactivity duration and eviction reason
func TestWorkerConnectionCacheEvictionLogging(t *testing.T) {
	// Set up log capture
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)
	zap.ReplaceGlobals(logger)

	// Save original timeout and set a short one for testing
	originalTimeout := consts.Timeout
	consts.Timeout = 200 * time.Millisecond
	t.Cleanup(func() {
		consts.Timeout = originalTimeout
	})

	proxyResponse := &invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "test-auth-token",
	}
	director := NewStreamDirector((*testMockInvoker)(proxyResponse))
	defer director.Close()

	// Create a mock connection
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Register the actual worker connection
	err := director.RegisterWorker(proxyResponse.RequestId, proxyResponse.WorkerAuthorizationToken, testFunctionId, testFunctionVersionId, server)
	if err != nil {
		t.Fatalf("failed to register worker: %v", err)
	}

	// Wait for TTL to expire
	time.Sleep(300 * time.Millisecond)

	// Verify logging occurred
	logEntries := logs.All()

	// Should have: registration, eviction, closure, and inactive logs
	var foundEviction, foundClosure, foundInactive bool
	for _, entry := range logEntries {
		switch entry.Message {
		case "worker connection cache eviction triggered":
			foundEviction = true
			// Verify eviction reason is present
			evictionReason := entry.ContextMap()["eviction_reason"]
			if evictionReason != "ttl_expired" {
				t.Errorf("expected eviction_reason='ttl_expired', got %v", evictionReason)
			}
		case "closing worker connection":
			foundClosure = true
			// Verify required fields are present
			fields := entry.ContextMap()
			if _, ok := fields["reason"]; !ok {
				t.Error("missing 'reason' field in closure log")
			}
			if _, ok := fields["request_id"]; !ok {
				t.Error("missing 'request_id' field in closure log")
			}
		case "connection going inactive, removing from cache":
			foundInactive = true
		}
	}

	if !foundEviction {
		t.Error("expected 'worker connection cache eviction triggered' log entry")
	}
	if !foundClosure {
		t.Error("expected 'closing worker connection' log entry")
	}
	if !foundInactive {
		t.Error("expected 'connection going inactive' log entry")
	}
}

// TestClientConnectionClosureLogging verifies that client connection closure
// triggers detailed logging with function context
func TestClientConnectionClosureLogging(t *testing.T) {
	// Set up log capture
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)
	zap.ReplaceGlobals(logger)

	// Create a mock connection
	mockConn := &testMockConn{}
	conn := worker.NewConnectionTrackingConn(mockConn)

	requestId := uuid.New()
	workerConn := worker.NewWorkerConnection(requestId, "test-function-id", "v1.0", func() {}, func() {})

	// Initialize a worker connection
	_, err := conn.InitWorkerConn("test-function-id", "v1.0", func() (*worker.WorkerConnection, error) {
		return workerConn, nil
	})
	if err != nil {
		t.Fatalf("failed to init worker connection: %v", err)
	}

	// Close the tracking connection - this should trigger detailed logging
	err = conn.Close()
	if err != nil {
		t.Errorf("expected no error closing connection, got %v", err)
	}

	// Verify logging occurred
	logEntries := logs.All()

	var foundClientClose, foundWorkerShutdown bool
	for _, entry := range logEntries {
		fields := entry.ContextMap()
		switch entry.Message {
		case "closing client connection":
			foundClientClose = true
			// Verify connection count is logged
			if _, ok := fields["active_function_connections"]; !ok {
				t.Error("missing 'active_function_connections' field in client close log")
			}
		case "triggering worker connection shutdown from client close":
			foundWorkerShutdown = true
			// Verify function context is present
			if fields["function_id"] != "test-function-id" {
				t.Errorf("expected function_id='test-function-id', got %v", fields["function_id"])
			}
			if fields["function_version_id"] != "v1.0" {
				t.Errorf("expected function_version_id='v1.0', got %v", fields["function_version_id"])
			}
			if _, ok := fields["request_id"]; !ok {
				t.Error("missing 'request_id' field in worker shutdown log")
			}
		}
	}

	if !foundClientClose {
		t.Error("expected 'closing client connection' log entry")
	}
	if !foundWorkerShutdown {
		t.Error("expected 'triggering worker connection shutdown' log entry")
	}
}

// testMockConn is a minimal net.Conn implementation for testing
type testMockConn struct {
	closed bool
}

func (m *testMockConn) Read(b []byte) (n int, err error)   { return 0, nil }
func (m *testMockConn) Write(b []byte) (n int, err error)  { return len(b), nil }
func (m *testMockConn) Close() error                       { m.closed = true; return nil }
func (m *testMockConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (m *testMockConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (m *testMockConn) SetDeadline(t time.Time) error      { return nil }
func (m *testMockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *testMockConn) SetWriteDeadline(t time.Time) error { return nil }

type testMockInvoker invocation.Result

func (m *testMockInvoker) InvokeStatefulFunction(_ context.Context, _ net.Conn, _, _, _ string, _ *uuid.UUID, onWorkerAuthSet func(workerAuthToken string, requestId uuid.UUID, apiFunctionId string, apiFunctionVersionId string)) (invocation.Result, context.CancelFunc, error) {
	result := invocation.Result(*m)
	onWorkerAuthSet(result.WorkerAuthorizationToken, result.RequestId, testFunctionId, testFunctionVersionId)
	return result, nil, nil
}
