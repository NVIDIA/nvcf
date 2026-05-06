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
package worker

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// mockConn implements net.Conn for testing
type mockConn struct {
	closed bool
}

func (m *mockConn) Read(b []byte) (n int, err error)   { return 0, nil }
func (m *mockConn) Write(b []byte) (n int, err error)  { return len(b), nil }
func (m *mockConn) Close() error                       { m.closed = true; return nil }
func (m *mockConn) LocalAddr() net.Addr                { return nil }
func (m *mockConn) RemoteAddr() net.Addr               { return nil }
func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }

func TestWorkerConnectionWithInit_GetWorkerConnection(t *testing.T) {
	t.Run("successful_init", func(t *testing.T) {
		requestId := uuid.New()
		expectedConn := NewWorkerConnection(requestId, "", "", func() {}, func() {})

		wcInit := &workerConnectionWithInit{}

		initFunc := func() (*WorkerConnection, error) {
			return expectedConn, nil
		}

		// First call should initialize
		conn, err := wcInit.getWorkerConnection(initFunc)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if conn != expectedConn {
			t.Errorf("expected connection %v, got %v", expectedConn, conn)
		}

		// Second call should return the same connection without re-initializing
		conn2, err2 := wcInit.getWorkerConnection(func() (*WorkerConnection, error) {
			t.Error("init function should not be called on second call")
			return nil, errors.New("should not be called")
		})
		if err2 != nil {
			t.Errorf("expected no error on second call, got %v", err2)
		}
		if conn2 != expectedConn {
			t.Errorf("expected same connection on second call, got %v", conn2)
		}
	})

	t.Run("init_error", func(t *testing.T) {
		wcInit := &workerConnectionWithInit{}
		expectedErr := errors.New("initialization failed")

		initFunc := func() (*WorkerConnection, error) {
			return nil, expectedErr
		}

		// First call should return the error
		conn, err := wcInit.getWorkerConnection(initFunc)
		if err != expectedErr {
			t.Errorf("expected error %v, got %v", expectedErr, err)
		}
		if conn != nil {
			t.Errorf("expected nil connection on error, got %v", conn)
		}

		// Second call should return the same error without re-initializing
		conn2, err2 := wcInit.getWorkerConnection(func() (*WorkerConnection, error) {
			t.Error("init function should not be called on second call")
			return nil, errors.New("should not be called")
		})
		if err2 != expectedErr {
			t.Errorf("expected same error on second call, got %v", err2)
		}
		if conn2 != nil {
			t.Errorf("expected nil connection on second call, got %v", conn2)
		}
	})

	t.Run("concurrent_init", func(t *testing.T) {
		wcInit := &workerConnectionWithInit{}
		requestId := uuid.New()
		expectedConn := NewWorkerConnection(requestId, "", "", func() {}, func() {})

		initCallCount := 0
		var mu sync.Mutex

		initFunc := func() (*WorkerConnection, error) {
			mu.Lock()
			defer mu.Unlock()
			initCallCount++
			// Simulate some work
			time.Sleep(10 * time.Millisecond)
			return expectedConn, nil
		}

		// Launch multiple goroutines to call getWorkerConnection concurrently
		const numGoroutines = 10
		results := make([]struct {
			conn *WorkerConnection
			err  error
		}, numGoroutines)

		var wg sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				conn, err := wcInit.getWorkerConnection(initFunc)
				results[index] = struct {
					conn *WorkerConnection
					err  error
				}{conn, err}
			}(i)
		}

		wg.Wait()

		// Check that init was called only once
		mu.Lock()
		if initCallCount != 1 {
			t.Errorf("expected init to be called once, but was called %d times", initCallCount)
		}
		mu.Unlock()

		// Check that all results are the same
		for i, result := range results {
			if result.err != nil {
				t.Errorf("goroutine %d got error: %v", i, result.err)
			}
			if result.conn != expectedConn {
				t.Errorf("goroutine %d got different connection", i)
			}
		}
	})
}

