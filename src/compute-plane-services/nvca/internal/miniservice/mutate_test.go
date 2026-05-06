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

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
)

const (
	grafanaCloudProvider = "GRAFANA_CLOUD"
)

func Test_newWorkloadTelemetriesEnvVars(t *testing.T) {
	telemetries := &common.TelemetriesLaunchSpecification{}
	telemetries.Telemetries.Logs = &common.Telemetry{
		Protocol: "http",
		Endpoint: "endpoint",
		Provider: grafanaCloudProvider,
		Name:     "telemetry-foo",
	}
	telemetries.Telemetries.Metrics = &common.Telemetry{
		Protocol: "http",
		Endpoint: "endpoint",
		Provider: grafanaCloudProvider,
		Name:     "telemetry-baz",
	}
	telemetries.Telemetries.Traces = &common.Telemetry{
		Protocol: "http",
		Endpoint: "endpoint",
		Provider: grafanaCloudProvider,
		Name:     "telemetry-bar",
	}

	envs, err := newWorkloadTelemetriesEnvVars(telemetries,
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: common.ByooOTelCollectorPodNameBase},
		},
	)
	require.NoError(t, err)
	require.NotEmpty(t, envs)

	envNames := make(map[string]bool, len(envs))
	for _, e := range envs {
		envNames[e.Name] = true
	}
	assert.True(t, envNames[common.OTelExporterLogsEndpointEnv], "should have logs endpoint")
	assert.True(t, envNames[common.OTelExporterMetricsEndpointEnv], "should have metrics endpoint")
	assert.True(t, envNames[common.OTelExporterTracesEndpointEnv], "should have traces endpoint")
	assert.True(t, envNames[common.OTelHealthCheckEndpointEnv], "should have health check endpoint")
}

func Test_newInstanceIDEnv(t *testing.T) {
	funcEnv := newInstanceIDEnv(common.FunctionCreationAction)
	assert.Equal(t, "NVCF_INSTANCE_ID", funcEnv.Name)
	require.NotNil(t, funcEnv.ValueFrom)
	require.NotNil(t, funcEnv.ValueFrom.FieldRef)

	taskEnv := newInstanceIDEnv(common.TaskCreationAction)
	assert.Equal(t, "NVCT_INSTANCE_ID", taskEnv.Name)
	require.NotNil(t, taskEnv.ValueFrom)
	require.NotNil(t, taskEnv.ValueFrom.FieldRef)
}

