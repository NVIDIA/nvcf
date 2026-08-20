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
// answers, which is how a proxy pod that has gone away behaves from the
// dialler's point of view: the handshake times out rather than being refused.
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

// portInUse reports the source port the cache currently holds for a host, or ""
// if it holds no socket for it.
func portInUse(t *testing.T, cache *h3ConnectionCache, hostname string) string {
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

// capturePort watches for the socket a dial opens and reports its port.
func capturePort(t *testing.T, cache *h3ConnectionCache, hostname string) <-chan string {
	t.Helper()
	out := make(chan string, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if p := portInUse(t, cache, hostname); p != "" {
				out <- p
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		out <- ""
	}()
	return out
}

// canBindUDP reports whether a UDP socket can be bound to a port, which is only
// true once whoever held it has actually released it. This is what proves the
// socket was closed rather than merely dropped from a map.
func canBindUDP(t *testing.T, port string) bool {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:"+port)
	if err != nil {
		return false
	}
	_ = pc.Close()
	return true
}

// The property the change exists for. A UDP load balancer hashes on the source
// port, so a dead flow only moves to a healthy instance when the port changes.
// Asserting on cache state alone would not prove that, so this checks the
// socket is genuinely released and that the retry leaves from a different port.
func TestFailedDialReleasesSocketAndRetryUsesANewPort(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	addr := deadUDPHost(t)

	cache := createH3RoundTripper()
	t.Cleanup(func() { _ = cache.Close() })

	first := capturePort(t, cache, addr)
	_, err := quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(addr), cache)
	require.Error(t, err, "a dial to a host that never answers must fail")

	firstPort := <-first
	require.NotEmpty(t, firstPort, "the first attempt should have opened a socket")

	// The socket must actually be released, not just forgotten. If it were
	// leaked, a dial storm would leak one per failure.
	require.Eventually(t, func() bool { return canBindUDP(t, firstPort) }, 3*time.Second, 10*time.Millisecond,
		"source port %s was never released after the dial failed", firstPort)

	// Hold the old port so the retry cannot reuse it by chance, which would
	// make the assertion below pass for the wrong reason.
	guard, err := net.ListenPacket("udp", "127.0.0.1:"+firstPort)
	require.NoError(t, err)
	defer func() { _ = guard.Close() }()

	second := capturePort(t, cache, addr)
	_, _ = quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(addr), cache)
	secondPort := <-second

	require.NotEmpty(t, secondPort, "the retry should have opened a socket")
	assert.NotEqual(t, firstPort, secondPort,
		"the retry must leave from a new source port so the balancer can re-place the flow")
	t.Logf("first attempt port=%s, retry port=%s", firstPort, secondPort)
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
	t.Cleanup(func() { _ = cache.Close() })

	watchA := capturePort(t, cache, hostA)
	watchB := capturePort(t, cache, hostB)
	go func() { _, _ = quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(hostA), cache) }()
	_, _ = quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(hostB), cache)

	portA, portB := <-watchA, <-watchB
	require.NotEmpty(t, portA)
	require.NotEmpty(t, portB)
	assert.NotEqual(t, portA, portB, "each host must have its own source port, got %s and %s", portA, portB)
}

// Closing the cache must release the sockets, not just drop the map.
func TestCloseReleasesHostSockets(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	addr := deadUDPHost(t)

	cache := createH3RoundTripper()
	watch := capturePort(t, cache, addr)
	go func() { _, _ = quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(addr), cache) }()

	port := <-watch
	require.NotEmpty(t, port, "a socket should have been opened for the host")

	require.NoError(t, cache.Close())

	assert.Eventually(t, func() bool { return canBindUDP(t, port) }, 3*time.Second, 10*time.Millisecond,
		"Close left source port %s bound", port)
}

// Failed dials must not accumulate cache entries, which is the other half of
// not accumulating sockets.
func TestFailedDialsLeaveNoCacheEntries(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	addr := deadUDPHost(t)

	cache := createH3RoundTripper()
	t.Cleanup(func() { _ = cache.Close() })

	for i := 0; i < 3; i++ {
		_, _ = quicConnect(context.Background(), uuid.New().String(), h3ConnConfig(addr), cache)
	}

	cache.mutex.Lock()
	n := len(cache.clients)
	cache.mutex.Unlock()
	assert.Zero(t, n, "failed dials should leave no entries behind, found %d", n)
}
