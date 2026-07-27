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
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsPodReady(t *testing.T) {
	tests := []struct {
		name      string
		podStatus corev1.PodStatus
		expected  bool
	}{
		{
			name: "pod is ready",
			podStatus: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
			expected: true,
		},
		{
			name: "pod is not ready",
			podStatus: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionFalse,
					},
				},
			},
			expected: false,
		},
		{
			name: "pod has multiple conditions, but is ready",
			podStatus: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReadyToStartContainers,
						Status: corev1.ConditionFalse,
					},
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
			expected: true,
		},
		{
			name: "pod has no conditions",
			podStatus: corev1.PodStatus{
				Conditions: []corev1.PodCondition{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := IsPodReady(tt.podStatus)
			if actual != tt.expected {
				t.Errorf("IsPodReady() = %v, want %v", actual, tt.expected)
			}
		})
	}
}

func TestIsPodInInitialStartup(t *testing.T) {
	now := time.Now()
	nowMeta := metav1.NewTime(now)

	tests := []struct {
		name     string
		status   corev1.PodStatus
		expected bool
	}{
		{
			name: "pod has no start time",
			status: corev1.PodStatus{
				StartTime: nil,
			},
			expected: true,
		},
		{
			name: "pod has start time, but no conditions",
			status: corev1.PodStatus{
				StartTime: &nowMeta,
			},
			expected: false,
		},
		{
			name: "pod has start time and ready condition with last transition time before start time",
			status: corev1.PodStatus{
				StartTime: &nowMeta,
				Conditions: []corev1.PodCondition{
					{
						Type:               corev1.PodReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Second)),
					},
				},
			},
			expected: true,
		},
		{
			name: "pod has start time and ready condition with last transition time two seconds after start time",
			status: corev1.PodStatus{
				StartTime: &nowMeta,
				Conditions: []corev1.PodCondition{
					{
						Type:               corev1.PodReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(2 * time.Second)),
					},
				},
			},
			expected: false,
		},
		{
			name: "pod has start time and ready condition with last transition time equal to start time",
			status: corev1.PodStatus{
				StartTime: &nowMeta,
				Conditions: []corev1.PodCondition{
					{
						Type:               corev1.PodReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(now),
					},
				},
			},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsPodInInitialStartup(tt.status))
		})
	}
}

func TestAddEnvsToContainer(t *testing.T) {
	tests := []struct {
		name         string
		container    *corev1.Container
		envs         []corev1.EnvVar
		expected     []corev1.EnvVar
		wantModified bool
	}{
		{
			name: "add new env vars to empty container",
			container: &corev1.Container{
				Name: "test-container",
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
			expected: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
			wantModified: true,
		},
		{
			name: "update existing env vars",
			container: &corev1.Container{
				Name: "test-container",
				Env: []corev1.EnvVar{
					{Name: "ENV1", Value: "old-value1"},
					{Name: "ENV2", Value: "old-value2"},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV2", Value: "new-value2"},
			},
			expected: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV2", Value: "new-value2"},
			},
			wantModified: true,
		},
		{
			name: "mix of new and existing env vars",
			container: &corev1.Container{
				Name: "test-container",
				Env: []corev1.EnvVar{
					{Name: "ENV1", Value: "value1"},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
			expected: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
			wantModified: true,
		},
		{
			name: "no changes needed",
			container: &corev1.Container{
				Name: "test-container",
				Env: []corev1.EnvVar{
					{Name: "ENV1", Value: "value1"},
					{Name: "ENV2", Value: "value2"},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
			expected: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
			wantModified: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modified := AddEnvsToContainer(tt.container, tt.envs...)
			assert.Equal(t, tt.wantModified, modified)
			assert.Equal(t, tt.expected, tt.container.Env)
		})
	}
}

func TestAddEnvsToContainers(t *testing.T) {
	tests := []struct {
		name         string
		containers   []corev1.Container
		envs         []corev1.EnvVar
		expected     []corev1.Container
		wantModified bool
	}{
		{
			name: "add to multiple containers",
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "EXISTING", Value: "value"},
					},
				},
				{
					Name: "container2",
				},
			},
			envs: []corev1.EnvVar{
				{Name: "NEW_ENV", Value: "new-value"},
			},
			expected: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "EXISTING", Value: "value"},
						{Name: "NEW_ENV", Value: "new-value"},
					},
				},
				{
					Name: "container2",
					Env: []corev1.EnvVar{
						{Name: "NEW_ENV", Value: "new-value"},
					},
				},
			},
			wantModified: true,
		},
		{
			name: "no changes needed",
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
			},
			expected: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
					},
				},
			},
			wantModified: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modified := AddEnvsToContainers(tt.containers, tt.envs...)
			assert.Equal(t, tt.wantModified, modified)
			assert.Equal(t, tt.expected, tt.containers)
		})
	}
}