func TestConnectionTrackingConn_InitWorkerConn(t *testing.T) {
	t.Run("successful_init_new_function", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)
		defer conn.Close()

		requestId := uuid.New()
		expectedWorkerConn := NewWorkerConnection(requestId, "", "", func() {}, func() {})

		initFunc := func() (*WorkerConnection, error) {
			return expectedWorkerConn, nil
		}

		workerConn, err := conn.InitWorkerConn("func1", "v1", initFunc)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if workerConn != expectedWorkerConn {
			t.Errorf("expected worker connection %v, got %v", expectedWorkerConn, workerConn)
		}
	})

	t.Run("init_error_propagation", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)
		defer conn.Close()

		expectedErr := errors.New("NVCF API call failed")

		initFunc := func() (*WorkerConnection, error) {
			return nil, expectedErr
		}

		workerConn, err := conn.InitWorkerConn("func1", "v1", initFunc)
		if err != expectedErr {
			t.Errorf("expected error %v, got %v", expectedErr, err)
		}
		if workerConn != nil {
			t.Errorf("expected nil worker connection on error, got %v", workerConn)
		}
	})

	t.Run("error_resets_mapping_for_retry", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)
		defer conn.Close()

		firstErr := errors.New("first attempt failed")
		requestId := uuid.New()
		successConn := NewWorkerConnection(requestId, "", "", func() {}, func() {})

		callCount := 0
		initFunc := func() (*WorkerConnection, error) {
			callCount++
			if callCount == 1 {
				return nil, firstErr
			}
			return successConn, nil
		}

		// First call should fail
		workerConn1, err1 := conn.InitWorkerConn("func1", "v1", initFunc)
		if err1 != firstErr {
			t.Errorf("expected first error %v, got %v", firstErr, err1)
		}
		if workerConn1 != nil {
			t.Errorf("expected nil worker connection on first error, got %v", workerConn1)
		}

		// Second call should succeed (mapping was reset)
		workerConn2, err2 := conn.InitWorkerConn("func1", "v1", initFunc)
		if err2 != nil {
			t.Errorf("expected no error on second call, got %v", err2)
		}
		if workerConn2 != successConn {
			t.Errorf("expected success connection on second call, got %v", workerConn2)
		}

		if callCount != 2 {
			t.Errorf("expected init func to be called twice, was called %d times", callCount)
		}
	})

	t.Run("same_function_returns_cached_connection", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)
		defer conn.Close()

		requestId := uuid.New()
		expectedWorkerConn := NewWorkerConnection(requestId, "", "", func() {}, func() {})

		callCount := 0
		initFunc := func() (*WorkerConnection, error) {
			callCount++
			return expectedWorkerConn, nil
		}

		// First call
		workerConn1, err1 := conn.InitWorkerConn("func1", "v1", initFunc)
		if err1 != nil {
			t.Errorf("expected no error on first call, got %v", err1)
		}

		// Second call with same function should return cached connection
		workerConn2, err2 := conn.InitWorkerConn("func1", "v1", func() (*WorkerConnection, error) {
			t.Error("init function should not be called for cached connection")
			return nil, errors.New("should not be called")
		})
		if err2 != nil {
			t.Errorf("expected no error on second call, got %v", err2)
		}

		if workerConn1 != workerConn2 {
			t.Errorf("expected same worker connection, got different connections")
		}

		if callCount != 1 {
			t.Errorf("expected init func to be called once, was called %d times", callCount)
		}
	})

	t.Run("different_functions_get_different_connections", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)
		defer conn.Close()

		requestId1 := uuid.New()
		requestId2 := uuid.New()
		workerConn1 := NewWorkerConnection(requestId1, "", "", func() {}, func() {})
		workerConn2 := NewWorkerConnection(requestId2, "", "", func() {}, func() {})

		callCount := 0
		initFunc := func() (*WorkerConnection, error) {
			callCount++
			if callCount == 1 {
				return workerConn1, nil
			}
			return workerConn2, nil
		}

		// First function
		result1, err1 := conn.InitWorkerConn("func1", "v1", initFunc)
		if err1 != nil {
			t.Errorf("expected no error for func1, got %v", err1)
		}
		if result1 != workerConn1 {
			t.Errorf("expected workerConn1 for func1, got %v", result1)
		}

		// Second function
		result2, err2 := conn.InitWorkerConn("func2", "v1", initFunc)
		if err2 != nil {
			t.Errorf("expected no error for func2, got %v", err2)
		}
		if result2 != workerConn2 {
			t.Errorf("expected workerConn2 for func2, got %v", result2)
		}

		if callCount != 2 {
			t.Errorf("expected init func to be called twice, was called %d times", callCount)
		}
	})

	t.Run("closed_worker_connection_retry", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)
		defer conn.Close()

		requestId := uuid.New()
		// Create a worker connection and close it immediately to simulate a closed worker
		closedWorkerConn := NewWorkerConnection(requestId, "", "", func() {}, func() {})
		closedWorkerConn.Close()

		newWorkerConn := NewWorkerConnection(requestId, "", "", func() {}, func() {})

		callCount := 0
		initFunc := func() (*WorkerConnection, error) {
			callCount++
			if callCount == 1 {
				return closedWorkerConn, nil
			}
			return newWorkerConn, nil
		}

		// First call should get the closed connection, detect it's closed, and retry
		result, err := conn.InitWorkerConn("func1", "v1", initFunc)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if result != newWorkerConn {
			t.Errorf("expected new worker connection after retry, got %v", result)
		}

		// Should have called init twice - once for closed connection, once for retry
		if callCount != 2 {
			t.Errorf("expected init func to be called twice (original + retry), was called %d times", callCount)
		}
	})

	t.Run("closed_worker_connection_retry_with_error", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)
		defer conn.Close()

		requestId := uuid.New()
		// Create a worker connection and close it immediately
		closedWorkerConn := NewWorkerConnection(requestId, "", "", func() {}, func() {})
		closedWorkerConn.Close()

		retryErr := errors.New("retry failed")

		callCount := 0
		initFunc := func() (*WorkerConnection, error) {
			callCount++
			if callCount == 1 {
				return closedWorkerConn, nil
			}
			return nil, retryErr
		}

		// Should detect closed connection and retry, but retry fails
		result, err := conn.InitWorkerConn("func1", "v1", initFunc)
		if err != retryErr {
			t.Errorf("expected retry error %v, got %v", retryErr, err)
		}
		if result != nil {
			t.Errorf("expected nil result when retry fails, got %v", result)
		}

		if callCount != 2 {
			t.Errorf("expected init func to be called twice, was called %d times", callCount)
		}
	})

	t.Run("multiple_requests_after_worker_timeout_closes_connection", func(t *testing.T) {
		// This test reproduces the exact bug scenario:
		// 1. Client sends request 1, gets a worker connection
		// 2. After idle timeout (35s), worker connection is closed by director cache eviction
		// 3. Client sends request 2 with same function ID - reconnection fails (e.g., "no existing session found")
		// 4. Client sends request 3 with same function ID - should retry, not return stale error
		// Without the fix, stale error entries aren't cleaned up properly

		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)
		defer conn.Close()

		requestId1 := uuid.New()
		requestId3 := uuid.New()

		callCount := 0
		var firstWorkerConn *WorkerConnection
		reconnectErr := errors.New("no existing session found")

		// Request 1: Initial connection succeeds
		result1, err1 := conn.InitWorkerConn("func1", "v1", func() (*WorkerConnection, error) {
			callCount++
			if callCount == 1 {
				firstWorkerConn = NewWorkerConnection(requestId1, "", "", func() {}, func() {})
				return firstWorkerConn, nil
			}
			t.Errorf("init should not be called on request 1 again, call count: %d", callCount)
			return nil, errors.New("should not be called")
		})
		if err1 != nil {
			t.Fatalf("request 1 failed: %v", err1)
		}
		if result1 != firstWorkerConn {
			t.Errorf("request 1 should return first worker connection")
		}

		// Simulate worker connection timeout - the director cache evicts it and closes it
		// This is what happens after 30-35 seconds of idle time
		firstWorkerConn.Close()

		// Request 2: Client tries to reconnect with request_id, but session not found
		// This should detect closed worker and try to reconnect, but reconnection fails
		result2, err2 := conn.InitWorkerConn("func1", "v1", func() (*WorkerConnection, error) {
			callCount++
			if callCount == 2 {
				// This is the retry after detecting the first connection was closed
				// But reconnection fails (session expired or not found)
				return nil, reconnectErr
			}
			t.Errorf("unexpected call count in request 2: %d", callCount)
			return nil, fmt.Errorf("unexpected call count: %d", callCount)
		})
		if err2 != reconnectErr {
			t.Errorf("request 2 should return reconnect error, got: %v", err2)
		}
		if result2 != nil {
			t.Error("request 2 should return nil when reconnection fails")
		}

		// Request 3: Client tries again (maybe without request_id, starting fresh)
		// BUG: Without the fix, this will return the cached error from request 2 instead of calling init
		result3, err3 := conn.InitWorkerConn("func1", "v1", func() (*WorkerConnection, error) {
			callCount++
			if callCount == 3 {
				// This should be called to start a fresh session
				return NewWorkerConnection(requestId3, "", "", func() {}, func() {}), nil
			}
			t.Errorf("unexpected call count in request 3: %d", callCount)
			return nil, fmt.Errorf("unexpected call count: %d", callCount)
		})
		if err3 != nil {
			t.Fatalf("request 3 should succeed, got error: %v", err3)
		}
		if result3 == nil {
			t.Fatal("request 3 should return a valid connection")
		}
		if result3.RequestId != requestId3 {
			t.Errorf("request 3 should return new worker connection with requestId3")
		}

		if callCount != 3 {
			t.Errorf("expected init to be called exactly 3 times (request 1 + request 2 retry + request 3), was called %d times", callCount)
		}
	})
}

