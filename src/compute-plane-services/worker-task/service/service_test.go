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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/progress"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/types"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/worker"
)

var ctx context.Context
var zapLogger *logs.ZapLogger
var workerConfig configs.Config

func TestMain(m *testing.M) {
	ctx = context.Background()

	zapLogger = logs.NewZapLogger(zap.NewAtomicLevelAt(zap.InfoLevel))
	zap.RedirectStdLog(zapLogger.GetZapLogger())

	zap.L().Info("======== NVCT E2E Tests ========")

	issuedAt := time.Now().Unix()
	workerToken, err := testutils.GenerateJWT(issuedAt)
	if err != nil {
		zap.L().Fatal("failed to generate worker token")
	}

	workerConfig = configs.Config{
		NcaId:                    "test-nca-id",
		AccountName:              "test-account",
		NVCTWorkerToken:          workerToken,
		NVCTFqdnGrpc:             "http://localhost:9092",
		TaskId:                   "10b076eb-b6d2-4cd9-878b-a3614a931570",
		TaskName:                 "test-task",
		InstanceId:               "test-instance",
		InstanceTypeName:         "test-instance-type",
		HealthPort:               18080,
		MaxRunTime:               "PT1H",
		OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
		TracingAccessToken:       "fake-tracing-token",
		TerminationGracePeriod:   "PT1H",
		ResultHandlingStrategy:   types.NO_STRATEGY,
		SharedConfigDir:          "/tmp/config/shared",
	}

	nvctServer := testutils.NewMockNvctServer(
		workerConfig.TaskId,
		workerConfig.InstanceId,
		workerConfig.InstanceTypeName,
		"", time.Hour,
	)
	if err = nvctServer.Run("0.0.0.0:9092"); err != nil {
		zap.L().Fatal("failed to start mock nvct server", zap.Error(err))
	}
	defer nvctServer.Shutdown()

	exitCode := m.Run()

	zap.L().Info("======== NVCT E2E Tests Complete ========")
	zapLogger.Close()
	os.Exit(exitCode)
}

func TestE2E(t *testing.T) {
	testDir := t.TempDir()
	curWorkerConfig := workerConfig
	curWorkerConfig.ResultsDir = testDir
	curWorkerConfig.ProgressFilePath = filepath.Join(curWorkerConfig.ResultsDir, "progress")

	nvctWorker, err := worker.NewNVCTWorker(
		ctx,
		zapLogger,
		curWorkerConfig,
	)
	if err != nil {
		t.Fatalf("failed to create worker: %s", err)
	}

	if err = nvctWorker.Setup(withHttpServer); err != nil {
		t.Fatalf("failed to setup worker: %s", err)
	}

	go func() {
		time.Sleep(1 * time.Second)
		progressToSend := []uint32{25, 50, 75, 100}
		for _, p := range progressToSend {
			progressData := progress.Progress{
				TaskId:          curWorkerConfig.TaskId,
				Name:            "test-task",
				PercentComplete: p,
				Metadata: map[string]any{
					"message": fmt.Sprintf("i'm %d complete", p),
				},
				LastUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}

			progressMessage, _ := json.Marshal(progressData)

			os.WriteFile(curWorkerConfig.ProgressFilePath, progressMessage, 0644)

			time.Sleep(50 * time.Millisecond)
		}
	}()

	if err = nvctWorker.Run(ctx, withHttpServer); err != nil {
		t.Fatalf("failed to run nvct worker: %s", err)
	}
}

