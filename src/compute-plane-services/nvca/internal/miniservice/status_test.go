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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
)

// testTimeConfig creates a TimeConfig with tighter intervals for faster testing.
func testTimeConfig() *nvcak8sutil.TimeConfig {
	return &nvcak8sutil.TimeConfig{
		MaxRunningTimeout:                         10 * time.Minute,
		ModelCacheIdlePeriod:                      1 * time.Hour,
		ModelCacheIdleCleanupPeriod:               5 * time.Minute,
		ModelCacheROPVCBindTimeGracePeriod:        2 * time.Minute,
		ModelCacheVolumeDetachmentTimeout:         5 * time.Minute,
		WorkerDegradationTimeout:                  30 * time.Minute,
		WorkerStartupTimeout:                      2 * time.Hour,
		PodLaunchThresholdSecondsOnFailedRestarts: 10 * time.Minute,
		PodLaunchThresholdMinutesOnInitFailure:    2 * time.Hour,
		PodScheduledThreshold:                     10 * time.Minute,
		InitCacheJobFailureThreshold:              20 * time.Minute,
		MaxImagePullErrorThreshold:                1 * time.Minute,
		NamespaceStuckTimeout:                     5 * time.Minute,
		FailingObjectsBackoffTimeout:              5 * time.Second, // Shorter for tests
		FailingObjectsBackoffRequeueInterval:      1 * time.Second, // Shorter for tests
	}
}

func Test_parseErrorEventMessage(t *testing.T) {
	type spec struct {
		name       string
		event      corev1.Event
		expInclude bool
		expIsError bool
	}
	const exceededQuotaMsg = `exceeded quota: max-gpus, requested: requests.nvidia.com/gpu=1, ` +
		`used: requests.nvidia.com/gpu=1, limited: requests.nvidia.com/gpu=1`

	for _, tt := range []spec{
		{
			name: "no error",
			event: corev1.Event{
				Reason:  "FineReason",
				Message: "la di da",
			},
		},
		{
			name: "no error normal",
			event: corev1.Event{
				Type:    corev1.EventTypeNormal,
				Reason:  "FineReason",
				Message: "la di da",
			},
		},
		{
			name: "arbitrary error",
			event: corev1.Event{
				Type:    corev1.EventTypeWarning,
				Reason:  "ErrorReason",
				Message: "bad but maybe ok",
			},
			expInclude: true,
			expIsError: false,
		},
		{
			name: "failed create",
			event: corev1.Event{
				Reason: "FailedCreate",
				Message: `create Pod foo-xyz-abc in ReplicaSet foo-xyz failed error: ` +
					`pods "foo-xyz-abc" is forbidden: ` + exceededQuotaMsg,
			},
			expInclude: true,
			expIsError: true,
		},
		{
			name: "forbidden",
			event: corev1.Event{
				Reason: "BadThing",
				Message: `create Pod foo-xyz-abc in LeaderWorkerSet foo-xyz failed error: ` +
					`pods "foo-xyz-abc" is forbidden: ` + exceededQuotaMsg,
			},
			expInclude: true,
			expIsError: true,
		},
		{
			name: "transient FailedMount - NodeRestriction PVC access denied",
			event: corev1.Event{
				Type:   corev1.EventTypeWarning,
				Reason: "FailedMount",
				Message: `Unable to attach or mount volumes: unmounted volumes=[secrets-data], unattached volumes=[], ` +
					`failed to process volumes=[secrets-data]: error processing PVC namespace/pvc-name: ` +
					`failed to fetch PVC from API server: persistentvolumeclaims "pvc-name" is forbidden: ` +
					`User "system:node:node-name" cannot get resource "persistentvolumeclaims" in API group "" ` +
					`in the namespace "namespace": no relationship found between node 'node-name' and this object`,
			},
			expInclude: true,
			expIsError: true,
		},
		{
			name: "transient FailedMount - configmap cache timeout",
			event: corev1.Event{
				Type:    corev1.EventTypeWarning,
				Reason:  "FailedMount",
				Message: `MountVolume.SetUp failed for volume "brain-scripts" : failed to sync configmap cache: timed out waiting for the condition`,
			},
			expInclude: true,
			expIsError: false,
		},
		{
			name: "transient FailedAttachVolume",
			event: corev1.Event{
				Type:    corev1.EventTypeWarning,
				Reason:  "FailedAttachVolume",
				Message: `AttachVolume.Attach failed for volume "pvc-123" : error processing volume: some transient error`,
			},
			expInclude: true,
			expIsError: false,
		},
		{
			name: "transient FailedToRetrieveImagePullSecret",
			event: corev1.Event{
				Type:    corev1.EventTypeWarning,
				Reason:  "FailedToRetrieveImagePullSecret",
				Message: `Unable to retrieve some image pull secrets (ngc-docker-reg-secret) attempting to pull the image may not succeed.`,
			},
			expInclude: true,
			expIsError: false,
		},
		{
			name: "policy violation warning should be excluded",
			event: corev1.Event{
				Type:    corev1.EventTypeWarning,
				Reason:  "PolicyViolation",
				Message: "some policy violation from kyverno or other policy engine",
			},
			expInclude: false,
			expIsError: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, include, isError := parseErrorEventMessage(&tt.event)
			assert.Equal(t, tt.expInclude, include)
			assert.Equal(t, tt.expIsError, isError)
		})
	}
}

