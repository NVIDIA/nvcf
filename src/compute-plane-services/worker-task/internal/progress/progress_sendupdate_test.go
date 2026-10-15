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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/types"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/upload"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
)

const (
	updTaskId     = "send-update-task"
	updInstanceId = "send-update-instance"
	updInstance   = "mock-instance-type"
)

// newSendCapture returns a Monitor wired with a capturing SendResult func plus
// a pointer to the captured requests and a pointer to the call count. The
// Monitor is constructed directly (not via NewProgressMonitor + Start) so no
// watcher, ticker, or HTTP/server loop is ever started, and no port is bound.
func newSendCapture(t *testing.T, strategy types.ResultHandlingStrategy) (*Monitor, *[]*pb.ResultMetadataRequest, *int) {
	t.Helper()
	captured := make([]*pb.ResultMetadataRequest, 0)
	calls := 0
	m := &Monitor{
		taskId:                 updTaskId,
		instanceId:             updInstanceId,
		instanceType:           updInstance,
		resultHandlingStrategy: strategy,
		// Buffered so a 100% (completed) update never blocks on the
		// unbuffered-by-default TaskCompleted channel during a direct call.
		TaskCompleted: make(chan error, 1),
		SendResult: func(req *pb.ResultMetadataRequest) error {
			calls++
			captured = append(captured, req)
			return nil
		},
	}
	return m, &captured, &calls
}

func nowStamp() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// TaskId mismatch between incoming progress and the monitor's taskId -> error,
// and SendResult must not be invoked.
func TestSendProgressUpdate_TaskIdMismatch(t *testing.T) {
	t.Parallel()
	m, _, calls := newSendCapture(t, types.NO_STRATEGY)

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          "some-other-task",
		Name:            "result-a",
		PercentComplete: 25,
		LastUpdatedAt:   nowStamp(),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrInvalidTaskId)
	assert.Equal(t, 0, *calls, "SendResult must not be called on taskId mismatch")
}

// Empty taskId on the incoming progress is adopted from the monitor and the
// update proceeds (happy-ish path for the defaulting branch).
func TestSendProgressUpdate_EmptyTaskIdAdopted(t *testing.T) {
	t.Parallel()
	m, captured, calls := newSendCapture(t, types.NO_STRATEGY)

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          "",
		Name:            "result-a",
		PercentComplete: 25,
		LastUpdatedAt:   nowStamp(),
	})

	require.NoError(t, err)
	require.Equal(t, 1, *calls)
	assert.Equal(t, updTaskId, (*captured)[0].GetTaskId())
}

// Invalid (unparseable) LastUpdatedAt -> ErrInvalidTimestamp, no send.
func TestSendProgressUpdate_InvalidTimestamp(t *testing.T) {
	t.Parallel()
	m, _, calls := newSendCapture(t, types.NO_STRATEGY)

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          updTaskId,
		Name:            "result-a",
		PercentComplete: 25,
		LastUpdatedAt:   "not-a-timestamp",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrInvalidTimestamp)
	assert.Equal(t, 0, *calls)
}

// Percentage out of range (0 and >100) delegates to isValidPercentage ->
// ErrOutOfRangePercentageComplete, no send.
func TestSendProgressUpdate_PercentageOutOfRange(t *testing.T) {
	t.Parallel()
	for _, pct := range []uint32{0, 101} {
		m, _, calls := newSendCapture(t, types.NO_STRATEGY)
		err := m.sendProgressUpdate(context.Background(), Progress{
			TaskId:          updTaskId,
			Name:            "result-a",
			PercentComplete: pct,
			LastUpdatedAt:   nowStamp(),
		})
		require.Error(t, err, "pct=%d should be rejected", pct)
		assert.ErrorIs(t, err, types.ErrOutOfRangePercentageComplete)
		assert.Equal(t, 0, *calls)
	}
}