func TestConnectionTrackingConn_Close(t *testing.T) {
	t.Run("close_notifies_worker_connections", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)

		requestId := uuid.New()
		onInactiveCalled := false
		workerConn := NewWorkerConnection(requestId, "", "", func() {}, func() {
			onInactiveCalled = true
		})

		// Initialize a worker connection
		initFunc := func() (*WorkerConnection, error) {
			return workerConn, nil
		}

		_, err := conn.InitWorkerConn("func1", "v1", initFunc)
		if err != nil {
			t.Fatalf("failed to init worker connection: %v", err)
		}

		// Close the tracking connection
		err = conn.Close()
		if err != nil {
			t.Errorf("expected no error closing connection, got %v", err)
		}

		// Verify that the worker connection's onInactive was called
		if !onInactiveCalled {
			t.Error("expected onInactive to be called on worker connection")
		}

		// Verify that the underlying connection was closed
		if !mockConn.closed {
			t.Error("expected underlying connection to be closed")
		}
	})

	t.Run("close_handles_error_in_worker_init", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)

		// Initialize with an error (simulating failed worker connection)
		initFunc := func() (*WorkerConnection, error) {
			return nil, errors.New("worker init failed")
		}

		_, err := conn.InitWorkerConn("func1", "v1", initFunc)
		if err == nil {
			t.Fatal("expected error from worker init")
		}

		// Closing should not panic even though worker connection init failed
		err = conn.Close()
		if err != nil {
			t.Errorf("expected no error closing connection, got %v", err)
		}

		if !mockConn.closed {
			t.Error("expected underlying connection to be closed")
		}
	})

	t.Run("close_is_idempotent", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)

		// Close multiple times
		err1 := conn.Close()
		err2 := conn.Close()
		err3 := conn.Close()

		if err1 != nil {
			t.Errorf("expected no error on first close, got %v", err1)
		}
		if err2 != nil {
			t.Errorf("expected no error on second close, got %v", err2)
		}
		if err3 != nil {
			t.Errorf("expected no error on third close, got %v", err3)
		}

		if !mockConn.closed {
			t.Error("expected underlying connection to be closed")
		}
	})
}

