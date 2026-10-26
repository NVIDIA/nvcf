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

package nvca

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

func TestAgentNewBYOOFederationChecker(t *testing.T) {
	const target = "vmsingle.monitoring.svc.cluster.local:8428"
	tests := []struct {
		name        string
		byoo        bool
		federation  nvcaconfig.BYOOOTelPlatformMetricsFederationConfig
		wantChecker bool
	}{
		{
			name:        "configured target is checked",
			byoo:        true,
			federation:  nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{Target: target},
			wantChecker: true,
		},
		{
			name:       "BYOO off",
			federation: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{Target: target},
		},
		{
			name: "collector default target is not checked",
			byoo: true,
		},
		{
			name:       "explicitly enabled without a target is not checked",
			byoo:       true,
			federation: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{Enabled: ptr.To(true)},
		},
		{
			name: "disabled federation is not checked",
			byoo: true,
			federation: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{
				Enabled: ptr.To(false),
				Target:  target,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := &featureflagmock.Fetcher{}
			if tt.byoo {
				flags.SetFeatureFlags(featureflag.BYOObservability)
			}
			opts := &AgentOptions{FeatureFlagFetcher: flags}
			opts.Config.Agent.BYOOOTelCollector.PlatformMetricsFederation = tt.federation
			a := &Agent{AgentOptions: opts}

			assert.Equal(t, tt.wantChecker, a.newBYOOFederationChecker() != nil)
		})
	}
}