func TestPollingE2E(t *testing.T) {
	testDir := t.TempDir()
	curWorkerConfig := workerConfig
	curWorkerConfig.ResultsDir = testDir
	curWorkerConfig.ProgressFilePath = filepath.Join(curWorkerConfig.ResultsDir, "progress")
	curWorkerConfig.PollProgress = true
	curWorkerConfig.PollProgressInterval = time.Second

	nvctWorker, err := worker.NewNVCTWorker(
		ctx,
		zapLogger,
		curWorkerConfig,
	)
	if err != nil {
		t.Fatalf("failed to create worker: %s", err)
	}

	if err = nvctWorker.Setup(withoutHttpServer); err != nil {
		t.Fatalf("failed to setup worker: %s", err)
	}

	go func() {
		time.Sleep(1 * time.Second)
		progressToSend := []uint32{25, 50, 100}
		for _, p := range progressToSend {
			progressData := progress.Progress{
				TaskId:          curWorkerConfig.TaskId,
				Name:            "test-task",
				PercentComplete: p,
				Metadata: map[string]any{
					"message": fmt.Sprintf("i'm %d complete", p),
				},
				LastUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}

			progressMessage, _ := json.Marshal(progressData)

			os.WriteFile(curWorkerConfig.ProgressFilePath, progressMessage, 0644)

			time.Sleep(2 * time.Second)
		}
	}()

	if err = nvctWorker.Run(ctx, withoutHttpServer); err != nil {
		t.Fatalf("failed to run nvct worker: %s", err)
	}
}

func TestTaskFailedWithErrors(t *testing.T) {
	t.Parallel()
	testDir := t.TempDir()
	curWorkerConfig := workerConfig
	curWorkerConfig.ResultsDir = testDir
	curWorkerConfig.ProgressFilePath = filepath.Join(curWorkerConfig.ResultsDir, "progress")

	nvctWorker, err := worker.NewNVCTWorker(
		ctx,
		zapLogger,
		curWorkerConfig,
	)
	if err != nil {
		t.Fatalf("failed to create worker: %s", err)
	}

	if err = nvctWorker.Setup(withoutHttpServer); err != nil {
		t.Fatalf("failed to setup worker: %s", err)
	}

	go func() {
		time.Sleep(1 * time.Second)
		progressToSend := []uint32{25, 200}
		for _, p := range progressToSend {
			progressData := progress.Progress{
				TaskId:          curWorkerConfig.TaskId,
				Name:            "test-task",
				PercentComplete: p,
				Metadata: map[string]any{
					"message": fmt.Sprintf("i'm %d complete", p),
				},
				LastUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}

			progressMessage, _ := json.Marshal(progressData)

			os.WriteFile(curWorkerConfig.ProgressFilePath, progressMessage, 0644)

			time.Sleep(1 * time.Second)
		}
	}()

	if err = nvctWorker.Run(ctx, withoutHttpServer); !errors.Is(err, types.ErrOutOfRangePercentageComplete) {
		t.Fatalf("failed to run nvct worker: %v", err)
	}
}

func TestTaskExceedTimeout(t *testing.T) {
	t.Parallel()
	testDir := t.TempDir()
	curWorkerConfig := workerConfig
	curWorkerConfig.ResultsDir = testDir
	curWorkerConfig.MaxRunTime = "PT3S"
	curWorkerConfig.ProgressFilePath = filepath.Join(curWorkerConfig.ResultsDir, "progress")
	nvctWorker, err := worker.NewNVCTWorker(
		ctx,
		zapLogger,
		curWorkerConfig,
	)
	if err != nil {
		t.Fatalf("failed to create worker: %s", err)
	}

	if err = nvctWorker.Setup(withoutHttpServer); err != nil {
		t.Fatalf("failed to setup worker: %s", err)
	}

	go func() {
		progressToSend := []uint32{25, 50, 75, 100}
		for _, p := range progressToSend {
			if ctx.Err() != nil {
				return
			}

			progressData := progress.Progress{
				TaskId:          curWorkerConfig.TaskId,
				Name:            "test-task",
				PercentComplete: p,
				Metadata: map[string]any{
					"message": fmt.Sprintf("i'm %d complete", p),
				},
				LastUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}

			progressMessage, _ := json.Marshal(progressData)
			os.WriteFile(curWorkerConfig.ProgressFilePath, progressMessage, 0644)
			time.Sleep(2 * time.Second)
		}
	}()

	if err = nvctWorker.Run(ctx, withoutHttpServer); !errors.Is(err, types.ErrMaxRunTimeDurationExceeded) {
		t.Fatalf("failed to run nvct worker: %s", err)
	}
}

