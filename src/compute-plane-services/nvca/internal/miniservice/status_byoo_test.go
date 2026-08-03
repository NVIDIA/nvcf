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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/miniservice/chartcache"
	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

// TestDoStatus_BYOOSidecarUnhealthy confirms the legacy health behavior for BYOO
// (Bring Your Own Observability) sidecar failures.
//
// When StatusByWorkerReadiness is disabled (the default), an OOMKilled BYOO
// sidecar must mark the function unhealthy. When StatusByWorkerReadiness is
// enabled, the same sidecar failure is informational only and the function
// remains healthy as long as the worker container is ready.
func TestDoStatus_BYOOSidecarUnhealthy(t *testing.T) {
	const ns = "test-byoo-ns"

	// degradedUtilsPod simulates a utils pod where:
	//   - worker container (utils) is running and ready
	//   - BYOO OTel collector sidecar is OOMKilled (terminated, exit 137)
	//   - pod-level readiness is false because the sidecar made ContainersReady false
	//
	// RestartPolicy Never + terminated container with non-zero exit causes
	// IsPodDegraded to return true without relying on wall-clock timeouts.
	degradedUtilsPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.UtilsPodName,
			Namespace: ns,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{Name: common.UtilsContainerName},
				{Name: common.ByooOTelCollectorPodNameBase},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				{Type: corev1.ContainersReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  common.UtilsContainerName,
					Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
				{
					Name:  common.ByooOTelCollectorPodNameBase,
					Ready: false,
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 137,
							Reason:   "OOMKilled",
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name                       string
		workloadConfig             *v1alpha1.WorkloadConfig
		wantPhase                  v1alpha1.MiniServicePhase
		wantConditionType          string
		wantConditionStatus        metav1.ConditionStatus
		wantConditionReason        string
		wantWorkersHealthyStatus   metav1.ConditionStatus
		wantWorkersHealthyReason   string
		wantErr                    bool
	}{
		{
			// StatusByWorkerReadiness absent (default): doStatusAggressive runs.
			// A degraded BYOO sidecar makes the whole pod terminal-bad, so the
			// function must be marked unhealthy with reason DegradedWorker.
			name:                "per-function instance health disabled: BYOO sidecar OOMKill marks function unhealthy",
			workloadConfig:      nil,
			wantConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			wantConditionStatus: metav1.ConditionFalse,
			wantConditionReason: v1alpha1.MiniServiceStatusReasonDegradedWorker,
			wantErr:             true,
		},
		{
			// StatusByWorkerReadiness enabled: doStatusByWorkerReadiness runs.
			// Only the worker container readiness is checked for MiniService health.
			// The sidecar failure does not terminate the instance. NVCA exposes the
			// worker's healthy state through WorkersHealthy=True so callers can
			// distinguish sidecar noise from real worker failures.
			name: "per-function instance health enabled: BYOO sidecar OOMKill leaves function healthy",
			workloadConfig: &v1alpha1.WorkloadConfig{
				FeatureFlags: map[string]bool{featureflag.StatusByWorkerReadiness: true},
			},
			wantPhase:                v1alpha1.MiniServiceRunning,
			wantWorkersHealthyStatus: metav1.ConditionTrue,
			wantWorkersHealthyReason: "WorkersReady",
			wantErr:                  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()

			ms := &v1alpha1.MiniService{
				ObjectMeta: metav1.ObjectMeta{
					Name: "byoo-test-ms",
					UID:  "byoo-test-uid",
				},
				Spec: v1alpha1.MiniServiceSpec{
					Namespace:       ns,
					ICMSRequestName: "byoo-test-icms",
					WorkloadConfig:  tt.workloadConfig,
				},
			}

			icmsReq := &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "byoo-test-icms",
					Namespace: "nvcf-backend",
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					Action: common.RequestICMSInstances,
				},
			}

			c, _ := newFakeClient(mgrScheme,
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
				degradedUtilsPod,
			)

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

			// Pre-save empty rendered objects so collectObjectStatuses does not
			// attempt a full Helm render. The BYOO sidecar lives in the utils pod
			// (not as a Helm-rendered object), so no Helm objects are needed.
			require.NoError(t, r.saveRenderedData(ctx, ms, []byte("[]")))

			_, err := r.doStatus(ctx, ms, icmsReq)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			if tt.wantConditionType != "" {
				cond := meta.FindStatusCondition(ms.Status.Conditions, tt.wantConditionType)
				if assert.NotNil(t, cond, "expected condition %q to be set", tt.wantConditionType) {
					assert.Equal(t, tt.wantConditionStatus, cond.Status)
					assert.Equal(t, tt.wantConditionReason, cond.Reason)
				}
			}

			if tt.wantPhase != "" {
				assert.Equal(t, tt.wantPhase, ms.Status.Phase)
			}

			if tt.wantWorkersHealthyStatus != "" {
				cond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionWorkersHealthy)
				if assert.NotNil(t, cond, "expected WorkersHealthy condition to be set") {
					assert.Equal(t, tt.wantWorkersHealthyStatus, cond.Status)
					assert.Equal(t, tt.wantWorkersHealthyReason, cond.Reason)
				}
			}
		})
	}
}
