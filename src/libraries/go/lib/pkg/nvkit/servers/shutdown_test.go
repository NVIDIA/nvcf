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

package servers

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/shutdown"
)

func TestAwaitShutdownSignal_ReportsSignalAsShutdown(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			signals := make(chan os.Signal, 1)
			signals <- sig

			err := awaitShutdownSignal(signals, make(chan struct{}))

			if err == nil {
				t.Fatal("awaitShutdownSignal() = nil, want a shutdown error")
			}
			if !errors.Is(err, shutdown.ErrSignal) {
				t.Errorf("errors.Is(%v, shutdown.ErrSignal) = false, want true", err)
			}
			if !shutdown.IsSignalError(err) {
				t.Errorf("shutdown.IsSignalError(%v) = false, want true", err)
			}

			var signalErr *shutdown.SignalError
			if errors.As(err, &signalErr) && signalErr.Signal != sig {
				t.Errorf("Signal = %v, want %v", signalErr.Signal, sig)
			}
		})
	}
}

func TestAwaitShutdownSignal_ReturnsNilWhenInterruptedByRunGroup(t *testing.T) {
	// Another actor failed first and the run group is tearing this one down.
	// That is not this actor's error to report.
	cancelInterrupt := make(chan struct{})
	close(cancelInterrupt)

	if err := awaitShutdownSignal(make(chan os.Signal), cancelInterrupt); err != nil {
		t.Errorf("awaitShutdownSignal() = %v, want nil", err)
	}
}

func TestAwaitShutdownSignal_BlocksUntilSomethingHappens(t *testing.T) {
	signals := make(chan os.Signal, 1)
	done := make(chan error, 1)

	go func() { done <- awaitShutdownSignal(signals, make(chan struct{})) }()

	select {
	case err := <-done:
		t.Fatalf("awaitShutdownSignal() returned %v before any signal arrived", err)
	case <-time.After(50 * time.Millisecond):
	}

	signals <- syscall.SIGTERM
	select {
	case err := <-done:
		if !errors.Is(err, shutdown.ErrSignal) {
			t.Errorf("errors.Is(%v, shutdown.ErrSignal) = false, want true", err)
		}
	case <-time.After(time.Second):
		t.Fatal("awaitShutdownSignal() did not return after a signal arrived")
	}
}
