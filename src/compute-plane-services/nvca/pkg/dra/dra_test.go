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

package nvcfdra

import (
	"testing"

	nvresourcev1beta1 "github.com/NVIDIA/k8s-dra-driver-gpu/api/nvidia.com/resource/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Test_containerRequestsStaticGPU(t *testing.T) {
	tests := []struct {
		name string
		c    corev1.Container
		want bool
	}{
		{
			name: "empty container",
			c:    corev1.Container{},
			want: false,
		},
		{
			name: "cpu only in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
				},
			},
			want: false,
		},
		{
			name: "nvidia.com/gpu in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
				},
			},
			want: true,
		},
		{
			name: "nvidia.com/gpu in requests",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
				},
			},
			want: true,
		},
		{
			name: "nvidia.com/pgpu in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/pgpu": resource.MustParse("2")},
				},
			},
			want: true,
		},
		{
			name: "nvidia.com/gpu.shared in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/gpu.shared": resource.MustParse("4")},
				},
			},
			want: true,
		},
		{
			name: "mig resource in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/mig-1g.5gb": resource.MustParse("1")},
				},
			},
			want: true,
		},
		{
			name: "mig resource in requests",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{"nvidia.com/mig-3g.20gb": resource.MustParse("2")},
				},
			},
			want: true,
		},
		{
			name: "zero nvidia.com/gpu in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("0")},
				},
			},
			want: false,
		},
		{
			name: "zero mig resource in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/mig-1g.5gb": resource.MustParse("0")},
				},
			},
			want: false,
		},
		{
			name: "gpu in both limits and requests",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits:   corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
					Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
				},
			},
			want: true,
		},
		{
			name: "zero gpu in limits, nonzero in requests",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits:   corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("0")},
					Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
				},
			},
			want: true,
		},
		{
			name: "unrelated nvidia resource",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/rdma": resource.MustParse("1")},
				},
			},
			want: false,
		},
		{
			name: "gpu mixed with cpu",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:                           resource.MustParse("4"),
						corev1.ResourceName("nvidia.com/gpu"):        resource.MustParse("2"),
						corev1.ResourceName("nvidia.com/mig-1g.5gb"): resource.MustParse("0"),
					},
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containerRequestsStaticGPU(tt.c)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNVLinkDomainSchedulingParametersNarrowRequiredNodeAffinity(t *testing.T) {
	cliqueReq := corev1.NodeSelectorRequirement{
		Key:      GPUCliqueNodeLabel,
		Operator: corev1.NodeSelectorOpExists,
	}
	hostnameReq := corev1.NodeSelectorRequirement{
		Key:      "kubernetes.io/hostname",
		Operator: corev1.NodeSelectorOpIn,
		Values:   []string{"selected-node-a", "selected-node-b"},
	}
	instanceTypeReq := corev1.NodeSelectorRequirement{
		Key:      "nvidia.com/instance-type",
		Operator: corev1.NodeSelectorOpIn,
		Values:   []string{"gb200"},
	}
	nameField := corev1.NodeSelectorRequirement{
		Key:      "metadata.name",
		Operator: corev1.NodeSelectorOpIn,
		Values:   []string{"selected-node-a"},
	}
	newRequiredNodeAffinity := func(terms ...corev1.NodeSelectorTerm) *corev1.Affinity {
		return &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: terms,
				},
			},
		}
	}

	tests := []struct {
		name     string
		affinity *corev1.Affinity
		applyN   int
		want     []corev1.NodeSelectorTerm
	}{
		{
			name:   "no affinity yields a single clique term",
			applyN: 1,
			want: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{cliqueReq},
			}},
		},
		{
			name:     "empty required selector yields a single clique term",
			affinity: newRequiredNodeAffinity(),
			applyN:   1,
			want: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{cliqueReq},
			}},
		},
		{
			name: "clique requirement is ANDed into an existing hostname term",
			affinity: newRequiredNodeAffinity(corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{hostnameReq},
			}),
			applyN: 1,
			want: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{hostnameReq, cliqueReq},
			}},
		},
		{
			name: "every existing term gets the clique requirement",
			affinity: newRequiredNodeAffinity(
				corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{hostnameReq}},
				corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{instanceTypeReq}},
			),
			applyN: 1,
			want: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{hostnameReq, cliqueReq}},
				{MatchExpressions: []corev1.NodeSelectorRequirement{instanceTypeReq, cliqueReq}},
			},
		},
		{
			name: "existing match fields are preserved",
			affinity: newRequiredNodeAffinity(corev1.NodeSelectorTerm{
				MatchFields: []corev1.NodeSelectorRequirement{nameField},
			}),
			applyN: 1,
			want: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{cliqueReq},
				MatchFields:      []corev1.NodeSelectorRequirement{nameField},
			}},
		},
		{
			name: "repeated mutation does not duplicate the clique requirement",
			affinity: newRequiredNodeAffinity(corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{hostnameReq},
			}),
			applyN: 3,
			want: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{hostnameReq, cliqueReq},
			}},
		},
	}

	setters := []struct {
		name  string
		apply func(pod *corev1.Pod)
	}{
		{
			name:  "preferred",
			apply: func(pod *corev1.Pod) { SetPreferredNVLinkDomainSchedulingParameters("foo", pod) },
		},
		{
			name:  "required",
			apply: func(pod *corev1.Pod) { SetRequiredNVLinkDomainSchedulingParameters("foo", "0", pod) },
		},
	}

	for _, setter := range setters {
		for _, tt := range tests {
			t.Run(setter.name+"/"+tt.name, func(t *testing.T) {
				pod := &corev1.Pod{Spec: corev1.PodSpec{Affinity: tt.affinity.DeepCopy()}}
				for i := 0; i < tt.applyN; i++ {
					setter.apply(pod)
				}
				got := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
				assert.Equal(t, tt.want, got.NodeSelectorTerms)
			})
		}
	}
}

