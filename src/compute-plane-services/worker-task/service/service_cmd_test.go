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
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/configs"
)

// TestNewRootCommandStructure exercises the construction body of NewRootCommand,
// including the reflection-driven flag-registration loop, without invoking any
// run path that binds the HTTP server.
func TestNewRootCommandStructure(t *testing.T) {
	cmd := NewRootCommand(ctx, zapLogger, time.Now())
	require.NotNil(t, cmd, "NewRootCommand must return a non-nil command")

	assert.Equal(t, "worker", cmd.Use)

	// The persistent "config" flag is registered explicitly.
	assert.NotNil(t, cmd.PersistentFlags().Lookup("config"), "expected persistent 'config' flag")

	// One flag must be registered for every exported configs.Config field,
	// keyed by its mapstructure tag.
	configType := reflect.TypeOf(configs.Config{})
	for i := 0; i < configType.NumField(); i++ {
		tag := configType.Field(i).Tag.Get("mapstructure")
		if tag == "" {
			continue
		}
		assert.NotNilf(t, cmd.Flags().Lookup(tag), "expected flag registered for config field tag %q", tag)
	}
}

// TestPersistentPreRunError forces worker.NewNVCTWorker to fail inside
// PersistentPreRunE by supplying an invalid (non-UUID) TASK_ID, covering the
// InitConfig/bind/unmarshal/NewNVCTWorker error path of the closure.
func TestPersistentPreRunError(t *testing.T) {
	cmd := NewRootCommand(ctx, zapLogger, time.Now())

	// TASK_ID is the mapstructure tag for configs.Config.TaskId.
	t.Setenv("TASK_ID", "not-a-uuid")

	err := cmd.PersistentPreRunE(cmd, nil)
	require.Error(t, err, "expected PersistentPreRunE to error on invalid task id")
}

// TestPersistentPreRunInitConfigError covers the config.InitConfig error branch
// of PersistentPreRunE (lines 87-89) by pointing the --config flag at a malformed
// YAML file, which makes viper's ReadInConfig return a non-NotFound parse error.
func TestPersistentPreRunInitConfigError(t *testing.T) {
	badCfg := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(badCfg, []byte("::not: valid: yaml: ["), 0o600))

	cmd := NewRootCommand(ctx, zapLogger, time.Now())
	require.NoError(t, cmd.PersistentFlags().Set("config", badCfg))

	err := cmd.PersistentPreRunE(cmd, nil)
	require.Error(t, err, "expected PersistentPreRunE to error on malformed config file")
}

// TestPersistentPreRunUnmarshalError covers the v.Unmarshal error branch of
// PersistentPreRunE (lines 97-99) by supplying a duration-typed env var with an
// unparseable value, which fails inside the StringToDuration decode hook.
func TestPersistentPreRunUnmarshalError(t *testing.T) {
	cmd := NewRootCommand(ctx, zapLogger, time.Now())

	// POLL_PROGRESS_INTERVAL maps to a time.Duration field; "30" has no unit and
	// fails the duration decode hook during Unmarshal.
	t.Setenv("POLL_PROGRESS_INTERVAL", "30")

	err := cmd.PersistentPreRunE(cmd, nil)
	require.Error(t, err, "expected PersistentPreRunE to error on unparseable duration")
}

// TestPersistentPreRunSuccess drives the full PersistentPreRunE closure to a
// nil-error completion, including Setup(true) which connects to the mock nvct
// gRPC server started by TestMain on localhost:9092. Env vars mirror the working
// workerConfig from TestMain.
func TestPersistentPreRunSuccess(t *testing.T) {
	// worker.Setup(withHttpServer) registers /metrics on the global
	// http.DefaultServeMux (see go-lib nvkit/servers/grpc.go). Calling it more
	// than once per process panics with a duplicate-pattern error, and TestE2E
	// also performs a withHttpServer Setup. Swap in a fresh DefaultServeMux for
	// the duration of this test so our registration is isolated, then restore it.
	previousMux := http.DefaultServeMux
	http.DefaultServeMux = http.NewServeMux()
	t.Cleanup(func() {
		http.DefaultServeMux = previousMux
	})

	cmd := NewRootCommand(ctx, zapLogger, time.Now())

	resultsDir := t.TempDir()
	progressFilePath := filepath.Join(resultsDir, "progress")

	workerToken, err := testutils.GenerateJWT(time.Now().Unix())
	require.NoError(t, err, "failed to generate worker token")

	// Env var names are the mapstructure tags from configs.Config.
	t.Setenv("NCA_ID", "test-nca-id")
	t.Setenv("ACCOUNT_NAME", "test-account")
	t.Setenv("NVCT_WORKER_TOKEN", workerToken)
	t.Setenv("NVCT_FQDN_GRPC", "http://localhost:9092")
	t.Setenv("TASK_ID", "10b076eb-b6d2-4cd9-878b-a3614a931570")
	t.Setenv("TASK_NAME", "test-task")
	t.Setenv("INSTANCE_ID", "test-instance")
	t.Setenv("INSTANCE_TYPE_NAME", "test-instance-type")
	t.Setenv("HEALTH_PORT", "18080")
	t.Setenv("NVCT_MAX_RUN_TIME_DURATION", "PT1H")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:8360")
	t.Setenv("TRACING_ACCESS_TOKEN", "fake-tracing-token")
	t.Setenv("TERMINATION_GRACE_PERIOD", "PT1H")
	t.Setenv("NVCT_RESULT_HANDLING_STRATEGY", "NONE")
	t.Setenv("SHARED_CONFIG_DIR", "/tmp/config/shared")
	t.Setenv("NVCT_RESULTS_DIR", resultsDir)
	t.Setenv("NVCT_PROGRESS_FILE_PATH", progressFilePath)

	err = cmd.PersistentPreRunE(cmd, nil)
	require.NoError(t, err, "expected PersistentPreRunE to succeed against mock nvct")
}
