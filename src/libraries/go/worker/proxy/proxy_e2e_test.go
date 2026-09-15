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

package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

// TestProxy_EndToEnd drives HttpProxy.Proxy through a full happy path:
// the keepalive registration publishes to JetStream, the worker connects to a
// mock h3 grpc proxy, requests are forwarded to an inference server, and the
// session shuts down cleanly once the context is canceled and the connection
// is closed. This exercises Proxy and keepaliveReconnectRegistration.
func TestProxy_EndToEnd(t *testing.T) {
	setupLogger()
	allowInsecure(t)

	cluster, err := testutils.NewNatsSuperCluster(t)
	require.NoError(t, err)
	defer cluster.Shutdown()

	nc, err := nats.Connect(cluster.Clusters[0].Servers[0].ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	region := cluster.Clusters[0].Region
	functionId := uuid.New().String()
	functionVersionId := uuid.New().String()

	// Create the stream the keepalive registration publishes to, so the publish
	// succeeds and the registration goroutine runs its full body.
	setupCtx, setupCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer setupCancel()
	streamName := "stateful_session_lookup_" + region
	_, err = js.CreateStream(setupCtx, jetstream.StreamConfig{
		Name:              streamName,
		Subjects:          []string{"stateful_session.lookup." + region + ".>"},
		Discard:           jetstream.DiscardNew,
		MaxAge:            time.Hour,
		MaxMsgsPerSubject: 1,
		Storage:           jetstream.MemoryStorage,
	})
	require.NoError(t, err)

	// Mock grpc proxy (h3) that hands back hijacked server-side conns.
	serverConns, _, _, server := mockGrpcProxy()
	defer server.Close()

	// Inference server that echoes the request body.
	inferenceServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, r.Body)
	}))
	protocols := &http.Protocols{}
	protocols.SetUnencryptedHTTP2(true)
	protocols.SetHTTP1(true)
	inferenceServer.Config.Protocols = protocols
	inferenceServer.Start()
	defer inferenceServer.Close()

	httpProxy, err := NewHttpProxy(nc, js, functionId, functionVersionId,
		func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme = "http"
			request.Out.URL.Host = inferenceServer.Listener.Addr().String()
		}, nil, nil, nil)
	require.NoError(t, err)
	defer httpProxy.Close()

	requestId := uuid.New().String()
	work := &pb.WorkerInvokeFunctionRequest{
		RequestId: requestId,
		NcaId:     "nca-1",
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}

	proxyCtx, proxyCancel := context.WithCancel(t.Context())
	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- httpProxy.Proxy(proxyCtx, work, region)
	}()

	// The grpc proxy receives the worker's CONNECT and hands us the server side
	// of the tunnel. Drive HTTP requests into it.
	serverConn := <-serverConns

	client := http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return serverConn, nil
		},
	}}
	for i := 0; i < 5; i++ {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/echo", strings.NewReader("ping"))
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "ping", string(body))
	}

	// Confirm the keepalive registration landed a message on the lookup stream.
	stream, err := js.Stream(setupCtx, streamName)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		info, infoErr := stream.Info(setupCtx)
		return infoErr == nil && info.State.Msgs >= 1
	}, 5*time.Second, 20*time.Millisecond, "keepalive registration should publish to the lookup stream")

	// A malformed reconnect message must be skipped without crashing the loop.
	require.NoError(t, nc.Publish("stateful_session.reconnect."+requestId, []byte{0xff, 0xff, 0xff}))
	require.NoError(t, nc.Flush())

	// Publish a reconnect message for this request id. The reconnect goroutine
	// inside Proxy should pick it up, establish a fresh tunnel, and serve it,
	// exercising the reconnect branch.
	reconnectBody, err := proto.Marshal(work)
	require.NoError(t, err)
	require.NoError(t, nc.Publish("stateful_session.reconnect."+requestId, reconnectBody))
	require.NoError(t, nc.Flush())

	// A second tunnel should be established for the reconnect.
	select {
	case reconnectConn := <-serverConns:
		_ = reconnectConn.Close()
	case <-time.After(10 * time.Second):
		t.Fatal("reconnect did not establish a new tunnel")
	}

	// Tear down: close the tunnel conn so ServeConn returns, then cancel the
	// proxy context so the reconnect loop exits and Proxy returns.
	_ = serverConn.Close()
	proxyCancel()

	select {
	case <-proxyDone:
		// Proxy returned. Error is acceptable here since we forcibly tore the
		// connection down; we only assert it terminates.
	case <-time.After(15 * time.Second):
		t.Fatal("Proxy did not return after teardown")
	}
}