func TestMergePodSpecTolerations(t *testing.T) {
	existing := corev1.Toleration{
		Key:      "existing",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoExecute,
	}
	configured := corev1.Toleration{
		Key:      "configured",
		Operator: corev1.TolerationOpEqual,
		Value:    "true",
		Effect:   corev1.TaintEffectNoSchedule,
	}

	ps := &corev1.PodSpec{
		Tolerations: []corev1.Toleration{existing},
	}

	modified := MergePodSpecTolerations(ps, configured, configured)
	assert.True(t, modified)
	assert.Equal(t, []corev1.Toleration{existing, configured}, ps.Tolerations)

	modified = MergePodSpecTolerations(ps, configured)
	assert.False(t, modified)
	assert.Equal(t, []corev1.Toleration{existing, configured}, ps.Tolerations)
}

func TestIsTimeSincePodLaunchedLaterThan(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		pod      *corev1.Pod
		duration time.Duration
		want     bool
	}{
		{
			name: "pod started after threshold",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: now.Add(-2 * time.Hour)},
				},
			},
			duration: time.Hour,
			want:     true,
		},
		{
			name: "pod not scheduled after threshold",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{{
						Type:               corev1.PodScheduled,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.Time{Time: now.Add(-2 * time.Hour)},
					}},
				},
			},
			duration: time.Hour,
			want:     true,
		},
		{
			name: "pod created after threshold",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.Time{Time: now.Add(-2 * time.Hour)}},
			},
			duration: time.Hour,
			want:     true,
		},
		{
			name: "pod started before threshold",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: now.Add(-30 * time.Minute)},
				},
			},
			duration: time.Hour,
			want:     false,
		},
		{
			name: "pod not scheduled before threshold",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{{
						Type:               corev1.PodScheduled,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.Time{Time: now.Add(-30 * time.Minute)},
					}},
				},
			},
			duration: time.Hour,
			want:     false,
		},
		{
			name: "pod scheduled after threshold",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{{
						Type:               corev1.PodScheduled,
						Status:             corev1.ConditionTrue,
						LastTransitionTime: metav1.Time{Time: now.Add(-2 * time.Hour)},
					}},
				},
			},
			duration: time.Hour,
			want:     false,
		},
		{
			name: "pod created before threshold",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.Time{Time: now.Add(-30 * time.Minute)}},
			},
			duration: time.Hour,
			want:     false,
		},
		{
			name: "pod with no start time",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					StartTime: nil,
				},
			},
			duration: time.Hour,
			want:     false,
		},
		{
			name: "zero duration",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: now.Add(-time.Hour)},
				},
			},
			duration: 0,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTimeSincePodLaunchedLaterThan(tt.pod, tt.duration)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsPodDegraded(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name                    string
		status                  corev1.PodStatus
		workerDegradationPeriod time.Duration
		startupTimeoutPeriod    time.Duration
		restartPolicy           corev1.RestartPolicy
		want                    PodDegraded
		wantDegraded            bool
	}{
		{
			name: "not degraded - all conditions healthy",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: "foo",
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
					{
						Name: "bar",
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
			workerDegradationPeriod: time.Minute,
			startupTimeoutPeriod:    time.Minute,
			want:                    PodDegraded{},
			wantDegraded:            false,
		},
		{
			name: "degraded - containers not ready and pod not ready beyond threshold",
			status: corev1.PodStatus{
				StartTime: &metav1.Time{Time: now.Add(-2 * time.Hour)},
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.ContainersReady,
						Status: corev1.ConditionFalse,
						Reason: "ContainersNotReady",
					},
					{
						Type:               corev1.PodReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.Time{Time: now.Add(-2 * time.Hour)},
					},
					{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: "foo",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 1,
							},
						},
					},
					{
						Name: "bar",
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
			workerDegradationPeriod: time.Hour,
			startupTimeoutPeriod:    time.Hour,
			want: PodDegraded{
				Containers: []ContainerDegraded{},
				Reason:     "ContainersNotReady",
			},
			wantDegraded: true,
		},
		{
			name: "not degraded - within startup timeout period",
			status: corev1.PodStatus{
				StartTime: &metav1.Time{Time: now.Add(-30 * time.Minute)},
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.ContainersReady,
						Status: corev1.ConditionFalse,
						Reason: "ContainersNotReady",
					},
					{
						Type:               corev1.PodReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.Time{Time: now.Add(-30 * time.Minute)},
					},
					{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: "foo",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 1,
							},
						},
					},
					{
						Name: "bar",
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
			workerDegradationPeriod: time.Hour,
			startupTimeoutPeriod:    time.Hour,
			want:                    PodDegraded{},
			wantDegraded:            false,
		},
		{
			name: "degraded - within startup timeout period but restart policy is never",
			status: corev1.PodStatus{
				StartTime: &metav1.Time{Time: now.Add(-30 * time.Minute)},
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.ContainersReady,
						Status: corev1.ConditionFalse,
						Reason: "ContainersNotReady",
					},
					{
						Type:               corev1.PodReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.Time{Time: now.Add(-30 * time.Minute)},
					},
					{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: "foo",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 1,
							},
						},
					},
					{
						Name: "bar",
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
			workerDegradationPeriod: time.Hour,
			startupTimeoutPeriod:    time.Hour,
			restartPolicy:           corev1.RestartPolicyNever,
			want: PodDegraded{
				Containers: []ContainerDegraded{{Name: "foo"}},
				Reason:     "ContainersNotReady",
			},
			wantDegraded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sTimeConfig := &TimeConfig{
				WorkerDegradationTimeout: tt.workerDegradationPeriod,
				WorkerStartupTimeout:     tt.startupTimeoutPeriod,
			}
			got, degraded := IsPodDegraded(&corev1.Pod{
				Spec:   corev1.PodSpec{RestartPolicy: tt.restartPolicy},
				Status: tt.status,
			}, k8sTimeConfig)
			assert.Equal(t, tt.wantDegraded, degraded)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestImagePullIssuesReported(t *testing.T) {
	tests := []struct {
		name       string
		status     corev1.PodStatus
		wantIssue  bool
		wantReason string
	}{
		{
			name: "no image pull issues",
			status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
			wantIssue:  false,
			wantReason: "",
		},
		{
			name: "image pull error in container",
			status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason: ImagePullIssueReason,
							},
						},
					},
				},
			},
			wantIssue:  true,
			wantReason: ImagePullIssueReason,
		},
		{
			name: "image pull backoff in init container",
			status: corev1.PodStatus{
				InitContainerStatuses: []corev1.ContainerStatus{
					{
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason: ImagePullIssueAlternateReason,
							},
						},
					},
				},
			},
			wantIssue:  true,
			wantReason: ImagePullIssueAlternateReason,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imagePullIssues, hasIssue := ImagePullIssuesReported(tt.status)
			assert.Equal(t, tt.wantIssue, hasIssue)
			if tt.wantIssue {
				if assert.Len(t, imagePullIssues, 1) {
					assert.Equal(t, tt.wantReason, imagePullIssues[0].Reason)
				}
			} else {
				assert.Empty(t, imagePullIssues)
			}
		})
	}
}

