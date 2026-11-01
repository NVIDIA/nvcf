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

package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/require"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
)

func TestNew(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	newPodAdmissionReviewRequest := func(pods ...*v1.Pod) *http.Request {
		t.Helper()
		b := newPodAdmissionReviewRequestBodyPods(t, pods...)
		r := httptest.NewRequest("POST", "/foo", bytes.NewBuffer(b))
		r.Header.Set("Content-Type", "application/json")
		return r
	}

	t.Run("validating", func(t *testing.T) {
		h, err := NewHelmMiniServiceValidatingWebhook(ctx, "helm-mini-service-validating-webhook", featureflag.DefaultFetcher)
		require.NoError(t, err)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newPodAdmissionReviewRequest())

		require.Equal(t, http.StatusOK, w.Code)
		res := &admissionv1.AdmissionReview{}
		err = json.NewDecoder(w.Body).Decode(res)
		require.NoError(t, err)
		require.NotNil(t, res.Response)
		require.True(t, res.Response.Allowed)
	})

	t.Run("node mutating", func(t *testing.T) {
		sc := &atomic.Bool{}
		sc.Store(true)
		h, err := NewPodAffinityMutatingWebhook(ctx, "mutate-pod-nodeaffinity.nvca.nvcf.nvidia.io", PodAffinityOptions{
			SharedClusterOn:       sc,
			UniformInstanceLabels: true,
			HostIsolation:         false,
		})
		require.NoError(t, err)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newPodAdmissionReviewRequest(&v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "default"},
			Spec: v1.PodSpec{
				NodeSelector: map[string]string{
					"node.kubernetes.io/instance-type": "ON-PREM.GPU.A100",
				},
				Affinity: &v1.Affinity{
					PodAffinity: &v1.PodAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{{
							PodAffinityTerm: v1.PodAffinityTerm{
								LabelSelector: &metav1.LabelSelector{
									MatchExpressions: []metav1.LabelSelectorRequirement{{
										Key:      "GPU_COUNT",
										Operator: metav1.LabelSelectorOpIn,
										Values:   []string{"1"},
									}},
								},
								TopologyKey: "kubernetes.io/hostname",
							},
							Weight: 100,
						}},
						RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{{
							LabelSelector: &metav1.LabelSelector{
								MatchExpressions: []metav1.LabelSelectorRequirement{{
									Key:      "GPU_COUNT",
									Operator: metav1.LabelSelectorOpIn,
									Values:   []string{"1"},
								}},
							},
							TopologyKey: "topology.kubernetes.io/zone",
						}},
					},
					PodAntiAffinity: &v1.PodAntiAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{{
							LabelSelector: &metav1.LabelSelector{
								MatchExpressions: []metav1.LabelSelectorRequirement{{
									Key:      "GPU_COUNT",
									Operator: metav1.LabelSelectorOpIn,
									Values:   []string{"2"},
								}},
							},
							TopologyKey: "topology.kubernetes.io/zone",
						}},
					},
					NodeAffinity: &v1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
							NodeSelectorTerms: []v1.NodeSelectorTerm{{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      nodefeatures.DeprecatedInstanceTypeLabelKey,
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{"ON-PREM.GPU.A100"},
									},
								},
							}},
						},
					},
				},
			}}))

		require.Equal(t, http.StatusOK, w.Code)
		res := &admissionv1.AdmissionReview{}
		err = json.NewDecoder(w.Body).Decode(res)
		require.NoError(t, err)
		require.NotNil(t, res.Response)
		require.True(t, res.Response.Allowed, res.Response.Result)

		// Decode and sort these for consistency.
		var gotPatch []jsonpatch.Operation
		err = json.Unmarshal(res.Response.Patch, &gotPatch)
		require.NoError(t, err)
		sort.Slice(gotPatch, func(i, j int) bool {
			if gotPatch[i].Operation != gotPatch[j].Operation {
				return gotPatch[i].Operation < gotPatch[j].Operation
			}
			return gotPatch[i].Path < gotPatch[j].Path
		})
		require.Equal(t, []jsonpatch.Operation{
			{Operation: "add", Path: "/spec/affinity/nodeAffinity/requiredDuringSchedulingIgnoredDuringExecution/nodeSelectorTerms/0/matchExpressions/1", Value: map[string]any{"key": "nvca.nvcf.nvidia.io/schedule", "operator": "In", "values": []any{"true"}}},
			{Operation: "add", Path: "/spec/nodeSelector/nvca.nvcf.nvidia.io~1instance-type", Value: "ON-PREM.GPU.A100"},
			{Operation: "remove", Path: "/spec/nodeSelector/node.kubernetes.io~1instance-type"},
			{Operation: "replace", Path: "/spec/affinity/nodeAffinity/requiredDuringSchedulingIgnoredDuringExecution/nodeSelectorTerms/0/matchExpressions/0/key", Value: "nvca.nvcf.nvidia.io/instance-type"},
		}, gotPatch)
	})

	t.Run("pod enforcement mutating kata", func(t *testing.T) {
		h, err := NewPodEnforcementMutatingWebhook(ctx, "mutate-pod-enforcement.nvca.nvcf.nvidia.io", EnforcementOptions{
			DCGMMetrics: DCGMMetricsConfig{
				Annotations: map[string]string{
					"prometheus.io/scrape": "true",
					"prometheus.io/path":   "/metrics",
					"prometheus.io/port":   "9400",
				},
				ContainerPort: 9400,
			},
		})
		require.NoError(t, err)

		w := httptest.NewRecorder()

		h.ServeHTTP(w, newPodAdmissionReviewRequest(&v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo", Namespace: "default",
				Annotations: map[string]string{
					"nvca.nvcf.nvidia.io/enforcements": featureflag.AttrKataRuntimeIsolation.Key,
				},
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{{
					Name: "bar",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
						Limits: v1.ResourceList{
							v1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
					},
				}},
			}}))

		require.Equal(t, http.StatusOK, w.Code)
		res := &admissionv1.AdmissionReview{}
		err = json.NewDecoder(w.Body).Decode(res)
		require.NoError(t, err)
		require.NotNil(t, res.Response)
		require.True(t, res.Response.Allowed)

		// Decode and sort these for consistency.
		var gotPatch []jsonpatch.Operation
		require.NotEmpty(t, res.Response.Patch)
		err = json.Unmarshal(res.Response.Patch, &gotPatch)
		require.NoError(t, err)
		sort.Slice(gotPatch, func(i, j int) bool {
			if gotPatch[i].Operation != gotPatch[j].Operation {
				return gotPatch[i].Operation < gotPatch[j].Operation
			}
			return gotPatch[i].Path < gotPatch[j].Path
		})
		require.ElementsMatch(t, []jsonpatch.Operation{
			{
				Operation: "add",
				Path:      "/metadata/annotations/nvca.nvcf.nvidia.io~1container-mutation-enforced",
				Value:     "true",
			},
			{
				Operation: "add",
				Path:      "/metadata/annotations/nvca.nvcf.nvidia.io~1mutation-enforced",
				Value:     "true",
			},
			{
				Operation: "add",
				Path:      "/metadata/annotations/prometheus.io~1path",
				Value:     "/metrics",
			},
			{
				Operation: "add",
				Path:      "/metadata/annotations/prometheus.io~1port",
				Value:     "9400",
			},
			{
				Operation: "add",
				Path:      "/metadata/annotations/prometheus.io~1scrape",
				Value:     "true",
			},
			{
				Operation: "add",
				Path:      "/spec/containers/0/resources/limits/nvidia.com~1pgpu",
				Value:     "1",
			},
			{
				Operation: "add",
				Path:      "/spec/containers/0/resources/requests/nvidia.com~1pgpu",
				Value:     "1",
			},
			{
				Operation: "add",
				Path:      "/spec/runtimeClassName",
				Value:     kata.RuntimeClassNameGPU,
			},
			{
				Operation: "remove",
				Path:      "/spec/containers/0/resources/limits/nvidia.com~1gpu",
			},
			{
				Operation: "remove",
				Path:      "/spec/containers/0/resources/requests/nvidia.com~1gpu",
			},
			{
				Operation: "add",
				Path:      "/metadata/labels",
				Value: map[string]any{
					dcgmMetricsPresentLabelKey: "true",
				},
			},
			{
				Operation: "add",
				Path:      "/spec/containers/0/ports",
				Value: []any{
					map[string]any{
						"containerPort": float64(9400),
						"name":          "dcgm-metrics",
						"protocol":      "TCP",
					},
				},
			},
		}, gotPatch)
	})

	t.Run("pod enforcement mutating dcgm metrics", func(t *testing.T) {
		h, err := NewPodEnforcementMutatingWebhook(ctx, "mutate-pod-enforcement.nvca.nvcf.nvidia.io", EnforcementOptions{
			DCGMMetrics: DCGMMetricsConfig{
				Annotations: map[string]string{
					"prometheus.io/scrape": "true",
					"prometheus.io/path":   "/metrics",
					"prometheus.io/port":   "9400",
				},
				ContainerPort: 9400,
			},
		})
		require.NoError(t, err)

		w := httptest.NewRecorder()

		h.ServeHTTP(w, newPodAdmissionReviewRequest(&v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo", Namespace: "default",
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{{
					Name: "bar",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
						Limits: v1.ResourceList{
							v1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
					},
				}},
			}}))

		require.Equal(t, http.StatusOK, w.Code)
		res := &admissionv1.AdmissionReview{}
		err = json.NewDecoder(w.Body).Decode(res)
		require.NoError(t, err)
		require.NotNil(t, res.Response)
		require.True(t, res.Response.Allowed)

		// Since its non-kata, no Patch
		require.Empty(t, res.Response.Patch)
	})

	t.Run("pod enforcement mutating gpu passthrough", func(t *testing.T) {
		h, err := NewPodEnforcementMutatingWebhook(ctx, "mutate-pod-enforcement.nvca.nvcf.nvidia.io", EnforcementOptions{})
		require.NoError(t, err)

		w := httptest.NewRecorder()

		h.ServeHTTP(w, newPodAdmissionReviewRequest(&v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo", Namespace: "default",
				Annotations: map[string]string{
					"nvca.nvcf.nvidia.io/enforcements": featureflag.AttrPassthroughGPUEnabled.Key,
				},
			},
			Spec: v1.PodSpec{
				InitContainers: []v1.Container{{
					Name: "foo",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
						Limits: v1.ResourceList{
							v1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
					},
				}},
				Containers: []v1.Container{{
					Name: "bar",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
						Limits: v1.ResourceList{
							v1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
					},
				}},
			}}))

		require.Equal(t, http.StatusOK, w.Code)
		res := &admissionv1.AdmissionReview{}
		err = json.NewDecoder(w.Body).Decode(res)
		require.NoError(t, err)
		require.NotNil(t, res.Response)
		require.True(t, res.Response.Allowed)

		// Decode and sort these for consistency.
		var gotPatch []jsonpatch.Operation
		require.NotEmpty(t, res.Response.Patch)
		err = json.Unmarshal(res.Response.Patch, &gotPatch)
		require.NoError(t, err)
		sort.Slice(gotPatch, func(i, j int) bool {
			if gotPatch[i].Operation != gotPatch[j].Operation {
				return gotPatch[i].Operation < gotPatch[j].Operation
			}
			return gotPatch[i].Path < gotPatch[j].Path
		})
		require.Equal(t, []jsonpatch.Operation{
			{
				Operation: "add",
				Path:      "/metadata/annotations/nvca.nvcf.nvidia.io~1container-mutation-enforced",
				Value:     "true",
			},
			{
				Operation: "add",
				Path:      "/metadata/annotations/nvca.nvcf.nvidia.io~1init-mutation-enforced",
				Value:     "true",
			},
			{
				Operation: "add",
				Path:      "/spec/containers/0/resources/limits/nvidia.com~1pgpu",
				Value:     "1",
			},
			{
				Operation: "add",
				Path:      "/spec/containers/0/resources/requests/nvidia.com~1pgpu",
				Value:     "1",
			},
			{
				Operation: "add",
				Path:      "/spec/initContainers/0/resources/limits/nvidia.com~1pgpu",
				Value:     "1",
			},
			{
				Operation: "add",
				Path:      "/spec/initContainers/0/resources/requests/nvidia.com~1pgpu",
				Value:     "1",
			},
			{
				Operation: "remove",
				Path:      "/spec/containers/0/resources/limits/nvidia.com~1gpu",
			},
			{
				Operation: "remove",
				Path:      "/spec/containers/0/resources/requests/nvidia.com~1gpu",
			},
			{
				Operation: "remove",
				Path:      "/spec/initContainers/0/resources/limits/nvidia.com~1gpu",
			},
			{
				Operation: "remove",
				Path:      "/spec/initContainers/0/resources/requests/nvidia.com~1gpu",
			},
		}, gotPatch)
	})
}

func newPodAdmissionReviewRequestBodyPods(t require.TestingT, pods ...*v1.Pod) []byte {
	var pod *v1.Pod
	if len(pods) == 0 {
		pod = &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-name",
				Namespace: "default",
			},
		}
	} else {
		pod = pods[0]
	}
	pod.APIVersion = "v1"
	pod.Kind = "Pod"
	return newPodAdmissionReviewRequestBody(t, pod)
}

func newPodAdmissionReviewRequestBody(t require.TestingT, obj client.Object) []byte {
	rev := admissionv1.AdmissionReview{}
	rev.TypeMeta.APIVersion = "admission.k8s.io/v1"
	rev.TypeMeta.Kind = "AdmissionReview"
	rev.Request = &admissionv1.AdmissionRequest{}
	gvk := obj.GetObjectKind().GroupVersionKind()
	rev.Request.Kind.Group = gvk.Group
	rev.Request.Kind.Version = gvk.Version
	rev.Request.Kind.Kind = gvk.Kind
	rev.Request.Operation = admissionv1.Create
	pb, err := json.Marshal(obj)
	require.NoError(t, err)
	rev.Request.Object.Raw = pb
	b, err := json.Marshal(rev)
	require.NoError(t, err)
	return b
}
