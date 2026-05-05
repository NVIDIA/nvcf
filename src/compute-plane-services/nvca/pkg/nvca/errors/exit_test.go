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

package nvcaerrors

// Grabbed from https://github.com/NVIDIA/nvcf/users-sandbox/mmou/go-nvcf-worker-2/-/blob/f8aa4fb58f5289d84637ad0a1daa9cb8ca645ea7/nvcf-worker-lib/utils/exit.go
// need to port to nvcf-go repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExitReason(t *testing.T) {
	// Test with nil error
	ExitReason(context.Background(), nil) // Should not panic

	// Test with non-nil error
	ExitReason(context.Background(), errors.New("test error"))
}

func TestWriteTerminationLog(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()
	testTerminationLogPath := filepath.Join(tmpDir, "termination-log")

	// Save original path and restore after test
	originalPath := terminationLogPath
	defer func() {
		terminationLogPath = originalPath
	}()

	// Test cases
	tests := []struct {
		name        string
		setupEnv    func(*testing.T)
		message     string
		wantErr     bool
		errContains string
	}{
		{
			name: "successful write",
			setupEnv: func(t *testing.T) {
				t.Setenv("KUBERNETES_SERVICE_HOST", "test")
				// Create the termination log file
				os.WriteFile(testTerminationLogPath, []byte{}, 0644)
				terminationLogPath = testTerminationLogPath
			},
			message: "test message",
			wantErr: false,
		},
		{
			name: "not in kubernetes env",
			setupEnv: func(t *testing.T) {
				os.Unsetenv("KUBERNETES_SERVICE_HOST")
				terminationLogPath = testTerminationLogPath
			},
			message:     "test message",
			wantErr:     true,
			errContains: "not running in Kubernetes environment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment
			tt.setupEnv(t)

			// Execute test
			err := writeTerminationLog(tt.message)

			// Verify results
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				if tt.errContains != "" && err.Error() != tt.errContains {
					t.Errorf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			// Verify file contents if write was successful
			if !tt.wantErr {
				content, err := os.ReadFile(testTerminationLogPath)
				if err != nil {
					t.Fatalf("failed to read termination log: %v", err)
				}
				if string(content) != tt.message {
					t.Errorf("expected message %q, got %q", tt.message, string(content))
				}
			}
		})
	}
}

func TestIsKubernetesEnv(t *testing.T) {
	tests := []struct {
		name     string
		setupEnv func(*testing.T)
		want     bool
	}{
		{
			name: "in kubernetes env",
			setupEnv: func(t *testing.T) {
				t.Setenv("KUBERNETES_SERVICE_HOST", "test")
			},
			want: true,
		},
		{
			name: "not in kubernetes env",
			setupEnv: func(t *testing.T) {
				os.Unsetenv("KUBERNETES_SERVICE_HOST")
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnv(t)
			got := isKubernetesEnv()
			if got != tt.want {
				t.Errorf("isKubernetesEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}
