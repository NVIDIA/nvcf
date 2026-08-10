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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"go.uber.org/zap"

	taskMetrics "github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/types"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/upload"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
	sharedTypes "github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
)

const initializeTimeout = 2 * time.Hour

type Progress struct {
	TaskId          string         `json:"taskId"`
	Name            string         `json:"name"`
	PercentComplete uint32         `json:"percentComplete"`
	Metadata        map[string]any `json:"metadata"`
	LastUpdatedAt   string         `json:"lastUpdatedAt"`
}

type LastResult struct {
	LastResultName          string
	LastCompletedPercentage uint32
	LastUpdate              time.Time
}

type Monitor struct {
	taskId                 string
	instanceId             string
	instanceType           string
	resultsDir             string
	progressFile           string
	resultHandlingStrategy types.ResultHandlingStrategy
	watcher                *fsnotify.Watcher
	UploadClient           upload.Uploader
	TaskCompleted          chan error
	SendResult             func(request *pb.ResultMetadataRequest) error
	lastResult             LastResult
	progressUpdateTimeout  time.Duration
	pollProgress           bool
	pollProgressInterval   time.Duration
	pollProgressTicker     *time.Ticker
}

func NewProgressMonitor(
	taskId, instanceId, resultsDir, instanceType, progressFile string,
	pollProgress bool,
	pollProgressInterval time.Duration,
	progressUpdateTimeout time.Duration,
	resultHandlingStrategy types.ResultHandlingStrategy,
	sendResult func(request *pb.ResultMetadataRequest) error) *Monitor {
	return &Monitor{
		taskId:                 taskId,
		instanceId:             instanceId,
		instanceType:           instanceType,
		resultsDir:             resultsDir,
		progressFile:           progressFile,
		SendResult:             sendResult,
		resultHandlingStrategy: resultHandlingStrategy,
		TaskCompleted:          make(chan error),
		pollProgress:           pollProgress,
		pollProgressInterval:   pollProgressInterval,
		progressUpdateTimeout:  progressUpdateTimeout,
	}
}

func (m *Monitor) Start(ctx context.Context) error {
	zap.L().Info("Starting progress monitor")
	if m.resultHandlingStrategy == types.UPLOAD_STRATEGY && m.UploadClient == nil {
		zap.L().Error("progress monitor was not configured with upload client for task UPLOAD strategy")
		return types.ErrInternal
	}
	if m.watcher != nil {
		return nil
	}

	if !m.pollProgress {
		zap.L().Info("Run progress monitor in watcher mode")
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			return types.ErrInternal
		}

		progressFileDir := filepath.Dir(m.progressFile)
		zap.L().Info("watching dir", zap.String("progressFileDir", progressFileDir))
		err = watcher.Add(progressFileDir)
		if err != nil {
			return types.ErrInternal
		}
		m.watcher = watcher
		go m.monitorProgress(ctx)
	} else {
		zap.L().Info("Run progress monitor in polling mode")
		m.pollProgressTicker = time.NewTicker(m.pollProgressInterval)
		go m.pollingProgress(ctx)
	}

	zap.L().Info("Successfully started progress monitor")
	return nil
}

func (m *Monitor) Stop() error {
	zap.L().Info("Stopping progress monitor")

	if m.pollProgress {
		m.pollProgressTicker.Stop()
	} else {
		if m.watcher == nil {
			return nil
		}

		if err := m.watcher.Close(); err != nil {
			return err
		}
	}

	zap.L().Info("Progress monitor is stopped")
	return nil
}

