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

// White-box tests for NewNVCFWorker config validation and normalization. These
// do not start NATS/gRPC; NewNVCFWorker performs validation and field
// derivation before any network connection is established.
package worker

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
)

const (
	validFunctionId        = "10b076eb-b6d2-4cd9-878b-a3614a931570"
	validFunctionVersionId = "f85f1808-966c-4ac5-8e19-1c6defadb891"
)

func newTestLogger(t *testing.T) *logs.ZapLogger {
	t.Helper()
	l := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.InfoLevel))
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// baseValidConfig is the minimal config that lets NewNVCFWorker succeed.
func baseValidConfig(t *testing.T) Config {
	return Config{
		FunctionId:               validFunctionId,
		FunctionVersionId:        validFunctionVersionId,
		OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
		InferencePort:            8000,
		InferenceHealthEndpoint:  "/health",
		BaseAssetDir:             t.TempDir(),
		BaseResponseDir:          t.TempDir(),
		SharedConfigDir:          t.TempDir(),
		NVCFFqdnNATS:             nats.DefaultURL,
		HealthPort:               9099,
	}
}

func TestNewNVCFWorker_InvalidFunctionId(t *testing.T) {
	cfg := baseValidConfig(t)
	cfg.FunctionId = "not-a-uuid"
	_, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid function id")
}

func TestNewNVCFWorker_InvalidFunctionVersionId(t *testing.T) {
	cfg := baseValidConfig(t)
	cfg.FunctionVersionId = "still-not-a-uuid"
	_, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid function version id")
}

func TestNewNVCFWorker_Normalization(t *testing.T) {
	t.Run("max concurrency floored at one", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.MaxRequestConcurrency = 0
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, 1, w.config.MaxRequestConcurrency)
	})

	t.Run("negative max concurrency floored at one", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.MaxRequestConcurrency = -5
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, 1, w.config.MaxRequestConcurrency)
	})

	t.Run("ICMS environment defaults from deprecated spot environment", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.SpotEnvironment = "stage"
		cfg.ICMSEnvironment = ""
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, "stage", w.config.ICMSEnvironment)
		assert.Equal(t, "stage", w.meteringConfig.ICMSEnvironment)
	})

	t.Run("explicit ICMS environment is preserved", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.SpotEnvironment = "stage"
		cfg.ICMSEnvironment = "prod"
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, "prod", w.config.ICMSEnvironment)
	})

	t.Run("shared config dir defaults when empty", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.SharedConfigDir = ""
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, defaultSharedConfigDir, w.config.SharedConfigDir)
	})

	t.Run("asset and response dirs default when empty", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.BaseAssetDir = ""
		cfg.BaseResponseDir = ""
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, defaultBaseAssetDir, w.baseAssetDir)
		assert.Equal(t, defaultBaseResponseDir, w.baseResponseDir)
	})

	t.Run("health port defaults to 8080 when non-positive", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.HealthPort = 0
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, 8080, w.config.HealthPort)
	})

	t.Run("nats url defaults when empty", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.NVCFFqdnNATS = ""
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, nats.DefaultURL, w.config.NVCFFqdnNATS)
	})

	t.Run("infra metering heartbeat defaults when zero", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.InfraMeteringHeartbeatInterval = 0
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, metering.DefaultInfraMeteringHeartbeatInterval, w.meteringConfig.InfraHeartbeatInterval)
	})

	t.Run("explicit infra metering heartbeat preserved", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.InfraMeteringHeartbeatInterval = 42 * time.Second
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, 42*time.Second, w.meteringConfig.InfraHeartbeatInterval)
	})

	t.Run("billing nca id falls back to nca id", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.NcaId = "owner-nca"
		cfg.BillingNcaId = ""
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, "owner-nca", w.meteringConfig.BillingNcaId)
	})

	t.Run("explicit billing nca id preserved", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.NcaId = "owner-nca"
		cfg.BillingNcaId = "billing-nca"
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, "billing-nca", w.meteringConfig.BillingNcaId)
	})

	t.Run("NGN backend rewrites to GFN", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.CloudProvider = "NGN"
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, "GFN", w.meteringConfig.Backend)
	})

	t.Run("nspect id is populated from environment for metering events", func(t *testing.T) {
		t.Setenv(metering.EnvNspectId, "NSPECT-TEST-1234")
		cfg := baseValidConfig(t)
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, "NSPECT-TEST-1234", w.meteringConfig.NspectId)
	})

	t.Run("inference ready timeout defaults when zero", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.InferenceReadyTimeout = 0
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, defaultInferenceReadyTimeout, w.config.InferenceReadyTimeout)
	})

	t.Run("inference service name and namespace set cluster domain", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.InferenceServiceName = "infsvc"
		cfg.InferenceNamespace = "ns"
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, "infsvc.ns.svc.cluster.local", w.baseInferenceDomain)
	})

	t.Run("helm chart service name and namespace override domain", func(t *testing.T) {
		cfg := baseValidConfig(t)
		cfg.HelmChartInferenceServiceName = "helmsvc"
		cfg.HelmChartNamespace = "helmns"
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, "helmsvc.helmns.svc.cluster.local", w.baseInferenceDomain)
	})

	t.Run("default inference domain is loopback", func(t *testing.T) {
		cfg := baseValidConfig(t)
		w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
		require.NoError(t, err)
		assert.Equal(t, defaultBaseInferenceDomain, w.baseInferenceDomain)
	})
}

func TestNewNVCFWorker_InvalidOTELEndpoint(t *testing.T) {
	cfg := baseValidConfig(t)
	// A control character in the endpoint makes url.Parse fail.
	cfg.OTELExporterOTLPEndpoint = "http://\x7f-bad-host"
	_, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
	require.Error(t, err)
}

// TestSetupWorkDirs drives SetupWorkDirs across the create-new and
// already-exists branches via a fresh worker built from a valid config.
func TestSetupWorkDirs(t *testing.T) {
	cfg := baseValidConfig(t)
	w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
	require.NoError(t, err)

	// First call creates the directories (they already exist as t.TempDir()).
	require.NoError(t, w.SetupWorkDirs())
	// Second call hits the already-exists branch.
	require.NoError(t, w.SetupWorkDirs())
}
