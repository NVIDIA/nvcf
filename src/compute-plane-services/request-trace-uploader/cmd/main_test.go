// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
)

// runMainEnv makes the test binary run main instead of the tests, so a test
// can check the process exit code.
const runMainEnv = "REQUEST_TRACE_UPLOADER_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		// The sentinel is not a setting; strict loading would reject it.
		_ = os.Unsetenv(runMainEnv)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestMainExitsNonZeroOnInvalidSuppression(t *testing.T) {
	tests := map[string]map[string]string{
		"unparseable rules":         {config.EnvSuppressionRules: `[{"name":`},
		"unknown action":            {config.EnvSuppressionRules: `[{"name":"a","headers":["x-tenant"],"values":["t1"],"action":"hide"}]`},
		"empty value list":          {config.EnvSuppressionRules: `[{"name":"a","headers":["x-tenant"],"values":[],"action":"dropRecord"}]`},
		"unknown headerless policy": {config.EnvSuppressHeaderless: "keep"},
	}
	for name, extra := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
			cmd.Env = []string{
				runMainEnv + "=1",
				config.EnvSourceDir + "=" + t.TempDir(),
				config.EnvBackend + "=" + string(config.BackendDebug),
				config.EnvHealthAddr + "=127.0.0.1:0",
			}
			for key, value := range extra {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() == 0 {
				t.Fatalf("process error = %v, want a non-zero exit\n%s", err, stderr.String())
			}
			if !strings.Contains(stderr.String(), "invalid request trace uploader configuration") {
				t.Fatalf("stderr = %s, want the configuration error", stderr.String())
			}
		})
	}
}