func TestParseImageRegistry(t *testing.T) {
	type spec struct {
		imgTag, exp string
	}
	cases := []spec{
		{imgTag: "", exp: ""},
		{imgTag: "foo", exp: "docker.io"},
		{imgTag: "foo/bar", exp: "docker.io"},
		{imgTag: "foo.com/bar", exp: "foo.com"},
		{imgTag: "foo:12345/bar", exp: "foo:12345"},
	}
	for _, tt := range cases {
		t.Run(tt.imgTag, func(t *testing.T) {
			assert.Equal(t, tt.exp, ParseImageRegistry(tt.imgTag))
		})
	}
}

func TestIsPodAdmissionRejected(t *testing.T) {
	tests := []struct {
		name         string
		status       corev1.PodStatus
		wantRejected bool
		wantReason   string
	}{
		{
			name: "admission not rejected",
			status: corev1.PodStatus{
				Reason: "SomeOtherReason",
			},
			wantRejected: false,
			wantReason:   "",
		},
		{
			name: "admission rejected",
			status: corev1.PodStatus{
				Reason: UnexpectedAdmissionErrReason,
			},
			wantRejected: true,
			wantReason:   UnexpectedAdmissionErrReason,
		},
		{
			name: "admission rejected case insensitive",
			status: corev1.PodStatus{
				Reason: "unexpectedadmissionerror",
			},
			wantRejected: true,
			wantReason:   UnexpectedAdmissionErrReason,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rejected, reason := IsPodAdmissionRejected(tt.status)
			assert.Equal(t, tt.wantRejected, rejected)
			assert.Equal(t, tt.wantReason, reason)
		})
	}
}