func (m *Monitor) sendProgressUpdate(ctx context.Context, progress Progress) error {
	logger := zap.L().With(zap.String("task id", m.taskId), zap.String("progress name", progress.Name))

	// Task ID validation
	if progress.TaskId == "" {
		logger.Info("Progress task metadata does not have task ID, setting it")
		progress.TaskId = m.taskId
	} else if progress.TaskId != m.taskId {
		return sharedTypes.NewInternalError(fmt.Errorf("taskId '%s' is invalid: %w", progress.TaskId, types.ErrInvalidTaskId))
	}

	// LastUpdatedAt should be in RFC3339Nano format
	lastUpdatedAt, err := time.Parse(time.RFC3339Nano, progress.LastUpdatedAt)
	if err != nil {
		logger.Error("invalid timestamp format", zap.Error(err))
		return sharedTypes.NewUserActionableError(types.ErrInvalidTimestamp)
	}

	// Ensure we don't update twice
	if progress.Name == m.lastResult.LastResultName &&
		progress.PercentComplete == m.lastResult.LastCompletedPercentage {
		logger.Info("No progress, not sending progress update")
		return nil
	}

	// Task percentage validation
	err = isValidPercentage(progress.PercentComplete, m.lastResult.LastCompletedPercentage)
	if err != nil {
		return sharedTypes.NewUserActionableError(
			fmt.Errorf("percentComplete '%d' is invalid: %w", progress.PercentComplete, err),
		)
	}

	// Build and validate result message
	progressMessage, err := buildResultResponse(m.instanceId, m.instanceType, progress)
	if err != nil {
		logger.Error("failed to build result response, not sending progress update", zap.Error(err))
		return sharedTypes.NewInternalError(err)
	}

	if m.resultHandlingStrategy == types.UPLOAD_STRATEGY {
		// Send error result to NVCT when invalid result name is found
		err := isValidProgressName(progress.Name)
		if err == nil && (progress.Name == m.lastResult.LastResultName) {
			err = types.ErrDuplicateResultName
		}
		if err != nil {
			logger.Error("invalid result name", zap.Error(err))
			return sharedTypes.NewUserActionableError(
				fmt.Errorf("result name: '%s' is invalid: %w", progress.Name, err),
			)
		}
		// Add a UUID4 suffix to the progress name to create a unique versionId
		resultVersionId := fmt.Sprintf("%s_%s", progress.Name, uuid.New().String())
		progressMessage.ResultName = resultVersionId

		// Upload checkpoints results during execution
		zap.L().Info(
			"Triggered result upload",
			zap.String("task id", progress.TaskId),
			zap.String("result name", progress.Name),
			zap.String("version id", resultVersionId),
		)

		m.UploadClient.Submit(ctx, filepath.Join(m.resultsDir, progress.Name), resultVersionId)
	}

	taskMetrics.ResultCounter.Inc()

	taskCompleted := false
	if progress.PercentComplete == 100 {
		// Task has completed
		logger.Info(
			"Task completed",
			zap.String("result name", progress.Name),
		)
		progressMessage.Status = pb.ExecutionStatus_COMPLETED
		if m.resultHandlingStrategy == types.UPLOAD_STRATEGY {
			// Wait all ongoing upload jobs to finish
			// before sending COMPLETED results
			if err = m.UploadClient.Wait(); err != nil {
				return sharedTypes.NewInternalError(types.ErrFailedToUploadResults)
			}
		}

		taskCompleted = true
	} else {
		progressMessage.Status = pb.ExecutionStatus_IN_PROGRESS
		zap.L().Info(
			"Got new task result",
			zap.String("task id", progress.TaskId),
			zap.String("result name", progress.Name),
		)
	}

	if err = m.SendResult(progressMessage); err != nil {
		zap.L().Error("failed to send result", zap.String("task id", progress.TaskId), zap.Error(err))
		return sharedTypes.NewInternalError(err)
	}

	m.lastResult = LastResult{progress.Name, progress.PercentComplete, lastUpdatedAt}

	// Signal completion only after the result is successfully sent. Doing this
	// via a defer would fire even when SendResult fails, sending a spurious
	// "success" (nil) that races with monitorProgress sending the real error
	// onto the same channel -> double-send / send-on-closed-channel panic.
	if taskCompleted {
		m.TaskCompleted <- nil
	}
	return nil
}