func TestTaskProgressUpdateTimeout(t *testing.T) {
	t.Parallel()
	testDir := t.TempDir()
	curWorkerConfig := workerConfig
	curWorkerConfig.ResultsDir = testDir
	curWorkerConfig.ProgressFilePath = filepath.Join(curWorkerConfig.ResultsDir, "progress")
	curWorkerConfig.ProgressUpdateTimeout = 2 * time.Second

	nvctWorker, err := worker.NewNVCTWorker(
		ctx,
		zapLogger,
		curWorkerConfig,
	)
	if err != nil {
		t.Fatalf("failed to create worker: %s", err)
	}

	if err = nvctWorker.Setup(withoutHttpServer); err != nil {
		t.Fatalf("failed to setup worker: %s", err)
	}

	go func() {
		time.Sleep(1 * time.Second)
		progressToSend := []uint32{25}
		for _, p := range progressToSend {
			progressData := progress.Progress{
				TaskId:          curWorkerConfig.TaskId,
				Name:            "test-task",
				PercentComplete: p,
				Metadata: map[string]any{
					"message": fmt.Sprintf("i'm %d complete", p),
				},
				LastUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}

			progressMessage, _ := json.Marshal(progressData)

			os.WriteFile(curWorkerConfig.ProgressFilePath, progressMessage, 0644)
		}
	}()

	if err = nvctWorker.Run(ctx, withoutHttpServer); !errors.Is(err, types.ErrNoProgressFileUpdates) {
		t.Fatalf("failed to run nvct worker: %v", err)
	}
}

func TestTaskReadyTimeout(t *testing.T) {
	t.Parallel()
	testDir := t.TempDir()
	curWorkerConfig := workerConfig
	curWorkerConfig.ResultsDir = testDir
	curWorkerConfig.ProgressFilePath = filepath.Join(curWorkerConfig.ResultsDir, "progress")
	curWorkerConfig.TaskReadyTimeout = 6 * time.Second

	nvctWorker, err := worker.NewNVCTWorker(
		ctx,
		zapLogger,
		curWorkerConfig,
	)
	if err != nil {
		t.Fatalf("failed to create worker: %s", err)
	}

	if err = nvctWorker.Setup(withoutHttpServer); err != nil {
		t.Fatalf("failed to setup worker: %s", err)
	}

	if err = nvctWorker.Run(ctx, withoutHttpServer); !errors.Is(err, types.ErrTaskNotReady) {
		t.Fatalf("failed to run nvct worker: %v", err)
	}
}

func TestViperDecoderConfig(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		v := viper.New()
		config := map[string]string{
			"NVCT_RESULT_HANDLING_STRATEGY": "UPLOAD",
			"POLL_PROGRESS_INTERVAL":        "2m30s",
		}

		for k, val := range config {
			v.Set(k, val)
		}

		var cfg configs.Config
		err := v.Unmarshal(&cfg, viperDecoderConfig())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cfg.ResultHandlingStrategy != types.UPLOAD_STRATEGY {
			t.Errorf("expected ResultHandlingStrategy to be UPLOAD_STRATEGY, got %v", cfg.ResultHandlingStrategy)
		}
		if cfg.PollProgressInterval != 150*time.Second {
			t.Errorf("expected PollProgressInterval to be 150s, got %v", cfg.PollProgressInterval)
		}
	})

	t.Run("invalid duration", func(t *testing.T) {
		v := viper.New()
		config := map[string]string{
			"POLL_PROGRESS_INTERVAL": "30",
		}

		for k, val := range config {
			v.Set(k, val)
		}

		var cfg configs.Config
		err := v.Unmarshal(&cfg, viperDecoderConfig())
		if err == nil {
			t.Error("expected error for invalid duration, got nil")
		}
	})

	t.Run("invalid strategy", func(t *testing.T) {
		v := viper.New()
		config := map[string]string{
			"NVCT_RESULT_HANDLING_STRATEGY": "UNKNOWN",
		}

		for k, val := range config {
			v.Set(k, val)
		}

		var cfg configs.Config
		err := v.Unmarshal(&cfg, viperDecoderConfig())
		if err == nil {
			t.Error("expected error for invalid strategy, got nil")
		}
	})
}
