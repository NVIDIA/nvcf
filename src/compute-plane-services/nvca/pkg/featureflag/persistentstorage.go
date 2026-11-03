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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

const nvcaInternalPersistentStorageConfigJSONBase64Key = "NVCA_INTERNAL_PERSISTENT_STORAGE_CONFIG_JSON_BASE64"

type InternalPersistentStorageFeatureFlag struct {
	FeatureFlag
	Spec InternalPersistentStorageSpec
}

type internalPersistentStorageEnvironmentConfig struct {
	Enabled          *bool                                                `json:"enabled"`
	StorageClassName string                                               `json:"storageClassName"`
	ResourceQuota    nvcav1new.InternalPersistentStorageResourceQuotaSpec `json:"resourceQuota"`
}

func newHelmInternalPersistentStorageFeatureFlag(defaultValue bool) *InternalPersistentStorageFeatureFlag {
	return &InternalPersistentStorageFeatureFlag{
		FeatureFlag: FeatureFlag{
			defaultValue: newBool(defaultValue),
			enabled:      newBool(defaultValue),
			Key:          "HelmInternalPersistentStorage",
		},
		Spec: InternalPersistentStorageSpec{
			Enabled: false,
		},
	}
}

// ConfigureHelmInternalPersistentStorage initializes the runtime IPS feature
// from the mounted agent configuration. The environment variable remains a
// compatibility override for deployments that used the original activation
// path before agent.internalPersistentStorage became authoritative.
func ConfigureHelmInternalPersistentStorage(ctx context.Context, cfg nvcaconfig.InternalPersistentStorageConfig) error {
	spec, source, overridesAgentConfig, err := resolveInternalPersistentStorageConfig(cfg)
	if err != nil {
		return err
	}

	HelmInternalPersistentStorage.Spec = spec
	HelmInternalPersistentStorage.enabled = newBool(spec.Enabled)

	log := core.GetLogger(ctx)
	if overridesAgentConfig {
		log.WithField("environmentVariable", nvcaInternalPersistentStorageConfigJSONBase64Key).
			Warn("Internal persistent storage environment configuration overrides agent config")
	}
	log.WithFields(logrus.Fields{
		"source":            source,
		"enabled":           spec.Enabled,
		"storageClassName":  spec.StorageClassName,
		"hardResourceQuota": spec.ResourceQuota.Hard,
	}).Info("Configured internal persistent storage")

	return nil
}

func resolveInternalPersistentStorageConfig(
	cfg nvcaconfig.InternalPersistentStorageConfig,
) (InternalPersistentStorageSpec, string, bool, error) {
	spec := InternalPersistentStorageSpec{}
	configuredInAgentConfig := cfg.StorageClassName != "" || len(cfg.HardResourceQuota) > 0
	if configuredInAgentConfig {
		storageClassName := strings.TrimSpace(cfg.StorageClassName)
		if storageClassName == "" {
			return InternalPersistentStorageSpec{}, "", false,
				fmt.Errorf("agent.internalPersistentStorage.storageClassName is required when internal persistent storage is configured")
		}
		spec = InternalPersistentStorageSpec{
			Enabled:          true,
			StorageClassName: storageClassName,
			ResourceQuota: nvcav1new.InternalPersistentStorageResourceQuotaSpec{
				Hard: corev1.ResourceList(cfg.HardResourceQuota).DeepCopy(),
			},
		}
	}

	value, hasEnvironmentOverride := os.LookupEnv(nvcaInternalPersistentStorageConfigJSONBase64Key)
	if !hasEnvironmentOverride || strings.TrimSpace(value) == "" {
		source := "default"
		if configuredInAgentConfig {
			source = "agent-config"
		}
		return spec, source, false, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return InternalPersistentStorageSpec{}, "", configuredInAgentConfig,
			fmt.Errorf("decode base64 environment variable %s: %w", nvcaInternalPersistentStorageConfigJSONBase64Key, err)
	}
	var environmentConfig internalPersistentStorageEnvironmentConfig
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&environmentConfig); err != nil {
		return InternalPersistentStorageSpec{}, "", configuredInAgentConfig,
			fmt.Errorf("decode JSON environment variable %s: %w", nvcaInternalPersistentStorageConfigJSONBase64Key, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return InternalPersistentStorageSpec{}, "", configuredInAgentConfig,
			fmt.Errorf("decode JSON environment variable %s: expected exactly one JSON object",
				nvcaInternalPersistentStorageConfigJSONBase64Key)
	}
	if environmentConfig.Enabled == nil {
		return InternalPersistentStorageSpec{}, "", configuredInAgentConfig,
			fmt.Errorf("environment variable %s requires a non-null enabled field",
				nvcaInternalPersistentStorageConfigJSONBase64Key)
	}
	environmentSpec := InternalPersistentStorageSpec{
		Enabled:          *environmentConfig.Enabled,
		StorageClassName: environmentConfig.StorageClassName,
		ResourceQuota:    environmentConfig.ResourceQuota,
	}
	environmentSpec.StorageClassName = strings.TrimSpace(environmentSpec.StorageClassName)
	if environmentSpec.Enabled && environmentSpec.StorageClassName == "" {
		return InternalPersistentStorageSpec{}, "", configuredInAgentConfig,
			fmt.Errorf("environment variable %s requires storageClassName when enabled",
				nvcaInternalPersistentStorageConfigJSONBase64Key)
	}

	return environmentSpec, "environment-override", configuredInAgentConfig, nil
}

type InternalPersistentStorageSpec struct {
	Enabled          bool                                                 `json:"enabled"`
	StorageClassName string                                               `json:"storageClassName"`
	ResourceQuota    nvcav1new.InternalPersistentStorageResourceQuotaSpec `json:"resourceQuota"`
}
