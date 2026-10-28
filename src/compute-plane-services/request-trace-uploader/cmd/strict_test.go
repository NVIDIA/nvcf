// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
)

// TestMainExitsNonZeroOnAnyInvalidOrUnknownSetting checks that the process
// is strict: a value the configuration would once have replaced by a
// default, or a misspelled setting name, stops startup.
func TestMainExitsNonZeroOnAnyInvalidOrUnknownSetting(t *testing.T) {
	tests := map[string]struct {
		env   []string
		named string
	}{
		"value that used to fall back": {env: []string{config.EnvScanInterval + "=soon"}, named: config.EnvScanInterval},
		"misspelled setting":           {env: []string{"REQUEST_TRACE_UPLOADER_SCAN_INTERVAL=30"}, named: "REQUEST_TRACE_UPLOADER_SCAN_INTERVAL"},
		"alias value out of range":     {env: []string{"SHUTDOWN_DRAIN_SECONDS=-5"}, named: config.EnvDrainTimeout},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			// A readable secrets file, so only the configuration can stop
			// startup.
			secrets := filepath.Join(t.TempDir(), "secrets.json")
			if err := os.WriteFile(secrets, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
			cmd.Env = append([]string{
				runMainEnv + "=1",
				config.EnvSourceDir + "=" + t.TempDir(),
				config.EnvBackend + "=" + string(config.BackendDebug),
				config.EnvHealthAddr + "=127.0.0.1:0",
				config.EnvSecretsFile + "=" + secrets,
			}, tt.env...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("process still running after 5 s, want it to stop at startup\n%s", stderr.String())
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() == 0 {
				t.Fatalf("process error = %v, want a non-zero exit\n%s", err, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.named) {
				t.Fatalf("stderr = %s, want the setting %s named", stderr.String(), tt.named)
			}
		})
	}
}
