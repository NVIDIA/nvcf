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

package storage

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
)

func (r *Reconciler) translateWorkload(
	namespace string,
	icmsReq *nvcav2beta1.ICMSRequest,
) (objs []client.Object, err error) {
	cmnTCfg := common.TranslateConfig{
		Namespace:                    namespace,
		ObjectNameBase:               icmsReq.Name,
		InstanceTypeLabelSelectorKey: nodefeatures.UniformInstanceTypeLabelKey,
		// These don't matter but make the translator happy.
		WorkloadResources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				"nvidia.com/gpu": resource.MustParse("1"),
			},
		},
		Tolerations:        append([]corev1.Toleration(nil), r.cfg.Workload.Tolerations...),
		OTelResources:      k8sutil.GetContainerResourcesBYOO(r.cfg),
		FluentbitResources: k8sutil.GetContainerResourcesFluentBit(r.cfg),
		FluentbitEnabled:   false, // Storage workloads don't need FluentBit
		ClusterRegion:      r.clusterRegion,
	}

	switch icmsReq.Spec.Action {
	case common.FunctionCreationAction:
		if cmi := icmsReq.Spec.CreationMsgInfo; cmi.FunctionLaunchSpecification == nil {
			return nil, fmt.Errorf("ICMSRequest %s creation message is invalid: missing function launch specification",
				icmsReq.Name,
			)
		}
		objs, err = r.translateFunctionWorkload(cmnTCfg, icmsReq)
	case common.TaskCreationAction:
		if cmi := icmsReq.Spec.CreationMsgInfo; cmi.TaskLaunchSpecification == nil {
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

func findModelCacheObjects(objs []client.Object) (
	cacheInitJob *batchv1.Job,
	cacheInitPVC *corev1.PersistentVolumeClaim,
	pullSecrets []*corev1.Secret,
) {
	// Get the artifacts and perform type checks.
	for _, obj := range objs {
		switch t := obj.(type) {
		case *corev1.PersistentVolumeClaim:
			if strings.HasPrefix(t.Name, "rw-pvc-") {
				cacheInitPVC = t
			}
		case *batchv1.Job:
			if strings.HasPrefix(t.Name, "writer-job-") {
				cacheInitJob = t
			}
		case *corev1.Secret:
			if k8sutil.IsNVCFWorkerImagePullSecretObject(t) {
				pullSecrets = append(pullSecrets, t)
			}
		}
	}

	return cacheInitJob, cacheInitPVC, pullSecrets
}