func TestComputeDomainsForWorkload(t *testing.T) {
	podWith := func(idx *string) client.Object {
		p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "w",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
				corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("4"),
			}},
		}}}}
		if idx != nil {
			p.Annotations = map[string]string{RequiredNVLinkDomainIndexAnnotation: *idx}
		}
		return p
	}
	names := func(cds []*nvresourcev1beta1.ComputeDomain) []string {
		out := make([]string, 0, len(cds))
		for _, cd := range cds {
			out = append(out, cd.Name)
		}
		return out
	}

	t.Run("no annotation still yields the default domain", func(t *testing.T) {
		// Regression guard: returning nothing here strips the IMEX channel from every
		// unannotated workload, and removes the name an older agent claims on rollback.
		cds := ComputeDomainsForWorkload(podWith(nil))
		assert.Equal(t, []string{defaultComputeDomainName}, names(cds))
	})

	t.Run("annotated workload gets the default domain plus its own", func(t *testing.T) {
		idx := "0"
		cds := ComputeDomainsForWorkload(podWith(&idx))
		require.Len(t, cds, 2)
		assert.Equal(t, defaultComputeDomainName, cds[0].Name)
		assert.Equal(t, ComputeDomainForIndex("0").Name, cds[1].Name)
	})

	t.Run("distinct indices get distinct domains", func(t *testing.T) {
		a, b := "0", "1"
		cds := ComputeDomainsForWorkload(podWith(&a), podWith(&b))
		assert.Len(t, cds, 3)
		assert.NotEqual(t, ComputeDomainForIndex("0").Name, ComputeDomainForIndex("1").Name)
	})

	t.Run("naming is independent of which other indices are present", func(t *testing.T) {
		// A rank-based name would shift when another value joins the render, renaming a
		// domain out from under pods that already claim it.
		one := "1"
		zero := "0"
		alone := ComputeDomainsForWorkload(podWith(&one))
		together := ComputeDomainsForWorkload(podWith(&zero), podWith(&one))
		assert.Contains(t, names(alone), ComputeDomainForIndex("1").Name)
		assert.Contains(t, names(together), ComputeDomainForIndex("1").Name)
	})

	t.Run("non-numeric index is accepted, not a terminal error", func(t *testing.T) {
		// The value is tenant-authored; it must never be able to fail an install.
		for _, raw := range []string{"prefill", "1e3", " 0", "2147483648"} {
			cds := ComputeDomainsForWorkload(podWith(&raw))
			assert.Len(t, cds, 2, "raw=%q", raw)
		}
	})

	t.Run("unstructured operator CRD is searched for nested annotations", func(t *testing.T) {
		// Grove PodCliqueSet / DynamoGraphDeployment decode to unstructured and hold their
		// pod templates at operator-defined paths.
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodCliqueSet",
			"metadata":   map[string]any{"name": "x"},
			"spec": map[string]any{
				"template": map[string]any{
					"cliques": []any{
						map[string]any{
							"spec": map[string]any{
								"podSpec": map[string]any{},
								"annotations": map[string]any{
									RequiredNVLinkDomainIndexAnnotation: "7",
								},
							},
						},
					},
				},
			},
		}}
		cds := ComputeDomainsForWorkload(u)
		require.Len(t, cds, 2)
		assert.Equal(t, ComputeDomainForIndex("7").Name, cds[1].Name)
	})

	t.Run("a CRD's own top-level annotations are not treated as a pod template", func(t *testing.T) {
		u := &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{
				"annotations": map[string]any{RequiredNVLinkDomainIndexAnnotation: "9"},
			},
			"spec": map[string]any{},
		}}
		assert.Equal(t, []string{defaultComputeDomainName}, names(ComputeDomainsForWorkload(u)))
	})
}

// TestComputeDomainsForWorkload_MultiTemplateUnstructured guards the disaggregated shape: one
// operator CRD carrying several pod templates with different domain indices. Returning only the
// first would leave the other groups' Pods claiming a ResourceClaimTemplate nobody created,
// which strands them Pending with no diagnostic.
func TestComputeDomainsForWorkload_MultiTemplateUnstructured(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "grove.io/v1alpha1",
		"kind":       "PodCliqueSet",
		"metadata":   map[string]any{"name": "disagg"},
		"spec": map[string]any{
			"template": map[string]any{
				"cliques": []any{
					map[string]any{"spec": map[string]any{
						"annotations": map[string]any{RequiredNVLinkDomainIndexAnnotation: "0"},
					}},
					map[string]any{"spec": map[string]any{
						"annotations": map[string]any{RequiredNVLinkDomainIndexAnnotation: "1"},
					}},
				},
			},
		},
	}}
	got := make([]string, 0)
	for _, cd := range ComputeDomainsForWorkload(u) {
		got = append(got, cd.Name)
	}
	assert.ElementsMatch(t, []string{
		defaultComputeDomainName,
		ComputeDomainForIndex("0").Name,
		ComputeDomainForIndex("1").Name,
	}, got)
}
