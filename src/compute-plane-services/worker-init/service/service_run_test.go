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
)

// TestRootCommandExecuteNvct drives the root command end to end: env-var
// binding, config unmarshal, NewInitializer, Setup, and RunE (signal wiring +
// Run) against the mock NVCT and artifact servers started in TestMain.
func TestRootCommandExecuteNvct(t *testing.T) {
	testDir := t.TempDir()

	t.Setenv("TASK_ID", workerInitConfig.TaskId)
	t.Setenv("NVCT_FQDN_GRPC", nvctFQDN)
	t.Setenv("NVCT_WORKER_TOKEN", workerInitConfig.NvctWorkerToken)
	t.Setenv("INSTANCE_ID", workerInitConfig.InstanceId)
	t.Setenv("INSTANCE_TYPE_NAME", workerInitConfig.InstanceTypeName)
	t.Setenv("NCA_ID", workerInitConfig.NcaId)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", workerInitConfig.OTELExporterOTLPEndpoint)
	t.Setenv("TRACING_ACCESS_TOKEN", workerInitConfig.TracingAccessToken)
	t.Setenv("WORKER_CONCURRENT_DOWNLOADS", "1")
	t.Setenv("MODEL_REPO", filepath.Join(testDir, "models"))
	t.Setenv("SHARED_CONFIG_DIR", filepath.Join(testDir, "shared"))

	cmd := NewRootCommand(context.Background())
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("command execution failed: %v", err)
	}
}

// TestRunHappyPath exercises the package-level Run entrypoint, which builds the
// root command and executes it against os.Args. It is driven to a clean NVCT
// run so Run returns without hitting the panic-on-error branch.
func TestRunHappyPath(t *testing.T) {
	testDir := t.TempDir()

	t.Setenv("TASK_ID", workerInitConfig.TaskId)
	t.Setenv("NVCT_FQDN_GRPC", nvctFQDN)
	t.Setenv("NVCT_WORKER_TOKEN", workerInitConfig.NvctWorkerToken)
	t.Setenv("INSTANCE_ID", workerInitConfig.InstanceId)
	t.Setenv("INSTANCE_TYPE_NAME", workerInitConfig.InstanceTypeName)
	t.Setenv("NCA_ID", workerInitConfig.NcaId)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", workerInitConfig.OTELExporterOTLPEndpoint)
	t.Setenv("TRACING_ACCESS_TOKEN", workerInitConfig.TracingAccessToken)
	t.Setenv("WORKER_CONCURRENT_DOWNLOADS", "1")
	t.Setenv("MODEL_REPO", filepath.Join(testDir, "models"))
	t.Setenv("SHARED_CONFIG_DIR", filepath.Join(testDir, "shared"))

	// Run reads os.Args via cobra; restrict it to the program name so no test
	// flags leak into the command parser.
	origArgs := os.Args
	os.Args = []string{"worker-init"}
	defer func() { os.Args = origArgs }()

	// A clean run must return normally (no panic).
	Run()
}

// TestRootCommandExecuteSetupError drives the command with an invalid OTEL
// endpoint so NewInitializer (and thus PersistentPreRunE) fails, exercising the
// error path of the lifecycle.
func TestRootCommandExecuteSetupError(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "://bad-endpoint")
	t.Setenv("NVCT_WORKER_TOKEN", "tok")

	cmd := NewRootCommand(context.Background())
	cmd.SetArgs([]string{})
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected command execution to fail with an invalid OTEL endpoint")
	}
}
