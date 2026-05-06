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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestClientShim(t *testing.T) {
	ctx := t.Context()

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sr-foo",
		},
	}
	crClient := clientfake.NewClientBuilder().
		WithObjects(namespace).
		WithStatusSubresource(&corev1.ResourceQuota{}).
		Build()
	k8sClient := k8sfake.NewSimpleClientset(namespace)

	csCR := NewControllerRuntimeClientShim(crClient)
	csK8s := NewK8sClientShim(k8sClient, &corev1.ResourceQuota{})

	rq := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-rq",
			Namespace: namespace.Name,
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				"foo": resource.MustParse("1"),
			},
			Scopes: []corev1.ResourceQuotaScope{corev1.ResourceQuotaScopeBestEffort},
		},
	}

	for i, cs := range []ClientShim{csCR, csK8s} {
		t.Run(fmt.Sprintf("cs %d", i), func(t *testing.T) {
			var err error
			rq := rq.DeepCopy()
			err = cs.Create(ctx, rq)
			require.NoError(t, err)
			err = cs.Create(ctx, rq)
			require.Error(t, err)

			_, err = cs.Get(ctx, rq)
			require.NoError(t, err)
			isPatched, err := cs.Patch(ctx, rq, rq)
			require.NoError(t, err)
			assert.False(t, isPatched)

			oldRQ := rq.DeepCopy()
			rq.Spec.Hard["foo"] = resource.MustParse("2")

			if i == 0 {
				err = crClient.Update(ctx, rq)
				require.NoError(t, err)
			} else {
				rq, err = k8sClient.CoreV1().ResourceQuotas(rq.Namespace).Update(ctx, rq, metav1.UpdateOptions{})
				require.NoError(t, err)
			}

			gotObj, err := cs.Get(ctx, rq)
			require.NoError(t, err)
			isPatched, err = cs.Patch(ctx, oldRQ, gotObj)
			require.NoError(t, err)
			assert.True(t, isPatched)
			gotObj, err = cs.Get(ctx, rq)
			require.NoError(t, err)
			isPatched, err = cs.Patch(ctx, rq, gotObj)
			require.NoError(t, err)
			assert.False(t, isPatched)

			oldRQ = rq.DeepCopy()
			gotObj, err = cs.Get(ctx, rq)
			require.NoError(t, err)
			rq = gotObj.(*corev1.ResourceQuota)
			rq.Status = corev1.ResourceQuotaStatus{
				Hard: rq.Spec.Hard,
				Used: rq.Spec.Hard,
			}

			if i == 0 {
				err = crClient.Status().Update(ctx, rq)
				require.NoError(t, err)
			} else {
				rq, err = k8sClient.CoreV1().ResourceQuotas(rq.Namespace).UpdateStatus(ctx, rq, metav1.UpdateOptions{})
				require.NoError(t, err)
			}

			gotObj, err = cs.Get(ctx, rq)
			require.NoError(t, err)
			isPatched, err = cs.Patch(ctx, oldRQ, gotObj)
			require.NoError(t, err)
			assert.True(t, isPatched)
		})
	}
}
