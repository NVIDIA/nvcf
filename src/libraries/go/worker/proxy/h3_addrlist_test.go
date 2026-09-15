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
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsIPv4(t *testing.T) {
	require.True(t, isIPv4(net.IPAddr{IP: net.IPv4(1, 2, 3, 4)}))
	require.False(t, isIPv4(net.IPAddr{IP: net.ParseIP("::1")}))
	require.False(t, isNotIPv4(net.IPAddr{IP: net.IPv4(1, 2, 3, 4)}))
	require.True(t, isNotIPv4(net.IPAddr{IP: net.ParseIP("::1")}))
}

func TestAddrList_First(t *testing.T) {
	v6 := net.IPAddr{IP: net.ParseIP("::1")}
	v4 := net.IPAddr{IP: net.IPv4(10, 0, 0, 1)}
	list := addrList{v6, v4}

	// IPv4 strategy returns the v4 entry even though it is second.
	require.True(t, list.first(isIPv4).IP.Equal(v4.IP))
	// IPv6 strategy returns the v6 entry.
	require.True(t, list.first(isNotIPv4).IP.Equal(v6.IP))

	// When nothing matches, the first element of any kind is returned.
	onlyV6 := addrList{v6}
	require.True(t, onlyV6.first(isIPv4).IP.Equal(v6.IP))
}

func TestAddrList_ForResolve(t *testing.T) {
	v6 := net.IPAddr{IP: net.ParseIP("2001:db8::1")}
	v4 := net.IPAddr{IP: net.IPv4(192, 0, 2, 1)}
	list := addrList{v6, v4}

	// "ip" network: addr containing ':' is treated as an IPv6 literal -> want6.
	require.True(t, list.forResolve("ip", "2001:db8::1").IP.Equal(v6.IP))
	require.True(t, list.forResolve("ip", "example.com").IP.Equal(v4.IP))

	// "tcp"/"udp" network: a bracketed literal indicates IPv6.
	require.True(t, list.forResolve("tcp", "[2001:db8::1]:443").IP.Equal(v6.IP))
	require.True(t, list.forResolve("udp", "example.com:443").IP.Equal(v4.IP))

	// Unknown network never sets want6, so IPv4 is preferred.
	require.True(t, list.forResolve("weird", "anything").IP.Equal(v4.IP))
}

func TestResolveUDPAddr_BadAddrAndPort(t *testing.T) {
	tr := createH3RoundTripper()
	defer tr.Close()

	// Missing port produces a SplitHostPort error.
	_, err := tr.resolveUDPAddr(t.Context(), "udp", "no-port-here")
	require.Error(t, err)

	// Unresolvable named port produces a LookupPort error.
	_, err = tr.resolveUDPAddr(t.Context(), "udp", "127.0.0.1:definitely-not-a-port")
	require.Error(t, err)
}
