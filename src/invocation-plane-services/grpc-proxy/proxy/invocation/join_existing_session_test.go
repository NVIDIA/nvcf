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
package invocation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"nvcf-grpc-proxy/nvcf/pb"
)

// startEmbeddedNats starts an in-process NATS server on an OS-assigned
// ephemeral port so these tests can run alongside the rest of the suite
// without a fixed-port collision. Torn down via t.Cleanup.
func startEmbeddedNats(t *testing.T) string {
	t.Helper()

	s, err := natsserver.NewServer(&natsserver.Options{
		Host:   "127.0.0.1",
		Port:   -1,
		NoSigs: true,
	})
	require.NoError(t, err)

	s.Start()
	t.Cleanup(func() {
		s.Shutdown()
		s.WaitForShutdown()
	})

	require.True(t, s.ReadyForConnections(10*time.Second), "embedded nats server did not become ready")
	return s.ClientURL()
}

func newTestInvoker(t *testing.T) (*FunctionInvoker, *nats.Conn) {
	t.Helper()

	nc, err := nats.Connect(startEmbeddedNats(t))
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	// no_responders is what lets a rejoin tell a dead session from a live one.
	// If the server or client ever stopped supporting it the rest of these
	// assertions would still pass for the wrong reason, so check it directly.
	require.True(t, nc.HeadersSupported(), "no-responders detection needs header support")

	return &FunctionInvoker{
		nc:           nc,
		region:       "region-1",
		connectPaths: ConnectPaths{HTTP1: "http://10.0.0.1:10086/v1/proxy"},
	}, nc
}

// A session whose worker is gone has nothing subscribed to its reconnect
// subject. That has to surface as ErrSessionNotFound, because that is the only
// error the director turns into a cookie-clearing response, which is what lets
// the client open a fresh session without the function being restarted.
func TestJoinExistingSessionNoWorkerSubscribed(t *testing.T) {
	invoker, _ := newTestInvoker(t)
	requestId := uuid.New()

	start := time.Now()
	err := invoker.joinExistingSession(context.Background(), requestId, &pb.ProxyAuthResponse{FunctionId: "fn-1"}, "worker-token")

	require.Error(t, err)
	require.ErrorIs(t, err, ErrSessionNotFound)
	assert.Contains(t, err.Error(), requestId.String())
	// No responders is answered from interest state, so detection must not
	// cost the full probe deadline even with the confirmation probe.
	assert.Less(t, time.Since(start), 2*reconnectAckTimeout+reconnectNoRespondersRetryDelay,
		"dead session should be detected without waiting out both probe deadlines")
}

// A worker that acknowledges the reconnect is live, so the rejoin succeeds and
// the worker receives the connection config it needs to CONNECT back.
func TestJoinExistingSessionWorkerAcks(t *testing.T) {
	invoker, nc := newTestInvoker(t)
	requestId := uuid.New()

	received := make(chan *pb.WorkerInvokeFunctionRequest, 1)
	sub, err := nc.Subscribe(reconnectSubject(requestId), func(msg *nats.Msg) {
		var work pb.WorkerInvokeFunctionRequest
		if err := proto.Unmarshal(msg.Data, &work); err != nil {
			return
		}
		// mirrors the worker's reconnect listener
		_ = msg.Respond(nil)
		received <- &work
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	start := time.Now()
	err = invoker.joinExistingSession(context.Background(), requestId, &pb.ProxyAuthResponse{FunctionId: "fn-1"}, "worker-token")
	require.NoError(t, err)
	assert.Less(t, time.Since(start), reconnectAckTimeout, "an acknowledged rejoin should not wait on the deadline")

	select {
	case work := <-received:
		assert.Equal(t, requestId.String(), work.RequestId)
		require.Len(t, work.StatefulConfig.ConnectionConfigs, 1)
		assert.Equal(t, "worker-token",
			work.StatefulConfig.ConnectionConfigs[0].GetHttp1Config().ProxyAuthorizationToken)
	case <-time.After(5 * time.Second):
		t.Fatal("worker never received the reconnect message")
	}
}

// A worker built before the acknowledgement is still subscribed, and interest
// alone proves the session is live. The rejoin must succeed rather than be
// mistaken for a dead session, otherwise deploying the proxy ahead of the
// worker would sever every live session.
func TestJoinExistingSessionSubscribedWorkerWithoutAck(t *testing.T) {
	invoker, nc := newTestInvoker(t)
	requestId := uuid.New()

	received := make(chan struct{}, 1)
	sub, err := nc.Subscribe(reconnectSubject(requestId), func(msg *nats.Msg) {
		received <- struct{}{} // deliberately no Respond
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	err = invoker.joinExistingSession(context.Background(), requestId, &pb.ProxyAuthResponse{FunctionId: "fn-1"}, "worker-token")
	require.NoError(t, err, "a subscribed worker that does not ack must be treated as live")

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never received the reconnect message")
	}
}

// A caller that goes away mid-rejoin must not be reported as a dead session:
// that would clear a client cookie on the strength of our own cancellation.
func TestJoinExistingSessionCancelledCallerIsNotASessionLoss(t *testing.T) {
	invoker, nc := newTestInvoker(t)
	requestId := uuid.New()

	sub, err := nc.Subscribe(reconnectSubject(requestId), func(msg *nats.Msg) {})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = invoker.joinExistingSession(ctx, requestId, &pb.ProxyAuthResponse{FunctionId: "fn-1"}, "worker-token")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrSessionNotFound)
	assert.True(t, errors.Is(err, context.Canceled), "expected the caller's cancellation, got %v", err)
}
