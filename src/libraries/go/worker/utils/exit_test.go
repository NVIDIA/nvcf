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

package utils

import (
	"encoding/json"
	"errors"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"os"
	"path/filepath"
	"testing"
)

func TestExitReason(t *testing.T) {
	// Test with nil error
	ExitReason(nil) // Should not panic

	// Test with non-nil error
	ExitReason(errors.New("test error"))
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

func TestExitReasonWithWorkerError(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Save original path and restore after test
	originalPath := terminationLogPath
	defer func() {
		terminationLogPath = originalPath
	}()

	// Setup Kubernetes environment
	t.Setenv("KUBERNETES_SERVICE_HOST", "test")

	// Test cases
	tests := []struct {
		name      string
		workerErr *types.WorkerError
	}{
		{
			name:      "internal_error",
			workerErr: types.NewInternalError(errors.New("internal system failure")),
		},
		{
			name:      "user_actionable_error",
			workerErr: types.NewUserActionableError(errors.New("invalid input parameter")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup termination log file
			testTerminationLogPath := filepath.Join(tmpDir, "termination-log-user")
			if err := os.WriteFile(testTerminationLogPath, []byte{}, 0644); err != nil {
				t.Fatalf("failed to create termination log file: %v", err)
			}

			// Set the termination log path for this test
			terminationLogPath = testTerminationLogPath

			// Execute ExitReason
			ExitReason(tt.workerErr)

			// Verify the termination log contains the JSON marshaled WorkerError
			content, err := os.ReadFile(testTerminationLogPath)
			if err != nil {
				t.Fatalf("failed to read termination log: %v", err)
			}

			// Unmarshal and validate the result
			var actualWorkerErr *types.WorkerError
			err = json.Unmarshal(content, &actualWorkerErr)
			if err != nil {
				t.Fatalf("failed to unmarshal termination log: %v", err)
			}
			if !validateWorkerError(actualWorkerErr, tt.workerErr) {
				t.Errorf("expected worker error %v, got %v", actualWorkerErr, tt.workerErr)
			}
		})
	}
}

func validateWorkerError(workerErr *types.WorkerError, expectedErr *types.WorkerError) bool {
	return workerErr.Error() == expectedErr.Error()
}
