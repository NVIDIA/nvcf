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

package progress

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/types"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/upload"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
)

// ------------------------------------------------------------------------

const taskVersionId = "test-task"

func TestProgressMonitorTaskAlreadyCompleted(t *testing.T) {
	t.Parallel()
	taskId := "task_already_completed"
	testDir, _ := os.MkdirTemp("", taskId)
	progressPercentagesUpdatedToFile := make([]uint32, 0)
	progressPercentagesExpected := []uint32{100}
	progressFile := filepath.Join(testDir, "progress")
	if err := writeProgressMessage(progressFile, taskId, taskVersionId, 100); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	runTest(t, taskId, testDir, false, progressPercentagesUpdatedToFile, progressPercentagesExpected, types.NO_STRATEGY)
}

func TestProgressMonitorTaskHappyCase(t *testing.T) {
	t.Parallel()
	taskId := "task_happy_case"
	testDir, _ := os.MkdirTemp("", taskId)
	progressPercentagesUpdatedToFile := []uint32{25, 50, 100}
	runTest(t, taskId, testDir, false, progressPercentagesUpdatedToFile, progressPercentagesUpdatedToFile, types.NO_STRATEGY)
}

func TestProgressMonitorLongRunning(t *testing.T) {
	t.Parallel()
	taskId := "task_long_running"
	testDir, _ := os.MkdirTemp("", taskId)
	progressPercentagesUpdatedToFile := []uint32{50, 100}
	progressPercentagesExpected := []uint32{25, 50, 100}
	progressFile := filepath.Join(testDir, "progress")
	err := writeProgressMessage(progressFile, taskId, taskVersionId, 25)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	runTest(t, taskId, testDir, false, progressPercentagesUpdatedToFile, progressPercentagesExpected, types.NO_STRATEGY)
}

func TestProgressMonitorNoUpload(t *testing.T) {
	t.Parallel()
	taskId := "task_no_upload"
	testDir, _ := os.MkdirTemp("", taskId)
	progressPercentagesUpdatedToFile := []uint32{25, 100}
	runTest(t, taskId, testDir, false, progressPercentagesUpdatedToFile, progressPercentagesUpdatedToFile, types.NO_STRATEGY)
}

func TestProgressDuplicateResult(t *testing.T) {
	t.Parallel()
	taskId := "task_duplicate"
	testDir, _ := os.MkdirTemp("", taskId)
	progressPercentagesUpdatedToFile := []uint32{25, 50, 50, 100}
	progressPercentagesExpected := []uint32{25, 50, 100}
	runTest(t, taskId, testDir, false, progressPercentagesUpdatedToFile, progressPercentagesExpected, types.NO_STRATEGY)
}

func TestProgressUploadSucceeded(t *testing.T) {
	t.Parallel()
	taskId := "task_upload"
	testDir, _ := os.MkdirTemp("", taskId)
	progressPercentagesUpdatedToFile := []uint32{25, 50, 100}
	progressPercentagesExpected := []uint32{25, 50, 100}
	runTest(t, taskId, testDir, false, progressPercentagesUpdatedToFile, progressPercentagesExpected, types.UPLOAD_STRATEGY)
}

func TestProgressMonitorPollProgress(t *testing.T) {
	t.Parallel()
	taskId := "poll_progress"
	testDir, _ := os.MkdirTemp("", taskId)
	progressPercentagesUpdatedToFile := []uint32{25, 50, 100}
	runTest(t, taskId, testDir, true, progressPercentagesUpdatedToFile, progressPercentagesUpdatedToFile, types.NO_STRATEGY)
}