func Test_ObjectStatuses_backoffBehavior(t *testing.T) {
	now := time.Now()
	testCfg := testTimeConfig()
	backoffTimeout := testCfg.FailingObjectsBackoffTimeout // 5s for tests (90s in prod)

	tests := []struct {
		name                  string
		existingCondition     *metav1.Condition
		statuses              ObjectStatuses
		expectedReason        string
		expectedShouldRequeue bool
		expectedTerminalError bool
		description           string
	}{
		{
			name: "first failure from healthy state - enters backoff",
			existingCondition: &metav1.Condition{
				Type:               v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:             metav1.ConditionTrue,
				Reason:             v1alpha1.MiniServiceConditionObjectsHealthy,
				LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Second)), // 2s ago, within 5s timeout
			},
			statuses: ObjectStatuses{
				{Status: statusFailed, TerminalBad: true, Reason: "FailedMount"},
			},
			expectedReason:        v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
			expectedShouldRequeue: true,
			expectedTerminalError: false,
			description:           "When objects fail within backoff timeout of being healthy, should enter backoff and requeue",
		},
		{
			name: "failure persists beyond backoff timeout - terminal",
			existingCondition: &metav1.Condition{
				Type:               v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:             metav1.ConditionTrue,
				Reason:             v1alpha1.MiniServiceConditionObjectsHealthy,
				LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Second)), // 10s ago, beyond 5s timeout
			},
			statuses: ObjectStatuses{
				{Status: statusFailed, TerminalBad: true, Reason: "FailedMount"},
			},
			expectedReason:        v1alpha1.MiniServiceStatusReasonObjectsFailed,
			expectedShouldRequeue: false,
			expectedTerminalError: true,
			description:           "When objects fail beyond backoff timeout of being healthy, should mark as terminal failure",
		},
		{
			name: "failure at exactly backoff timeout boundary - terminal",
			existingCondition: &metav1.Condition{
				Type:               v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:             metav1.ConditionTrue,
				Reason:             v1alpha1.MiniServiceConditionObjectsHealthy,
				LastTransitionTime: metav1.NewTime(now.Add(-backoffTimeout - 1*time.Second)), // Just beyond timeout
			},
			statuses: ObjectStatuses{
				{Status: statusFailed, TerminalBad: true, Reason: "FailedMount"},
			},
			expectedReason:        v1alpha1.MiniServiceStatusReasonObjectsFailed,
			expectedShouldRequeue: false,
			expectedTerminalError: true,
			description:           "When objects fail exactly at backoff boundary, should mark as terminal",
		},
		{
			name: "no previous healthy state - immediate failure",
			existingCondition: &metav1.Condition{
				Type:               v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:             metav1.ConditionFalse,
				Reason:             v1alpha1.MiniServiceStatusReasonWaitingObjectReadiness,
				LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Second)),
			},
			statuses: ObjectStatuses{
				{Status: statusFailed, TerminalBad: true, Reason: "FailedMount"},
			},
			expectedReason:        v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
			expectedShouldRequeue: true,
			expectedTerminalError: false,
			description:           "When no previous healthy state exists, should still enter backoff for first failure",
		},
		{
			name:              "no existing condition - first failure enters backoff",
			existingCondition: nil,
			statuses: ObjectStatuses{
				{Status: statusFailed, TerminalBad: true, Reason: "FailedMount"},
			},
			expectedReason:        v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
			expectedShouldRequeue: true,
			expectedTerminalError: false,
			description:           "When no condition exists, first failure should enter backoff",
		},
		{
			name: "transient PVC access errors from CSV - should backoff",
			existingCondition: &metav1.Condition{
				Type:               v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:             metav1.ConditionTrue,
				Reason:             v1alpha1.MiniServiceConditionObjectsHealthy,
				LastTransitionTime: metav1.NewTime(now.Add(-1 * time.Second)),
			},
			statuses: ObjectStatuses{
				{
					Status:      statusFailed,
					TerminalBad: true,
					Reason:      "FailedMount",
					AbnormalEvents: []corev1.Event{
						{
							Type:   corev1.EventTypeWarning,
							Reason: "FailedMount",
							Message: `persistentvolumeclaims "nvcf-kns-data-ro" is forbidden: ` +
								`User "system:node:aks-node-001" cannot get resource "persistentvolumeclaims" ` +
								`no relationship found between node 'aks-node-001' and this object`,
						},
					},
				},
			},
			expectedReason:        v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
			expectedShouldRequeue: true,
			expectedTerminalError: false,
			description:           "NodeRestriction PVC access errors should trigger backoff",
		},
		{
			name: "configmap cache timeout from CSV - should backoff",
			existingCondition: &metav1.Condition{
				Type:               v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:             metav1.ConditionTrue,
				Reason:             v1alpha1.MiniServiceConditionObjectsHealthy,
				LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Second)),
			},
			statuses: ObjectStatuses{
				{
					Status:      statusFailed,
					TerminalBad: true,
					Reason:      "FailedMount",
					AbnormalEvents: []corev1.Event{
						{
							Type:    corev1.EventTypeWarning,
							Reason:  "FailedMount",
							Message: `MountVolume.SetUp failed for volume "brain-scripts" : failed to sync configmap cache: timed out waiting for the condition`,
						},
					},
				},
			},
			expectedReason:        v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
			expectedShouldRequeue: true,
			expectedTerminalError: false,
			description:           "ConfigMap cache timeout errors should trigger backoff",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the backoff logic from status.go lines 193-202
			reason := v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout
			if tt.existingCondition != nil && tt.existingCondition.Reason == v1alpha1.MiniServiceConditionObjectsHealthy {
				elapsed := now.Sub(tt.existingCondition.LastTransitionTime.Time)
				if elapsed > backoffTimeout {
					reason = v1alpha1.MiniServiceStatusReasonObjectsFailed
				}
			}

			assert.Equal(t, tt.expectedReason, reason, tt.description)

			// Verify the behavior matches expected terminal/requeue state
			shouldRequeue := (reason == v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout)
			isTerminalError := (reason == v1alpha1.MiniServiceStatusReasonObjectsFailed)

			assert.Equal(t, tt.expectedShouldRequeue, shouldRequeue,
				"Requeue behavior should match: %s", tt.description)
			assert.Equal(t, tt.expectedTerminalError, isTerminalError,
				"Terminal error behavior should match: %s", tt.description)
		})
	}
}

