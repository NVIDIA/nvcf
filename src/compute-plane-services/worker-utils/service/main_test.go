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
	"os"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
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
	os.Exit(m.Run())
}
