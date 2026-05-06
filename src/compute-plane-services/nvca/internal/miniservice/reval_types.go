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

package mscontroller

import (
	"encoding/json"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
)

type HelmReValRenderInput struct {
	HelmChartURL            string                    `json:"helmChart,omitempty"`
	ReleaseName             string                    `json:"releaseName,omitempty"`
	HelmChartServicePort    *int32                    `json:"helmChartServicePort,omitempty"`
	HelmChartServiceName    string                    `json:"helmChartServiceName,omitempty"`
	InstanceType            string                    `json:"instanceType,omitempty"`
	GPUName                 string                    `json:"gpu,omitempty"`
	Values                  json.RawMessage           `json:"configuration,omitempty"`
	K8sVersion              string                    `json:"k8sVersion,omitempty"`
	APIVersions             []string                  `json:"apiVersions,omitempty"`
	APIKey                  string                    `json:"apiKey,omitempty"`
	HelmRegistryAuthConfig  common.RegistryAuthConfig `json:"helmRegistryAuthConfig,omitempty"`
	ImageRegistryAuthConfig common.RegistryAuthConfig `json:"imageRegistryAuthConfig,omitempty"`
	// ValidationPolicy to configure ReVal's validation policy name and allowed k8s types.
	ValidationPolicy *nvcaconfig.ValidationPolicyConfig `json:"validationPolicy,omitempty"`

	// NCAID is used for metrics internally.
	NCAID string `json:"-"`
}

type HelmReValRenderOutput struct {
	Valid            *bool           `json:"valid,omitempty"`
	ValidationErrors []string        `json:"validationErrors,omitempty"`
	InternalErrors   []string        `json:"internalErrors,omitempty"`
	Output           json.RawMessage `json:"output,omitempty"`
}