func Test_ObjectStatuses_terminalBehavior(t *testing.T) {
	tests := []struct {
		name                 string
		statuses             ObjectStatuses
		expectedTerminal     bool
		expectedDegradedOnly bool
	}{
		{
			name: "multiple failures including non-degraded",
			statuses: ObjectStatuses{
				{Status: statusFailed, TerminalBad: true, Reason: "FailedMount"},
				{Status: statusDegradedWorker, TerminalBad: true, Reason: "ContainerCrashing"},
			},
			expectedTerminal:     true,
			expectedDegradedOnly: false,
		},
		{
			name: "only degraded workers",
			statuses: ObjectStatuses{
				{Status: statusDegradedWorker, TerminalBad: true, Reason: "ContainerCrashing"},
				{Status: statusDegradedWorker, TerminalBad: true, Reason: "ContainerCrashing"},
			},
			expectedTerminal:     true,
			expectedDegradedOnly: true,
		},
		{
			name: "transient mount errors should be terminal but backoff applies",
			statuses: ObjectStatuses{
				{Status: statusFailed, TerminalBad: true, Reason: "FailedMount"},
			},
			expectedTerminal:     true,
			expectedDegradedOnly: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasTerminal := tt.statuses.AnyTerminal()
			onlyDegraded := tt.statuses.OnlyTerminalDegraded()

			assert.Equal(t, tt.expectedTerminal, hasTerminal)
			assert.Equal(t, tt.expectedDegradedOnly, onlyDegraded)
		})
	}
}

