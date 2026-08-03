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

// White-box unit tests focused on the pure validation/defaulting matrix of
// NewNVCTWorker. These tests only construct the worker; they never call Setup,
// Run, or bind any port.
package worker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
)

// testLogger returns a nop-ish zap logger wrapped in the type NewNVCTWorker expects.
func testLogger() *logs.ZapLogger {
	return logs.NewZapLogger(zap.NewAtomicLevelAt(zap.InfoLevel))
}

// validConfig returns a baseline configs.Config that constructs successfully.
// NO_STRATEGY is used so that ResultsDir is not required by the upload branch.
func validConfig() configs.Config {
	return configs.Config{
		TaskId:                   "10b076eb-b6d2-4cd9-878b-a3614a931570",
		OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
		ProgressFilePath:         "/tmp/progress",
		ResultHandlingStrategy:   types.NO_STRATEGY,
		NcaId:                    "nca-baseline",
		CloudProvider:            "AWS",
	}
}

// build is a small helper that constructs a worker from a baseline that is
// mutated by mutate. It returns the worker and error from NewNVCTWorker.
func build(t *testing.T, mutate func(c *configs.Config)) (*NVCTWorker, error) {
	t.Helper()
	cfg := validConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	return NewNVCTWorker(context.Background(), testLogger(), cfg)
}

func TestNewNVCTWorker_NspectIdFromEnv(t *testing.T) {
	// NspectId is sourced from the NVCF_NSPECT_ID env var and must propagate
	// into the metering config so NVCT_Infrastructure events carry nspect_id.
	t.Setenv(metering.EnvNspectId, "NSPECT-TASK-1234")
	w, err := build(t, nil)
	require.NoError(t, err)
	assert.Equal(t, "NSPECT-TASK-1234", w.meteringConfig.NspectId)
}

func TestNewNVCTWorker_ValidBaseline(t *testing.T) {
	w, err := build(t, nil)
	require.NoError(t, err)
	require.NotNil(t, w)
}

func TestNewNVCTWorker_TaskIdValidation(t *testing.T) {
	tests := []struct {
		name    string
		taskID  string
		wantErr bool
	}{
		{"valid uuid", "10b076eb-b6d2-4cd9-878b-a3614a931570", false},
		{"empty", "", true},
		{"not a uuid", "not-a-uuid", true},
		{"garbage", "12345", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := build(t, func(c *configs.Config) { c.TaskId = tt.taskID })
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid task id")
				assert.Nil(t, w)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, w)
		})
	}
}

func TestNewNVCTWorker_HealthPortDefault(t *testing.T) {
	tests := []struct {
		name     string
		port     int
		wantPort int
	}{
		{"zero defaults", 0, configs.DefaultHealthPort},
		{"negative defaults", -1, configs.DefaultHealthPort},
		{"explicit preserved", 9999, 9999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := build(t, func(c *configs.Config) { c.HealthPort = tt.port })
			require.NoError(t, err)
			assert.Equal(t, tt.wantPort, w.config.HealthPort)
		})
	}
	// Confirm the default constant is the documented 8080.
	assert.Equal(t, 8080, configs.DefaultHealthPort)
}

func TestNewNVCTWorker_OTELUrlParse(t *testing.T) {
	// url.Parse is extremely permissive; a control character in the URL is one
	// of the few inputs that actually fails it.
	w, err := build(t, func(c *configs.Config) { c.OTELExporterOTLPEndpoint = "http://\x7f\x00invalid" })
	require.Error(t, err)
	assert.Nil(t, w)
}

func TestNewNVCTWorker_OTELScheme(t *testing.T) {
	// https endpoint should be treated as non-insecure; just assert it constructs.
	w, err := build(t, func(c *configs.Config) { c.OTELExporterOTLPEndpoint = "https://collector.example:4317" })
	require.NoError(t, err)
	require.NotNil(t, w)
}

