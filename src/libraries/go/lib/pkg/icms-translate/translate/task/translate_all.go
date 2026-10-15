/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package task

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
)

type TranslateConfig struct {
	common.TranslateConfig `json:",inline"`

	PodUseHostNetwork bool             `json:"podUseHostNetwork,omitempty"`
	PodDNSPolicy      corev1.DNSPolicy `json:"podDNSPolicy,omitempty"`

	// Control Helm workload secret translation.
	// Setting this to true means that the caller should use common.FilterObjectImageRegistryAuths
	// to find only relevant secrets to translation.
	DisableHelmWorkloadSecretTranslation bool
}

func Translate(t CreationQueueMessage, tcfg TranslateConfig) (objs []metav1.Object, err error) {
	tcfg.Default()

	switch {
	case t.LaunchSpecification.HelmChartLaunchSpecification != nil:
		objs, err = translateHelmChart(t, tcfg)
	default:
		objs, err = translateContainer(t, tcfg)
	}
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		if err := translateutil.SetObjectGVK(obj); err != nil {
			return nil, err
		}
	}
	return objs, nil
}
