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
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
)

// TestMain pre-registers the shared worker metrics once, before any test spawns
// a worker. NVCFWorker.Run starts the metadata-credentials token refresher
// (which reads the shared TokenRefreshFailureGauge) before it calls
// metrics.Initialize (which lazily registers that gauge under a sync.Once). When
// successive integration sub-tests each spin up a worker, a prior worker's
// still-running refresher can read the gauge while a later worker's Initialize
// writes it for the first time, which the race detector flags. Performing the
// one-time registration here establishes the write happens-before any worker's
// refresher read, so it is the correct place to serialize the upstream global
// initialization for tests. This does not change production behavior.
func TestMain(m *testing.M) {
	_ = metrics.Initialize(metrics.NvcfRootNamespace, nil)
	os.Exit(m.Run())
}
