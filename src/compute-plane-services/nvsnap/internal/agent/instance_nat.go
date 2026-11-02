// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A restored instance keeps the addresses its pods had when it was captured,
// not only in its sockets (criu rewrites those through the inet address map)
// but in its data: an engine advertises its own address to its peers (a
// Dynamo frontend names the address workers stream responses back to; NCCL,
// Ray and others exchange addresses the same way). Those addresses belong to
// pods that no longer exist. So each restored pod's network namespace sends
// traffic for a captured address of the instance to the pod that now has it:
// the instance reaches itself under the addresses it remembers.

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

// instanceNATRules returns, for an inet address map ("OLD=NEW,..."), the
// iptables arguments of one DNAT rule per moved address, by address family.
func instanceNATRules(inetAddrMap string) (v4, v6 [][]string) {
	seen := map[string]bool{}
	for _, pair := range strings.Split(inetAddrMap, ",") {
		o, n, ok := strings.Cut(strings.TrimSpace(pair), "=")
		old, nw := net.ParseIP(strings.TrimSpace(o)), net.ParseIP(strings.TrimSpace(n))
		if !ok || old == nil || nw == nil || old.Equal(nw) || seen[old.String()] {
			continue
		}
		if (old.To4() == nil) != (nw.To4() == nil) {
			continue
		}
		seen[old.String()] = true
		if old.To4() != nil {
			v4 = append(v4, []string{"-t", "nat", "-A", "OUTPUT", "-d", old.String() + "/32", "-j", "DNAT", "--to-destination", nw.String()})
		} else {
			v6 = append(v6, []string{"-t", "nat", "-A", "OUTPUT", "-d", old.String() + "/128", "-j", "DNAT", "--to-destination", nw.String()})
		}
	}
	return v4, v6
}

// inetAddrMapMoves reports whether inetAddrMap ("OLD=NEW,...") has an entry
// for ip as an old address.
func inetAddrMapMoves(inetAddrMap, ip string) bool {
	a := net.ParseIP(strings.TrimSpace(ip))
	if a == nil {
		return false
	}
	for _, pair := range strings.Split(inetAddrMap, ",") {
		o, _, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && a.Equal(net.ParseIP(strings.TrimSpace(o))) {
			return true
		}
	}
	return false
}

// natInstanceAddresses installs instanceNATRules in the network namespace of
// host pid hostPID.
func natInstanceAddresses(hostPID int, inetAddrMap string, log *logrus.Entry) error {
	v4, v6 := instanceNATRules(inetAddrMap)
	netns := "--net=" + hostProcBase() + "/" + strconv.Itoa(hostPID) + "/ns/net"
	for tool, rules := range map[string][][]string{"iptables": v4, "ip6tables": v6} {
		for _, r := range rules {
			args := append([]string{netns, "--", tool, "-w"}, r...)
			if out, err := exec.Command("nsenter", args...).CombinedOutput(); err != nil { //nolint:gosec // fixed tool; addresses parsed as IPs above
				return fmt.Errorf("%s %s: %w (%s)", tool, strings.Join(r, " "), err, strings.TrimSpace(string(out)))
			}
		}
	}
	if n := len(v4) + len(v6); n > 0 {
		log.WithField("addresses", n).Info("CRIU restore: the instance's captured addresses lead to its restored pods")
	}
	return nil
}
