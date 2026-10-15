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
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/internal/workerinit"
)

const (
	nvctFQDN          = "http://localhost:9093"
	artifactServerUrl = "http://localhost:8003"
)

var ctx context.Context
var zapLogger *logs.ZapLogger
var workerInitConfig configs.InitConfig

func TestMain(m *testing.M) {
	ctx = context.Background()

	// No OTEL collector runs in the unit suite. Cap the OTLP export timeout so
	// span flushing on tracer shutdown fails fast instead of blocking on the
	// default 10s timeout to the dead endpoint.
	_ = os.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "300")

	zapLogger = logs.NewZapLogger(zap.NewAtomicLevelAt(zap.DebugLevel))
	zap.RedirectStdLog(zapLogger.GetZapLogger())

	zap.L().Info("======== NVCF Worker Init E2E Tests ========")

	mockArtifactServer := testutils.MockArtifactServer{}
	if err := mockArtifactServer.Start("0.0.0.0:8003"); err != nil {
		zap.L().Fatal("failed to start mock artifact server", zap.Error(err))
	}
	defer mockArtifactServer.Close(ctx)

	issuedAt := time.Now().Unix()
	workerToken, err := testutils.GenerateJWT(issuedAt)
	if err != nil {
		zap.L().Fatal("failed to create worker token", zap.Error(err))
	}

	baseConfig := configs.BaseConfig{
		ConcurrentDownloads:      1,
		InstanceId:               "test-instance-id",
		InstanceTypeName:         "test-instance-type",
		NcaId:                    "test-nca-id",
		OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
		TracingAccessToken:       "fake-tracing-token",
	}

	workerInitConfig = configs.InitConfig{
		BaseConfig: baseConfig,
		NvctConfig: configs.NvctConfig{
			TaskId:          "10b076eb-b6d2-4cd9-878b-a3614a931570",
			NvctFqdnGrpc:    nvctFQDN,
			NvctWorkerToken: workerToken,
		},
	}

	mockNvctServer := testutils.NewMockNvctServer(
		workerInitConfig.TaskId,
		workerInitConfig.InstanceId,
		workerInitConfig.InstanceType,
		artifactServerUrl,
		time.Hour,
	)
	if err = mockNvctServer.Run("0.0.0.0:9093"); err != nil {
		zap.L().Fatal("failed to start mock nvct server", zap.Error(err))
	}
	defer mockNvctServer.Shutdown()

	exitCode := m.Run()

	zap.L().Info("======== NVCF Worker Init E2E Tests Complete ========")
	zapLogger.Close()
	os.Exit(exitCode)
}

func TestE2E(t *testing.T) {
	testDir := t.TempDir()
	workerInitConfig.ModelRepo = filepath.Join(testDir, "models")
	workerInitConfig.SharedConfigDir = filepath.Join(testDir, "shared")

	nvctInit, err := workerinit.NewInitializer(workerInitConfig)
	if err != nil {
		t.Fatal(err)
	}

	if err := nvctInit.Setup(); err != nil {
		t.Fatal(err)
	}

	if err := nvctInit.Run(ctx); err != nil {
		t.Fatal(err)
	}

	artifacts := []string{
		"config.pbtxt",
		"1/model.graphdef",
	}

	for _, artifact := range artifacts {
		localArtifactPath := filepath.Join(workerInitConfig.ModelRepo, "simple_int8", artifact)
		if _, err := os.Stat(localArtifactPath); err != nil {
			t.Fatalf("artifact file [%s] not found locally", localArtifactPath)
		}

		if _, err := testutils.GetFileHash(localArtifactPath); err != nil {
			t.Fatalf("failed to get hash for file: %v", err)
		}

		if err = testutils.ValidateDownloadedArtifacts(workerInitConfig.ModelRepo); err != nil {
			t.Fatal(err)
		}
	}
}
