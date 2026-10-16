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
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	"nvcf-grpc-proxy/proxy/consts"
	"nvcf-grpc-proxy/proxy/invocation"
)

func awaitWorkersAsync(director *StreamDirector, ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		director.AwaitWorkers(ctx)
		close(done)
	}()
	return done
}

// Shutdown must not close the worker listeners while a request is still
// waiting on its worker: the work is already queued with a token only this pod
// accepts, so closing first fails a request whose worker is about to arrive.
func TestAwaitWorkersWaitsForWaitingRequests(t *testing.T) {
	director := NewStreamDirector((*mockInvoker)(&invocation.Result{}))
	t.Cleanup(func() { _ = director.Close() })

	director.awaitingWorker.Add(1)
	done := awaitWorkersAsync(director, context.Background())

	select {
	case <-done:
		t.Fatal("AwaitWorkers returned while a request was still waiting on a worker")
	case <-time.After(5 * awaitWorkersPollInterval):
	}

	director.awaitingWorker.Add(-1)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitWorkers did not return once no request was waiting")
	}
}

// The wait is bounded so a request whose worker never arrives cannot hold the
// pod past its termination grace period.
func TestAwaitWorkersStopsWhenContextEnds(t *testing.T) {
	director := NewStreamDirector((*mockInvoker)(&invocation.Result{}))
	t.Cleanup(func() { _ = director.Close() })

	director.awaitingWorker.Add(1)
	t.Cleanup(func() { director.awaitingWorker.Add(-1) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	select {
	case <-awaitWorkersAsync(director, ctx):
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitWorkers ignored the end of its context")
	}
}

// A request counts as waiting from the moment it asks for a worker until the
// wait ends, whichever way it ends.
func TestServeStatefulRequestCountsWhileWaitingOnAWorker(t *testing.T) {
	timeout := consts.Timeout
	consts.Timeout = time.Second
	t.Cleanup(func() { consts.Timeout = timeout })

	proxyListener, proxyAddr := bindEphemeral(t)
	director := NewStreamDirector((*mockInvoker)(&invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "123abc",
	}))
	t.Cleanup(func() { _ = director.Close() })
	healthManager, err := healthManager("http://"+proxyAddr, nil)
	require.NoError(t, err)
	defer serveProxy(t, createHttp2Server(director, "", healthManager), proxyListener)()

	responded := make(chan *http.Response, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, "http://"+proxyAddr+"/asdf", nil)
		request.Header.Set("authorization", "Bearer asdf")
		request.Header.Set("function-id", "1234")
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
		}
		responded <- response
	}()

	require.Eventually(t, func() bool { return director.awaitingWorker.Load() == 1 },
		consts.Timeout/2, 10*time.Millisecond, "request never counted as waiting on a worker")

	// No worker ever connects, so the request times out.
	response := <-responded
	require.NotNil(t, response)
	assert.Equal(t, http.StatusGatewayTimeout, response.StatusCode)
	assert.Zero(t, director.awaitingWorker.Load(), "a finished request is still counted as waiting")
}

// h2c hijacks client connections, so http.Server.Shutdown does not see them.
// Without a GOAWAY the gateway keeps sending new calls to a pod that is going
// away; with one it finishes what is running and sends new calls elsewhere.
func TestHttp2ShutdownSendsGoAwayToClientConnections(t *testing.T) {
	proxyListener, proxyAddr := bindEphemeral(t)
	director := NewStreamDirector((*mockInvoker)(&invocation.Result{}))
	t.Cleanup(func() { _ = director.Close() })
	healthManager, err := healthManager("http://"+proxyAddr, nil)
	require.NoError(t, err)
	http2Server := createHttp2Server(director, "", healthManager)
	defer serveProxy(t, http2Server, proxyListener)()

	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	conn, err := net.Dial("tcp", proxyAddr)
	require.NoError(t, err)
	clientConn, err := transport.NewClientConn(conn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.Close() })

	request, _ := http.NewRequest(http.MethodGet, "http://"+proxyAddr+"/info", nil)
	response, err := clientConn.RoundTrip(request)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.False(t, clientConn.State().Closing, "connection closing before shutdown")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, http2Server.Shutdown(ctx))

	assert.Eventually(t, func() bool { return clientConn.State().Closing || clientConn.State().Closed },
		2*time.Second, 10*time.Millisecond, "client connection never told the server is shutting down")
}
