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

package metering

import (
	"context"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// Minimal projection of the emitted JSON, so the assertions do not depend on the
// dynamic timestamp/transaction_id fields (extra JSON fields are ignored).
type infraEventProjection struct {
	Environment string `json:"env"`
	Data        struct {
		NcaId      string `json:"nca_id"`
		EventType  string `json:"event_type"`
		Properties struct {
			Status       InfraStatus `json:"status"`
			Backend      string      `json:"backend"`
			Gpus         int         `json:"gpus"`
			Duration     float64     `json:"duration"`
			OwnerNcaId   string      `json:"owner_nca_id"`
			FunctionId   string      `json:"function_id"`
			TaskId       string      `json:"task_id"`
			UserTaskTags []string    `json:"user_task_tags"`
		} `json:"properties"`
	} `json:"data"`
}

func waitForLogs(t *testing.T, logs *observer.ObservedLogs, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if logs.Len() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("expected at least %d log(s), got %d", n, logs.Len())
}

func baseInfraConfig() *Config {
	return &Config{
		ICMSEnvironment:        "LocalEnvironment",
		BillingNcaId:           "billing-nca",
		NspectId:               "TEST-NSPECT",
		InstanceType:           "it-1",
		InstanceId:             "iid-1",
		Backend:                "GFN",
		GpuCount:               2,
		GpuType:                "L40S",
		NcaId:                  "owner-nca",
		StartupTime:            time.Now().Add(-time.Second),
		InfraHeartbeatInterval: time.Hour, // long enough that the ticker never fires mid-test
	}
}

func parseInfraEvent(t *testing.T, msg string) infraEventProjection {
	t.Helper()
	var ev infraEventProjection
	require.NoError(t, json.Unmarshal([]byte(msg), &ev))
	return ev
}

func TestInfraMeteringFunctionEvent(t *testing.T) {
	logger, logs := configureLogCapture()
	zap.ReplaceGlobals(logger)

	cfg := baseInfraConfig()
	cfg.FunctionId = "fid-1"
	cfg.FunctionVersionId = "fvid-1"

	im := NewInfraMetering(context.Background(), Function, cfg, InfraInitializing)
	require.Equal(t, 1, logs.Len(), "constructor emits one event synchronously")

	ev := parseInfraEvent(t, logs.All()[0].Entry.Message)
	require.Equal(t, "LocalEnvironment", ev.Environment)
	require.Equal(t, "NVCF_Infrastructure", ev.Data.EventType)
	require.Equal(t, "billing-nca", ev.Data.NcaId)
	require.Equal(t, "fid-1", ev.Data.Properties.FunctionId)
	require.Equal(t, "owner-nca", ev.Data.Properties.OwnerNcaId)
	require.Equal(t, "GFN", ev.Data.Properties.Backend)
	require.Equal(t, 2, ev.Data.Properties.Gpus)
	require.Equal(t, InfraInitializing, ev.Data.Properties.Status)
	require.GreaterOrEqual(t, ev.Data.Properties.Duration, float64(0))

	im.SetStatus(InfraReady)
	require.NoError(t, im.Close())

	// Close drives the metering goroutine to emit one final event; waiting for it
	// also confirms the goroutine has exited.
	waitForLogs(t, logs, 2)
	final := parseInfraEvent(t, logs.All()[1].Entry.Message)
	require.Equal(t, InfraReady, final.Data.Properties.Status, "final event reflects the updated status")
}

func TestInfraMeteringTaskEvent(t *testing.T) {
	logger, logs := configureLogCapture()
	zap.ReplaceGlobals(logger)

	cfg := baseInfraConfig()
	cfg.TaskId = "task-1"
	cfg.TaskTags = []string{"tt1"}

	im := NewInfraMetering(context.Background(), Task, cfg, InfraReady)
	require.Equal(t, 1, logs.Len())

	ev := parseInfraEvent(t, logs.All()[0].Entry.Message)
	require.Equal(t, "NVCT_Infrastructure", ev.Data.EventType)
	require.Equal(t, "task-1", ev.Data.Properties.TaskId)
	require.Equal(t, []string{"tt1"}, ev.Data.Properties.UserTaskTags)
	require.Equal(t, InfraReady, ev.Data.Properties.Status)

	require.NoError(t, im.Close())
	waitForLogs(t, logs, 2) // reap the goroutine via its final event
}

func TestInfraMeteringUnknownWorkloadErrors(t *testing.T) {
	logger, _ := configureLogCapture()
	zap.ReplaceGlobals(logger)

	im := NewInfraMetering(context.Background(), Workload("bogus"), baseInfraConfig(), InfraReady)
	defer im.Close()
	require.ErrorContains(t, im.logEvent(), "invalid workload")
}
