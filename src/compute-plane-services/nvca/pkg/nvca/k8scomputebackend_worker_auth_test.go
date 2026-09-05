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

package nvca

import (
	"context"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	k8sinformers "k8s.io/client-go/informers"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/miniservice"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func newWorkerAuthBackend(t *testing.T, ctx context.Context, objs ...runtime.Object) K8sComputeBackend {
	k8sClient := &kubeclients.KubeClients{K8s: fakek8sclient.NewSimpleClientset(objs...)}
	f := k8sinformers.NewSharedInformerFactoryWithOptions(k8sClient.K8s, 0)
	pi := f.Core().V1().Pods()
	pinf := pi.Informer()
	f.Start(ctx.Done())
	require.True(t, cache.WaitForCacheSync(ctx.Done(), pinf.HasSynced))
	return K8sComputeBackend{
		clients: k8sClient,
		bk8s:    &BackendK8sCache{podLister: pi.Lister()},
	}
}

func TestMiniServiceWorkerAuth(t *testing.T) {
	const ns = "sr-req-1"
	workerSA := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: miniservice.WorkerServiceAccountName, Namespace: ns, UID: k8stypes.UID("sa-uid"),
	}}
	utilsPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: common.UtilsPodName, Namespace: ns, UID: k8stypes.UID("pod-uid"),
			Annotations: map[string]string{nvcatypes.InfraObjectAnnotationKey: "true"},
		},
		Spec: corev1.PodSpec{ServiceAccountName: miniservice.WorkerServiceAccountName},
	}

	t.Run("registers the NVCA utils pod", func(t *testing.T) {
		ctx, cancel := context.WithCancel(newTestContext())
		t.Cleanup(cancel)
		kc := newWorkerAuthBackend(t, ctx, workerSA, utilsPod)

		auth := kc.miniServiceWorkerAuth(ctx, ns)
		require.NotNil(t, auth)
		assert.Equal(t, &nvcatypes.WorkerAuth{
			Sub:               "system:serviceaccount:sr-req-1:nvcf-worker",
			Namespace:         ns,
			SAuid:             "sa-uid",
			WorkerIdentifiers: []nvcatypes.WorkerIdentifier{{Name: common.UtilsPodName, UID: "pod-uid"}},
		}, auth)
	})

	t.Run("nil when the utils pod runs as the default SA (feature disabled)", func(t *testing.T) {
		ctx, cancel := context.WithCancel(newTestContext())
		t.Cleanup(cancel)
		legacyPod := utilsPod.DeepCopy()
		legacyPod.Spec.ServiceAccountName = "default"
		kc := newWorkerAuthBackend(t, ctx, workerSA, legacyPod)

		assert.Nil(t, kc.miniServiceWorkerAuth(ctx, ns))
	})

	t.Run("nil when the utils pod does not exist yet", func(t *testing.T) {
		ctx, cancel := context.WithCancel(newTestContext())
		t.Cleanup(cancel)
		kc := newWorkerAuthBackend(t, ctx, workerSA)

		assert.Nil(t, kc.miniServiceWorkerAuth(ctx, ns))
	})

	t.Run("nil when the worker SA is missing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(newTestContext())
		t.Cleanup(cancel)
		kc := newWorkerAuthBackend(t, ctx, utilsPod)

		assert.Nil(t, kc.miniServiceWorkerAuth(ctx, ns))
	})

	t.Run("nil when the utils pod lacks the NVCA infra annotation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(newTestContext())
		t.Cleanup(cancel)
		unowned := utilsPod.DeepCopy()
		unowned.Annotations = nil
		kc := newWorkerAuthBackend(t, ctx, workerSA, unowned)

		assert.Nil(t, kc.miniServiceWorkerAuth(ctx, ns))
	})
}
