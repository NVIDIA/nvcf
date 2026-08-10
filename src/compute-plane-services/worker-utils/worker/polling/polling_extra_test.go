/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package polling

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
)

func newConn(t *testing.T) *nats.Conn {
	t.Helper()
	url, err := startEmbeddedNats(t)
	require.NoError(t, err)

	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// TestHandlePollingRequestsDeliversWork publishes valid polling messages and
// verifies the callback fires once per message and each message is acked.
func TestHandlePollingRequestsDeliversWork(t *testing.T) {
	nc := newConn(t)
	const reqID = "req-deliver"

	got := make(chan *pb.WorkerInvokeFunctionRequest, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh, err := HandlePollingRequests(ctx, nc, reqID, time.Second, func(work *pb.WorkerInvokeFunctionRequest) {
		got <- work
	})
	require.NoError(t, err)
	require.NotNil(t, errCh)

	// Subscriber must be active before we publish. Flush establishes that the
	// SubscribeSync call has reached the server.
	require.NoError(t, nc.Flush())

	for _, id := range []string{"a", "b"} {
		payload, err := proto.Marshal(&pb.WorkerInvokeFunctionRequest{RequestId: id})
		require.NoError(t, err)
		// Request-reply: the handler acks via msg.Respond(nil). A successful
		// Request confirms the message was delivered and acked.
		_, err = nc.Request("rq_polling."+reqID, payload, 5*time.Second)
		require.NoError(t, err, "expected ack for message %s", id)
	}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case work := <-got:
			seen[work.GetRequestId()] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for callback %d", i)
		}
	}
	require.True(t, seen["a"])
	require.True(t, seen["b"])

	// Cancelling the context drives the loop to exit and unsubscribe. The
	// backoff retry observes the cancelled context and surfaces
	// context.Canceled; the important guarantee is that the goroutine returns.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			require.ErrorIs(t, err, context.Canceled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("polling loop did not exit after context cancel")
	}
}

// TestHandlePollingRequestsMalformedMessage verifies a non-proto payload is
// skipped (continue) and does not stop the loop; a subsequent valid message
// still reaches the callback.
func TestHandlePollingRequestsMalformedMessage(t *testing.T) {
	nc := newConn(t)
	const reqID = "req-malformed"

	got := make(chan *pb.WorkerInvokeFunctionRequest, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := HandlePollingRequests(ctx, nc, reqID, time.Second, func(work *pb.WorkerInvokeFunctionRequest) {
		got <- work
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	// Garbage bytes that are not a valid protobuf message. proto.Unmarshal of
	// arbitrary bytes is not guaranteed to fail, so use bytes that decode as an
	// invalid wire format (a lone tag with no value).
	require.NoError(t, nc.Publish("rq_polling."+reqID, []byte{0x08}))
	require.NoError(t, nc.Flush())

	valid, err := proto.Marshal(&pb.WorkerInvokeFunctionRequest{RequestId: "ok"})
	require.NoError(t, err)
	_, err = nc.Request("rq_polling."+reqID, valid, 5*time.Second)
	require.NoError(t, err)

	select {
	case work := <-got:
		require.Equal(t, "ok", work.GetRequestId())
	case <-time.After(5 * time.Second):
		t.Fatal("valid message after malformed one was not delivered")
	}
}

// TestHandlePollingRequestsSubscribeError exercises the early-return error path
// when the connection is already closed.
func TestHandlePollingRequestsSubscribeError(t *testing.T) {
	nc := newConn(t)
	nc.Close()

	errCh, err := HandlePollingRequests(context.Background(), nc, "req-closed", time.Second, func(*pb.WorkerInvokeFunctionRequest) {})
	require.Error(t, err)
	require.Nil(t, errCh)
}

// TestHandlePollingRequestsContextAlreadyCancelled verifies the loop exits
// immediately when the context is already done, returning nil.
func TestHandlePollingRequestsContextAlreadyCancelled(t *testing.T) {
	nc := newConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errCh, err := HandlePollingRequests(ctx, nc, "req-precancelled", time.Second, func(*pb.WorkerInvokeFunctionRequest) {})
	require.NoError(t, err)
	select {
	case err := <-errCh:
		// Either the top-of-loop ctx.Err() check returns nil, or the backoff
		// retry surfaces context.Canceled; both are acceptable clean exits.
		if err != nil {
			require.ErrorIs(t, err, context.Canceled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not exit for pre-cancelled context")
	}
}
