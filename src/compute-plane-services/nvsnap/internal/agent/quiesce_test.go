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
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// TestProcessHasInterceptMapped_OwnPid_ReturnsFalse verifies the
// detection function returns false for a process (the test process
// itself) that does not have libnvsnap_intercept.so mapped. The test
// binary doesn't link the intercept library, so this is a known-good
// negative case.
func TestProcessHasInterceptMapped_OwnPid_ReturnsFalse(t *testing.T) {
	pid := os.Getpid()
	if processHasInterceptMapped(pid) {
		t.Fatalf("processHasInterceptMapped(%d) returned true; the test binary does not link libnvsnap_intercept.so", pid)
	}
}

// TestProcessHasInterceptMapped_NonexistentPid_ReturnsFalse verifies
// the gate fails closed: any error reading /proc/PID/maps (including
// the process not existing) returns false. The caller depends on this
// to avoid sending SIGUSR1 to a process whose state we can't confirm.
func TestProcessHasInterceptMapped_NonexistentPid_ReturnsFalse(t *testing.T) {
	// PID 0 never represents a real process. /proc/0/maps doesn't exist.
	if processHasInterceptMapped(0) {
		t.Fatal("processHasInterceptMapped(0) returned true; expected fail-closed false on missing /proc/PID/maps")
	}
	// Try a clearly-invalid high PID too (above PID_MAX on most systems).
	if processHasInterceptMapped(9_999_999) {
		t.Fatal("processHasInterceptMapped(9999999) returned true; expected fail-closed false")
	}
}

// TestSendQuiesceSignal_NoIntercept_SkipsKill is the actual NIM
// regression test: when the target process has no intercept library
// mapped, sendQuiesceSignal MUST NOT invoke syscall.Kill. The
// SIGUSR1 default action is process termination (POSIX); sending it
// to a non-intercepted workload (NIM 2026-06-03) killed the workload
// → kubelet restart → checkpoint loop.
//
// We can't intercept syscall.Kill in Go, so we use a sentinel:
// install a SIGUSR1 handler on the test process that records whether
// the signal arrived. The test passes os.Getpid() (no libnvsnap
// mapped). If the gate is broken, our own handler will fire.
func TestSendQuiesceSignal_NoIntercept_SkipsKill(t *testing.T) {
	// Re-route incoming SIGUSR1 into a test-side channel so the
	// Go runtime doesn't terminate the test process if the gate fails.
	// Without this, a regression in the gate would kill the entire
	// test binary on the first sendQuiesceSignal call.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGUSR1)
	defer signal.Reset(syscall.SIGUSR1)

	log := logrus.New().WithField("test", t.Name())
	sendQuiesceSignal(os.Getpid(), log)

	// Give a brief moment for any erroneously-sent signal to be
	// delivered via Go's signal handler runtime path.
	select {
	case <-sigCh:
		t.Fatal("sendQuiesceSignal sent SIGUSR1 to a process without libnvsnap_intercept.so mapped — the NIM kill-loop regression has returned (GCP-H100-a 2026-06-03)")
	case <-time.After(50 * time.Millisecond):
		// Expected — gate held.
	}
}