func TestIsPodScheduled(t *testing.T) {
	tests := []struct {
		name          string
		status        corev1.PodStatus
		wantScheduled bool
	}{
		{
			name: "pod scheduled",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					},
				},
			},
			wantScheduled: true,
		},
		{
			name: "pod not scheduled",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
					},
				},
			},
			wantScheduled: false,
		},
		{
			name: "no scheduling condition",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
			wantScheduled: false,
		},
		{
			name: "empty conditions",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{},
			},
			wantScheduled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheduled := IsPodScheduled(tt.status)
			assert.Equal(t, tt.wantScheduled, scheduled)
		})
	}
}

func TestIsPodStuckInitializing(t *testing.T) {
	defaultTimeConfig := (&TimeConfig{}).Complete()
	now := time.Now()
	tests := []struct {
		name      string
		podStatus corev1.PodStatus
		want      PodStuckInitializing
		wantStuck bool
	}{
		{
			name: "pod successfully initialized",
			podStatus: corev1.PodStatus{
				StartTime: &metav1.Time{Time: now},
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodInitialized,
						Status: corev1.ConditionTrue,
					},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{
						RestartCount: 0,
					},
				},
			},
			want:      PodStuckInitializing{},
			wantStuck: false,
		},
		{
			name: "container in restart loop",
			podStatus: corev1.PodStatus{
				StartTime: &metav1.Time{Time: now.Add(-defaultTimeConfig.PodLaunchThresholdSecondsOnFailedRestarts - time.Minute)},
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodInitialized,
						Status: corev1.ConditionTrue,
					},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{
						RestartCount: RestartCountToFailInstance + 1,
						LastTerminationState: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								Reason: "CrashLoopBackOff",
							},
						},
					},
				},
			},
			want: PodStuckInitializing{
				Containers: []ContainerStuckInitializing{{Reason: "CrashLoopBackOff"}},
				Reason:     "ContainersInRestartLoop",
			},
			wantStuck: true,
		},
		{
			name: "init container in restart loop",
			podStatus: corev1.PodStatus{
				StartTime: &metav1.Time{Time: now.Add(-defaultTimeConfig.PodLaunchThresholdSecondsOnFailedRestarts - time.Minute)},
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodInitialized,
						Status: corev1.ConditionFalse,
					},
				},
				InitContainerStatuses: []corev1.ContainerStatus{
					{
						RestartCount: RestartCountToFailInstance + 1,
						LastTerminationState: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								Reason: "Error",
							},
						},
					},
				},
			},
			want: PodStuckInitializing{
				Containers: []ContainerStuckInitializing{{Reason: "Error"}},
				Reason:     "InitContainersInRestartLoop",
			},
			wantStuck: true,
		},
		{
			name: "init container with waiting state",
			podStatus: corev1.PodStatus{
				StartTime: &metav1.Time{Time: now.Add(-defaultTimeConfig.PodLaunchThresholdSecondsOnFailedRestarts - time.Minute)},
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodInitialized,
						Status: corev1.ConditionFalse,
					},
				},
				InitContainerStatuses: []corev1.ContainerStatus{
					{
						RestartCount: RestartCountToFailInstance + 1,
						LastTerminationState: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason: "ImagePullBackOff",
							},
						},
					},
				},
			},
			want: PodStuckInitializing{
				Containers: []ContainerStuckInitializing{{Reason: "ImagePullBackOff"}},
				Reason:     "InitContainersInRestartLoop",
			},
			wantStuck: true,
		},
		{
			name: "pod stuck in initialization beyond threshold",
			podStatus: corev1.PodStatus{
				StartTime: &metav1.Time{Time: now.Add(-defaultTimeConfig.PodLaunchThresholdMinutesOnInitFailure - time.Hour)},
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodInitialized,
						Status: corev1.ConditionFalse,
					},
				},
				InitContainerStatuses: []corev1.ContainerStatus{
					{
						RestartCount: 1, // Less than RestartCountToFailInstance
					},
				},
			},
			want: PodStuckInitializing{
				Containers: []ContainerStuckInitializing{},
				Reason:     "ContainerStuckAfterThreshold",
			},
			wantStuck: true,
		},
		{
			name: "pod still initializing within threshold",
			podStatus: corev1.PodStatus{
				StartTime: &metav1.Time{Time: now.Add(-time.Minute)},
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodInitialized,
						Status: corev1.ConditionFalse,
					},
				},
				InitContainerStatuses: []corev1.ContainerStatus{
					{
						RestartCount: 1,
					},
				},
			},
			want:      PodStuckInitializing{},
			wantStuck: false,
		},
		{
			name: "container restarts within threshold",
			podStatus: corev1.PodStatus{
				StartTime: &metav1.Time{Time: now.Add(-time.Minute)},
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodInitialized,
						Status: corev1.ConditionTrue,
					},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{
						RestartCount: RestartCountToFailInstance - 1,
					},
				},
			},
			want:      PodStuckInitializing{},
			wantStuck: false,
		},
		{
			name: "no start time",
			podStatus: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodInitialized,
						Status: corev1.ConditionFalse,
					},
				},
				InitContainerStatuses: []corev1.ContainerStatus{
					{
						RestartCount: RestartCountToFailInstance + 1,
					},
				},
			},
			want:      PodStuckInitializing{},
			wantStuck: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, stuck := IsPodStuckInitializing(&corev1.Pod{Status: tt.podStatus}, defaultTimeConfig)
			assert.Equal(t, tt.wantStuck, stuck, "stuck status mismatch")
			assert.Equal(t, tt.want, got, "result mismatch")
		})
	}
}