func TestConnectionTrackingConn_ConcurrentAccess(t *testing.T) {
	t.Run("concurrent_init_different_functions", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)
		defer conn.Close()

		const numGoroutines = 10
		results := make([]struct {
			conn *WorkerConnection
			err  error
		}, numGoroutines)

		var wg sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				functionId := fmt.Sprintf("func%d", index)
				requestId := uuid.New()
				expectedConn := NewWorkerConnection(requestId, "", "", func() {}, func() {})

				initFunc := func() (*WorkerConnection, error) {
					return expectedConn, nil
				}

				conn, err := conn.InitWorkerConn(functionId, "v1", initFunc)
				results[index] = struct {
					conn *WorkerConnection
					err  error
				}{conn, err}
			}(i)
		}

		wg.Wait()

		// All should succeed with different connections
		for i, result := range results {
			if result.err != nil {
				t.Errorf("goroutine %d got error: %v", i, result.err)
			}
			if result.conn == nil {
				t.Errorf("goroutine %d got nil connection", i)
			}
		}

		// All connections should be different
		for i := 0; i < numGoroutines; i++ {
			for j := i + 1; j < numGoroutines; j++ {
				if results[i].conn == results[j].conn {
					t.Errorf("goroutines %d and %d got the same connection", i, j)
				}
			}
		}
	})

	t.Run("concurrent_init_same_function", func(t *testing.T) {
		mockConn := &mockConn{}
		conn := NewConnectionTrackingConn(mockConn)
		defer conn.Close()

		requestId := uuid.New()
		expectedConn := NewWorkerConnection(requestId, "", "", func() {}, func() {})

		initCallCount := 0
		var initMu sync.Mutex

		initFunc := func() (*WorkerConnection, error) {
			initMu.Lock()
			defer initMu.Unlock()
			initCallCount++
			time.Sleep(10 * time.Millisecond) // Simulate work
			return expectedConn, nil
		}

		const numGoroutines = 10
		results := make([]struct {
			conn *WorkerConnection
			err  error
		}, numGoroutines)

		var wg sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				conn, err := conn.InitWorkerConn("func1", "v1", initFunc)
				results[index] = struct {
					conn *WorkerConnection
					err  error
				}{conn, err}
			}(i)
		}

		wg.Wait()

		// Check that init was called only once
		initMu.Lock()
		if initCallCount != 1 {
			t.Errorf("expected init to be called once, but was called %d times", initCallCount)
		}
		initMu.Unlock()

		// All should succeed with the same connection
		for i, result := range results {
			if result.err != nil {
				t.Errorf("goroutine %d got error: %v", i, result.err)
			}
			if result.conn != expectedConn {
				t.Errorf("goroutine %d got different connection", i)
			}
		}
	})
}