func Test_setKAISchedulerMutators(t *testing.T) {
	testCases := []struct {
		name             string
		obj              client.Object
		getSchedulerName func(client.Object) string
		getQueueLabel    func(client.Object) (string, bool)
		getExistingLabel func(client.Object) (string, bool)
	}{
		{
			name: "Pod",
			obj: func() client.Object {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"existing": "label"},
					},
					Spec: corev1.PodSpec{
						SchedulerName: "default-scheduler",
						Containers: []corev1.Container{
							{Name: "test-container"},
						},
					},
				}
				pod.SetGroupVersionKind(podGVK)
				return pod
			}(),
			getSchedulerName: func(obj client.Object) string {
				return obj.(*corev1.Pod).Spec.SchedulerName
			},
			getQueueLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.GetLabels()[kaischeduler.SchedulerQueueLabel]
				return val, ok
			},
			getExistingLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.GetLabels()["existing"]
				return val, ok
			},
		},
		{
			name: "Deployment",
			obj: func() client.Object {
				deployment := &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"existing": "label"},
					},
					Spec: appsv1.DeploymentSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"pod-existing": "pod-label"},
							},
							Spec: corev1.PodSpec{
								SchedulerName: "default-scheduler",
								Containers: []corev1.Container{
									{Name: "test-container"},
								},
							},
						},
					},
				}
				deployment.SetGroupVersionKind(deploymentGVK)
				return deployment
			}(),
			getSchedulerName: func(obj client.Object) string {
				return obj.(*appsv1.Deployment).Spec.Template.Spec.SchedulerName
			},
			getQueueLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.(*appsv1.Deployment).Spec.Template.Labels[kaischeduler.SchedulerQueueLabel]
				return val, ok
			},
			getExistingLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.(*appsv1.Deployment).Spec.Template.Labels["pod-existing"]
				return val, ok
			},
		},
		{
			name: "ReplicaSet",
			obj: func() client.Object {
				rs := &appsv1.ReplicaSet{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"existing": "label"},
					},
					Spec: appsv1.ReplicaSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"pod-existing": "pod-label"},
							},
							Spec: corev1.PodSpec{
								SchedulerName: "default-scheduler",
								Containers: []corev1.Container{
									{Name: "test-container"},
								},
							},
						},
					},
				}
				rs.SetGroupVersionKind(replicaSetGVK)
				return rs
			}(),
			getSchedulerName: func(obj client.Object) string {
				return obj.(*appsv1.ReplicaSet).Spec.Template.Spec.SchedulerName
			},
			getQueueLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.(*appsv1.ReplicaSet).Spec.Template.Labels[kaischeduler.SchedulerQueueLabel]
				return val, ok
			},
			getExistingLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.(*appsv1.ReplicaSet).Spec.Template.Labels["pod-existing"]
				return val, ok
			},
		},
		{
			name: "StatefulSet",
			obj: func() client.Object {
				sts := &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"existing": "label"},
					},
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"pod-existing": "pod-label"},
							},
							Spec: corev1.PodSpec{
								SchedulerName: "default-scheduler",
								Containers: []corev1.Container{
									{Name: "test-container"},
								},
							},
						},
					},
				}
				sts.SetGroupVersionKind(statefulSetGVK)
				return sts
			}(),
			getSchedulerName: func(obj client.Object) string {
				return obj.(*appsv1.StatefulSet).Spec.Template.Spec.SchedulerName
			},
			getQueueLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.(*appsv1.StatefulSet).Spec.Template.Labels[kaischeduler.SchedulerQueueLabel]
				return val, ok
			},
			getExistingLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.(*appsv1.StatefulSet).Spec.Template.Labels["pod-existing"]
				return val, ok
			},
		},
		{
			name: "Job",
			obj: func() client.Object {
				job := &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"existing": "label"},
					},
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"pod-existing": "pod-label"},
							},
							Spec: corev1.PodSpec{
								SchedulerName: "default-scheduler",
								Containers: []corev1.Container{
									{Name: "test-container"},
								},
							},
						},
					},
				}
				job.SetGroupVersionKind(jobGVK)
				return job
			}(),
			getSchedulerName: func(obj client.Object) string {
				return obj.(*batchv1.Job).Spec.Template.Spec.SchedulerName
			},
			getQueueLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.(*batchv1.Job).Spec.Template.Labels[kaischeduler.SchedulerQueueLabel]
				return val, ok
			},
			getExistingLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.(*batchv1.Job).Spec.Template.Labels["pod-existing"]
				return val, ok
			},
		},
		{
			name: "CronJob",
			obj: func() client.Object {
				cj := &batchv1.CronJob{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"existing": "label"},
					},
					Spec: batchv1.CronJobSpec{
						JobTemplate: batchv1.JobTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"job-existing": "job-label"},
							},
							Spec: batchv1.JobSpec{
								Template: corev1.PodTemplateSpec{
									ObjectMeta: metav1.ObjectMeta{
										Labels: map[string]string{"pod-existing": "pod-label"},
									},
									Spec: corev1.PodSpec{
										SchedulerName: "default-scheduler",
										Containers: []corev1.Container{
											{Name: "test-container"},
										},
									},
								},
							},
						},
					},
				}
				cj.SetGroupVersionKind(cronJobGVK)
				return cj
			}(),
			getSchedulerName: func(obj client.Object) string {
				return obj.(*batchv1.CronJob).Spec.JobTemplate.Spec.Template.Spec.SchedulerName
			},
			getQueueLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.(*batchv1.CronJob).Spec.JobTemplate.Spec.Template.Labels[kaischeduler.SchedulerQueueLabel]
				return val, ok
			},
			getExistingLabel: func(obj client.Object) (string, bool) {
				val, ok := obj.(*batchv1.CronJob).Spec.JobTemplate.Spec.Template.Labels["pod-existing"]
				return val, ok
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			objMutators := newEmptyObjectMutators()
			objMutators.setKAISchedulerMutators()
			for _, mf := range objMutators[tt.obj.GetObjectKind().GroupVersionKind()] {
				err := mf.mutate(context.Background(), tt.obj)
				require.NoError(t, err)
			}

			assert.Equal(t, kaischeduler.SchedulerName, tt.getSchedulerName(tt.obj),
				"%s should have kai-scheduler set", tt.name)
			queueLabel, ok := tt.getQueueLabel(tt.obj)
			assert.True(t, ok, "%s should have queue label", tt.name)
			assert.Equal(t, kaischeduler.GetQName(), queueLabel,
				"%s should have correct queue label value", tt.name)

			// Ensure existing labels are preserved
			existingLabel, ok := tt.getExistingLabel(tt.obj)
			assert.True(t, ok, "%s should preserve existing labels", tt.name)
			assert.NotEmpty(t, existingLabel, "%s existing label should not be empty", tt.name)
		})
	}
}

