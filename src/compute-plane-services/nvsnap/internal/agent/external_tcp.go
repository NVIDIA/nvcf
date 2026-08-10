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

package agent

import (
	"fmt"
	"net"
	"runtime"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// tcpStateEstablished is the kernel TCP_ESTABLISHED state constant.
// Vendored here to avoid pulling in a transitive header. Stable since
// Linux's earliest userspace ABI for /proc/net/tcp and SOCK_DIAG.
const tcpStateEstablished = 1

// isExternalPeer returns true when ip is OUTSIDE any range typically
// used for K8s pod/service networking. CRIU's TCP restore re-binds
// captured TCP_ESTABLISHED sockets to the dump-time pod IP; for
// peers in private ranges the restore loopback alias makes that work,
// but for public peers the alias is unreachable as a routable source
// and connect-back at soccr/soccr.c:529 returns EADDRNOTAVAIL.
//
// Heuristic: "external" = NOT loopback AND NOT inside any of:
//   - 10.0.0.0/8       (K8s pod networks; CGNAT/private)
//   - 172.16.0.0/12    (RFC1918)
//   - 192.168.0.0/16   (RFC1918)
//   - 100.64.0.0/10    (carrier-grade NAT, AWS EKS uses this)
//   - 169.254.0.0/16   (link-local, K8s instance metadata)
//   - 224.0.0.0/4      (multicast)
//
// IPv6 is handled too: anything outside fc00::/7 (ULA) + ::1 + fe80::/10
// is considered external.
//
// Edge case: VPC-peered private addresses in a different cluster are
// treated as internal here. That's fine for the bug we're fixing
// (NVCA-style public-internet peers like NATS) and avoids needing the
// agent to query the K8s API for podCIDR at runtime. If a customer
// later needs to also close cross-VPC connections, override via
// NVSNAP_EXTERNAL_CIDRS env var (parsed in agent startup; not yet wired).
func isExternalPeer(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// Treat all RFC1918 + CGNAT + link-local + multicast as internal.
		privateBlocks := []*net.IPNet{
			mustCIDR("10.0.0.0/8"),
			mustCIDR("172.16.0.0/12"),
			mustCIDR("192.168.0.0/16"),
			mustCIDR("100.64.0.0/10"),
			mustCIDR("169.254.0.0/16"),
			mustCIDR("224.0.0.0/4"),
		}
		for _, b := range privateBlocks {
			if b.Contains(v4) {
				return false
			}
		}
		return true
	}
	// IPv6
	privateBlocks6 := []*net.IPNet{
		mustCIDR("fc00::/7"),  // ULA
		mustCIDR("fe80::/10"), // link-local
		mustCIDR("ff00::/8"),  // multicast
	}
	for _, b := range privateBlocks6 {
		if b.Contains(ip) {
			return false
		}
	}
	return true
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// closeExternalTCPInNS enters the network namespace of pidInNS, lists
// all TCP_ESTABLISHED sockets, and destroys those whose peer satisfies
// isExternalPeer. Returns the number of sockets destroyed and an error
// only when the netns enumeration itself fails — per-socket destroy
// errors are logged and skipped (we want best-effort: closing 9 of 10
// is still progress, and SOCK_DESTROY can return ENOENT if the kernel
// already closed the socket between enumerate and destroy).
//
// MUST be called from a goroutine where runtime.LockOSThread is held;
// the caller is responsible for that + restoring the original netns.
// We do the lock/restore inside this function for safety.
func closeExternalTCPInNS(pidInNS int, log *logrus.Entry) (int, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNs, err := netns.Get()
	if err != nil {
		return 0, fmt.Errorf("get current netns: %w", err)
	}
	defer func() { _ = origNs.Close() }()
	targetNs, err := netns.GetFromPid(pidInNS)
	if err != nil {
		return 0, fmt.Errorf("get netns for pid %d: %w", pidInNS, err)
	}
	defer func() { _ = targetNs.Close() }()
	if err := netns.Set(targetNs); err != nil {
		return 0, fmt.Errorf("setns to target: %w", err)
	}
	// Always restore the original netns — even on early return.
	defer func() {
		if err := netns.Set(origNs); err != nil {
			log.WithError(err).Error("Failed to restore original netns; OS thread will exit on goroutine end")
		}
	}()

	destroyed := 0
	for _, family := range []uint8{syscall.AF_INET, syscall.AF_INET6} {
		sockets, err := netlink.SocketDiagTCP(family)
		if err != nil {
			log.WithError(err).WithField("family", family).Warn("SocketDiagTCP failed; continuing")
			continue
		}
		for _, s := range sockets {
			if s.State != tcpStateEstablished {
				continue
			}
			if !isExternalPeer(s.ID.Destination) {
				continue
			}
			local := &net.TCPAddr{IP: s.ID.Source, Port: int(s.ID.SourcePort)}
			remote := &net.TCPAddr{IP: s.ID.Destination, Port: int(s.ID.DestinationPort)}
			if err := netlink.SocketDestroy(local, remote); err != nil {
				log.WithError(err).WithFields(logrus.Fields{
					"local":  local.String(),
					"remote": remote.String(),
				}).Warn("SocketDestroy failed; continuing")
				continue
			}
			destroyed++
			log.WithFields(logrus.Fields{
				"local":  local.String(),
				"remote": remote.String(),
				"family": family,
			}).Info("Destroyed external TCP socket pre-checkpoint (nvsnap#187)")
		}
	}
	return destroyed, nil
}
