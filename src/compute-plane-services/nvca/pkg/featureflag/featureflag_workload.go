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
	"fmt"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// minBYOOMemoryBytes is the floor for a per-workload BYOO collector memory override.
// A memory override below 1Gi is rejected.
const minBYOOMemoryBytes = 1 << 30 // 1Gi

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
// A malformed or invalid byooResources override is a hard error: rather than silently
// deploying the collector with resources the chart author did not ask for, decoding fails
// so the misconfiguration surfaces at deploy time.
func DecodeWorkloadConfig(ctx context.Context, log logr.Logger, cm *corev1.ConfigMap) (*v1alpha1.WorkloadConfig, error) {
	if cm == nil {
		return nil, nil
	}

	raw, ok := cm.Data[WorkloadConfigDataKey]
	if !ok || raw == "" {
		log.Info("Ignoring empty workload config in ConfigMap %q", WorkloadConfigConfigMapName)
		return nil, nil
	}
	var cfg v1alpha1.WorkloadConfig
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, err
	}

	for key := range cfg.FeatureFlags {
		if _, known := workloadFeatureFlagKeys[key]; !known {
			log.Info("Ignoring unknown workload feature flag %q in ConfigMap %q", key, WorkloadConfigConfigMapName)
			delete(cfg.FeatureFlags, key)
		}
	}

	if cfg.BYOOResources != nil {
		if err := validateBYOOResources(cfg.BYOOResources); err != nil {
			return nil, fmt.Errorf("invalid byooResources in ConfigMap %q: %w", WorkloadConfigConfigMapName, err)
		}
	}
	return &cfg, nil
}

// validateBYOOResources rejects a per-workload BYOO collector resource override that
// would be unsafe: every specified quantity must be positive, and any memory quantity
// must be at least 1Gi.
func validateBYOOResources(rr *corev1.ResourceRequirements) error {
	for kind, list := range map[string]corev1.ResourceList{"requests": rr.Requests, "limits": rr.Limits} {
		for name, q := range list {
			if q.Sign() <= 0 {
				return fmt.Errorf("%s.%s must be positive (got %q)", kind, name, q.String())
			}
			if name == corev1.ResourceMemory && q.CmpInt64(minBYOOMemoryBytes) < 0 {
				return fmt.Errorf("%s.memory must be at least 1Gi (got %q)", kind, q.String())
			}
		}
	}
	return nil
}
