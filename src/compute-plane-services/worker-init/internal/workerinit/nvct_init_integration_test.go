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

package workerinit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/configs"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

// Ports are intentionally distinct from the service package E2E (8003/9093)
// so the two test binaries do not collide when `go test ./...` runs them in
// parallel.
const (
	integrationArtifactAddr = "127.0.0.1:18003"
	integrationArtifactURL  = "http://127.0.0.1:18003"
	integrationNvctAddr     = "127.0.0.1:19093"
	integrationNvctURL      = "http://127.0.0.1:19093"
)

// TestNvctInitializerSetupAndRun drives the full NVCT initializer Setup -> Run
// path against the in-repo mock gRPC and artifact servers, exercising client
// creation, infra metering, artifact listing, and download/install.
func TestNvctInitializerSetupAndRun(t *testing.T) {
	ctx := context.Background()

	artifactServer := testutils.MockArtifactServer{}
	require.NoError(t, artifactServer.Start(integrationArtifactAddr))
	defer artifactServer.Close(ctx)

	workerToken, err := testutils.GenerateJWT(time.Now().Unix())
	require.NoError(t, err)

	const taskID = "10b076eb-b6d2-4cd9-878b-a3614a931570"
	const instanceID = "test-instance-id"
	const instanceType = "test-instance-type"

	nvctServer := testutils.NewMockNvctServer(taskID, instanceID, instanceType, integrationArtifactURL, time.Hour)
	require.NoError(t, nvctServer.Run(integrationNvctAddr))
	defer nvctServer.Shutdown()

	testDir := t.TempDir()
	cfg := configs.InitConfig{
		BaseConfig: configs.BaseConfig{
			ConcurrentDownloads:      1,
			InstanceId:               instanceID,
			InstanceTypeName:         instanceType,
			NcaId:                    "test-nca-id",
			OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
			TracingAccessToken:       "fake-tracing-token",
			ModelRepo:                filepath.Join(testDir, "models"),
			SharedConfigDir:          filepath.Join(testDir, "shared"),
		},
		NvctConfig: configs.NvctConfig{
			TaskId:          taskID,
			NvctFqdnGrpc:    integrationNvctURL,
			NvctWorkerToken: workerToken,
		},
	}

	init, err := NewInitializer(cfg)
	require.NoError(t, err)

	nvct, ok := init.(*NvctInitializer)
	require.True(t, ok, "no NVCF worker token must select the NVCT initializer")

	require.NoError(t, nvct.Setup())
	require.NoError(t, nvct.Run(ctx))

	// The mock NVCT server advertises the simple_int8 model; confirm the
	// artifacts were downloaded and installed under the model repo.
	for _, artifact := range []string{"config.pbtxt", "1/model.graphdef"} {
		local := filepath.Join(cfg.ModelRepo, "simple_int8", artifact)
		_, statErr := os.Stat(local)
		require.NoErrorf(t, statErr, "expected artifact %s to be installed locally", artifact)
	}
	require.NoError(t, testutils.ValidateDownloadedArtifacts(cfg.ModelRepo))
}

// TestNvcfInitializerSetup exercises the NVCF initializer Setup path. Setup
// passes a nil NATS FQDN, so no NATS connection is created; the gRPC client
// dial is lazy, so no live server is required. This covers client creation,
// shared-config dir creation, and the token-cache lookup.
func TestNvcfInitializerSetup(t *testing.T) {
	testDir := t.TempDir()
	cfg := configs.InitConfig{
		BaseConfig: configs.BaseConfig{
			OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
			InstanceId:               "instance-1",
			NcaId:                    "nca-1",
			SharedConfigDir:          filepath.Join(testDir, "shared"),
		},
		NvcfConfig: configs.NvcfConfig{
			NvcfFqdnGrpc:      "http://127.0.0.1:19094",
			NvcfWorkerToken:   "tok",
			FunctionId:        "fn-1",
			FunctionVersionId: "ver-1",
		},
	}

	init, err := NewInitializer(cfg)
	require.NoError(t, err)

	nvcf, ok := init.(*NvcfInitializer)
	require.True(t, ok, "an NVCF worker token must select the NVCF initializer")

	require.NoError(t, nvcf.Setup())
	require.NotNil(t, nvcf.client, "Setup must initialize the NVCF client")

	// SharedConfigDir did not exist; Setup must have created it.
	_, statErr := os.Stat(cfg.SharedConfigDir)
	require.NoError(t, statErr)
}

// TestNvcfInitializerSetupEssError exercises the NVCF Setup error path when the
// ESS assertion token is set but the raw ESS config directory does not exist.
func TestNvcfInitializerSetupEssError(t *testing.T) {
	cfg := configs.InitConfig{
		BaseConfig: configs.BaseConfig{
			OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
			SecretsAssertionToken:    "assertion-token",
			EssAgentConfigDir:        t.TempDir(),
			SharedConfigDir:          t.TempDir(),
		},
		NvcfConfig: configs.NvcfConfig{
			NvcfFqdnGrpc:    "http://127.0.0.1:19094",
			NvcfWorkerToken: "tok",
		},
	}

	init, err := NewInitializer(cfg)
	require.NoError(t, err)

	nvcf, ok := init.(*NvcfInitializer)
	require.True(t, ok)

	require.Error(t, nvcf.Setup())
}

// TestNvctInitializerSetupEssError exercises the Setup error path when the
// ESS assertion token is set but the raw ESS config directory is missing.
func TestNvctInitializerSetupEssError(t *testing.T) {
	cfg := configs.InitConfig{
		BaseConfig: configs.BaseConfig{
			OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
			SecretsAssertionToken:    "assertion-token",
			EssAgentConfigDir:        t.TempDir(),
		},
		NvctConfig: configs.NvctConfig{
			TaskId:          "task-1",
			NvctFqdnGrpc:    integrationNvctURL,
			NvctWorkerToken: "tok",
		},
	}

	init, err := NewInitializer(cfg)
	require.NoError(t, err)

	nvct, ok := init.(*NvctInitializer)
	require.True(t, ok)

	// The raw ESS config dir (DefaultRawEssAgentConfigDir, /etc/ess) does not
	// exist in the test environment, so SetupEssAgent fails reading config.hcl.
	err = nvct.Setup()
	require.Error(t, err)
}