// Percentage decrease relative to the last reported percentage ->
// ErrInvalidPercentageComplete, no send.
func TestSendProgressUpdate_PercentageDecrease(t *testing.T) {
	t.Parallel()
	m, _, calls := newSendCapture(t, types.NO_STRATEGY)
	// Seed a prior higher percentage with a different name so the duplicate
	// short-circuit does not fire first.
	m.lastResult = LastResult{LastResultName: "earlier", LastCompletedPercentage: 50}

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          updTaskId,
		Name:            "result-a",
		PercentComplete: 25,
		LastUpdatedAt:   nowStamp(),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrInvalidPercentageComplete)
	assert.Equal(t, 0, *calls)
}

// Duplicate update (same name AND same percentage as the last result) is a
// no-op: returns nil and never calls SendResult.
func TestSendProgressUpdate_DuplicateNoOp(t *testing.T) {
	t.Parallel()
	m, _, calls := newSendCapture(t, types.NO_STRATEGY)
	m.lastResult = LastResult{LastResultName: "result-a", LastCompletedPercentage: 25}

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          updTaskId,
		Name:            "result-a",
		PercentComplete: 25,
		LastUpdatedAt:   nowStamp(),
	})

	require.NoError(t, err)
	assert.Equal(t, 0, *calls, "duplicate update must not send")
}

// Happy path with an in-progress percentage: SendResult called exactly once
// with the expected request, status IN_PROGRESS, and lastResult updated.
func TestSendProgressUpdate_HappyInProgress(t *testing.T) {
	t.Parallel()
	m, captured, calls := newSendCapture(t, types.NO_STRATEGY)

	stamp := nowStamp()
	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          updTaskId,
		Name:            "result-a",
		PercentComplete: 42,
		Metadata:        map[string]any{"k": "v"},
		LastUpdatedAt:   stamp,
	})

	require.NoError(t, err)
	require.Equal(t, 1, *calls, "SendResult must be called exactly once")

	req := (*captured)[0]
	assert.Equal(t, updTaskId, req.GetTaskId())
	assert.Equal(t, updInstanceId, req.GetInstanceId())
	assert.Equal(t, updInstance, req.GetInstanceType())
	assert.Equal(t, uint32(42), req.GetPercentComplete())
	assert.Equal(t, "result-a", req.GetResultName())
	assert.Equal(t, pb.ExecutionStatus_IN_PROGRESS, req.GetStatus())
	assert.JSONEq(t, `{"k":"v"}`, string(req.GetMetadata().GetBody()))

	// lastResult is advanced after a successful send.
	assert.Equal(t, "result-a", m.lastResult.LastResultName)
	assert.Equal(t, uint32(42), m.lastResult.LastCompletedPercentage)
}

// Happy path at 100%: status COMPLETED and nil is signalled on TaskCompleted.
func TestSendProgressUpdate_HappyCompleted(t *testing.T) {
	t.Parallel()
	m, captured, calls := newSendCapture(t, types.NO_STRATEGY)

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          updTaskId,
		Name:            "final",
		PercentComplete: 100,
		LastUpdatedAt:   nowStamp(),
	})

	require.NoError(t, err)
	require.Equal(t, 1, *calls)
	assert.Equal(t, pb.ExecutionStatus_COMPLETED, (*captured)[0].GetStatus())

	// TaskCompleted is buffered; a nil must have been signalled.
	select {
	case got := <-m.TaskCompleted:
		assert.NoError(t, got)
	default:
		t.Fatal("expected nil completion signal on TaskCompleted")
	}
}

// When SendResult itself fails, the error is wrapped as an internal error and
// lastResult is NOT advanced.
func TestSendProgressUpdate_SendResultError(t *testing.T) {
	t.Parallel()
	sendErr := errors.New("transport down")
	m := &Monitor{
		taskId:                 updTaskId,
		instanceId:             updInstanceId,
		instanceType:           updInstance,
		resultHandlingStrategy: types.NO_STRATEGY,
		TaskCompleted:          make(chan error, 1),
		SendResult: func(*pb.ResultMetadataRequest) error {
			return sendErr
		},
	}

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          updTaskId,
		Name:            "result-a",
		PercentComplete: 30,
		LastUpdatedAt:   nowStamp(),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, sendErr)
	assert.Empty(t, m.lastResult.LastResultName, "lastResult must not advance when send fails")
}