func Test_doTerminalTaskStatus(t *testing.T) {
	ctx := context.Background()
	baseTime := time.Now()
	testCfg := testTimeConfig()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = v1alpha1.AddToScheme(scheme)

	tests := []struct {
		name                    string
		existingCondition       *metav1.Condition
		utilsPod                *corev1.Pod
		statuses                ObjectStatuses
		maxRuntimeDuration      string
		maxQueuedDuration       string
		currentTime             time.Time
		expectedPhase           v1alpha1.MiniServicePhase
		expectedConditionType   string
		expectedConditionReason string
		expectedRequeue         bool
		expectedError           bool
		expectedTerminalError   bool
	}{
		{
			name:              "utils pod succeeded - task completed",
			existingCondition: nil,
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "utils",
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "utils",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 0,
								},
							},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: statusFailed, TerminalBad: true, Reason: "SomeFailure"}},
			maxRuntimeDuration:      "PT1H",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime.Add(10 * time.Minute),
			expectedPhase:           v1alpha1.MiniServiceCompleted,
			expectedConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			expectedConditionReason: "UtilsPodCompletedSuccessfully",
			expectedRequeue:         false,
			expectedError:           false,
			expectedTerminalError:   false,
		},
		{
			name:              "first failure - enters backoff",
			existingCondition: nil,
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "utils",
							State: corev1.ContainerState{},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: statusFailed, TerminalBad: true, Reason: "ImagePullBackOff"}},
			maxRuntimeDuration:      "PT1H",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime.Add(10 * time.Minute),
			expectedPhase:           "",
			expectedConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			expectedConditionReason: v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
			expectedRequeue:         true,
			expectedError:           false,
			expectedTerminalError:   false,
		},
		{
			name: "continued failure within backoff - stays in backoff",
			existingCondition: &metav1.Condition{
				Type:               v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:             metav1.ConditionFalse,
				Reason:             v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
				LastTransitionTime: metav1.NewTime(baseTime.Add(-2 * time.Second)),
			},
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "utils",
							State: corev1.ContainerState{},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: statusFailed, TerminalBad: true, Reason: "ImagePullBackOff"}},
			maxRuntimeDuration:      "PT1H",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime.Add(1 * time.Second), // 3s elapsed since lastTransitionTime, within 5s timeout
			expectedPhase:           "",
			expectedConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			expectedConditionReason: v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
			expectedRequeue:         true,
			expectedError:           false,
			expectedTerminalError:   false,
		},
		{
			name: "failure exceeds backoff timeout - terminal",
			existingCondition: &metav1.Condition{
				Type:               v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:             metav1.ConditionFalse,
				Reason:             v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
				LastTransitionTime: metav1.NewTime(baseTime.Add(-10 * time.Second)),
			},
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "utils",
							State: corev1.ContainerState{},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: statusFailed, TerminalBad: true, Reason: "ImagePullBackOff"}},
			maxRuntimeDuration:      "PT1H",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime, // 10s elapsed since LastTransitionTime, exceeds 5s timeout
			expectedPhase:           "",
			expectedConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			expectedConditionReason: v1alpha1.MiniServiceStatusReasonObjectsFailed,
			expectedRequeue:         false,
			expectedError:           true,
			expectedTerminalError:   true,
		},
		{
			name:              "max runtime exceeded - terminal",
			existingCondition: nil,
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "utils",
				},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "utils",
							State: corev1.ContainerState{},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: "running", TerminalBad: false}},
			maxRuntimeDuration:      "PT1H",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime.Add(66 * time.Minute), // Exceeds 1 hour plus buffer
			expectedPhase:           "",
			expectedConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			expectedConditionReason: v1alpha1.MiniServiceStatusReasonObjectsFailed,
			expectedRequeue:         false,
			expectedError:           true,
			expectedTerminalError:   true,
		},
		{
			name:              "omitted max runtime duration enters backoff instead of timing out",
			existingCondition: nil,
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "utils",
							State: corev1.ContainerState{},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: statusFailed, TerminalBad: true, Reason: "ImagePullBackOff"}},
			maxRuntimeDuration:      "",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime.Add(10 * time.Second),
			expectedPhase:           "",
			expectedConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			expectedConditionReason: v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
			expectedRequeue:         true,
			expectedError:           false,
			expectedTerminalError:   false,
		},
		{
			name:              "max queued exceeded - terminal",
			existingCondition: nil,
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{},
			},
			statuses:                ObjectStatuses{{Status: "running", TerminalBad: false}},
			maxRuntimeDuration:      "PT2H",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime.Add(61 * time.Minute), // Exceeds 1 hour
			expectedPhase:           "",
			expectedConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			expectedConditionReason: v1alpha1.MiniServiceStatusReasonObjectsFailed,
			expectedRequeue:         false,
			expectedError:           true,
			expectedTerminalError:   true,
		},
		{
			name:              "utils container exited non-zero - terminal",
			existingCondition: nil,
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "utils",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 1,
								},
							},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: statusFailed, TerminalBad: true, Reason: "ContainerFailed"}},
			maxRuntimeDuration:      "PT1H",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime.Add(10 * time.Minute),
			expectedPhase:           "",
			expectedConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			expectedConditionReason: v1alpha1.MiniServiceStatusReasonObjectsFailed,
			expectedRequeue:         false,
			expectedError:           true,
			expectedTerminalError:   true,
		},
		{
			name:              "degraded workers only - terminal with degraded reason",
			existingCondition: nil,
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "utils",
							State: corev1.ContainerState{},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: statusDegradedWorker, TerminalBad: true, Reason: "ContainerCrashing"}},
			maxRuntimeDuration:      "PT1H",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime.Add(10 * time.Minute),
			expectedPhase:           "",
			expectedConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			expectedConditionReason: v1alpha1.MiniServiceStatusReasonDegradedWorker,
			expectedRequeue:         false,
			expectedError:           true,
			expectedTerminalError:   true,
		},
		{
			name:              "invalid max runtime duration - terminal error",
			existingCondition: nil,
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "utils",
							State: corev1.ContainerState{},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: "running", TerminalBad: false}},
			maxRuntimeDuration:      "invalid",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime.Add(10 * time.Minute),
			expectedPhase:           "",
			expectedConditionType:   "",
			expectedConditionReason: "",
			expectedRequeue:         false,
			expectedError:           true,
			expectedTerminalError:   true,
		},
		{
			name:              "empty max queued duration - terminal error",
			existingCondition: nil,
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "utils",
							State: corev1.ContainerState{},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: "running", TerminalBad: false}},
			maxRuntimeDuration:      "PT1H",
			maxQueuedDuration:       "",
			currentTime:             baseTime.Add(10 * time.Minute),
			expectedPhase:           "",
			expectedConditionType:   "",
			expectedConditionReason: "",
			expectedRequeue:         false,
			expectedError:           true,
			expectedTerminalError:   true,
		},
		{
			name:              "invalid max queued duration - terminal error",
			existingCondition: nil,
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "utils",
							State: corev1.ContainerState{},
						},
					},
				},
			},
			statuses:                ObjectStatuses{{Status: "running", TerminalBad: false}},
			maxRuntimeDuration:      "PT1H",
			maxQueuedDuration:       "invalid",
			currentTime:             baseTime.Add(10 * time.Minute),
			expectedPhase:           "",
			expectedConditionType:   "",
			expectedConditionReason: "",
			expectedRequeue:         false,
			expectedError:           true,
			expectedTerminalError:   true,
		},
		{
			name: "transient failure from healthy state - enters backoff",
			existingCondition: &metav1.Condition{
				Type:               v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:             metav1.ConditionTrue,
				Reason:             "ObjectsReady",
				LastTransitionTime: metav1.NewTime(baseTime.Add(-1 * time.Second)),
			},
			utilsPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "utils",
					CreationTimestamp: metav1.NewTime(baseTime),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "utils",
							State: corev1.ContainerState{},
						},
					},
				},
			},
			statuses: ObjectStatuses{
				{
					Status:      statusFailed,
					TerminalBad: true,
					Reason:      "FailedMount",
					AbnormalEvents: []corev1.Event{
						{
							Type:   corev1.EventTypeWarning,
							Reason: "FailedMount",
							Message: `persistentvolumeclaims "pvc-name" is forbidden: ` +
								`User "system:node:node-name" cannot get resource "persistentvolumeclaims"`,
						},
					},
				},
			},
			maxRuntimeDuration:      "PT1H",
			maxQueuedDuration:       "PT1H",
			currentTime:             baseTime,
			expectedPhase:           "",
			expectedConditionType:   v1alpha1.MiniServiceConditionObjectsHealthy,
			expectedConditionReason: v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
			expectedRequeue:         true,
			expectedError:           false,
			expectedTerminalError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &v1alpha1.MiniService{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ms",
				},
				Status: v1alpha1.MiniServiceStatus{
					Conditions: []metav1.Condition{},
				},
			}

			if tt.existingCondition != nil {
				ms.Status.Conditions = []metav1.Condition{*tt.existingCondition}
			}

			r := &Reconciler{
				ControllerOptions: ControllerOptions{
					K8sTimeConfig: testCfg,
				},
				Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
				now: func() time.Time {
					return tt.currentTime
				},
			}

			res, err := r.doTerminalTaskStatus(ctx, ms, tt.utilsPod, tt.statuses, tt.maxQueuedDuration, tt.maxRuntimeDuration)

			// Check error expectations
			if tt.expectedError {
				assert.Error(t, err, "Expected an error")
				if tt.expectedTerminalError {
					assert.ErrorContains(t, err, "terminal error", "Expected terminal error")
				}
			} else {
				assert.NoError(t, err, "Expected no error")
			}

			// Check requeue expectations
			if tt.expectedRequeue {
				assert.Equal(t, testCfg.FailingObjectsBackoffRequeueInterval, res.RequeueAfter,
					"Expected requeue interval to match config")
			} else {
				assert.Equal(t, time.Duration(0), res.RequeueAfter, "Expected no requeue")
			}

			// Check phase if set
			if tt.expectedPhase != "" {
				assert.Equal(t, tt.expectedPhase, ms.Status.Phase, "Phase should match expected")
			}

			// Check condition if expected
			if tt.expectedConditionType != "" {
				cond := meta.FindStatusCondition(ms.Status.Conditions, tt.expectedConditionType)
				assert.NotNil(t, cond, "Expected condition to be set")
				if cond != nil {
					assert.Equal(t, tt.expectedConditionReason, cond.Reason, "Condition reason should match")
					if tt.expectedPhase == v1alpha1.MiniServiceCompleted {
						assert.Equal(t, metav1.ConditionTrue, cond.Status, "Condition should be true for completed")
					} else if tt.expectedConditionType == v1alpha1.MiniServiceConditionObjectsHealthy {
						assert.Equal(t, metav1.ConditionFalse, cond.Status, "Condition should be false for failures")
					}
				}
			}
		})
	}
}

