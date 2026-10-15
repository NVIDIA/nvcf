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

// White-box integration tests that drive the orchestration code paths in
// worker.go (Setup, Run, workSession, waitForTaskReady, handleTaskError,
// handleWorkerTermination, PreStopCheck) end-to-end against a mock NVCT
// server. These never bind the HTTP health server (withHttpServer == false)
// and never touch port 8080. The mock NVCT server listens on 19092 so it does
// not collide with the service package's mock under parallel `go test ./...`.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/progress"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/nvct"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

// Run/Setup are always called with withHttpServer == false so no HTTP server
// binds during these tests.
const noHttpServer = false

var (
	integrationCtx       context.Context
	integrationZapLogger *logs.ZapLogger
	baselineConfig       configs.Config
)

func TestMain(m *testing.M) {
	integrationCtx = context.Background()

	integrationZapLogger = logs.NewZapLogger(zap.NewAtomicLevelAt(zap.InfoLevel))
	zap.RedirectStdLog(integrationZapLogger.GetZapLogger())

	zap.L().Info("======== internal/worker integration tests ========")

	issuedAt := time.Now().Unix()
	workerToken, err := testutils.GenerateJWT(issuedAt)
	if err != nil {
		zap.L().Fatal("failed to generate worker token", zap.Error(err))
	}

	// Unique temp dir for SharedConfigDir to avoid cross-test contention.
	sharedConfigDir, err := os.MkdirTemp("", "worker-shared-config-")
	if err != nil {
		zap.L().Fatal("failed to create shared config dir", zap.Error(err))
	}

	baselineConfig = configs.Config{
		NcaId:                    "test-nca-id",
		AccountName:              "test-account",
		NVCTWorkerToken:          workerToken,
		NVCTFqdnGrpc:             "http://localhost:19092",
		TaskId:                   "10b076eb-b6d2-4cd9-878b-a3614a931570",
		TaskName:                 "test-task",
		InstanceId:               "test-instance",
		InstanceTypeName:         "test-instance-type",
		HealthPort:               8080,
		MaxRunTime:               "PT1H",
		OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
		TracingAccessToken:       "fake-tracing-token",
		TerminationGracePeriod:   "PT1H",
		ResultHandlingStrategy:   types.NO_STRATEGY,
		SharedConfigDir:          sharedConfigDir,
	}

	nvctServer := testutils.NewMockNvctServer(
		baselineConfig.TaskId,
		baselineConfig.InstanceId,
		baselineConfig.InstanceTypeName,
		"", time.Hour,
	)
	if err = nvctServer.Run("0.0.0.0:19092"); err != nil {
		zap.L().Fatal("failed to start mock nvct server", zap.Error(err))
	}

	exitCode := m.Run()

	zap.L().Info("======== internal/worker integration tests complete ========")
	nvctServer.Shutdown()
	_ = os.RemoveAll(sharedConfigDir)
	integrationZapLogger.Close()
	os.Exit(exitCode)
}

