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
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
)

const (
	// Translation error events for metrics.
	EventTranslateFunctionError = "EVENT_TRANSLATE_FUNCTION_ERROR"
	EventTranslateTaskError     = "EVENT_TRANSLATE_TASK_ERROR"
)

func (r *Reconciler) translateWorkload(
	ms *nvcav1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) (objs []client.Object, err error) {
	cmnTCfg := common.TranslateConfig{
		Namespace:                    ms.Spec.Namespace,
		ObjectNameBase:               icmsReq.Name,
		HelmInstanceID:               ms.Name,
		InstanceTypeLabelSelectorKey: nodefeatures.UniformInstanceTypeLabelKey,
		WorkloadResources:            corev1.ResourceRequirements{},
		Tolerations:                  append([]corev1.Toleration(nil), r.cfg.Workload.Tolerations...),
		OTelResources:                k8sutil.GetContainerResourcesBYOO(r.cfg),
		FluentbitResources:           k8sutil.GetContainerResourcesFluentBit(r.cfg),
		FluentbitEnabled:             r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.BYOOFluentBit),
		ClusterRegion:                r.ClusterRegion,
		ClusterName:                  r.ClusterName,
	}

	switch icmsReq.Spec.Action {
	case common.FunctionCreationAction:
		if cmi := icmsReq.Spec.CreationMsgInfo; cmi.FunctionLaunchSpecification == nil ||
			cmi.FunctionLaunchSpecification.HelmChartLaunchSpecification == nil {
			return nil, fmt.Errorf("ICMSRequest %s creation message is invalid: missing function launch specification",
				icmsReq.Name,
			)
		}
		objs, err = r.translateFunctionWorkload(cmnTCfg, icmsReq)
	case common.TaskCreationAction:
		if cmi := icmsReq.Spec.CreationMsgInfo; cmi.TaskLaunchSpecification == nil ||
			cmi.TaskLaunchSpecification.HelmChartLaunchSpecification == nil {
			return nil, fmt.Errorf("ICMSRequest %s creation message is invalid: missing task launch specification",
				icmsReq.Name,
			)
		}
		objs, err = r.translateTaskWorkload(cmnTCfg, icmsReq)
	default:
		return nil, fmt.Errorf("unknown ICMSRequest action: %s", icmsReq.Spec.Action)
	}
	return objs, err
}

func (r *Reconciler) translateFunctionWorkload(
	cmnTCfg common.TranslateConfig,
	icmsReq *nvcav2beta1.ICMSRequest,
) ([]client.Object, error) {
	tcfg := function.TranslateConfig{
		TranslateConfig:        cmnTCfg,
		DefaultStargateAddress: r.cfg.Workload.DefaultStargateAddress,
		StargateQUICInsecure:   r.cfg.Workload.StargateQUICInsecure,
	}

	msg := function.CreationQueueMessage{
		Details:                      icmsReq.Spec.FunctionDetails,
		CreationQueueMessageMetadata: icmsReq.Spec.CreationMsgInfo.CreationQueueMessageMetadata,
		LaunchSpecification:          icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification,
	}

	objs, err := function.Translate(msg, tcfg)
	if err != nil {
		r.Metrics.RecordMiniServiceEventError(EventTranslateFunctionError)
		return nil, err
	}
	return metaToClientObjs(objs), nil
}

func (r *Reconciler) translateTaskWorkload(
	cmnTCfg common.TranslateConfig,
	icmsReq *nvcav2beta1.ICMSRequest,
) ([]client.Object, error) {
	tcfg := task.TranslateConfig{
		TranslateConfig: cmnTCfg,
	}

	msg := task.CreationQueueMessage{
		Details:                      icmsReq.Spec.TaskDetails,
		CreationQueueMessageMetadata: icmsReq.Spec.CreationMsgInfo.CreationQueueMessageMetadata,
		LaunchSpecification:          *icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification,
	}

	objs, err := task.Translate(msg, tcfg)
	if err != nil {
		r.Metrics.RecordMiniServiceEventError(EventTranslateTaskError)
		return nil, err
	}
	return metaToClientObjs(objs), nil
}

func metaToClientObjs(mobjs []metav1.Object) (cobjs []client.Object) {
	cobjs = make([]client.Object, len(mobjs))
	for i, obj := range mobjs {
		cobjs[i] = obj.(client.Object)
	}
	return cobjs
}