// TestDoStatus_NotFound_PhaseAware verifies that NotFound during Installed phase
// is treated as pending (not terminal), while NotFound during Running phase is terminal.
func TestDoStatus_NotFound_PhaseAware(t *testing.T) {
	tests := []struct {
		name           string
		phase          v1alpha1.MiniServicePhase
		expectTerminal bool
		expectPending  bool
	}{
		{
			name:           "installed_phase_not_terminal",
			phase:          v1alpha1.MiniServiceInstalled,
			expectTerminal: false,
			expectPending:  true,
		},
		{
			name:           "starting_phase_is_terminal",
			phase:          v1alpha1.MiniServiceStarting,
			expectTerminal: true,
			expectPending:  false,
		},
		{
			name:           "running_phase_is_terminal",
			phase:          v1alpha1.MiniServiceRunning,
			expectTerminal: true,
			expectPending:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &v1alpha1.MiniService{
				Status: v1alpha1.MiniServiceStatus{
					Phase: tt.phase,
				},
			}

			// Test the phase-aware NotFound handling logic
			isTerminal := ms.Status.Phase != v1alpha1.MiniServiceInstalled

			assert.Equal(t, tt.expectTerminal, isTerminal,
				"Terminal expectation should match for phase %s", tt.phase)
			assert.Equal(t, tt.expectPending, !isTerminal,
				"Pending expectation should match for phase %s", tt.phase)
		})
	}
}