// ----------------------------------------------------------------------------
// Cheap helper coverage: checkProgressLastUpdateTime, buildResultResponse,
// SendExceedMaxDurationResult, SendErrorProgressMessage.

func TestCheckProgressLastUpdateTime(t *testing.T) {
	t.Parallel()

	t.Run("invalid timestamp", func(t *testing.T) {
		t.Parallel()
		m := &Monitor{taskId: updTaskId, progressUpdateTimeout: time.Minute}
		err := m.checkProgressLastUpdateTime(Progress{TaskId: updTaskId, LastUpdatedAt: "bad"})
		require.Error(t, err)
		assert.ErrorIs(t, err, types.ErrInvalidTimestamp)
	})

	t.Run("stale update not yet complete", func(t *testing.T) {
		t.Parallel()
		m := &Monitor{taskId: updTaskId, progressUpdateTimeout: time.Second}
		stale := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		err := m.checkProgressLastUpdateTime(Progress{TaskId: updTaskId, PercentComplete: 50, LastUpdatedAt: stale})
		require.Error(t, err)
		assert.ErrorIs(t, err, types.ErrNoProgressFileUpdates)
	})

	t.Run("stale but completed is fine", func(t *testing.T) {
		t.Parallel()
		m := &Monitor{taskId: updTaskId, progressUpdateTimeout: time.Second}
		stale := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		err := m.checkProgressLastUpdateTime(Progress{TaskId: updTaskId, PercentComplete: 100, LastUpdatedAt: stale})
		require.NoError(t, err)
	})

	t.Run("fresh update records lastUpdate", func(t *testing.T) {
		t.Parallel()
		m := &Monitor{taskId: updTaskId, progressUpdateTimeout: time.Hour}
		err := m.checkProgressLastUpdateTime(Progress{TaskId: updTaskId, PercentComplete: 50, LastUpdatedAt: nowStamp()})
		require.NoError(t, err)
		assert.False(t, m.lastResult.LastUpdate.IsZero())
	})
}

func TestBuildResultResponse(t *testing.T) {
	t.Parallel()

	t.Run("empty metadata defaults to empty json object", func(t *testing.T) {
		t.Parallel()
		req, err := buildResultResponse(updInstanceId, updInstance, Progress{
			TaskId:          updTaskId,
			Name:            "n",
			PercentComplete: 10,
		})
		require.NoError(t, err)
		assert.Equal(t, "{}", string(req.GetMetadata().GetBody()))
		assert.Equal(t, updTaskId, req.GetTaskId())
		assert.Equal(t, uint32(10), req.GetPercentComplete())
	})

	t.Run("non-empty metadata is marshalled", func(t *testing.T) {
		t.Parallel()
		req, err := buildResultResponse(updInstanceId, updInstance, Progress{
			TaskId:   updTaskId,
			Name:     "n",
			Metadata: map[string]any{"a": float64(1)},
		})
		require.NoError(t, err)
		assert.JSONEq(t, `{"a":1}`, string(req.GetMetadata().GetBody()))
	})
}

func TestSendExceedMaxDurationResult(t *testing.T) {
	t.Parallel()
	var captured *pb.ResultMetadataRequest
	m := &Monitor{
		taskId:       updTaskId,
		instanceId:   updInstanceId,
		instanceType: updInstance,
		SendResult: func(req *pb.ResultMetadataRequest) error {
			captured = req
			return nil
		},
	}

	require.NoError(t, m.SendExceedMaxDurationResult())
	require.NotNil(t, captured)
	assert.Equal(t, pb.ExecutionStatus_EXCEEDED_MAX_RUNTIME_DURATION, captured.GetStatus())
	assert.Equal(t, updTaskId, captured.GetTaskId())
	assert.Equal(t, types.ErrMaxRunTimeDurationExceeded.Error(), captured.GetErrorDetails().GetDetail())
}