func TestHasContainerNamed(t *testing.T) {
	tests := []struct {
		name       string
		containers []corev1.Container
		target     string
		want       bool
	}{
		{
			name: "container exists",
			containers: []corev1.Container{
				{Name: "container1"},
				{Name: "container2"},
				{Name: "container3"},
			},
			target: "container2",
			want:   true,
		},
		{
			name: "container does not exist",
			containers: []corev1.Container{
				{Name: "container1"},
				{Name: "container2"},
			},
			target: "container3",
			want:   false,
		},
		{
			name:       "empty container list",
			containers: []corev1.Container{},
			target:     "container1",
			want:       false,
		},
		{
			name: "case sensitive match",
			containers: []corev1.Container{
				{Name: "Container1"},
				{Name: "container1"},
			},
			target: "Container1",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasContainerNamed(tt.containers, tt.target); got != tt.want {
				t.Errorf("HasContainerNamed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsUtilsPod(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		exp  bool
	}{
		{
			name: "not helm utils pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "bar",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1"},
					},
				},
			},
			exp: false,
		},
		{
			name: "helm utils pod not helm chart namespace",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      common.UtilsPodName,
					Namespace: "bar",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1"},
					},
				},
			},
			exp: false,
		},
		{
			name: "helm utils pod in helm chart namespace",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      common.UtilsPodName,
					Namespace: "sr-bar",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1"},
					},
				},
			},
			exp: true,
		},
		{
			name: "not container function utils pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "0-sr-foo",
					Namespace: "nvcf-backend",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1"},
					},
				},
			},
			exp: false,
		},
		{
			name: "utils pod no env",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "0-sr-foo",
					Namespace: "nvcf-backend",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: common.UtilsContainerName},
					},
				},
			},
			exp: false,
		},
		{
			name: "utils pod with env",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "0-sr-foo",
					Namespace: "nvcf-backend",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: common.UtilsContainerName, Env: []corev1.EnvVar{{Name: InstanceIDEnvKey}}},
					},
				},
			},
			exp: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.exp, IsUtilsPod(tt.pod))
		})
	}
}

