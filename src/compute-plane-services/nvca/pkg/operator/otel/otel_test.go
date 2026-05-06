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

package otel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	otelattr "go.opentelemetry.io/otel/attribute"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

func TestOTel_NewTracer(t *testing.T) {
	t.Parallel()
	assert.NotNil(t, NewTracer())
	assert.NotNil(t, NewTracer(&mockAttributesGetter{
		attributes: []otelattr.KeyValue{
			otelattr.String("foo", "bar"),
		},
	}))
}

func TestOTel_GetOTelAttributesFromNVCFBackend(t *testing.T) {
	nvcfbe := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "1.26.7",
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "randomNCAId",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "nv-dgx-cloud-a100-azure",
					ClusterGroupName: "nv-dgx-cloud-a100",
				},
			},
		},
	}

	expectedAttrs := map[string]string{
		NVCAClusterNameAttributeKey:  nvcfbe.Spec.ClusterConfig.ClusterName,
		NVCAClusterGroupAttributeKey: nvcfbe.Spec.ClusterConfig.ClusterGroupName,
		NVCAVersionAttributeKey:      nvcfbe.Spec.Version,
	}
	otelAttrs := GetOTelAttributesFromNVCFBackend(nvcfbe)
	assert.Len(t, otelAttrs, len(expectedAttrs))
	actualAttrs := map[string]string{}
	for _, attr := range otelAttrs {
		actualAttrs[string(attr.Key)] = attr.Value.AsString()
	}
	assert.Equal(t, expectedAttrs, actualAttrs)
}

type mockAttributesGetter struct {
	attributes []otelattr.KeyValue
}

func (m *mockAttributesGetter) GetOTelAttributes() []otelattr.KeyValue {
	return m.attributes
}
