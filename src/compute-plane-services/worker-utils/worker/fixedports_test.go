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

package worker

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// lockFixedPorts serializes tests that bind ports hardcoded by the upstream
// testutils mocks (the asset/S3 servers on 8001/8002 and the mock NVCF gRPC API
// on 9090, plus the OTEL collector on 8360). Those ports cannot be moved from
// test code, so the only way to run `go test ./...` with package-level
// parallelism is to make the suites that use them mutually exclusive.
//
// The lock is a flock on a fixed file under os.TempDir so it spans separate
// package test binaries (worker and service) in the same `go test ./...`
// invocation. It is released via t.Cleanup when the test finishes.
func lockFixedPorts(t *testing.T) {
	t.Helper()

	lockPath := filepath.Join(os.TempDir(), "nvcf-worker-utils-fixedports.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open fixed-port lock: %v", err)
	}

	deadline := time.Now().Add(90 * time.Second)
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			t.Fatal("timed out acquiring fixed-port lock")
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}