func Test_newWorkloadTelemetriesEnvVars_NilService(t *testing.T) {
	telemetries := &common.TelemetriesLaunchSpecification{}
	telemetries.Telemetries.Logs = &common.Telemetry{
		Protocol: "http",
		Endpoint: "endpoint",
		Provider: grafanaCloudProvider,
		Name:     "telemetry-foo",
	}

	_, err := newWorkloadTelemetriesEnvVars(telemetries, nil)
	require.Error(t, err, "should fail when byoo service is nil")
}

func Test_setCPUWorkloadNodeAffinity(t *testing.T) {
	const instanceTypeKey = "nvca.nvcf.nvidia.io/instance-type"

	testCases := []struct {
		name                      string
		existingPodSpec           corev1.PodSpec
		wantRequiredAffinityNil   bool
		wantPreferredCount        int
		wantExistingPrefsPreserve bool
	}{
		{
			name:                    "nil affinity",
			existingPodSpec:         corev1.PodSpec{},
			wantRequiredAffinityNil: true,
			wantPreferredCount:      1,
		},
		{
			name: "existing affinity with nil node affinity",
			existingPodSpec: corev1.PodSpec{
				Affinity: &corev1.Affinity{},
			},
			wantRequiredAffinityNil: true,
			wantPreferredCount:      1,
		},
		{
			name: "existing node affinity with non-instance-type required terms",
			existingPodSpec: corev1.PodSpec{
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      "existing-key",
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"existing-value"},
										},
									},
								},
							},
						},
					},
				},
			},
			wantRequiredAffinityNil: false,
			wantPreferredCount:      1,
		},
		{
			name: "existing preferred terms",
			existingPodSpec: corev1.PodSpec{
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
							{
								Weight: 50,
								Preference: corev1.NodeSelectorTerm{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      "existing-preference",
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"value"},
										},
									},
								},
							},
						},
					},
				},
			},
			wantRequiredAffinityNil:   true,
			wantPreferredCount:        2,
			wantExistingPrefsPreserve: true,
		},
		{
			name: "removes existing instance-type required affinity (utilsPod scenario)",
			existingPodSpec: corev1.PodSpec{
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      instanceTypeKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"ON-PREM.GPU.H100"},
										},
									},
								},
							},
						},
					},
				},
			},
			wantRequiredAffinityNil: true,
			wantPreferredCount:      1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			podSpec := tc.existingPodSpec.DeepCopy()
			setCPUWorkloadNodeAffinity(podSpec)

			// Verify affinity is not nil
			require.NotNil(t, podSpec.Affinity)
			require.NotNil(t, podSpec.Affinity.NodeAffinity)

			// Verify anti-preference is added
			preferred := podSpec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
			require.NotEmpty(t, preferred, "should have preferred scheduling terms")
			assert.Len(t, preferred, tc.wantPreferredCount, "should have expected number of preferred terms")

			// Find the CPU workload anti-preference
			found := false
			for _, term := range preferred {
				if term.Weight == 100 {
					for _, expr := range term.Preference.MatchExpressions {
						if expr.Operator == corev1.NodeSelectorOpDoesNotExist &&
							expr.Key == instanceTypeKey {
							found = true
							break
						}
					}
				}
			}
			assert.True(t, found, "should have anti-preference for instance-type label")

			// Verify required affinity state
			if tc.wantRequiredAffinityNil {
				assert.Nil(t, podSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
					"required affinity should be nil")
			} else {
				require.NotNil(t, podSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
					"required affinity should not be nil")
				// Verify no instance-type keys remain in required affinity
				for _, term := range podSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
					for _, expr := range term.MatchExpressions {
						assert.NotEqual(t, instanceTypeKey, expr.Key, "instance-type key should be removed from required affinity")
					}
				}
			}

			// Verify existing preferred terms are preserved if expected
			if tc.wantExistingPrefsPreserve {
				assert.Greater(t, len(preferred), 1, "should preserve existing preferred terms and add new one")
			}
		})
	}
}

