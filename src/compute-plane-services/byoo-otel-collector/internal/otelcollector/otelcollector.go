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
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/execabs"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/metrics"
)

const (
	otelColGracefulShutdownTimeout = 30 * time.Second
)

func RunOtelCollector(args []string) (*os.Process, error) {
	start := time.Now()
	status := metrics.StatusSuccess

	defer func() {
		duration := time.Since(start)
		metrics.RecordOperationDuration(metrics.RunOtelCollector, status, duration)
		metrics.IncrementOperationStatus(metrics.RunOtelCollector, status)
	}()

	logger.Logger.Infof("Running otelcol-contrib with args: %v", args)
	cmd := execabs.Command("/app/otelcol-contrib", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// start the otelcol-contrib command but don't wait for it to complete
	if err := cmd.Start(); err != nil {
		status = metrics.StatusError
		return nil, fmt.Errorf("error running otelcol-contrib: %w", err)
	}

	return cmd.Process, nil
}

// Adapter to allow fakeProcess to be used in place of *os.Process
// Define a Process interface for testability
type Process interface {
	Signal(os.Signal) error
	Kill() error
	Wait() (*os.ProcessState, error)
}

// GracefulShutdown sends SIGTERM to the process and waits for graceful shutdown.
// If the process doesn't exit within the timeout, it sends SIGKILL.
// NOTE: This function does NOT call Wait(). The caller is responsible for waiting
// on the process (e.g., via waitProcessExit channel) to avoid "waitid: no child processes" errors.
func GracefulShutdown(proc Process, timeout ...time.Duration) {
	to := otelColGracefulShutdownTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		to = timeout[0]
	}

	logger.Logger.Info("Sending SIGTERM to otelcol-contrib")
	err := proc.Signal(syscall.SIGTERM)
	if err != nil {
		if err == os.ErrProcessDone {
			logger.Logger.Info("otelcol-contrib process already done")
			return
		}
		logger.Logger.Errorf("failed to send SIGTERM to otelcol-contrib: %v", err)
		return
	}

	// Don't call Wait() here - let the existing waitProcessExit goroutine handle it
	// Poll the process to see if it's still alive instead of sleeping the full timeout
	pollInterval := 100 * time.Millisecond
	elapsed := time.Duration(0)

	for elapsed < to {
		time.Sleep(pollInterval)
		elapsed += pollInterval

		// Try to send signal 0 to check if process is still alive
		err := proc.Signal(syscall.Signal(0))
		if err != nil {
			// Process is gone (either exited or permission denied means we can't kill it)
			if err == os.ErrProcessDone {
				logger.Logger.Info("otelcol-contrib exited gracefully")
			}
			return
		}
	}

	// If we reach here, the process is still alive after the timeout
	logger.Logger.Warn("otelcol-contrib did not exit after SIGTERM, sending SIGKILL")
	if killErr := proc.Kill(); killErr != nil {
		// Check if it already exited
		if killErr != os.ErrProcessDone {
			logger.Logger.Errorf("failed to kill otelcol-contrib: %v", killErr)
		}
	}
}
