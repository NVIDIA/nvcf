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

package utils

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/semaphore"
)

func TestSleepWithContextTimerFires(t *testing.T) {
	// No cancellation: the timer.C branch is taken.
	start := time.Now()
	SleepWithContext(context.Background(), 10*time.Millisecond)
	require.GreaterOrEqual(t, time.Since(start), 10*time.Millisecond)
}

func TestSleepWithContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Already-canceled context: the ctx.Done() branch and timer.Stop() run.
	SleepWithContext(ctx, time.Hour)
}

func TestAcquireUpToMaxBlocksThenAcquiresOne(t *testing.T) {
	sem := semaphore.NewWeighted(1)
	require.True(t, sem.TryAcquire(1)) // exhaust capacity so AcquireMax returns 0

	released := make(chan struct{})
	go func() {
		// Release after a short delay so the blocked Acquire(ctx, 1) succeeds.
		time.Sleep(20 * time.Millisecond)
		sem.Release(1)
		close(released)
	}()

	n, err := AcquireUpToMax(context.Background(), sem)
	require.NoError(t, err)
	require.Equal(t, uint32(1), n, "blocks until one token frees, then acquires exactly one")
	<-released
}

func TestWriteTerminationLogOpenError(t *testing.T) {
	// In a k8s env but with a path that cannot be opened (parent missing) so
	// os.OpenFile fails, exercising the "failed to open termination log" branch.
	t.Setenv("KUBERNETES_SERVICE_HOST", "test")
	orig := terminationLogPath
	t.Cleanup(func() { terminationLogPath = orig })
	terminationLogPath = filepath.Join(t.TempDir(), "no-such-dir", "termination-log")

	err := writeTerminationLog("boom")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to open termination log")
}

func TestExitReasonLogsWriteFailure(t *testing.T) {
	// ExitReason should swallow (log, not return) a real write failure that is
	// not ErrNotInKubernetesEnv. Drives the err != nil && err != ErrNotInKube branch.
	t.Setenv("KUBERNETES_SERVICE_HOST", "test")
	orig := terminationLogPath
	t.Cleanup(func() { terminationLogPath = orig })
	terminationLogPath = filepath.Join(t.TempDir(), "no-such-dir", "termination-log")

	require.NotPanics(t, func() { ExitReason(os.ErrPermission) })
}

func TestCreateDirectoryMkdirError(t *testing.T) {
	// MkdirAll on a path whose parent component is a regular file fails, taking
	// the error branch of CreateDirectory.
	base := t.TempDir()
	filePath := filepath.Join(base, "regular.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o644))

	target := filepath.Join(filePath, "child") // parent is a file, not a dir
	err := CreateDirectory(target, 0o755)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to make directory")
}
