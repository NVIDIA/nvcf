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
	"os"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

const nvcaSharedStorageonfigJSONBase64Key = "NVCA_SHARED_STORAGE_CONFIG_JSON_BASE64"

type HelmSharedStorageFeatureFlag struct {
	FeatureFlag
	ServerSpec   SharedStorageSpec
	TaskDataSpec nvcav1new.SharedStorageTaskDataSpec
}

func newHelmSharedStorageFeatureFlag(key string, defaultValue bool) *HelmSharedStorageFeatureFlag {
	f := &HelmSharedStorageFeatureFlag{
		FeatureFlag: FeatureFlag{
			defaultValue: newBool(defaultValue),
			enabled:      newBool(defaultValue),
			Key:          key,
		},
		ServerSpec: SharedStorageSpec{
			Server: &nvcav1new.SharedStorageServerSpec{
				SMBServerContainerResources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("20m"),
						corev1.ResourceMemory: resource.MustParse("150Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			},
		},
		TaskDataSpec: nvcav1new.SharedStorageTaskDataSpec{
			// Size defaults to 100Gi
			Size: resource.MustParse("100Gi"),
		},
	}

	// Retrieve the nvca internal key from the environment
	cfg, err := ParseHelmSharedStorageSpecFromEnv()
	if err != nil {
		return f
	}
	// Check if shared storage is enabled, and override the default value
	if cfg.Enabled != nil {
		f.defaultValue = cfg.Enabled
	}

	if cfg.Server != nil {
		f.ServerSpec.Server = cfg.Server
	}

	// Only override task data if specified, otherwise leave defaults
	if cfg.TaskData != nil {
		f.TaskDataSpec = *cfg.TaskData
	}

	// Store the flag in the flags map to be set by the parser later
	flagsMutex.Lock()
	flags[key] = &f.FeatureFlag
	flagsMutex.Unlock()

	return f
}

func ParseHelmSharedStorageSpecFromEnv() (cfg SharedStorageSpec, err error) {
	ctx := core.WithDefaultLogger(context.Background())
	log := core.GetLogger(ctx)

	// Retrieve the nvca internal key from the environment
	if v, ok := os.LookupEnv(nvcaSharedStorageonfigJSONBase64Key); ok && v != "" {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			log.WithError(err).Errorf("failed to decode base64 env var %s", nvcaSharedStorageonfigJSONBase64Key)
			return SharedStorageSpec{}, err
		}
		err = json.NewDecoder(bytes.NewReader(b)).Decode(&cfg)
		if err != nil {
			log.WithError(err).Errorf("failed to decode JSON resource for env var %s", nvcaSharedStorageonfigJSONBase64Key)
			return SharedStorageSpec{}, err
		}
	}

	return cfg, nil
}

type SharedStorageSpec struct {
	Enabled  *bool                                `json:"enabled,omitempty"`
	Server   *nvcav1new.SharedStorageServerSpec   `json:"server,omitempty"`
	TaskData *nvcav1new.SharedStorageTaskDataSpec `json:"taskData,omitempty"`
}