func TestNewNVCTWorker_ResultsDirRequiredForUpload(t *testing.T) {
	// UPLOAD_STRATEGY without ResultsDir must fail.
	w, err := build(t, func(c *configs.Config) {
		c.ResultHandlingStrategy = types.UPLOAD_STRATEGY
		c.ResultsDir = ""
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "results directory")
	assert.Nil(t, w)

	// UPLOAD_STRATEGY with ResultsDir set must succeed.
	w, err = build(t, func(c *configs.Config) {
		c.ResultHandlingStrategy = types.UPLOAD_STRATEGY
		c.ResultsDir = "/tmp/results"
	})
	require.NoError(t, err)
	require.NotNil(t, w)

	// NO_STRATEGY without ResultsDir is fine (baseline already does this).
	w, err = build(t, func(c *configs.Config) {
		c.ResultHandlingStrategy = types.NO_STRATEGY
		c.ResultsDir = ""
	})
	require.NoError(t, err)
	require.NotNil(t, w)
}

func TestNewNVCTWorker_ProgressFilePathRequired(t *testing.T) {
	w, err := build(t, func(c *configs.Config) { c.ProgressFilePath = "" })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "progress file path")
	assert.Nil(t, w)
}

func TestNewNVCTWorker_InstanceTypeNameOverride(t *testing.T) {
	// When InstanceTypeName is set, it overrides InstanceType.
	w, err := build(t, func(c *configs.Config) {
		c.InstanceType = "old-type"
		c.InstanceTypeName = "new-type"
	})
	require.NoError(t, err)
	assert.Equal(t, "new-type", w.config.InstanceType)
	assert.Equal(t, "new-type", w.meteringConfig.InstanceType)

	// When InstanceTypeName is empty, InstanceType is preserved.
	w, err = build(t, func(c *configs.Config) {
		c.InstanceType = "old-type"
		c.InstanceTypeName = ""
	})
	require.NoError(t, err)
	assert.Equal(t, "old-type", w.config.InstanceType)
}

func TestNewNVCTWorker_ICMSEnvironmentFallback(t *testing.T) {
	// Empty ICMSEnvironment falls back to SpotEnvironment.
	w, err := build(t, func(c *configs.Config) {
		c.ICMSEnvironment = ""
		c.SpotEnvironment = "stage"
	})
	require.NoError(t, err)
	assert.Equal(t, "stage", w.config.ICMSEnvironment)
	assert.Equal(t, "stage", w.meteringConfig.ICMSEnvironment)

	// Explicit ICMSEnvironment is preserved over SpotEnvironment.
	w, err = build(t, func(c *configs.Config) {
		c.ICMSEnvironment = "prod"
		c.SpotEnvironment = "stage"
	})
	require.NoError(t, err)
	assert.Equal(t, "prod", w.config.ICMSEnvironment)
}

func TestNewNVCTWorker_SharedConfigDirDefault(t *testing.T) {
	w, err := build(t, func(c *configs.Config) { c.SharedConfigDir = "" })
	require.NoError(t, err)
	assert.Equal(t, configs.DefaultSharedConfigDir, w.config.SharedConfigDir)

	w, err = build(t, func(c *configs.Config) { c.SharedConfigDir = "/custom/shared" })
	require.NoError(t, err)
	assert.Equal(t, "/custom/shared", w.config.SharedConfigDir)
}

func TestNewNVCTWorker_InfraMeteringHeartbeatIntervalDefault(t *testing.T) {
	w, err := build(t, func(c *configs.Config) { c.InfraMeteringHeartbeatInterval = 0 })
	require.NoError(t, err)
	assert.Equal(t, metering.DefaultInfraMeteringHeartbeatInterval, w.meteringConfig.InfraHeartbeatInterval)

	custom := 90 * time.Second
	w, err = build(t, func(c *configs.Config) { c.InfraMeteringHeartbeatInterval = custom })
	require.NoError(t, err)
	assert.Equal(t, custom, w.meteringConfig.InfraHeartbeatInterval)
}

func TestNewNVCTWorker_BillingNcaIdFallback(t *testing.T) {
	// Empty BillingNcaId falls back to NcaId.
	w, err := build(t, func(c *configs.Config) {
		c.NcaId = "nca-123"
		c.BillingNcaId = ""
	})
	require.NoError(t, err)
	assert.Equal(t, "nca-123", w.meteringConfig.BillingNcaId)

	// Explicit BillingNcaId is preserved.
	w, err = build(t, func(c *configs.Config) {
		c.NcaId = "nca-123"
		c.BillingNcaId = "billing-456"
	})
	require.NoError(t, err)
	assert.Equal(t, "billing-456", w.meteringConfig.BillingNcaId)
}

