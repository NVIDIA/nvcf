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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/nvcf"
)

// TestMain pre-registers the shared worker metrics once, before any test spawns
// a worker. The end-to-end test starts a worker whose token refresher reads the
// shared TokenRefreshFailureGauge before NVCFWorker.Run lazily registers it via
// metrics.Initialize (sync.Once); the race detector flags that read-vs-first-write
// across worker spawns. Performing the one-time registration here establishes a
// happens-before so the suite is race-clean. This does not change production
// behavior.
func TestMain(m *testing.M) {
	_ = metrics.Initialize(metrics.NvcfRootNamespace, nil)
	metadataDir, err := os.MkdirTemp("", "nvcf-worker-service-test-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create test metadata directory: %v\n", err)
		os.Exit(1)
	}
	nvcf.MetadataCredsTokenFile = filepath.Join(metadataDir, "self")

	code := m.Run()
	if err := os.RemoveAll(metadataDir); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "remove test metadata directory: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