func TestSendExceedMaxDurationResult_SendError(t *testing.T) {
	t.Parallel()
	sendErr := errors.New("boom")
	m := &Monitor{
		taskId:       updTaskId,
		instanceId:   updInstanceId,
		instanceType: updInstance,
		SendResult:   func(*pb.ResultMetadataRequest) error { return sendErr },
	}
	assert.ErrorIs(t, m.SendExceedMaxDurationResult(), sendErr)
}

func TestSendErrorProgressMessage(t *testing.T) {
	t.Parallel()
	var captured *pb.ResultMetadataRequest
	m := &Monitor{
		taskId:       updTaskId,
		instanceId:   updInstanceId,
		instanceType: updInstance,
		SendResult: func(req *pb.ResultMetadataRequest) error {
			captured = req
			return nil
		},
	}

	m.SendErrorProgressMessage("something failed")
	require.NotNil(t, captured)
	assert.Equal(t, pb.ExecutionStatus_ERRORED, captured.GetStatus())
	assert.Equal(t, "something failed", captured.GetErrorDetails().GetDetail())
	assert.Equal(t, updTaskId, captured.GetTaskId())
}

func TestSendErrorProgressMessage_SendErrorIsSwallowed(t *testing.T) {
	t.Parallel()
	// SendErrorProgressMessage logs but does not return on send failure; this
	// exercises that branch without panicking.
	m := &Monitor{
		taskId:       updTaskId,
		instanceId:   updInstanceId,
		instanceType: updInstance,
		SendResult:   func(*pb.ResultMetadataRequest) error { return errors.New("nope") },
	}
	assert.NotPanics(t, func() { m.SendErrorProgressMessage("detail") })
}

// ----------------------------------------------------------------------------
// UPLOAD_STRATEGY branch of sendProgressUpdate. Uses the in-package
// MockUploader so no real upload, no network, and no port binding occurs.

func newUploadMonitor(t *testing.T) (*Monitor, *int) {
	t.Helper()
	calls := 0
	m := &Monitor{
		taskId:                 updTaskId,
		instanceId:             updInstanceId,
		instanceType:           updInstance,
		resultsDir:             t.TempDir(),
		resultHandlingStrategy: types.UPLOAD_STRATEGY,
		UploadClient:           upload.NewMockUploader(2),
		TaskCompleted:          make(chan error, 1),
		SendResult: func(*pb.ResultMetadataRequest) error {
			calls++
			return nil
		},
	}
	return m, &calls
}

// UPLOAD_STRATEGY happy path: a unique versionId suffix is appended to the
// result name and SendResult is called once.
func TestSendProgressUpdate_UploadStrategy_AppendsVersionId(t *testing.T) {
	t.Parallel()
	m, calls := newUploadMonitor(t)
	var captured *pb.ResultMetadataRequest
	m.SendResult = func(req *pb.ResultMetadataRequest) error {
		*calls++
		captured = req
		return nil
	}

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          updTaskId,
		Name:            "checkpoint",
		PercentComplete: 40,
		LastUpdatedAt:   nowStamp(),
	})

	require.NoError(t, err)
	require.NotNil(t, captured)
	// ResultName must be name + "_" + uuid, i.e. not the bare name.
	assert.NotEqual(t, "checkpoint", captured.GetResultName())
	assert.Contains(t, captured.GetResultName(), "checkpoint_")
}

// UPLOAD_STRATEGY with an invalid result name surfaces a validation error
// before any send.
func TestSendProgressUpdate_UploadStrategy_InvalidName(t *testing.T) {
	t.Parallel()
	m, calls := newUploadMonitor(t)

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          updTaskId,
		Name:            "../escape",
		PercentComplete: 40,
		LastUpdatedAt:   nowStamp(),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrInvalidPrefix)
	assert.Equal(t, 0, *calls)
}

