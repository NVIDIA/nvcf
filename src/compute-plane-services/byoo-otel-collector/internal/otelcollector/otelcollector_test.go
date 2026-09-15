/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package otelcollector

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
)

func TestMain(m *testing.M) {
	logger.Init()
	os.Exit(m.Run())
}

type fakeProcess struct {
	signalCalled bool
	killCalled   bool
	waitDelay    time.Duration
	waitErr      error
	exited       bool
	killChan     chan struct{}
	mu           sync.Mutex
}

func newFakeProcess(waitDelay time.Duration) *fakeProcess {
	return &fakeProcess{
		waitDelay: waitDelay,
		killChan:  make(chan struct{}),
	}
}

func (f *fakeProcess) Signal(sig os.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.exited {
		return os.ErrProcessDone
	}

	// Signal 0 is used to check if process is alive
	if sig == syscall.Signal(0) {
		return nil
	}

	f.signalCalled = true
	if sig == syscall.SIGTERM {
		return nil
	}
	return errors.New("unsupported signal")
}

func (f *fakeProcess) Kill() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.exited {
		return os.ErrProcessDone
	}
	f.killCalled = true
	f.exited = true
	close(f.killChan)
	return nil
}

func (f *fakeProcess) Wait() (*os.ProcessState, error) {
	// Wait either for the delay or for kill signal
	select {
	case <-time.After(f.waitDelay):
		f.mu.Lock()
		f.exited = true
		f.mu.Unlock()
	case <-f.killChan:
		// Killed before natural exit
	}
	return nil, f.waitErr
}

func TestGracefulShutdown_KillAfterTimeout(t *testing.T) {
	logger.Init() // Ensure logger is initialized for tests
	// Set waitDelay longer than the timeout to ensure Kill is called
	fp := newFakeProcess(500 * time.Millisecond)

	// Start a goroutine to simulate the waitProcessExit pattern
	waitCh := make(chan *os.ProcessState)
	go func() {
		state, _ := fp.Wait()
		waitCh <- state
	}()

	// Call GracefulShutdown with short timeout (150ms)
	// Process will NOT exit within this time, so Kill should be called
	GracefulShutdown(fp, 150*time.Millisecond)

	// Verify that Signal and Kill were called
	if !fp.signalCalled {
		t.Error("GracefulShutdown should call Signal")
	}
	if !fp.killCalled {
		t.Error("GracefulShutdown should call Kill after timeout")
	}

	// Wait for the separate Wait() to complete
	select {
	case <-waitCh:
		// Process eventually exited (after being killed)
	case <-time.After(1 * time.Second):
		t.Error("Wait() did not complete in time")
	}
}

func TestGracefulShutdown_ExitNormally(t *testing.T) {
	logger.Init() // Ensure logger is initialized for tests
	fp := newFakeProcess(50 * time.Millisecond)

	// Start a goroutine to simulate the waitProcessExit pattern
	waitCh := make(chan *os.ProcessState)
	go func() {
		state, _ := fp.Wait()
		waitCh <- state
	}()

	// Call GracefulShutdown with longer timeout (200ms)
	// The process will exit after 50ms, so GracefulShutdown should detect it via polling
	GracefulShutdown(fp, 200*time.Millisecond)

	if !fp.signalCalled {
		t.Error("GracefulShutdown should call Signal")
	}

	// Wait for the separate Wait() to complete
	select {
	case <-waitCh:
		// Process exited normally before kill was attempted
		if fp.killCalled {
			t.Error("GracefulShutdown should not call Kill if process exits before timeout")
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("Wait() did not complete in time")
	}
}