func TestValidateAllContainerResourcesSet(t *testing.T) {
	one := resource.MustParse("1")
	oneGi := resource.MustParse("1Gi")
	cases := []struct {
		name         string
		resourceReqs corev1.ResourceRequirements
		expErr       string
	}{
		{
			name: "all set",
			resourceReqs: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    one,
					corev1.ResourceMemory: oneGi,
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    one,
					corev1.ResourceMemory: oneGi,
				},
			},
		},
		{
			name: "lims set",
			resourceReqs: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    one,
					corev1.ResourceMemory: oneGi,
				},
			},
		},
		{
			name: "reqs set",
			resourceReqs: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    one,
					corev1.ResourceMemory: oneGi,
				},
			},
		},
		{
			name: "lims missing cpu",
			resourceReqs: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceMemory: oneGi,
				},
			},
			expErr: `resources validation failed for Pod "foo": ["init container \"foo-init\": resources not set: [\"cpu\"]" "container \"foo-container\": resources not set: [\"cpu\"]"]`,
		},
		{
			name: "lims missing memory",
			resourceReqs: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU: one,
				},
			},
			expErr: `resources validation failed for Pod "foo": ["init container \"foo-init\": resources not set: [\"memory\"]" "container \"foo-container\": resources not set: [\"memory\"]"]`,
		},
		{
			name:         "no lims set",
			resourceReqs: corev1.ResourceRequirements{},
			expErr:       `resources validation failed for Pod "foo": ["init container \"foo-init\": no requests or limits" "container \"foo-container\": no requests or limits"]`,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAllContainerResourcesSet(&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{{Name: "foo-init", Resources: tt.resourceReqs}},
					Containers:     []corev1.Container{{Name: "foo-container", Resources: tt.resourceReqs}},
				},
			}); tt.expErr != "" {
				assert.EqualError(t, err, tt.expErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPodSpecRequestsGPU(t *testing.T) {
	testCases := []struct {
		name     string
		podSpec  *corev1.PodSpec
		expected bool
	}{
		{
			name:     "empty containers",
			podSpec:  &corev1.PodSpec{},
			expected: false,
		},
		{
			name: "CPU and memory only",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				}},
			},
			expected: false,
		},
		{
			name: "GPU in requests",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
					},
				}},
			},
			expected: true,
		},
		{
			name: "GPU in limits only",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
					},
				}},
			},
			expected: true,
		},
		{
			name: "shared GPU resource",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/gpu.shared"): resource.MustParse("1"),
						},
					},
				}},
			},
			expected: true,
		},
		{
			name: "PGPU resource",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/pgpu"): resource.MustParse("1"),
						},
					},
				}},
			},
			expected: true,
		},
		{
			name: "zero GPU resource",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("0"),
						},
					},
				}},
			},
			expected: false,
		},
		{
			name: "GPU in init container",
			podSpec: &corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
					},
				}},
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					},
				}},
			},
			expected: true,
		},
		{
			name: "multiple containers - one with GPU",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					},
					{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
							},
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test with default (no args) - uses DefaultGPUResourceNames
			result := PodSpecRequestsGPU(tc.podSpec)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestPodSpecRequestsGPU_CustomResourceNames(t *testing.T) {
	podSpec := &corev1.PodSpec{
		Containers: []corev1.Container{{
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceName("custom.io/accelerator"): resource.MustParse("1"),
				},
			},
		}},
	}

	// Default resource names should not match custom resource
	assert.False(t, PodSpecRequestsGPU(podSpec))

	// Custom resource names should match
	assert.True(t, PodSpecRequestsGPU(podSpec, "custom.io/accelerator"))
}