// UPLOAD_STRATEGY duplicate name (valid name, but equal to last result name)
// is rejected with ErrDuplicateResultName.
func TestSendProgressUpdate_UploadStrategy_DuplicateName(t *testing.T) {
	t.Parallel()
	m, calls := newUploadMonitor(t)
	// Different percentage so the generic duplicate short-circuit (name AND
	// percentage equal) does not fire; only the UPLOAD-specific duplicate-name
	// check should reject this.
	m.lastResult = LastResult{LastResultName: "checkpoint", LastCompletedPercentage: 10}

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          updTaskId,
		Name:            "checkpoint",
		PercentComplete: 40,
		LastUpdatedAt:   nowStamp(),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrDuplicateResultName)
	assert.Equal(t, 0, *calls)
}

// UPLOAD_STRATEGY at 100% waits for outstanding uploads then sends COMPLETED.
func TestSendProgressUpdate_UploadStrategy_Completed(t *testing.T) {
	t.Parallel()
	m, calls := newUploadMonitor(t)
	var captured *pb.ResultMetadataRequest
	m.SendResult = func(req *pb.ResultMetadataRequest) error {
		*calls++
		captured = req
		return nil
	}

	err := m.sendProgressUpdate(context.Background(), Progress{
		TaskId:          updTaskId,
		Name:            "final",
		PercentComplete: 100,
		LastUpdatedAt:   nowStamp(),
	})

	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Equal(t, pb.ExecutionStatus_COMPLETED, captured.GetStatus())

	select {
	case got := <-m.TaskCompleted:
		assert.NoError(t, got)
	default:
		t.Fatal("expected completion signal")
	}
}

// ----------------------------------------------------------------------------
// Stop branches that do not require a running goroutine or any port binding.

func TestStop_WatcherNilNoop(t *testing.T) {
	t.Parallel()
	// Watcher mode but watcher never created -> Stop returns nil without panic.
	m := &Monitor{pollProgress: false, watcher: nil}
	assert.NoError(t, m.Stop())
}

func TestStop_PollingStopsTicker(t *testing.T) {
	t.Parallel()
	// Polling mode: Stop just stops the ticker. Create the ticker directly and
	// never launch pollingProgress, so no file watching, goroutine, or port.
	m := &Monitor{
		pollProgress:       true,
		pollProgressTicker: time.NewTicker(time.Hour),
	}
	assert.NoError(t, m.Stop())
}

// ----------------------------------------------------------------------------
// ParseProgressFile: success, missing file, and invalid JSON. Pure file I/O.

func TestParseProgressFile(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		fp := filepath.Join(dir, "progress")
		data, _ := json.Marshal(Progress{
			TaskId:          updTaskId,
			Name:            "n",
			PercentComplete: 33,
			LastUpdatedAt:   nowStamp(),
		})
		require.NoError(t, os.WriteFile(fp, data, 0o644))

		got, err := ParseProgressFile(fp)
		require.NoError(t, err)
		assert.Equal(t, updTaskId, got.TaskId)
		assert.Equal(t, uint32(33), got.PercentComplete)
	})

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		got, err := ParseProgressFile(filepath.Join(t.TempDir(), "does-not-exist"))
		require.Error(t, err)
		assert.ErrorIs(t, err, types.ErrMissingProgressFile)
		assert.Empty(t, got.TaskId)
	})

	t.Run("invalid json", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		fp := filepath.Join(dir, "progress")
		require.NoError(t, os.WriteFile(fp, []byte("{not-json"), 0o644))

		_, err := ParseProgressFile(fp)
		require.Error(t, err)
		assert.ErrorIs(t, err, types.ErrInvalidProgressFile)
	})
}

// ----------------------------------------------------------------------------
// isValidProgressName invalid branches (the existing progress_test.go covers
// the valid cases / different angles; these target the error returns).

func TestIsValidProgressName_InvalidBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  error
	}{
		{"empty", "", types.ErrInvalidLength},
		{"too long", string(make([]byte, 191)), types.ErrInvalidLength},
		{"current dir prefix", "./foo", types.ErrInvalidPrefix},
		{"parent dir prefix", "../foo", types.ErrInvalidPrefix},
		{"bad characters", "bad name", types.ErrInvalidCharacters},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, isValidProgressName(tc.input), tc.want)
		})
	}
}
