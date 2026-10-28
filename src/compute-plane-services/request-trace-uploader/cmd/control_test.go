// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
)

// TestMainStartsWithAValidConfiguration is the control for the exit tests:
// with a valid configuration the process keeps running, so their non-zero
// exits come from the setting each one breaks.
func TestMainStartsWithAValidConfiguration(t *testing.T) {
	secrets := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(secrets, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = []string{
		runMainEnv + "=1",
		config.EnvSourceDir + "=" + t.TempDir(),
		config.EnvBackend + "=" + string(config.BackendDebug),
		config.EnvHealthAddr + "=127.0.0.1:0",
		config.EnvSecretsFile + "=" + secrets,
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if ctx.Err() == nil {
		t.Fatalf("process exited by itself with a valid configuration, want it running\n%s", stderr.String())
	}
}
