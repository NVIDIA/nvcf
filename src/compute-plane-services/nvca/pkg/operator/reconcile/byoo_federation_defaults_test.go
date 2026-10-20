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

package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
)

func TestNewAgentConfigConfigMap_BYOOPlatformMetricsFederationDefaults(t *testing.T) {
	const target = "vmsingle.monitoring.svc.cluster.local:8428"
	tests := []struct {
		name          string
		clusterSource nvcaoptypes.ClusterSource
		mergeConfig   string
		want          nvcaconfig.BYOOOTelPlatformMetricsFederationConfig
	}{
		{
			name:          "managed cluster keeps the collector default",
			clusterSource: nvcaoptypes.ClusterSourceNGCManaged,
		},
		{
			name:          "helm-managed cluster keeps the collector default",
			clusterSource: nvcaoptypes.ClusterSourceHelmManaged,
		},
		{
			name:          "self-hosted cluster disables unconfigured federation",
			clusterSource: nvcaoptypes.ClusterSourceSelfHosted,
			want:          nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{Enabled: ptr.To(false)},
		},
		{
			name:          "self-hosted cluster disables federation when other collector settings are set",
			clusterSource: nvcaoptypes.ClusterSourceSelfHosted,
			mergeConfig: `agent:
  byooOtelCollector:
    exporterHelper:
      timeout: 30s
`,
			want: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{Enabled: ptr.To(false)},
		},
		{
			name:          "self-hosted cluster keeps a configured target",
			clusterSource: nvcaoptypes.ClusterSourceSelfHosted,
			mergeConfig: `agent:
  byooOtelCollector:
    platformMetricsFederation:
      target: ` + target + `
`,
			want: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{Target: target},
		},
		{
			name:          "self-hosted cluster keeps an explicit enable",
			clusterSource: nvcaoptypes.ClusterSourceSelfHosted,
			mergeConfig: `agent:
  byooOtelCollector:
    platformMetricsFederation:
      enabled: true
`,
			want: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{Enabled: ptr.To(true)},
		},
		{
			name:          "managed cluster keeps an explicit disable",
			clusterSource: nvcaoptypes.ClusterSourceNGCManaged,
			mergeConfig: `agent:
  byooOtelCollector:
    platformMetricsFederation:
      enabled: false
`,
			want: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{Enabled: ptr.To(false)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			clients := mockKubeClientsForIntegrationTests()
			bc := &BackendK8sCache{
				clients:           clients,
				envType:           nvidiaiov1.EnvTypeStage,
				operatorNamespace: NVCAOperatorNamespace,
			}
			if tt.mergeConfig != "" {
				_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName},
					Data:       map[string]string{agentConfigFile: tt.mergeConfig},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
			}
			nb := ngcManagedBackendWithAgentConfig(nvidiaiov1.AgentConfig{})
			nb.Spec.ClusterSource = tt.clusterSource

			configMap, err := bc.newAgentConfigConfigMap(ctx, nb)
			require.NoError(t, err)
			cfg, err := nvcaconfig.DecodeConfig([]byte(configMap.Data[agentConfigFile]))
			require.NoError(t, err)

			assert.Equal(t, tt.want, cfg.Agent.BYOOOTelCollector.PlatformMetricsFederation)
			if tt.want.IsZero() {
				assert.NotContains(t, configMap.Data[agentConfigFile], "platformMetricsFederation")
			}
		})
	}
}

func TestApplySelfManagedBYOODefaults_DoesNotMutateManagedConfig(t *testing.T) {
	nb := ngcManagedBackendWithAgentConfig(nvidiaiov1.AgentConfig{})
	cfg := applySelfManagedBYOODefaults(nb, nvcaconfig.Config{})
	assert.True(t, cfg.Agent.BYOOOTelCollector.IsZero())
	assert.Empty(t, cfg.Agent.BYOOOTelCollectorEnvVars())

	nb.Spec.ClusterSource = nvcaoptypes.ClusterSourceSelfHosted
	cfg = applySelfManagedBYOODefaults(nb, nvcaconfig.Config{})
	envs := cfg.Agent.BYOOOTelCollectorEnvVars()
	require.Len(t, envs, 1)
	assert.Equal(t, nvcaconfig.BYOOOTelCollectorConfigEnv, envs[0].Name)
}
