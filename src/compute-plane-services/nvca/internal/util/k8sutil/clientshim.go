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

package k8sutil

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClientShim struct {
	Get    func(ctx context.Context, obj client.Object) (client.Object, error)
	Create func(ctx context.Context, obj client.Object) error
	Patch  func(ctx context.Context, oldObj, newObj client.Object) (bool, error)
}

func NewControllerRuntimeClientShim(crClient client.Client) ClientShim {
	return ClientShim{
		Get: func(ctx context.Context, obj client.Object) (client.Object, error) {
			gotObj := obj.DeepCopyObject().(client.Object)
			err := crClient.Get(ctx, client.ObjectKeyFromObject(obj), gotObj)
			return gotObj, err
		},
		Create: func(ctx context.Context, obj client.Object) error {
			return crClient.Create(ctx, obj)
		},
		Patch: func(ctx context.Context, oldObj, newObj client.Object) (bool, error) {
			_, _, p, isEmpty, err := getPatchData(oldObj, newObj)
			if err != nil || isEmpty {
				return false, err
			}
			return true, crClient.Patch(ctx, newObj, p)
		},
	}
}

func IsEmptyPatch(p client.Patch, newObj client.Object) (bool, error) {
	patchData, err := p.Data(newObj)
	if err != nil {
		return false, err
	}
	return isEmptyPatchData(patchData), nil
}

func isEmptyPatchData(patchData []byte) bool { return string(patchData) == "{}" }

func NewK8sClientShim(k8sClient kubernetes.Interface, t client.Object) (cs ClientShim) {
	// TODO: dynamic client with restmapper.
	switch t.(type) {
	case *corev1.ResourceQuota:
		cs.Get = func(ctx context.Context, obj client.Object) (client.Object, error) {
			return k8sClient.CoreV1().ResourceQuotas(obj.GetNamespace()).Get(ctx, obj.GetName(), metav1.GetOptions{})
		}
		cs.Create = func(ctx context.Context, obj client.Object) error {
			_, err := k8sClient.CoreV1().ResourceQuotas(obj.GetNamespace()).
				Create(ctx, obj.(*corev1.ResourceQuota), metav1.CreateOptions{})
			return err
		}
		cs.Patch = func(ctx context.Context, oldObj, newObj client.Object) (bool, error) {
			patchType, patchData, _, isEmpty, err := getPatchData(oldObj, newObj)
			if err != nil || isEmpty {
				return false, err
			}
			_, err = k8sClient.CoreV1().ResourceQuotas(newObj.GetNamespace()).
				Patch(ctx, newObj.GetName(), patchType, patchData, metav1.PatchOptions{})
			return true, err
		}
	default:
		panic(fmt.Sprintf("unknown object type %T", t))
	}
	return cs
}

func getPatchData(oldObj, newObj client.Object) (types.PatchType, []byte, client.Patch, bool, error) {
	// Set resource version to current so empty patches are detected.
	newObj.SetResourceVersion(oldObj.GetResourceVersion())
	p := client.StrategicMergeFrom(oldObj)
	patchData, err := p.Data(newObj)
	if err != nil {
		return "", nil, nil, false, err
	}
	if isEmptyPatchData(patchData) {
		return "", nil, nil, true, nil
	}
	return types.StrategicMergePatchType, patchData, p, false, nil
}