func TestFunctionRoutingKey(t *testing.T) {
	t.Run("different_function_ids_create_different_keys", func(t *testing.T) {
		key1 := functionRoutingKey{functionId: "func1", functionVersionId: "v1"}
		key2 := functionRoutingKey{functionId: "func2", functionVersionId: "v1"}

		if key1 == key2 {
			t.Error("expected different keys for different function IDs")
		}
	})

	t.Run("different_version_ids_create_different_keys", func(t *testing.T) {
		key1 := functionRoutingKey{functionId: "func1", functionVersionId: "v1"}
		key2 := functionRoutingKey{functionId: "func1", functionVersionId: "v2"}

		if key1 == key2 {
			t.Error("expected different keys for different version IDs")
		}
	})

	t.Run("empty_version_id_is_valid", func(t *testing.T) {
		key1 := functionRoutingKey{functionId: "func1", functionVersionId: ""}
		key2 := functionRoutingKey{functionId: "func1", functionVersionId: "v1"}

		if key1 == key2 {
			t.Error("expected different keys for empty vs non-empty version ID")
		}
	})

	t.Run("identical_keys_are_equal", func(t *testing.T) {
		key1 := functionRoutingKey{functionId: "func1", functionVersionId: "v1"}
		key2 := functionRoutingKey{functionId: "func1", functionVersionId: "v1"}

		if key1 != key2 {
			t.Error("expected identical keys to be equal")
		}
	})
}
