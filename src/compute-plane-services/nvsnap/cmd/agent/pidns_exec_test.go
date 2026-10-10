// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

func TestBindUnixListenersRejectsBadSpec(t *testing.T) {
	for _, spec := range []string{"x:/tmp/a", "1:", "nocolon"} {
		if _, err := bindUnixListeners(spec); err == nil {
			t.Errorf("accepted %q", spec)
		}
	}
	if n, err := bindUnixListeners(""); err != nil || n != 0 {
		t.Errorf("empty spec: %v", err)
	}
}
