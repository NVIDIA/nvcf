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
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

// recordRoundTripper records whether it was called and returns a canned response.
type recordRoundTripper struct {
	called bool
	resp   *http.Response
}

func (r *recordRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.called = true
	return r.resp, nil
}

// TestProtoRoutingTransport_RoutesByProtoMajor verifies that http/1 requests
// go to the h1 transport and http/2 requests go to the h2 transport.
func TestProtoRoutingTransport_RoutesByProtoMajor(t *testing.T) {
	h1 := &recordRoundTripper{resp: &http.Response{StatusCode: 201, Body: http.NoBody}}
	h2 := &recordRoundTripper{resp: &http.Response{StatusCode: 202, Body: http.NoBody}}
	tr := ProtoRoutingTransport{h1: h1, h2: h2}

	req1, err := http.NewRequest(http.MethodGet, "http://example/", nil)
	require.NoError(t, err)
	req1.ProtoMajor = 1
	resp, err := tr.RoundTrip(req1)
	require.NoError(t, err)
	require.Equal(t, 201, resp.StatusCode)
	require.True(t, h1.called)
	require.False(t, h2.called)

	h1.called = false
	req2, err := http.NewRequest(http.MethodGet, "http://example/", nil)
	require.NoError(t, err)
	req2.ProtoMajor = 2
	resp, err = tr.RoundTrip(req2)
	require.NoError(t, err)
	require.Equal(t, 202, resp.StatusCode)
	require.True(t, h2.called)
	require.False(t, h1.called)
}

func TestNewProtoRoutingTransport_Constructs(t *testing.T) {
	tr := NewProtoRoutingTransport(&http.Transport{}, &http2.Transport{})
	require.NotNil(t, tr)
	require.NotNil(t, tr.h1)
	require.NotNil(t, tr.h2)
}

// TestPushListener_Addr verifies the synthetic loopback address.
func TestPushListener_Addr(t *testing.T) {
	l := NewPushListener()
	defer l.Close()
	addr := l.Addr()
	require.NotNil(t, addr)
	ipAddr, ok := addr.(*net.IPAddr)
	require.True(t, ok)
	require.True(t, ipAddr.IP.Equal(net.IPv4(127, 0, 0, 1)))
}

// TestPushListener_AcceptAfterClose verifies Accept returns net.ErrClosed once
// the listener is closed.
func TestPushListener_AcceptAfterClose(t *testing.T) {
	l := NewPushListener()
	require.NoError(t, l.Close())
	conn, err := l.Accept()
	require.Nil(t, conn)
	require.ErrorIs(t, err, net.ErrClosed)
}

// TestPushListener_DoubleClose verifies the second Close returns net.ErrClosed.
func TestPushListener_DoubleClose(t *testing.T) {
	l := NewPushListener()
	require.NoError(t, l.Close())
	require.ErrorIs(t, l.Close(), net.ErrClosed)
}

// TestPushListener_ServeConnOnClosedListener verifies ServeConn returns
// net.ErrClosed when the listener has been closed.
func TestPushListener_ServeConnOnClosedListener(t *testing.T) {
	l := NewPushListener()
	require.NoError(t, l.Close())
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	require.ErrorIs(t, l.ServeConn(client), net.ErrClosed)
}

// TestPushListener_AcceptServeAndCloseFlow drives the happy path: a goroutine
// serves a connection, Accept hands it back, and closing the served conn
// unblocks ServeConn. Synchronization is done with channels, no sleeps.
func TestPushListener_AcceptServeAndCloseFlow(t *testing.T) {
	l := NewPushListener()
	defer l.Close()

	client, server := net.Pipe()
	defer server.Close()

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- l.ServeConn(client)
	}()

	accepted, err := l.Accept()
	require.NoError(t, err)
	require.NotNil(t, accepted)

	// ServeConn must still be blocked until the accepted conn is closed.
	select {
	case <-serveDone:
		t.Fatal("ServeConn returned before the accepted conn was closed")
	default:
	}

	require.NoError(t, accepted.Close())

	select {
	case err := <-serveDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ServeConn did not return after the accepted conn was closed")
	}

	// The waitForCloseConn must expose the underlying conn via Unwrap.
	type unwrapper interface{ Unwrap() net.Conn }
	uw, ok := accepted.(unwrapper)
	require.True(t, ok)
	require.Same(t, client, uw.Unwrap())
}

// TestWaitForCloseConn_CloseIsIdempotent verifies the closed channel is only
// closed once even with concurrent Close calls.
func TestWaitForCloseConn_CloseIsIdempotent(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &waitForCloseConn{Conn: client, closed: make(chan struct{})}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Close()
		}()
	}
	wg.Wait()
	select {
	case <-c.closed:
	default:
		t.Fatal("closed channel should be closed")
	}
}

// TestPurgeFromStream_PurgesSubject exercises PurgeFromStream against an
// in-process JetStream server: publish a message, purge the subject, and
// confirm the message count drops to zero.
func TestPurgeFromStream_PurgesSubject(t *testing.T) {
	setupLogger()
	cluster, err := testutils.NewNatsSuperCluster(t)
	require.NoError(t, err)
	defer cluster.Shutdown()

	nc, err := nats.Connect(cluster.Clusters[0].Servers[0].ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	const streamName = "purge_test_stream"
	const subject = "purge.test.subject"
	stream, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{subject},
		Storage:  jetstream.MemoryStorage,
	})
	require.NoError(t, err)

	_, err = js.Publish(ctx, subject, []byte("payload"))
	require.NoError(t, err)

	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), info.State.Msgs)

	err = PurgeFromStream(ctx, nc, streamName, jetstream.WithPurgeSubject(subject))
	require.NoError(t, err)

	info, err = stream.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(0), info.State.Msgs)
}

// TestPurgeFromStream_OptionError verifies that an option returning an error is
// surfaced before any network call.
func TestPurgeFromStream_OptionError(t *testing.T) {
	badOpt := func(*jetstream.StreamPurgeRequest) error {
		return errOptFailed
	}
	err := PurgeFromStream(t.Context(), nil, "stream", badOpt)
	require.ErrorIs(t, err, errOptFailed)
}

var errOptFailed = &optError{}

type optError struct{}

func (*optError) Error() string { return "opt failed" }

// TestTcpConnect_DialFailure verifies tcpConnect returns an error when the
// proxy address is unreachable. It uses an ephemeral port that is closed
// immediately so the dial fails fast without binding a fixed port.
func TestTcpConnect_DialFailure(t *testing.T) {
	setupLogger()
	// Reserve and immediately release an ephemeral loopback port so nothing is
	// listening on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	_, err = tcpConnect(t.Context(), "req-id", &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP1ConnectionConfig{
		ProxyURI:                "http://" + addr + "/v1/proxy",
		ProxyAuthorizationToken: "tok",
	})
	require.Error(t, err)
}

// TestTcpConnect_BadURI verifies tcpConnect errors on an unparseable URI.
func TestTcpConnect_BadURI(t *testing.T) {
	setupLogger()
	_, err := tcpConnect(t.Context(), "req-id", &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP1ConnectionConfig{
		ProxyURI:                "http://[::1]:namedport/bad",
		ProxyAuthorizationToken: "tok",
	})
	require.Error(t, err)
}
