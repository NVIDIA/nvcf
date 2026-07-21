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

package featureflag

import (
	"context"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

const (
	// WorkloadConfigConfigMapName is the fixed name of the ConfigMap a chart author may include
	// to supply workload-specific configuration. The ConfigMap is never created on-cluster; it is
	// read from the ReVal-rendered objects by the MiniService controller and then dropped from the
	// set of objects that are applied.
	WorkloadConfigConfigMapName = "nvcf-workload-config"

	// WorkloadConfigDataKey is the ConfigMap data key holding the workload config YAML document.
	WorkloadConfigDataKey = "config.yaml"
)

// Workload feature flag keys recognized under the workload config featureFlags map.
const (
	// StatusByWorkerReadiness directs the MiniService controller to only consider worker
	// container readiness when determining MiniService readiness, rather than aggressively
	// accounting for the health of all workload objects.
	StatusByWorkerReadiness = "StatusByWorkerReadiness"
)

// workloadFeatureFlagKeys is the set of recognized workload feature flag keys. Unrecognized
// keys are ignored (with a warning) by DecodeWorkloadConfig.
var workloadFeatureFlagKeys = map[string]struct{}{
	StatusByWorkerReadiness: {},
}

// DecodeWorkloadConfig decodes the workload config ConfigMap into a WorkloadConfig. The
// config is read from the WorkloadConfigDataKey data key as a YAML document. Unrecognized
// feature flags are dropped and logged as a warning. A nil ConfigMap yields a zero config.
func DecodeWorkloadConfig(ctx context.Context, cm *corev1.ConfigMap) (cfg *v1alpha1.WorkloadConfig, err error) {
	if cm == nil {
		return nil, nil
	}

	log := core.GetLogger(core.WithDefaultLogger(ctx))

	raw, ok := cm.Data[WorkloadConfigDataKey]
	if !ok || raw == "" {
		log.Warnf("Ignoring empty workload config in ConfigMap %q", WorkloadConfigConfigMapName)
		return nil, nil
	}
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, err
	}

	for key := range cfg.FeatureFlags {
		if _, known := workloadFeatureFlagKeys[key]; !known {
			log.Warnf("Ignoring unknown workload feature flag %q in ConfigMap %q", key, WorkloadConfigConfigMapName)
			delete(cfg.FeatureFlags, key)
		}
	}
	return cfg, nil
}
