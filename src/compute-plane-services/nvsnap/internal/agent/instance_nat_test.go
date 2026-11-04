// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"reflect"
	"testing"
)

func TestInstanceNATRules(t *testing.T) {
	v4, v6 := instanceNATRules("10.1.181.139=10.1.124.29, 10.1.38.3=10.1.70.240,10.1.38.3=10.1.70.240,10.1.5.5=10.1.5.5,bad=10.1.1.1,fd00::1=fd00::2,10.1.9.9=fd00::9")
	want4 := [][]string{
		{"-t", "nat", "-A", "OUTPUT", "-d", "10.1.181.139/32", "-j", "DNAT", "--to-destination", "10.1.124.29"},
		{"-t", "nat", "-A", "OUTPUT", "-d", "10.1.38.3/32", "-j", "DNAT", "--to-destination", "10.1.70.240"},
	}
	want6 := [][]string{{"-t", "nat", "-A", "OUTPUT", "-d", "fd00::1/128", "-j", "DNAT", "--to-destination", "fd00::2"}}
	if !reflect.DeepEqual(v4, want4) {
		t.Errorf("v4 = %v, want %v", v4, want4)
	}
	if !reflect.DeepEqual(v6, want6) {
		t.Errorf("v6 = %v, want %v", v6, want6)
	}
	if v4, v6 := instanceNATRules(""); v4 != nil || v6 != nil {
		t.Errorf("empty map = %v %v", v4, v6)
	}
}

// A restored pod's old address can be another member's new one (pod IPs are
// reused): here the worker had 10.1.48.132 and the new frontend got it. The
// map moves the worker's own address, so the worker gets no loopback alias
// for it; with one, its traffic to the frontend was delivered to itself.
func TestInetAddrMapMoves(t *testing.T) {
	m := "10.1.48.132=10.1.160.209,10.1.56.35=10.1.48.132,172.20.151.38=10.1.48.132,fd00::1=fd00::2"
	for ip, want := range map[string]bool{
		"10.1.48.132":  true,  // the worker's old address, now the frontend's
		"10.1.56.35":   true,  // the frontend's old address
		"10.1.160.209": false, // only a new address
		"fd00::1":      true,
		"10.9.9.9":     false,
		"":             false,
		"not-an-ip":    false,
	} {
		if got := inetAddrMapMoves(m, ip); got != want {
			t.Errorf("inetAddrMapMoves(%q) = %v, want %v", ip, got, want)
		}
	}
	if inetAddrMapMoves("", "10.1.48.132") {
		t.Error("an empty map moves nothing")
	}
}
