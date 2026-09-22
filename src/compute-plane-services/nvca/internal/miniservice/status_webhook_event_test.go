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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/miniservice/chartcache"
	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

// TestDoStatus_StatefulSetFailedCreate covers a StatefulSet that is short of replicas
// while carrying a FailedCreate event. When the API server could not call an admission
// webhook the object must stay pending so the StatefulSet controller's retry can
// complete it, and the MiniService must become healthy once it does even though the
// event is still present. A quota FailedCreate keeps the existing terminal handling.
func TestDoStatus_StatefulSetFailedCreate(t *testing.T) {
	const (
		ns     = "test-ss-failedcreate-ns"
		ssName = "multi-node-test"
		ssUID  = types.UID("ss-uid")
	)
	webhookMsg := `create Pod ` + ssName + `-2 in StatefulSet ` + ssName + ` failed error: ` +
		`Internal error occurred: failed calling webhook "mutate-pod-nodeaffinity.nvca.nvcf.nvidia.io": ` +
		`failed to call webhook: the server is currently unable to handle the request`
	quotaMsg := `create Pod ` + ssName + `-2 in StatefulSet ` + ssName + ` failed error: ` +
		`pods "` + ssName + `-2" is forbidden: exceeded quota: max-gpus`

	readyPodStatus := corev1.PodStatus{
		Phase: corev1.PodRunning,
		Conditions: []corev1.PodCondition{
			{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
			{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
			{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		},
	}
	newStatefulSet := func(replicas, ready int32) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
			ObjectMeta: metav1.ObjectMeta{Name: ssName, Namespace: ns, UID: ssUID},
			Spec: appsv1.StatefulSetSpec{
				Replicas:            ptr.To(replicas),
				PodManagementPolicy: appsv1.ParallelPodManagement,
				Selector:            &metav1.LabelSelector{MatchLabels: map[string]string{"app": ssName}},
			},
			Status: appsv1.StatefulSetStatus{
				Replicas:          ready,
				ReadyReplicas:     ready,
				AvailableReplicas: ready,
			},
		}
	}
	newReplicaPod := func(i int) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ssName + "-" + string(rune('0'+i)),
				Namespace: ns,
				Labels:    map[string]string{"app": ssName},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1", Kind: "StatefulSet", Name: ssName, UID: ssUID,
				}},
			},
			Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "worker"}}},
			Status: readyPodStatus,
		}
	}
	utilsPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: common.UtilsPodName, Namespace: ns},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: common.UtilsContainerName}}},
		Status:     readyPodStatus,
	}
	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  common.UtilsContainerName,
		Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	newFailedCreateEvent := func(msg string) *corev1.Event {
		return &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{Name: "ss-failed-create", Namespace: ns},
			InvolvedObject: corev1.ObjectReference{
				APIVersion: "apps/v1", Kind: "StatefulSet", Name: ssName, Namespace: ns, UID: ssUID,
			},
			Type:    corev1.EventTypeWarning,
			Reason:  "FailedCreate",
			Message: msg,
		}
	}

	newReconciler := func(t *testing.T, ms *v1alpha1.MiniService, ss *appsv1.StatefulSet, extra ...client.Object) *Reconciler {
		ctx := newTestContext()
		objs := append([]client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
			ss, utilsPod, newReplicaPod(0), newReplicaPod(1),
		}, extra...)
		c, _ := newFakeClient(mgrScheme, objs...)
		cache := chartcache.New(t.TempDir())
		require.NoError(t, cache.Start(ctx))
		r := &Reconciler{
			ControllerOptions: ControllerOptions{
				K8sTimeConfig: (&nvcak8sutil.TimeConfig{}).Complete(),
			},
			Client:                c,
			Decoder:               serializer.NewCodecFactory(mgrScheme).UniversalDeserializer(),
			chartCache:            cache,
			newPermissionsChecker: newFakePermissionsChecker,
			now:                   time.Now,
		}
		r.statusCheckers = r.makeStatusCheckers()
		rendered, err := json.Marshal([]any{ss})
		require.NoError(t, err)
		require.NoError(t, r.saveRenderedData(ctx, ms, rendered))
		return r
	}
	newMiniService := func() *v1alpha1.MiniService {
		return &v1alpha1.MiniService{
			ObjectMeta: metav1.ObjectMeta{Name: "ss-test-ms", UID: "ss-test-uid"},
			Spec:       v1alpha1.MiniServiceSpec{Namespace: ns, ICMSRequestName: "ss-test-icms"},
		}
	}
	icmsReq := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "ss-test-icms", Namespace: "nvcf-backend"},
		Spec:       nvcav2beta1.ICMSRequestSpec{Action: common.RequestICMSInstances},
	}
	healthyCond := func(ms *v1alpha1.MiniService) *metav1.Condition {
		return meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionObjectsHealthy)
	}

	t.Run("webhook call failure keeps the StatefulSet pending and it recovers", func(t *testing.T) {
		ctx := newTestContext()
		ms := newMiniService()
		ss := newStatefulSet(3, 2)
		r := newReconciler(t, ms, ss, newFailedCreateEvent(webhookMsg))

		res, err := r.doStatus(ctx, ms, icmsReq)
		require.NoError(t, err, "a webhook the API server could not call is not terminal")
		cond := healthyCond(ms)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, v1alpha1.MiniServiceStatusReasonWaitingObjectReadiness, cond.Reason,
			"the object is pending, not failing")
		assert.Contains(t, cond.Message, "failed calling webhook", "the event is still reported as a warning")
		assert.Greater(t, res.RequeueAfter, time.Duration(0))

		// The StatefulSet controller retries the create and the third replica comes up.
		// The FailedCreate event is still present; it must not hold the object in failure.
		require.NoError(t, r.Client.Create(ctx, newReplicaPod(2)))
		live := &appsv1.StatefulSet{}
		require.NoError(t, r.Client.Get(ctx, client.ObjectKeyFromObject(ss), live))
		live.Status = newStatefulSet(3, 3).Status
		require.NoError(t, r.Client.Status().Update(ctx, live))

		_, err = r.doStatus(ctx, ms, icmsReq)
		require.NoError(t, err)
		cond = healthyCond(ms)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status, "recovered once all replicas exist and are ready")
		assert.Equal(t, v1alpha1.MiniServiceRunning, ms.Status.Phase)
	})

	t.Run("quota failure still enters the failing backoff", func(t *testing.T) {
		ctx := newTestContext()
		ms := newMiniService()
		r := newReconciler(t, ms, newStatefulSet(3, 2), newFailedCreateEvent(quotaMsg))

		res, err := r.doStatus(ctx, ms, icmsReq)
		require.NoError(t, err, "first observation of a terminal object enters backoff")
		cond := healthyCond(ms)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout, cond.Reason)
		assert.Contains(t, cond.Message, terminalErrorEventReason)
		assert.Greater(t, res.RequeueAfter, time.Duration(0))
	})
}