func runTest(
	t *testing.T,
	taskId, testDir string,
	pollProgress bool,
	progressPercentagesUpdatedToFile []uint32,
	progressPercentagesExpected []uint32,
	resultHandlingStrategy types.ResultHandlingStrategy,
) {
	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.DebugLevel))
	zap.ReplaceGlobals(zapLogger.GetZapLogger())
	zap.RedirectStdLog(zapLogger.GetZapLogger())

	instanceId := "test-instance-id"
	ctx := context.Background()
	sendResultLock := sync.RWMutex{}
	defer os.RemoveAll(testDir)

	var progressMessages []*pb.ResultMetadataRequest
	sendResult := func(request *pb.ResultMetadataRequest) error {
		zap.L().Info("got result", zap.Uint32("percent complete", request.GetPercentComplete()))
		// For race test
		sendResultLock.Lock()
		progressMessages = append(progressMessages, request)
		sendResultLock.Unlock()
		return nil
	}

	progressFile := filepath.Join(testDir, "progress")
	taskTimeout := 5

	monitor := NewProgressMonitor(
		taskId,
		instanceId,
		testDir,
		"mock",
		progressFile,
		pollProgress,
		500*time.Millisecond,
		5*time.Minute,
		resultHandlingStrategy,
		sendResult,
	)
	if resultHandlingStrategy == types.UPLOAD_STRATEGY {
		monitor.UploadClient = upload.NewMockUploader(2)
		taskTimeout = 10
	}

	errCh := make(chan error, 1)
	err := monitor.Start(ctx)
	if err != nil {
		t.Fatal("failed to start progress monitor")
	}

	// Read TaskCompleted concurrently. The monitor's 100% path sends on the
	// unbuffered TaskCompleted channel from inside its watcher/polling
	// goroutine; if no reader is ready that send blocks and races with Stop()
	// closing the watcher. A dedicated reader guarantees the completion signal
	// is always consumed deterministically rather than depending on the main
	// goroutine happening to reach its receive in time.
	completed := make(chan error, 1)
	go func() {
		select {
		case e := <-monitor.TaskCompleted:
			completed <- e
		case <-time.After(time.Duration(taskTimeout) * time.Second):
			completed <- fmt.Errorf("expected task completion by now, timed out")
		}
	}()

	// If a progress file already exists at Start time, the monitor flushes it
	// via the pre-watch race-condition guard at the top of monitorProgress.
	// Wait until that initial flush has been observed before writing new
	// updates; otherwise a write can overwrite the file before the flush reads
	// it, losing the pre-written update (replaces the old fixed 500ms sleep).
	preexistingFlush := 0
	if _, statErr := os.Stat(progressFile); statErr == nil {
		preexistingFlush = 1
	}

	go func() {
		progressFile := filepath.Join(testDir, "progress")
		if preexistingFlush > 0 {
			waitForCountObserved(t, &sendResultLock, &progressMessages, preexistingFlush, 10*time.Second)
		}
		for i, p := range progressPercentagesUpdatedToFile {
			zap.L().Info("writing progress", zap.Uint32("percent complete", p))
			resultName := taskVersionId
			if resultHandlingStrategy == types.UPLOAD_STRATEGY {
				resultName = fmt.Sprintf("%s-%d", taskVersionId, i)
			}
			// Write the update, then wait until the monitor observes this
			// percentage before moving on. fsnotify can coalesce or drop an
			// event when consecutive writes land close together (the source
			// re-reads only the latest file content per event), which is the
			// root of the historical flakiness. Rather than rely on a fixed
			// sleep, re-touch the file until the update is observed: each rewrite
			// emits a fresh event, so a single coalesced/dropped event cannot
			// lose the update. Deduplicated updates (same name+percent) are
			// already-present and return immediately without extra rewrites.
			rewrite := func() error { return writeProgressMessage(progressFile, taskId, resultName, p) }
			if err = writeUntilPercentObserved(t, &sendResultLock, &progressMessages, p, rewrite, 10*time.Second); err != nil {
				errCh <- err
				return
			}
			zap.L().Info("Completed writing", zap.Uint32("percent complete", p))
		}
		errCh <- nil
	}()

	if err = <-errCh; err != nil {
		t.Fatal(err)
	}

	// Block until the monitor has reported completion (or timed out) so all
	// expected messages have been delivered before we assert on them.
	if err = <-completed; err != nil {
		t.Error(err)
	} else {
		t.Log("monitor successfully reported task completion")
	}

	if err = monitor.Stop(); err != nil {
		t.Fatalf("failed to stop progress monitor")
	}

	// For race test
	sendResultLock.RLock()
	defer sendResultLock.RUnlock()
	if len(progressMessages) != len(progressPercentagesExpected) {
		t.Fatalf("num received messages: %d, want: %d", len(progressMessages), len(progressPercentagesExpected))
	}

	for i, progressMessage := range progressMessages {
		percentComplete := progressMessage.GetPercentComplete()
		executionStatus := progressMessage.GetStatus()
		expectedMessage := fmt.Sprintf("{\"message\":\"i'm %d complete\"}", percentComplete)
		receivedMessage := string(progressMessage.GetMetadata().GetBody())

		if !slices.Contains(progressPercentagesExpected, percentComplete) {
			t.Fatalf("unexpected percent complete = %d", percentComplete)
		}

		if receivedMessage != expectedMessage {
			t.Fatalf("received message = %s, want = %s", receivedMessage, expectedMessage)
		}

		if percentComplete == 100 && executionStatus != pb.ExecutionStatus_COMPLETED {
			t.Fatalf("message status = %v, want = %v", executionStatus, pb.ExecutionStatus_COMPLETED)
		}

		if percentComplete != 100 && executionStatus != pb.ExecutionStatus_IN_PROGRESS {
			t.Fatalf("message status = %v, want = %v", executionStatus, pb.ExecutionStatus_IN_PROGRESS)
		}

		if resultHandlingStrategy == types.UPLOAD_STRATEGY {
			expectedResultName := fmt.Sprintf("%s-%d", taskVersionId, i)
			if progressMessage.ResultName == expectedResultName {
				t.Fatalf("message result name must not be same as version Id %v", taskVersionId)
			}
		}
		if resultHandlingStrategy == types.NO_STRATEGY && progressMessage.ResultName != taskVersionId {
			t.Fatalf("message result name= %v, want = %v", progressMessage.ResultName, taskVersionId)
		}
	}
}

