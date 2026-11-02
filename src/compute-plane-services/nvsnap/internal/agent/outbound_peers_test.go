// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"net"
	"slices"
	"testing"

	"github.com/vishvananda/netlink"
)

func tcpSock(state uint8, src string, sport uint16, dst string, dport uint16, inode uint32) *netlink.Socket {
	s := &netlink.Socket{State: state, INode: inode}
	s.ID.Source, s.ID.SourcePort = net.ParseIP(src), sport
	s.ID.Destination, s.ID.DestinationPort = net.ParseIP(dst), dport
	return s
}

// The sockets of a Kimi rank on 2026-10-18: the request proxy (utils)
// connected to its serving port; the rank connected out to its peer rank
// and to an etcd.
func rankSockets() []*netlink.Socket {
	const rank = "10.1.139.204"
	return []*netlink.Socket{
		tcpSock(tcpStateListen, "0.0.0.0", 8000, "0.0.0.0", 0, 1),
		tcpSock(tcpStateListen, "::", 29500, "::", 0, 2),
		tcpSock(tcpStateEstablished, rank, 8000, "10.1.62.66", 41682, 3),  // utils -> engine
		tcpSock(tcpStateEstablished, rank, 29500, "10.1.78.10", 60902, 4), // peer rank -> its rendezvous
		tcpSock(tcpStateEstablished, rank, 60036, "10.1.78.10", 39997, 5), // engine -> peer rank
		tcpSock(tcpStateEstablished, rank, 51000, "10.1.50.5", 2379, 6),   // engine -> etcd
		tcpSock(tcpStateEstablished, rank, 51002, "10.1.50.5", 2379, 7),   // a second one, same peer
		tcpSock(tcpStateEstablished, rank, 59598, "127.0.0.1", 59113, 8),  // loopback
		tcpSock(tcpStateEstablished, rank, 52000, "10.1.60.6", 4222, 9),   // a sidecar's, not the engine's
	}
}

func TestOutboundPeers_LeavesOutClients(t *testing.T) {
	engine := func(s *netlink.Socket) bool { return s.INode != 9 }
	var got []string
	for _, ip := range outboundPeers(rankSockets(), engine) {
		got = append(got, ip.String())
	}
	slices.Sort(got)
	if want := []string{"10.1.50.5", "10.1.78.10"}; !slices.Equal(got, want) {
		t.Errorf("peers = %v, want the peer rank and the etcd %v; the request proxy is a client", got, want)
	}
}
