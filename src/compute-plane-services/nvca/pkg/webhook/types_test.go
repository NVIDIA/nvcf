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

package webhook

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDCGMMetricsConfigFromAnnotations(t *testing.T) {
	tests := []struct {
		name           string
		annotations    map[string]string
		expectedConfig DCGMMetricsConfig
		hasError       bool
	}{
		{
			name: "Annotations with prometheus.io/port",
			annotations: map[string]string{
				prometheusPortAnnotationKey: "8080",
			},
			expectedConfig: DCGMMetricsConfig{
				Annotations: map[string]string{
					prometheusPortAnnotationKey: "8080",
				},
				ContainerPort: 8080,
			},
		},
		{
			name:        "Annotations without prometheus.io/port",
			annotations: map[string]string{},
			expectedConfig: DCGMMetricsConfig{
				Annotations: map[string]string{
					prometheusPortAnnotationKey: strconv.FormatInt(int64(prometheusDefaultPort), 10),
				},
				ContainerPort: prometheusDefaultPort,
			},
		},
		{
			name: "Annotations with invalid prometheus.io/port",
			annotations: map[string]string{
				prometheusPortAnnotationKey: "abc",
			},
			expectedConfig: DCGMMetricsConfig{},
			hasError:       true,
		},
		{
			name: "Annotations with prometheus.io/port out of range",
			annotations: map[string]string{
				prometheusPortAnnotationKey: "2147483648",
			},
			expectedConfig: DCGMMetricsConfig{},
			hasError:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actualConfig, actualError := DCGMMetricsConfigFromAnnotations(tt.annotations)
			assert.Equal(t, tt.expectedConfig, actualConfig)
			assert.Equal(t, actualError != nil, tt.hasError)
		})
	}
}