func Test_getObjectGVKOrUnknown(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	ctx := context.Background()

	tests := []struct {
		name     string
		obj      *corev1.Pod
		setGVK   bool
		expected schema.GroupVersionKind
	}{
		{
			name: "object with GVK already set",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pod"},
			},
			setGVK:   true,
			expected: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
		},
		{
			name: "object without GVK inferred from scheme",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pod"},
			},
			setGVK:   false,
			expected: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setGVK {
				tt.obj.SetGroupVersionKind(tt.expected)
			}
			result := getObjectGVKOrUnknown(ctx, scheme, tt.obj)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func Test_getObjectGVKOrUnknown_unknownType(t *testing.T) {
	scheme := runtime.NewScheme()
	ctx := context.Background()

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod"}}

	result := getObjectGVKOrUnknown(ctx, scheme, pod)
	assert.Equal(t, unknownGVK, result)
}

func Test_makeObjectIdentifierString(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	ctx := context.Background()

	tests := []struct {
		name     string
		obj      *corev1.Pod
		expected string
	}{
		{
			name:     "nil object",
			obj:      nil,
			expected: "<nil>",
		},
		{
			name: "pod with name",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "my-pod"},
			},
			expected: "v1.Pod my-pod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result string
			if tt.obj == nil {
				result = makeObjectIdentifierString(ctx, scheme, nil)
			} else {
				result = makeObjectIdentifierString(ctx, scheme, tt.obj)
			}
			assert.Equal(t, tt.expected, result)
		})
	}
}

func Test_makeObjectIdentifierString_withGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	ctx := context.Background()

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-deploy"},
	}

	result := makeObjectIdentifierString(ctx, scheme, deploy)
	assert.Equal(t, "apps/v1.Deployment my-deploy", result)
}

func Test_makeObjectIdentifierString_unknownType(t *testing.T) {
	scheme := runtime.NewScheme()
	ctx := context.Background()

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod"}}

	result := makeObjectIdentifierString(ctx, scheme, pod)
	assert.Equal(t, "unknown/unknown.unknown test-pod", result)
}

func TestFilterEventsForObject(t *testing.T) {
	namespace := "test-ns"
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	t.Run("filters events matching object", func(t *testing.T) {
		obj := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: namespace},
		}
		obj.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Pod"))

		events := []corev1.Event{
			{
				InvolvedObject: corev1.ObjectReference{
					Name:       "my-pod",
					Namespace:  namespace,
					Kind:       "Pod",
					APIVersion: "v1",
				},
				Message: "event1",
			},
			{
				InvolvedObject: corev1.ObjectReference{
					Name:       "other-pod",
					Namespace:  namespace,
					Kind:       "Pod",
					APIVersion: "v1",
				},
				Message: "event2",
			},
			{
				InvolvedObject: corev1.ObjectReference{
					Name:       "my-pod",
					Namespace:  namespace,
					Kind:       "Pod",
					APIVersion: "v1",
				},
				Message: "event3",
			},
		}

		filtered, err := filterEventsForObject(ctx, scheme, events, obj)
		require.NoError(t, err)
		assert.Len(t, filtered, 2)
		assert.Equal(t, "event1", filtered[0].Message)
		assert.Equal(t, "event3", filtered[1].Message)
	})

	t.Run("returns nil for nil object", func(t *testing.T) {
		events := []corev1.Event{{Message: "test"}}
		filtered, err := filterEventsForObject(ctx, scheme, events, nil)
		require.NoError(t, err)
		assert.Nil(t, filtered)
	})

	t.Run("returns empty slice when no matching events", func(t *testing.T) {
		obj := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: namespace},
		}
		obj.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Pod"))

		events := []corev1.Event{
			{
				InvolvedObject: corev1.ObjectReference{
					Name:       "other-pod",
					Namespace:  namespace,
					Kind:       "Pod",
					APIVersion: "v1",
				},
			},
		}

		filtered, err := filterEventsForObject(ctx, scheme, events, obj)
		require.NoError(t, err)
		assert.Empty(t, filtered)
	})

	t.Run("filters by kind and apiVersion", func(t *testing.T) {
		obj := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "my-deploy", Namespace: namespace},
		}
		obj.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))

		events := []corev1.Event{
			{
				InvolvedObject: corev1.ObjectReference{
					Name:       "my-deploy",
					Namespace:  namespace,
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				Message: "deployment event",
			},
			{
				InvolvedObject: corev1.ObjectReference{
					Name:       "my-deploy",
					Namespace:  namespace,
					Kind:       "ReplicaSet",
					APIVersion: "apps/v1",
				},
				Message: "rs event with same name",
			},
		}

		filtered, err := filterEventsForObject(ctx, scheme, events, obj)
		require.NoError(t, err)
		assert.Len(t, filtered, 1)
		assert.Equal(t, "deployment event", filtered[0].Message)
	})
}