func TestNewNVCTWorker_ResultHandlingStrategyValidation(t *testing.T) {
	// UNKNOWN_STRATEGY is rejected.
	w, err := build(t, func(c *configs.Config) {
		c.ResultHandlingStrategy = types.UNKNOWN_STRATEGY
		c.ResultsDir = "/tmp/results"
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported result handling strategy")
	assert.Nil(t, w)

	// NO_STRATEGY and UPLOAD_STRATEGY (with ResultsDir) are accepted.
	w, err = build(t, func(c *configs.Config) { c.ResultHandlingStrategy = types.NO_STRATEGY })
	require.NoError(t, err)
	require.NotNil(t, w)

	w, err = build(t, func(c *configs.Config) {
		c.ResultHandlingStrategy = types.UPLOAD_STRATEGY
		c.ResultsDir = "/tmp/results"
	})
	require.NoError(t, err)
	require.NotNil(t, w)
}

func TestNewNVCTWorker_PollProgressIntervalDefault(t *testing.T) {
	w, err := build(t, func(c *configs.Config) { c.PollProgressInterval = 0 })
	require.NoError(t, err)
	assert.Equal(t, configs.DefaultPollProgressInterval, w.config.PollProgressInterval)

	custom := 11 * time.Second
	w, err = build(t, func(c *configs.Config) { c.PollProgressInterval = custom })
	require.NoError(t, err)
	assert.Equal(t, custom, w.config.PollProgressInterval)
}

func TestNewNVCTWorker_ProgressUpdateTimeoutDefault(t *testing.T) {
	w, err := build(t, func(c *configs.Config) { c.ProgressUpdateTimeout = 0 })
	require.NoError(t, err)
	assert.Equal(t, configs.DefaultProgressUpdateTimeout, w.config.ProgressUpdateTimeout)

	custom := 7 * time.Minute
	w, err = build(t, func(c *configs.Config) { c.ProgressUpdateTimeout = custom })
	require.NoError(t, err)
	assert.Equal(t, custom, w.config.ProgressUpdateTimeout)
}

func TestNewNVCTWorker_TaskReadyTimeoutDefault(t *testing.T) {
	w, err := build(t, func(c *configs.Config) { c.TaskReadyTimeout = 0 })
	require.NoError(t, err)
	assert.Equal(t, configs.DefaultTaskReadyTimeout, w.config.TaskReadyTimeout)

	custom := 3 * time.Hour
	w, err = build(t, func(c *configs.Config) { c.TaskReadyTimeout = custom })
	require.NoError(t, err)
	assert.Equal(t, custom, w.config.TaskReadyTimeout)
}

func TestNewNVCTWorker_MaxRunTimeParsing(t *testing.T) {
	// Empty MaxRunTime yields a zero duration and no error.
	w, err := build(t, func(c *configs.Config) { c.MaxRunTime = "" })
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), w.maxRunTimeDuration)

	// Valid ISO8601 duration parses into a positive duration.
	w, err = build(t, func(c *configs.Config) { c.MaxRunTime = "PT1H" })
	require.NoError(t, err)
	assert.Equal(t, time.Hour, w.maxRunTimeDuration)

	// Invalid duration is a user-actionable error.
	w, err = build(t, func(c *configs.Config) { c.MaxRunTime = "not-a-duration" })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max time duration")
	assert.Nil(t, w)
}

// TestNewNVCTWorker_MeteringBackendNGN documents CURRENT behavior of the
// metering backend assignment in NewNVCTWorker.
//
// BUG NVCF-10479 bug#4: normalized 'backend' computed but config.CloudProvider
// used; current behavior asserted, fix tracked separately.
//
// worker.go computes a normalized backend ("NGN" -> "GFN") at the local
// variable `backend` but then sets metering.Config.Backend from the
// un-normalized config.CloudProvider. So a CloudProvider of "NGN" leaks through
// as "NGN" instead of the intended "GFN".
func TestNewNVCTWorker_MeteringBackendNGN(t *testing.T) {
	w, err := build(t, func(c *configs.Config) { c.CloudProvider = "NGN" })
	require.NoError(t, err)
	// Current (buggy) behavior: backend is the un-normalized value.
	assert.Equal(t, "NGN", w.meteringConfig.Backend)

	// A non-NGN provider is passed through unchanged either way.
	w, err = build(t, func(c *configs.Config) { c.CloudProvider = "AWS" })
	require.NoError(t, err)
	assert.Equal(t, "AWS", w.meteringConfig.Backend)
}
