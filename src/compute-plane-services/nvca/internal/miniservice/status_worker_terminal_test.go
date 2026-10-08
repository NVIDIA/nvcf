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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

// TestDoStatus_WorkerTerminalBadStateForImagePullBackOff is a regression test for
// NVBug 6640820: a RestartPolicy=Always function worker stuck in ImagePullBackOff
// past the worker degradation period (wdp) must transition WorkersHealthy to the
// terminal reason WorkerTerminalBadState. Previously this never fired for any
// function because IsPodDegraded's returned Containers list was only ever populated
// inside a RestartPolicy=Never-gated closure (a Go && short-circuit bug), so the
// worker-container attribution check in doStatusByWorkerReadiness always saw an
// empty list and silently fell through to WaitingOnObjectReadiness forever.
func TestDoStatus_WorkerTerminalBadStateForImagePullBackOff(t *testing.T) {
	const ns = "test-imagepull-ns"
	now := time.Now()

	// imagePullBackOffPod simulates the TC-10a repro: a previously-Ready utils pod
	// (RestartPolicy: Always) whose worker container has been stuck in
	// ImagePullBackOff, and thus PodReady=False, for 40 minutes -- longer than the
	// 30-minute default WorkerDegradationTimeout, but well within the 2-hour
	// WorkerStartupTimeout, and started 2 hours ago so it is not considered to be
	// in its initial startup period.
	imagePullBackOffPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.UtilsPodName,
			Namespace: ns,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{
				{Name: common.UtilsContainerName},
			},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: now.Add(-2 * time.Hour)},
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				{Type: corev1.ContainersReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
				{
					Type:               corev1.PodReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.Time{Time: now.Add(-40 * time.Minute)},
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  common.UtilsContainerName,
					Ready: false,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: "Back-off pulling image \"no-such-image:nosuchtag\"",
						},
					},
				},
			},
		},
	}

	ctx := newTestContext()

	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "imagepull-test-ms",
			UID:  "imagepull-test-uid",
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       ns,
			ICMSRequestName: "imagepull-test-icms",
			WorkloadConfig: &v1alpha1.WorkloadConfig{
				FeatureFlags: map[string]bool{featureflag.StatusByWorkerReadiness: true},
			},
		},
	}

	icmsReq := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "imagepull-test-icms",
			Namespace: "nvcf-backend",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action: common.RequestICMSInstances,
		},
	}

	c, _ := newFakeClient(mgrScheme,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		imagePullBackOffPod,
	)

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			SystemNamespace: updateSystemNamespace,
			K8sTimeConfig:   (&nvcak8sutil.TimeConfig{}).Complete(),
		},
		Client:                c,
		Decoder:               serializer.NewCodecFactory(mgrScheme).UniversalDeserializer(),
		newPermissionsChecker: newFakePermissionsChecker,
		now:                   time.Now,
	}

	saveAndPersistRenderedData(t, ctx, r, ms, []byte("[]"))

	_, err := r.doStatus(ctx, ms, icmsReq)

	require.Error(t, err)
	assert.True(t, errors.Is(err, reconcile.TerminalError(nil)), "expected a terminal, non-retryable error")

	cond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionWorkersHealthy)
	if assert.NotNil(t, cond, "expected WorkersHealthy condition to be set") {
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, "WorkerTerminalBadState", cond.Reason)
	}
}
