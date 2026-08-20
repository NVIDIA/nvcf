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

// deadUDPHost returns the address of a socket that reads packets and never
// answers, which is how a proxy pod that has gone away behaves from the dialler's
// point of view: the handshake has to time out rather than being refused.
func deadUDPHost(t *testing.T) string {
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

func h3ConnConfig(addr string) *pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig {
	return &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
		ProxyURI:                "https://" + addr + "/v1/proxy",
		ProxyAuthorizationToken: "dummy-token",
	}
}

// localPortFor reports the source port the cache is currently using for a host,
// or "" when it holds no socket for it.
func localPortFor(t *testing.T, cache *h3ConnectionCache, hostname string) string {
	t.Helper()
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cl, ok := cache.clients[hostname]
	if !ok || cl.transport == nil {
		return ""
	}
	_, port, err := net.SplitHostPort(cl.transport.Conn.LocalAddr().String())
	require.NoError(t, err)
	return port
}

// The reason this change exists. A UDP load balancer hashes on the source port,
// so a dead flow only moves to a healthy instance when the source port changes.
// Discarding a failed host has to discard its socket, otherwise every retry
// leaves from the same port, lands on the same dead instance, and the worker
// never recovers while traffic continues.
func TestFailedHostGetsANewSourcePortOnRetry(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	addr := deadUDPHost(t)
	hostname := addr

	cache := createH3RoundTripper()
	requestId := uuid.New().String()

	_, err := quicConnect(context.Background(), requestId, h3ConnConfig(addr), cache)
	require.Error(t, err, "dial to a host that never answers must fail")

	// The failed host must not still be holding its socket.
	assert.Empty(t, localPortFor(t, cache, hostname),
		"a failed host should have been discarded together with its socket")

	// Dial again and capture the port actually in use during the attempt.
	seen := make(chan string, 1)
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if p := localPortFor(t, cache, hostname); p != "" {
				seen <- p
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		seen <- ""
	}()
	_, _ = quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(addr), cache)

	second := <-seen
	require.NotEmpty(t, second, "second attempt should have opened a socket")
	t.Logf("second attempt used source port %s", second)
}

// Hosts must not share a socket, otherwise one unreachable pod takes QUIC to
// every other pod down with it.
func TestDistinctHostsUseDistinctSourcePorts(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	hostA := deadUDPHost(t)
	hostB := deadUDPHost(t)
	require.NotEqual(t, hostA, hostB)

	cache := createH3RoundTripper()

	ports := make(chan string, 2)
	for _, h := range []string{hostA, hostB} {
		go func(h string) {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if p := localPortFor(t, cache, h); p != "" {
					ports <- p
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			ports <- ""
		}(h)
	}
	go func() { _, _ = quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(hostA), cache) }()
	_, _ = quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(hostB), cache)

	p1, p2 := <-ports, <-ports
	require.NotEmpty(t, p1)
	require.NotEmpty(t, p2)
	assert.NotEqual(t, p1, p2, "each host must have its own source port, got %s and %s", p1, p2)
}

// Closing the cache must not leak the per-host sockets.
func TestCloseReleasesHostSockets(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	addr := deadUDPHost(t)

	cache := createH3RoundTripper()
	go func() { _, _ = quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(addr), cache) }()

	require.Eventually(t, func() bool { return localPortFor(t, cache, addr) != "" },
		3*time.Second, 5*time.Millisecond, "a socket should have been opened for the host")

	require.NoError(t, cache.Close())

	cache.mutex.Lock()
	clients := cache.clients
	cache.mutex.Unlock()
	assert.Nil(t, clients, "Close should have released the client map")
}

// A worker holds one socket per proxy pod it talks to, so the count has to track
// distinct hosts rather than growing per dial attempt.
func TestSocketCountTracksDistinctHosts(t *testing.T) {
	setupLogger()
	allowInsecure(t)

	cache := createH3RoundTripper()
	addr := deadUDPHost(t)

	for i := 0; i < 3; i++ {
		_, _ = quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(addr), cache)
	}

	cache.mutex.Lock()
	n := len(cache.clients)
	cache.mutex.Unlock()
	assert.LessOrEqual(t, n, 1, "repeated attempts to one host must not accumulate entries, got %d", n)
}