// waitForCountObserved blocks until at least want progress messages have been
// captured, polling under the lock instead of sleeping a fixed duration.
func waitForCountObserved(
	t *testing.T,
	lock *sync.RWMutex,
	messages *[]*pb.ResultMetadataRequest,
	want int,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		lock.RLock()
		n := len(*messages)
		lock.RUnlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("timed out waiting for %d observed messages, have %d", want, n)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// percentObserved reports whether a progress message with the given
// percentComplete has been captured, taking the read lock.
func percentObserved(lock *sync.RWMutex, messages *[]*pb.ResultMetadataRequest, want uint32) bool {
	lock.RLock()
	defer lock.RUnlock()
	for _, m := range *messages {
		if m.GetPercentComplete() == want {
			return true
		}
	}
	return false
}

// writeUntilPercentObserved drives the monitor deterministically: it writes the
// update via rewrite() and waits for the monitor to capture a message with the
// given percentComplete. If the update is not observed promptly it rewrites the
// file again. Each rewrite emits a fresh fsnotify event, so a coalesced or
// dropped event cannot silently lose the update. This replaces brittle fixed
// time.Sleep spacing between writes. Deduplicated updates (already observed)
// return immediately.
func writeUntilPercentObserved(
	t *testing.T,
	lock *sync.RWMutex,
	messages *[]*pb.ResultMetadataRequest,
	want uint32,
	rewrite func() error,
	timeout time.Duration,
) error {
	t.Helper()
	if err := rewrite(); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	lastRewrite := time.Now()
	for {
		if percentObserved(lock, messages, want) {
			return nil
		}
		if time.Now().After(deadline) {
			t.Errorf("timed out waiting for progress percent %d to be observed", want)
			return nil
		}
		// Periodically re-touch the file so a single coalesced/dropped fsnotify
		// event cannot stall progress.
		if time.Since(lastRewrite) > 250*time.Millisecond {
			if err := rewrite(); err != nil {
				return err
			}
			lastRewrite = time.Now()
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func writeProgressMessage(progressFile string, taskId string, resultName string, progress uint32) error {
	progressData := Progress{
		TaskId:          taskId,
		Name:            resultName,
		PercentComplete: progress,
		Metadata: map[string]any{
			"message": fmt.Sprintf("i'm %d complete", progress),
		},
		LastUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}

	progressMessage, err := json.Marshal(progressData)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(progressFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(progressMessage); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
