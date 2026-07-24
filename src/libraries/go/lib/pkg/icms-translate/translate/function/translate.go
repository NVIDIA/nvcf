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

package function

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
)

type TranslateConfig struct {
	common.TranslateConfig `json:",inline"`

	PodUseHostNetwork bool         `json:"podUseHostNetwork,omitempty"`
	PodDNSPolicy      v1.DNSPolicy `json:"podDNSPolicy,omitempty"`

	// enable this to create side-cars as Deployment instead-of
	// barepod for non-disruptive worker upgrades
	SidecarAsDeployment bool `json:"sidecarAsDeployment"`
	// Control Helm workload secret translation.
	// Setting this to true means that the caller should use common.FilterObjectImageRegistryAuths
	// to find only relevant secrets to translation.
	DisableHelmWorkloadSecretTranslation bool

	// Deprecated: LLM request router address must be supplied by environment.
	DefaultStargateAddress string `json:"defaultStargateAddress,omitempty"`
	// Whether to use QUIC insecure mode for stargate connections
	// If not set, "--quick-insecure" is omitted
	StargateQUICInsecure bool `json:"stargateQUICInsecure,omitempty"`

	// LLMSidecarRunAsUser is the UID both LLM sidecars run as (default 1000).
	// Negative leaves runAsUser unset so the platform (e.g. OpenShift) assigns.
	LLMSidecarRunAsUser *int64 `json:"llmSidecarRunAsUser,omitempty"`
}

func Translate(t CreationQueueMessage, tcfg TranslateConfig) (objs []metav1.Object, err error) {
	tcfg.Default()

	if t.LaunchSpecification == nil {
		return nil, fmt.Errorf("launch specification must be set")
	}
	if len(t.LaunchArtifacts) != 0 {
		return nil, fmt.Errorf("launch artifacts are not supported")
	}

	switch {
	case t.LaunchSpecification.HelmChartLaunchSpecification != nil:
		if tcfg.SidecarAsDeployment {
			objs, err = translateHelmChartUtilsDeploy(t, tcfg)
		} else {
			objs, err = translateHelmChart(t, tcfg)
		}
	default:
		if tcfg.SidecarAsDeployment {
			objs, err = translateContainerUtilsDeploy(t, tcfg)
		} else {
			objs, err = translateContainer(t, tcfg)
		}
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