func Test_removeInstanceTypeRequiredAffinity(t *testing.T) {
	const instanceTypeKey = "nvca.nvcf.nvidia.io/instance-type"

	testCases := []struct {
		name                     string
		existingNodeAffinity     *corev1.NodeAffinity
		wantRequiredAfterRemoval bool
		wantRemainingTermCount   int
	}{
		{
			name:                     "nil RequiredDuringScheduling",
			existingNodeAffinity:     &corev1.NodeAffinity{},
			wantRequiredAfterRemoval: false,
			wantRemainingTermCount:   0,
		},
		{
			name: "removes instance-type affinity only",
			existingNodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      instanceTypeKey,
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"ON-PREM.GPU.H100"},
								},
							},
						},
					},
				},
			},
			wantRequiredAfterRemoval: false,
			wantRemainingTermCount:   0,
		},
		{
			name: "preserves non-instance-type affinity",
			existingNodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "other-key",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"some-value"},
								},
							},
						},
					},
				},
			},
			wantRequiredAfterRemoval: true,
			wantRemainingTermCount:   1,
		},
		{
			name: "removes instance-type from mixed term and preserves rest",
			existingNodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      instanceTypeKey,
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"ON-PREM.GPU.H100"},
								},
								{
									Key:      "other-key",
									Operator: corev1.NodeSelectorOpExists,
								},
							},
						},
					},
				},
			},
			wantRequiredAfterRemoval: true,
			wantRemainingTermCount:   1,
		},
		{
			name: "handles multiple terms",
			existingNodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      instanceTypeKey,
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"ON-PREM.GPU.H100"},
								},
							},
						},
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "keep-this-key",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"value"},
								},
							},
						},
					},
				},
			},
			wantRequiredAfterRemoval: true,
			wantRemainingTermCount:   1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			na := tc.existingNodeAffinity.DeepCopy()
			k8sutil.RemoveInstanceTypeRequiredAffinity(na, nodefeatures.UniformInstanceTypeLabelKey)

			if tc.wantRequiredAfterRemoval {
				require.NotNil(t, na.RequiredDuringSchedulingIgnoredDuringExecution,
					"should still have required affinity")
				assert.Len(t, na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
					tc.wantRemainingTermCount, "should have expected number of terms")

				// Verify no instance-type key remains
				for _, term := range na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
					for _, expr := range term.MatchExpressions {
						assert.NotEqual(t, instanceTypeKey, expr.Key,
							"instance-type key should be removed")
					}
				}
			} else {
				assert.Nil(t, na.RequiredDuringSchedulingIgnoredDuringExecution,
					"required affinity should be nil")
			}
		})
	}
}

func Test_setPerPodInstanceTypeNodeAffinityMutators(t *testing.T) {
	const instanceTypeKey = "nvca.nvcf.nvidia.io/instance-type"
	const instanceTypeValue = "dgxa100.80gb.8.norm"

	testCases := []struct {
		name              string
		podSpec           corev1.PodSpec
		wantRequiredAffin bool
		wantPreferredAnti bool
	}{
		{
			name: "GPU pod gets required affinity",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
					},
				}},
			},
			wantRequiredAffin: true,
			wantPreferredAnti: false,
		},
		{
			name: "CPU pod gets preferred anti-affinity",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					},
				}},
			},
			wantRequiredAffin: false,
			wantPreferredAnti: true,
		},
		{
			name:              "empty pod gets preferred anti-affinity",
			podSpec:           corev1.PodSpec{},
			wantRequiredAffin: false,
			wantPreferredAnti: true,
		},
		{
			name: "utilsPod scenario: CPU pod with pre-existing instance-type affinity gets it removed",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "utils",
					Image: "utils:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				}},
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      instanceTypeKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"ON-PREM.GPU.H100"},
										},
									},
								},
							},
						},
					},
				},
			},
			wantRequiredAffin: false,
			wantPreferredAnti: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: *tc.podSpec.DeepCopy()}
			pod.SetGroupVersionKind(podGVK)

			objMutators := newEmptyObjectMutators()
			objMutators.setPerPodInstanceTypeNodeAffinityMutators(instanceTypeKey, instanceTypeValue)

			for _, mf := range objMutators[podGVK] {
				err := mf.mutate(context.Background(), pod)
				require.NoError(t, err)
			}

			require.NotNil(t, pod.Spec.Affinity, "affinity should be set")
			require.NotNil(t, pod.Spec.Affinity.NodeAffinity, "node affinity should be set")

			if tc.wantRequiredAffin {
				require.NotNil(t, pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
					"GPU pod should have required affinity")
				found := false
				for _, term := range pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
					for _, expr := range term.MatchExpressions {
						if expr.Key == instanceTypeKey && expr.Operator == corev1.NodeSelectorOpIn {
							assert.Contains(t, expr.Values, instanceTypeValue)
							found = true
						}
					}
				}
				assert.True(t, found, "should find required instance-type affinity")
			} else {
				// Verify no instance-type required affinity exists
				if pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
					for _, term := range pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
						for _, expr := range term.MatchExpressions {
							assert.NotEqual(t, instanceTypeKey, expr.Key,
								"instance-type required affinity should be removed for CPU pods")
						}
					}
				}
			}

			if tc.wantPreferredAnti {
				preferred := pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
				require.NotEmpty(t, preferred, "CPU pod should have preferred terms")
				found := false
				for _, term := range preferred {
					if term.Weight == 100 {
						for _, expr := range term.Preference.MatchExpressions {
							if expr.Key == instanceTypeKey && expr.Operator == corev1.NodeSelectorOpDoesNotExist {
								found = true
							}
						}
					}
				}
				assert.True(t, found, "should find anti-preference for instance-type label")
			}
		})
	}
}
