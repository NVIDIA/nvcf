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

package translateutil

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var scheme = runtime.NewScheme()

func init() {
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
}

func SetObjectGVK(obj metav1.Object) error {
	robj := obj.(runtime.Object)
	if robj.GetObjectKind().GroupVersionKind() != (schema.GroupVersionKind{}) {
		return nil
	}
	gvks, ok, err := scheme.ObjectKinds(robj)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("code bug: object is unversioned")
	}
	if len(gvks) != 1 {
		return fmt.Errorf("code bug: object versions are not exact (%+q)", gvks)
	}
	robj.GetObjectKind().SetGroupVersionKind(gvks[0])
	return nil
}
