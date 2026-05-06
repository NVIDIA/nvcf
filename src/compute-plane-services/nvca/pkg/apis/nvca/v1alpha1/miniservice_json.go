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

package v1alpha1

import (
	"encoding/json"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/internal/compat"
)

type miniServiceSpecJSON struct {
	Namespace       string            `json:"namespace"`
	ICMSRequestName string            `json:"icmsRequestName"`
	HelmChartConfig common.HelmConfig `json:"helmChartConfig"`
}

func (s MiniServiceSpec) MarshalJSON() ([]byte, error) {
	return json.Marshal(miniServiceSpecJSON{
		Namespace:       s.Namespace,
		ICMSRequestName: s.ICMSRequestName,
		HelmChartConfig: s.HelmChartConfig,
	})
}

func (s *MiniServiceSpec) UnmarshalJSON(data []byte) error {
	var payload miniServiceSpecJSON
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
	if requestName != "" {
		payload.ICMSRequestName = requestName
	}

	*s = MiniServiceSpec{
		Namespace:       payload.Namespace,
		ICMSRequestName: payload.ICMSRequestName,
		HelmChartConfig: payload.HelmChartConfig,
	}
	return nil
}
