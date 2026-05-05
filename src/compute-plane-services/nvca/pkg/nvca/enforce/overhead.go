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

package enforce

import (
	"context"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
)

func NewInfraOverheadGetter(fff featureflag.Fetcher, cfg nvcaconfig.Config, getRuntimeClass getRuntimeClassFunc) InfraOverheadGetter {
	return InfraOverheadGetterFunc(func(ctx context.Context) (corev1.ResourceList, error) {
		if !fff.IsFeatureFlagEnabled(featureflag.InfraResourceOverhead) {
			return corev1.ResourceList{}, nil
		}

		var kataOverhead []corev1.ResourceList
		if fff.IsAttributeEnabled(featureflag.AttrKataRuntimeIsolation) {
			kataRuntimeClass, err := getRuntimeClass(ctx, kata.RuntimeClassNameGPU)
			if err != nil {
				core.GetLogger(ctx).WithField("runtimeClass", kata.RuntimeClassNameGPU).WithError(err).Error("failed to get kata runtime class")
				return corev1.ResourceList{}, err
			}
			if kataRuntimeClass.Overhead != nil {
				kataOverhead = append(kataOverhead, kataRuntimeClass.Overhead.PodFixed)
			}
		}
		return k8sutil.GetInfraContainerResourceOverhead(cfg, fff, kataOverhead...), nil
	})
}

type getRuntimeClassFunc func(context.Context, string) (*nodev1.RuntimeClass, error)

func GetRuntimeClassK8sClient(k8sClient kubernetes.Interface) getRuntimeClassFunc {
	return func(ctx context.Context, name string) (*nodev1.RuntimeClass, error) {
		return k8sClient.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	}
}

type InfraOverheadGetter interface {
	GetInfraOverhead(context.Context) (corev1.ResourceList, error)
}

type InfraOverheadGetterFunc func(context.Context) (corev1.ResourceList, error)

func (f InfraOverheadGetterFunc) GetInfraOverhead(ctx context.Context) (corev1.ResourceList, error) {
	return f(ctx)
}

var NoOpInfraOverheadGetter = InfraOverheadGetterFunc(func(context.Context) (corev1.ResourceList, error) { return corev1.ResourceList{}, nil })