// writeProgress writes a progress message to path with the given percent.
func writeProgress(path string, percent uint32) {
	progressData := progress.Progress{
		TaskId:          baselineConfig.TaskId,
		Name:            "test-task",
		PercentComplete: percent,
		Metadata: map[string]any{
			"message": fmt.Sprintf("i'm %d complete", percent),
		},
		LastUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	progressMessage, _ := json.Marshal(progressData)
	_ = os.WriteFile(path, progressMessage, 0644)
}

// newWorker constructs and sets up a worker from a copy of baselineConfig that
// has been mutated by mutate. ResultsDir and ProgressFilePath are rooted in a
// fresh per-test temp dir.
func newWorker(t *testing.T, mutate func(c *configs.Config)) *NVCTWorker {
	t.Helper()
	curCfg := baselineConfig
	curCfg.ResultsDir = t.TempDir()
	curCfg.ProgressFilePath = filepath.Join(curCfg.ResultsDir, "progress")
	if mutate != nil {
		mutate(&curCfg)
	}

	nvctWorker, err := NewNVCTWorker(integrationCtx, integrationZapLogger, curCfg)
	if err != nil {
		t.Fatalf("failed to create worker: %s", err)
	}
	if err := nvctWorker.Setup(noHttpServer); err != nil {
		t.Fatalf("failed to setup worker: %s", err)
	}
	return nvctWorker
}

func TestE2E(t *testing.T) {
	// Configure the ESS assertion token refresher so that branch of Run is
	// exercised. The token directory is a fresh temp dir; the refresher runs in
	// the background and does not affect the happy-path result.
	w := newWorker(t, func(c *configs.Config) {
		c.SecretsAssertionToken = "fake-assertion-token"
		c.EssAgentConfigDir = t.TempDir()
	})
	progressPath := w.config.ProgressFilePath

	go func() {
		time.Sleep(1 * time.Second)
		for _, p := range []uint32{25, 50, 75, 100} {
			writeProgress(progressPath, p)
			time.Sleep(50 * time.Millisecond)
		}
	}()

	if err := w.Run(integrationCtx, noHttpServer); err != nil {
		t.Fatalf("failed to run nvct worker: %s", err)
	}
}

// TestSetupCreatesResultDirectory drives the Setup branch where ResultsDir is
// empty (so the progress file's parent directory is used) and that directory
// does not yet exist, forcing Setup to create it.
func TestSetupCreatesResultDirectory(t *testing.T) {
	curCfg := baselineConfig
	// ResultsDir empty -> Setup uses filepath.Dir(ProgressFilePath).
	curCfg.ResultsDir = ""
	// Point at a nested directory that does not exist yet.
	nested := filepath.Join(t.TempDir(), "does-not-exist-yet")
	curCfg.ProgressFilePath = filepath.Join(nested, "progress")

	w, err := NewNVCTWorker(integrationCtx, integrationZapLogger, curCfg)
	if err != nil {
		t.Fatalf("failed to create worker: %s", err)
	}
	if err := w.Setup(noHttpServer); err != nil {
		t.Fatalf("failed to setup worker: %s", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("expected Setup to create %s: %v", nested, err)
	}
}

func TestPollingE2E(t *testing.T) {
	w := newWorker(t, func(c *configs.Config) {
		c.PollProgress = true
		c.PollProgressInterval = time.Second
	})
	progressPath := w.config.ProgressFilePath

	go func() {
		time.Sleep(1 * time.Second)
		for _, p := range []uint32{25, 50, 100} {
			writeProgress(progressPath, p)
			time.Sleep(2 * time.Second)
		}
	}()

	if err := w.Run(integrationCtx, noHttpServer); err != nil {
		t.Fatalf("failed to run nvct worker: %s", err)
	}
}

func TestTaskFailedWithErrors(t *testing.T) {
	t.Parallel()
	w := newWorker(t, nil)
	progressPath := w.config.ProgressFilePath

	go func() {
		time.Sleep(1 * time.Second)
		for _, p := range []uint32{25, 200} {
			writeProgress(progressPath, p)
			time.Sleep(1 * time.Second)
		}
	}()

	if err := w.Run(integrationCtx, noHttpServer); !errors.Is(err, types.ErrOutOfRangePercentageComplete) {
		t.Fatalf("expected ErrOutOfRangePercentageComplete, got: %v", err)
	}
}

func TestTaskExceedTimeout(t *testing.T) {
	t.Parallel()
	w := newWorker(t, func(c *configs.Config) {
		c.MaxRunTime = "PT3S"
	})
	progressPath := w.config.ProgressFilePath

	// Write an initial, non-terminal progress value before Run starts so that
	// waitForTaskReady succeeds immediately and the max-runtime timer begins
	// promptly. The task never reaches 100, so the only way Run can return is
	// the max-runtime deadline (3s) firing.
	writeProgress(progressPath, 25)

	done := make(chan struct{})
	go func() {
		// Keep emitting non-terminal progress updates so the progress monitor
		// stays active but the task never completes.
		for _, p := range []uint32{50, 75} {
			select {
			case <-done:
				return
			case <-time.After(1 * time.Second):
			}
			writeProgress(progressPath, p)
		}
	}()
	defer close(done)

	if err := w.Run(integrationCtx, noHttpServer); !errors.Is(err, types.ErrMaxRunTimeDurationExceeded) {
		t.Fatalf("expected ErrMaxRunTimeDurationExceeded, got: %v", err)
	}
}

func TestTaskProgressUpdateTimeout(t *testing.T) {
	t.Parallel()
	w := newWorker(t, func(c *configs.Config) {
		c.ProgressUpdateTimeout = 2 * time.Second
	})
	progressPath := w.config.ProgressFilePath

	go func() {
		time.Sleep(1 * time.Second)
		writeProgress(progressPath, 25)
	}()

	if err := w.Run(integrationCtx, noHttpServer); !errors.Is(err, types.ErrNoProgressFileUpdates) {
		t.Fatalf("expected ErrNoProgressFileUpdates, got: %v", err)
	}
}

func TestTaskReadyTimeout(t *testing.T) {
	t.Parallel()
	w := newWorker(t, func(c *configs.Config) {
		c.TaskReadyTimeout = 6 * time.Second
	})

	// No progress file is ever written, so the task never becomes ready.
	if err := w.Run(integrationCtx, noHttpServer); !errors.Is(err, types.ErrTaskNotReady) {
		t.Fatalf("expected ErrTaskNotReady, got: %v", err)
	}
}

// TestUploadStrategySecretSetupFailure drives the UPLOAD_STRATEGY branch of
// workSession. The secrets file lives at the hardcoded
// configs.DefaultSecretsFilePath, which does not exist in the test
// environment, so secrets.New fails. workSession must call
// handleTaskError(ErrSecretSetup) and return an error. ICMSEnvironment is set
// to "stage" so the staging NGC base URL branch is also exercised.
func TestUploadStrategySecretSetupFailure(t *testing.T) {
	w := newWorker(t, func(c *configs.Config) {
		c.ResultHandlingStrategy = types.UPLOAD_STRATEGY
		c.ICMSEnvironment = "stage"
		// ResultsDir is required for UPLOAD_STRATEGY; newWorker already sets it.
	})
	progressPath := w.config.ProgressFilePath

	// Provide a progress file so waitForTaskReady succeeds and execution
	// reaches the UPLOAD secret-setup code path.
	go func() {
		time.Sleep(1 * time.Second)
		writeProgress(progressPath, 25)
	}()

	// Run must return an error: the secrets file does not exist, so secrets.New
	// fails inside the UPLOAD_STRATEGY branch.
	if err := w.Run(integrationCtx, noHttpServer); err == nil {
		t.Fatal("expected an error from Run under UPLOAD_STRATEGY with no secrets file, got nil")
	}
}

// preStopWorker builds a worker and synchronously wires up a connected
// nvctClient (the same client Run would build), without starting Run in the
// background. This avoids any data race on w.nvctClient and lets PreStopCheck
// be exercised directly. It returns the worker and the progress file path.
func preStopWorker(t *testing.T) (*NVCTWorker, string) {
	t.Helper()
	w := newWorker(t, nil)

	client, err := nvct.CreateClient(
		w.config.NVCTFqdnGrpc,
		w.config.NVCTWorkerToken,
		w.config.InstanceId,
		w.config.TaskId,
		w.config.InstanceType,
		nvct.DefaultNvctClientTimeout,
		w.config.SharedConfigDir,
	)
	if err != nil {
		t.Fatalf("failed to create nvct client: %s", err)
	}
	if err := client.Connect(integrationCtx); err != nil {
		t.Fatalf("failed to connect nvct client: %s", err)
	}
	w.nvctClient = client
	// Install a heartbeat cancel func so the cancelHeartbeat != nil branch of
	// handleWorkerTermination/handleTaskError is exercised.
	_, cancel := context.WithCancel(integrationCtx)
	w.cancelHeartbeat = cancel
	return w, w.config.ProgressFilePath
}

// TestPreStopCheck drives PreStopCheck (and handleWorkerTermination) directly
// against a synchronously connected nvctClient.
func TestPreStopCheck(t *testing.T) {
	w, progressPath := preStopWorker(t)

	// Progress is at 50 and the mock reports RUNNING, so PreStopCheck must
	// report the task is not complete and trigger worker termination
	// (handleWorkerTermination).
	writeProgress(progressPath, 50)
	if err := w.PreStopCheck(integrationCtx); !errors.Is(err, types.ErrTaskNotComplete) {
		t.Fatalf("expected ErrTaskNotComplete, got: %v", err)
	}

	// At 100%, PreStopCheck must treat the task as complete and return nil.
	writeProgress(progressPath, 100)
	if err := w.PreStopCheck(integrationCtx); err != nil {
		t.Fatalf("expected nil from PreStopCheck at 100%%, got: %v", err)
	}
}

// TestPreStopCheckMissingProgressFile drives the PreStopCheck branch where the
// progress file cannot be parsed (it does not exist), which triggers
// handleWorkerTermination and returns the parse error.
func TestPreStopCheckMissingProgressFile(t *testing.T) {
	w, _ := preStopWorker(t)

	// No progress file exists, so ParseProgressFile fails inside PreStopCheck.
	if err := w.PreStopCheck(integrationCtx); err == nil {
		t.Fatal("expected an error from PreStopCheck with a missing progress file, got nil")
	}
}