// Periodically check lastUpdateAt field in progress file and
// report error if there's no update within some timeout.
func (m *Monitor) checkProgressLastUpdateTime(progress Progress) error {
	lastUpdatedAt, err := time.Parse(time.RFC3339Nano, progress.LastUpdatedAt)
	if err != nil {
		zap.L().Error("invalid timestamp", zap.String("task id", progress.TaskId), zap.Error(err))
		return sharedTypes.NewUserActionableError(types.ErrInvalidTimestamp)
	}

	if time.Since(lastUpdatedAt) > m.progressUpdateTimeout && progress.PercentComplete != 100 {
		return sharedTypes.NewUserActionableError(types.ErrNoProgressFileUpdates)
	}

	m.lastResult.LastUpdate = lastUpdatedAt
	return nil
}

func (m *Monitor) monitorProgress(ctx context.Context) {
	zap.L().Info("Start task progress monitor job")
	startTime := time.Now()
	ticker := time.NewTicker(m.progressUpdateTimeout)
	defer ticker.Stop()

	// to address race condition where task might have completed even before monitoring starts,
	// check if file exists, report progress, and then send watcher reports. The progress might
	// have completed even before watcher started, and we need to flush the progress
	progress, err := ParseProgressFile(m.progressFile)
	if err == nil {
		sendErr := m.sendProgressUpdate(ctx, progress)
		if sendErr != nil {
			m.TaskCompleted <- sendErr
			return
		}
	}

	// TODO(csaikia): need a way to throttle too many write events on the file, also this approach
	// can't guarantee that it is able to publish every update on file for very frequent updates since
	// the file could have been overwritten
	for {
		select {
		case <-ctx.Done():
			// Main context is cancelled.
			zap.L().Info("Stop progress monitor job")
			return

		case err, ok := <-m.watcher.Errors:
			if !ok {
				// Errors channel has closed indicating watcher has closed.
				zap.L().Info("Progress monitor job is terminated")
				return
			}
			zap.L().Warn("progress watcher error", zap.Error(err))

		case event, ok := <-m.watcher.Events:
			if !ok {
				// Events channel has closed indicating watcher has closed.
				zap.L().Info("Progress monitor job is terminated")
				return
			}

			// Only watch creates/writes to the progress file.
			if event.Name != m.progressFile || !(event.Op == fsnotify.Create || event.Op == fsnotify.Write) {
				continue
			}

			progress, err := ParseProgressFile(m.progressFile)
			if err != nil {
				m.TaskCompleted <- err
				return
			}

			if err = m.sendProgressUpdate(ctx, progress); err != nil {
				m.TaskCompleted <- err
				return
			}

		case <-ticker.C:
			progress, err := ParseProgressFile(m.progressFile)
			if err != nil {
				if !errors.Is(err, types.ErrMissingProgressFile) {
					m.TaskCompleted <- err
					return
				}

				// Give task container or helm chart at most 2 hours
				// to initialize and create the progress file.
				if time.Since(startTime) > initializeTimeout {
					m.TaskCompleted <- err
					return
				}
				continue
			}

			if err := m.checkProgressLastUpdateTime(progress); err != nil {
				m.TaskCompleted <- err
				return
			}
		}
	}
}

// Fsnotiy does not work for network storage location.
// Need to actively poll progress file for helm chart deployment
// in non GFN clusters using NVMesh as shared result directory.
func (m *Monitor) pollingProgress(ctx context.Context) {
	zap.L().Info("Start task progress polling job")

	for {
		select {
		case <-ctx.Done():
			// Main context is cancelled.
			zap.L().Info("Stop progress polling job")
			return

		case _, ok := <-m.pollProgressTicker.C:
			if !ok {
				zap.L().Info("Progress polling job is terminated")
				return
			}

			progress, err := ParseProgressFile(m.progressFile)
			if err != nil {
				m.TaskCompleted <- err
				return
			}

			if err := m.checkProgressLastUpdateTime(progress); err != nil {
				m.TaskCompleted <- err
				return
			}

			if err := m.sendProgressUpdate(ctx, progress); err != nil {
				m.TaskCompleted <- err
				return
			}
		}
	}
}