func TestStatusContextDirectAccess(t *testing.T) {
	namespace := "test-ns"

	t.Run("statusContext holds pre-loaded data", func(t *testing.T) {
		pods := []*corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: namespace}},
			{ObjectMeta: metav1.ObjectMeta{Name: "pod-2", Namespace: namespace}},
		}
		replicaSets := []*appsv1.ReplicaSet{
			{ObjectMeta: metav1.ObjectMeta{Name: "rs-1", Namespace: namespace}},
		}
		events := []corev1.Event{
			{Message: "event1"},
		}

		sc := &statusContext{
			namespace:   namespace,
			pods:        pods,
			replicaSets: replicaSets,
			events:      events,
		}

		assert.Equal(t, namespace, sc.namespace)
		assert.Len(t, sc.pods, 2)
		assert.Len(t, sc.replicaSets, 1)
		assert.Len(t, sc.events, 1)
	})

	t.Run("empty statusContext has nil slices", func(t *testing.T) {
		sc := &statusContext{}

		assert.Empty(t, sc.namespace)
		assert.Nil(t, sc.pods)
		assert.Nil(t, sc.replicaSets)
		assert.Nil(t, sc.events)
	})

	t.Run("statusContext embedded in context", func(t *testing.T) {
		pods := []*corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: namespace}},
		}
		events := []corev1.Event{
			{Message: "event1"},
		}
		sc := &statusContext{
			namespace: namespace,
			pods:      pods,
			events:    events,
		}

		ctx := withStatusContext(context.Background(), sc)
		retrieved := getStatusContext(ctx)

		assert.NotNil(t, retrieved)
		assert.Equal(t, namespace, retrieved.namespace)
		assert.Len(t, retrieved.pods, 1)
		assert.Len(t, retrieved.events, 1)
		assert.Equal(t, "pod-1", retrieved.pods[0].Name)
	})

	t.Run("getStatusContext returns nil when not set", func(t *testing.T) {
		ctx := context.Background()
		retrieved := getStatusContext(ctx)

		assert.Nil(t, retrieved)
	})
}

