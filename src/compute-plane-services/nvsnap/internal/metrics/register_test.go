/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package metrics

import "testing"

// The agent and the server both register the shared API rate/duration pair.
// prometheus.MustRegister panics on a duplicate, so a process starting both --
// or either one twice -- must not blow up at startup. Guarded by apiOnce;
// this pins that.
func TestRegisterAgentAndServerDoNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("duplicate metric registration panicked: %v", r)
		}
	}()
	RegisterAgent()
	RegisterServer()
	RegisterAgent()
	RegisterServer()
}
