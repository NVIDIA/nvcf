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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
)

// blackholeHost returns the address of a UDP socket that reads packets and
// never answers. That is the condition behind "timeout: no recent network
// activity": the proxy pod is gone, so nothing responds and the handshake has
// to time out rather than being refused. A closed port would be refused
// immediately and would not exercise this path at all.
func blackholeHost(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	return pc.LocalAddr().String()
}

func deadHostWork(addr string) *pb.WorkerInvokeFunctionRequest {
	return &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
					Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
						ProxyURI:                "https://" + addr + "/v1/proxy",
						ProxyAuthorizationToken: "dummy-token",
					}}},
			},
		},
	}
}

// HandshakeIdleTimeout was previously left unset, so quic-go's 5s default
// applied and a work request naming a dead pod held a worker concurrency slot
// for 30s. Nothing failed, which is why it went unnoticed; this asserts the
// bound is actually in force so it cannot silently regress.
func TestDialToDeadHostGivesUpWithinHandshakeTimeout(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	addr := blackholeHost(t)

	h3 := createH3RoundTripper()
	require.Equal(t, handshakeIdleTimeout, h3.wrappedTransport.QUICConfig.HandshakeIdleTimeout,
		"handshake timeout must be set explicitly, not left to the library default")

	cfg := deadHostWork(addr).StatefulConfig.ConnectionConfigs[0].GetHttp3Config()

	start := time.Now()
	_, err := quicConnect(context.Background(), uuid.New().String(), cfg, h3)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 2*handshakeIdleTimeout,
		"a dial to a dead host took %v, expected to give up near %v", elapsed, handshakeIdleTimeout)
}

// The breaker's whole purpose: the first work request naming a dead pod pays
// the dial timeouts, and everything after it is refused immediately instead of
// paying them again. Without this a deep backlog of requests naming a pod that
// no longer exists drains at one slot-blocking timeout each.
func TestDeadHostIsRefusedAfterRepeatedFailures(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	addr := blackholeHost(t)

	h3 := createH3RoundTripper()
	work := deadHostWork(addr)

	// First request: pays the dial budget and trips the breaker on the way.
	firstStart := time.Now()
	_, err := getClientConnFromProxy(context.Background(), work, h3)
	firstElapsed := time.Since(firstStart)
	require.Error(t, err)

	host := "127.0.0.1:" + portOf(t, addr)
	require.True(t, h3.breaker.isOpen(host), "breaker should be open after the first request failed repeatedly")

	// Second request naming the same dead host must not dial at all.
	secondStart := time.Now()
	_, err = getClientConnFromProxy(context.Background(), deadHostWork(addr), h3)
	secondElapsed := time.Since(secondStart)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrHostUnreachable)
	assert.Less(t, secondElapsed, 250*time.Millisecond,
		"a request for a known-dead host should be refused immediately, took %v", secondElapsed)
	assert.Less(t, secondElapsed, firstElapsed/4,
		"refusal (%v) should be far cheaper than dialling (%v)", secondElapsed, firstElapsed)

	t.Logf("first request %v, subsequent request %v", firstElapsed, secondElapsed)
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	return port
}

// BenchmarkDialDeadHost records what a work request naming a vanished proxy pod
// costs, which is the number that decides how long a backlog takes to drain
// after a proxy restart. Run with:
//
//	go test ./proxy/ -run '^$' -bench BenchmarkDialDeadHost -benchtime 1x
func BenchmarkDialDeadHost(b *testing.B) {
	quicInsecure = true
	defer func() { quicInsecure = false }()

	b.Run("single dial", func(b *testing.B) {
		addr := blackholeHostB(b)
		h3 := createH3RoundTripper()
		cfg := deadHostWork(addr).StatefulConfig.ConnectionConfigs[0].GetHttp3Config()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = quicConnect(context.Background(), uuid.New().String(), cfg, h3)
		}
	})

	b.Run("full retry sequence", func(b *testing.B) {
		addr := blackholeHostB(b)
		h3 := createH3RoundTripper()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = getClientConnFromProxy(context.Background(), deadHostWork(addr), h3)
		}
	})

	// The same sequence once the breaker has tripped, which is what every
	// request after the first one in a backlog actually costs.
	b.Run("full retry sequence with breaker open", func(b *testing.B) {
		addr := blackholeHostB(b)
		h3 := createH3RoundTripper()
		_, _ = getClientConnFromProxy(context.Background(), deadHostWork(addr), h3)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = getClientConnFromProxy(context.Background(), deadHostWork(addr), h3)
		}
	})
}

func blackholeHostB(b *testing.B) string {
	b.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	return pc.LocalAddr().String()
}
