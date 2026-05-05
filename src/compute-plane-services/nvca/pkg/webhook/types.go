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
	"fmt"
	"strconv"
)

const (
	dcgmDefaultAnnotations            = "prometheus.io/scrape=true,prometheus.io/path=/metrics,prometheus.io/port=9400"
	prometheusPortAnnotationKey       = "prometheus.io/port"
	prometheusDefaultPort       int32 = 9400
)

type DCGMMetricsConfig struct {
	Annotations   map[string]string
	ContainerPort int32
}

func DCGMMetricsConfigFromAnnotations(annotations map[string]string) (DCGMMetricsConfig, error) {
	metricsContainerPort := prometheusDefaultPort
	if portV, ok := annotations[prometheusPortAnnotationKey]; ok {
		v, err := strconv.ParseInt(portV, 10, 32)
		if err != nil {
			return DCGMMetricsConfig{}, fmt.Errorf("failed to parse %s annotation value %s, %w", prometheusPortAnnotationKey, portV, err)
		}
		//nolint:gosec
		metricsContainerPort = int32(v)
	} else {
		annotations[prometheusPortAnnotationKey] = strconv.FormatInt(int64(metricsContainerPort), 10)
	}

	return DCGMMetricsConfig{
		Annotations:   annotations,
		ContainerPort: metricsContainerPort,
	}, nil
}
