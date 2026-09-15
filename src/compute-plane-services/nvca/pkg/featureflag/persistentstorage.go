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

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

const nvcaInternalPersistentStorageConfigJSONBase64Key = "NVCA_INTERNAL_PERSISTENT_STORAGE_CONFIG_JSON_BASE64"

type InternalPersistentStorageFeatureFlag struct {
	FeatureFlag
	Spec InternalPersistentStorageSpec
}

func newHelmInternalPersistentStorageFeatureFlag(defaultValue bool) *InternalPersistentStorageFeatureFlag {
	ctx := core.WithDefaultLogger(context.Background())
	log := core.GetLogger(ctx)

	f := &InternalPersistentStorageFeatureFlag{
		FeatureFlag: FeatureFlag{
			defaultValue: newBool(defaultValue),
			enabled:      newBool(defaultValue),
			Key:          "HelmInternalPersistentStorage",
		},
		Spec: InternalPersistentStorageSpec{
			Enabled: false,
		},
	}

	// Retrieve the nvca internal key from the environment
	if v, ok := os.LookupEnv(nvcaInternalPersistentStorageConfigJSONBase64Key); ok && v != "" {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			log.WithError(err).Errorf("failed to decode base64 env var %s", nvcaInternalPersistentStorageConfigJSONBase64Key)
			return f
		}
		cfg := InternalPersistentStorageSpec{}
		err = json.NewDecoder(bytes.NewReader(b)).Decode(&cfg)
		if err != nil {
			log.WithError(err).Errorf("failed to decode JSON resource for env var %s", nvcaInternalPersistentStorageConfigJSONBase64Key)
			return f
		}
		// Ensure the persisent storage class name is set otherwise skip
		if cfg.StorageClassName == "" {
			log.Errorf("the env var %s contains an empty storageClassName, will not enable internal-persistent-storage",
				nvcaInternalPersistentStorageConfigJSONBase64Key)
			return f
		}
		f.Spec = cfg
		f.enabled = newBool(cfg.Enabled)
	}

	return f
}

type InternalPersistentStorageSpec struct {
	Enabled          bool                                                 `json:"enabled"`
	StorageClassName string                                               `json:"storageClassName"`
	ResourceQuota    nvcav1new.InternalPersistentStorageResourceQuotaSpec `json:"resourceQuota"`
}