// Send an one-time result in current progress file.
// Should be only invoked on task failures.
func (m *Monitor) SendExceedMaxDurationResult() error {
	progress := &Progress{
		TaskId: m.taskId,
	}

	progressMessage, err := buildResultResponse(m.instanceId, m.instanceType, *progress)
	if err != nil {
		return err
	}

	progressMessage.Status = pb.ExecutionStatus_EXCEEDED_MAX_RUNTIME_DURATION
	progressMessage.Result = &pb.ResultMetadataRequest_ErrorDetails{
		ErrorDetails: &pb.ErrorDetails{
			Detail: types.ErrMaxRunTimeDurationExceeded.Error(),
		},
	}

	return m.SendResult(progressMessage)
}

func ParseProgressFile(filePath string) (Progress, error) {
	zap.L().Debug("Reading task progress file", zap.String("progress file", filePath))

	progress := Progress{}
	if err := backoff.Retry(func() error {
		progressData, err := os.ReadFile(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				return sharedTypes.NewUserActionableError(types.ErrMissingProgressFile)
			}
			zap.L().Error("failed to read progress file", zap.Error(err))
			return sharedTypes.NewInternalError(err)
		}

		if err = json.Unmarshal(progressData, &progress); err != nil {
			zap.L().Info("failed to parse progress file", zap.Error(err))
			return sharedTypes.NewUserActionableError(types.ErrInvalidProgressFile)
		}

		return nil

	}, backoff.WithMaxRetries(backoff.NewConstantBackOff(50*time.Millisecond), 10)); err != nil {
		return progress, err
	}

	return progress, nil
}

func buildResultResponse(instanceId, instanceType string, progress Progress) (*pb.ResultMetadataRequest, error) {
	var metadataBody []byte
	if len(progress.Metadata) == 0 {
		metadataBody = []byte("{}")
	} else {
		var err error
		metadataBody, err = json.Marshal(progress.Metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to convert result metadata: %w", err)
		}
	}

	return &pb.ResultMetadataRequest{
		TaskId:          progress.TaskId,
		InstanceId:      instanceId,
		InstanceType:    instanceType,
		PercentComplete: &progress.PercentComplete,
		ResultName:      progress.Name,
		Result: &pb.ResultMetadataRequest_Metadata{
			Metadata: &pb.ResultMetadata{
				Body: metadataBody,
			},
		},
	}, nil
}

func isValidProgressName(input string) error {
	// Apply NGC versionID restrictions and S3 key restrictions
	// Reference: https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-keys.html
	if len(input) < 1 || len(input) > 190 {
		return types.ErrInvalidLength
	}

	if strings.HasPrefix(input, "./") || strings.HasPrefix(input, "../") {
		return types.ErrInvalidPrefix
	}

	re := regexp.MustCompile(`^[a-zA-Z0-9!_\-.*'()]+$`)
	if !re.MatchString(input) {
		return types.ErrInvalidCharacters
	}

	return nil
}

func (m *Monitor) SendErrorProgressMessage(detail string) {
	progress := &Progress{
		TaskId: m.taskId,
	}

	progressMessage, err := buildResultResponse(m.instanceId, m.instanceType, *progress)
	if err != nil {
		return
	}
	progressMessage.Status = pb.ExecutionStatus_ERRORED
	progressMessage.Result = &pb.ResultMetadataRequest_ErrorDetails{
		ErrorDetails: &pb.ErrorDetails{
			Detail: detail,
		},
	}
	if err := m.SendResult(progressMessage); err != nil {
		zap.L().Error("failed to send result", zap.String("task id", progressMessage.TaskId), zap.Error(err))
	}
}

func isValidPercentage(progressPercentage uint32, prevProgressPercentage uint32) error {
	if progressPercentage == 0 || progressPercentage > 100 {
		return types.ErrOutOfRangePercentageComplete
	}
	if progressPercentage < prevProgressPercentage {
		return types.ErrInvalidPercentageComplete
	}
	return nil
}
