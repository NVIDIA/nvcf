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
