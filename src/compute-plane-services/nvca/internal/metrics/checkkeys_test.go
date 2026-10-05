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

package metrics_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/clustervalidator"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
)

// TestClusterValidatorCheckKeysSync guards the two hand-maintained check-key
// lists against drift: the init-to-zero baseline in metrics
// (clusterValidatorCheckKeys) must expose exactly the same checks the validator
// emits (clustervalidator.AllCheckKeys). If they diverge, the baseline would
// initialize a different set than real runs produce, a silent metric gap.
func TestClusterValidatorCheckKeysSync(t *testing.T) {
	assert.ElementsMatch(t, clustervalidator.AllCheckKeys, metrics.ClusterValidatorCheckKeys(),
		"clusterValidatorCheckKeys() must match clustervalidator.AllCheckKeys; "+
			"when adding a CheckKey* constant, update both lists")
}
