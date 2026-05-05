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

package v1

import (
	"encoding/json"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/internal/compat"
)

type storageRequestSpecJSON struct {
	Type                 StorageRequestType             `json:"type"`
	ICMSRequestName      string                         `json:"icmsRequestName"`
	ICMSRequestNamespace string                         `json:"icmsRequestNamespace,omitempty"`
	ModelCache           *ModelCacheSpec                `json:"modelCache,omitempty"`
	SharedStorage        *SharedStorageSpec             `json:"sharedStorage,omitempty"`
	InternalStorage      *InternalPersistentStorageSpec `json:"internalPersistentStorage,omitempty"`
}

func (s StorageRequestSpec) MarshalJSON() ([]byte, error) {
	return json.Marshal(storageRequestSpecJSON{
		Type:                 s.Type,
		ICMSRequestName:      s.ICMSRequestName,
		ICMSRequestNamespace: s.ICMSRequestNamespace,
		ModelCache:           s.ModelCache,
		SharedStorage:        s.SharedStorage,
		InternalStorage:      s.InternalPersistentStorage,
	})
}

func (s *StorageRequestSpec) UnmarshalJSON(data []byte) error {
	var payload storageRequestSpecJSON
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	requestName, err := compat.DecodeRequestName(fields)
	if err != nil {
		return err
	}
	requestNamespace, err := compat.DecodeRequestNamespace(fields)
	if err != nil {
		return err
	}
	if requestName != "" {
		payload.ICMSRequestName = requestName
	}
	if requestNamespace != "" {
		payload.ICMSRequestNamespace = requestNamespace
	}

	*s = StorageRequestSpec{
		Type:                      payload.Type,
		ICMSRequestName:           payload.ICMSRequestName,
		ICMSRequestNamespace:      payload.ICMSRequestNamespace,
		ModelCache:                payload.ModelCache,
		SharedStorage:             payload.SharedStorage,
		InternalPersistentStorage: payload.InternalStorage,
	}
	return nil
}