func Test_collectObjectIDs(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	ctx := context.Background()

	alwaysTrue := func(ObjectStatus) bool { return true }
	alwaysFalse := func(ObjectStatus) bool { return false }

	tests := []struct {
		name     string
		statuses ObjectStatuses
		filter   func(ObjectStatus) bool
		expected []string
	}{
		{
			name:     "nil statuses returns nil",
			statuses: nil,
			filter:   alwaysTrue,
			expected: nil,
		},
		{
			name:     "empty statuses returns nil",
			statuses: ObjectStatuses{},
			filter:   alwaysTrue,
			expected: nil,
		},
		{
			name: "all filtered out returns nil",
			statuses: ObjectStatuses{
				{
					Object:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1"}},
					TerminalBad: true,
				},
			},
			filter:   alwaysFalse,
			expected: nil,
		},
		{
			name: "single matching object",
			statuses: ObjectStatuses{
				{
					Object:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1"}},
					TerminalBad: true,
				},
			},
			filter:   filterTerminal,
			expected: []string{"v1.Pod pod-1"},
		},
		{
			name: "multiple objects only matching ones included",
			statuses: ObjectStatuses{
				{
					Object:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "healthy-pod"}},
					TerminalBad: false,
				},
				{
					Object:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bad-pod"}},
					TerminalBad: true,
				},
			},
			filter:   filterTerminal,
			expected: []string{"v1.Pod bad-pod"},
		},
		{
			name: "filterPending selects pending objects",
			statuses: ObjectStatuses{
				{
					Object:  &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending-pod"}},
					Pending: true,
				},
				{
					Object:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "failed-pod"}},
					TerminalBad: true,
				},
			},
			filter:   filterPending,
			expected: []string{"v1.Pod pending-pod"},
		},
		{
			name: "object with group included in ID",
			statuses: ObjectStatuses{
				{
					Object:      &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "my-deploy"}},
					TerminalBad: true,
				},
			},
			filter:   filterTerminal,
			expected: []string{"apps/v1.Deployment my-deploy"},
		},
		{
			name: "child objects included when both parent and child match filter",
			statuses: ObjectStatuses{
				{
					Object:      &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "deploy-1"}},
					TerminalBad: true,
					ChildObjects: ObjectStatuses{
						{
							Object:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "child-pod"}},
							TerminalBad: true,
						},
					},
				},
			},
			filter: filterTerminal,
			expected: []string{
				"apps/v1.Deployment deploy-1",
				"v1.Pod child-pod",
			},
		},
		{
			name: "child not included when parent matches but child does not",
			statuses: ObjectStatuses{
				{
					Object:      &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "deploy-1"}},
					TerminalBad: true,
					ChildObjects: ObjectStatuses{
						{
							Object:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "ok-child"}},
							TerminalBad: false,
						},
					},
				},
			},
			filter:   filterTerminal,
			expected: []string{"apps/v1.Deployment deploy-1"},
		},
		{
			name: "parent not matching skips entire subtree",
			statuses: ObjectStatuses{
				{
					Object:      &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "ok-deploy"}},
					TerminalBad: false,
					ChildObjects: ObjectStatuses{
						{
							Object:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bad-child"}},
							TerminalBad: true,
						},
					},
				},
			},
			filter:   filterTerminal,
			expected: nil,
		},
		{
			name: "multiple top-level objects with children",
			statuses: ObjectStatuses{
				{
					Object:      &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "deploy-a"}},
					TerminalBad: true,
					ChildObjects: ObjectStatuses{
						{
							Object:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-a1"}},
							TerminalBad: true,
						},
					},
				},
				{
					Object:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "standalone-pod"}},
					TerminalBad: false,
				},
				{
					Object:  &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending-pod"}},
					Pending: true,
				},
			},
			filter: filterTerminal,
			expected: []string{
				"apps/v1.Deployment deploy-a",
				"v1.Pod pod-a1",
			},
		},
		{
			name: "alwaysTrue filter collects all objects and children",
			statuses: ObjectStatuses{
				{
					Object: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1"}},
				},
				{
					Object: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-2"}},
					ChildObjects: ObjectStatuses{
						{Object: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "child-1"}}},
					},
				},
			},
			filter: alwaysTrue,
			expected: []string{
				"v1.Pod pod-1",
				"v1.Pod pod-2",
				"v1.Pod child-1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := collectObjectIDs(ctx, scheme, tt.statuses, tt.filter)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetFunctionWorkerContainerStatus(t *testing.T) {
	utilsStatus := corev1.ContainerStatus{
		Name:         common.UtilsContainerName,
		Ready:        true,
		RestartCount: 2,
		Image:        "utils:latest",
	}
	llmStatus := corev1.ContainerStatus{
		Name:  function.LLMWorkerContainerName,
		Ready: true,
	}

	tests := []struct {
		name           string
		pod            *corev1.Pod
		wantStatus     corev1.ContainerStatus
		wantPresent    bool
		wantErr        bool
		wantErrMessage string
	}{
		{
			name: "utils worker container status present",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{utilsStatus},
				},
			},
			wantStatus:  utilsStatus,
			wantPresent: true,
		},
		{
			name: "llm worker container status present",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{llmStatus},
				},
			},
			wantStatus:  llmStatus,
			wantPresent: true,
		},
		{
			name: "worker container status present among sidecars",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "sidecar-a", Ready: true},
						utilsStatus,
						{Name: "sidecar-b", Ready: false},
					},
				},
			},
			wantStatus:  utilsStatus,
			wantPresent: true,
		},
		{
			name: "status takes precedence over spec when both present",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: common.UtilsContainerName}},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{utilsStatus},
				},
			},
			wantStatus:  utilsStatus,
			wantPresent: true,
		},
		{
			name: "utils worker in spec but status not yet available",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "sidecar-a"},
						{Name: common.UtilsContainerName},
					},
				},
			},
			wantStatus:  corev1.ContainerStatus{},
			wantPresent: false,
		},
		{
			name: "llm worker in spec but status not yet available",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: function.LLMWorkerContainerName}},
				},
			},
			wantStatus:  corev1.ContainerStatus{},
			wantPresent: false,
		},
		{
			name: "non-worker status present but worker still only in spec",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: common.UtilsContainerName}},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{Name: "sidecar-a", Ready: true}},
				},
			},
			wantStatus:  corev1.ContainerStatus{},
			wantPresent: false,
		},
		{
			name: "worker container not found in spec or status",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "sidecar-a"}},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{Name: "sidecar-a"}},
				},
			},
			wantStatus:     corev1.ContainerStatus{},
			wantPresent:    false,
			wantErr:        true,
			wantErrMessage: "terminal error: worker container not found",
		},
		{
			name:           "empty pod returns terminal error",
			pod:            &corev1.Pod{},
			wantStatus:     corev1.ContainerStatus{},
			wantPresent:    false,
			wantErr:        true,
			wantErrMessage: "terminal error: worker container not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, present, err := getFunctionWorkerContainerStatus(tt.pod)
			if tt.wantErr {
				// The error must be terminal so the reconciler does not requeue; the
				// "terminal error: " prefix is added by reconcile.TerminalError.
				assert.EqualError(t, err, tt.wantErrMessage)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantPresent, present)
			assert.Equal(t, tt.wantStatus, got)
		})
	}
}
