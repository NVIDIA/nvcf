/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package gpu

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
)

func node(name, product string) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if product != "" {
		n.Labels = map[string]string{ProductLabel: product}
	}
	return n
}

// slices returns one EndpointSlice with one endpoint per node name; an empty
// name is an endpoint without a node.
func slices(nodes ...string) []discoveryv1.EndpointSlice {
	s := discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "svc-abc", Namespace: "models"}}
	for _, n := range nodes {
		e := discoveryv1.Endpoint{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(false)}}
		if n != "" {
			e.NodeName = ptr.To(n)
		}
		s.Endpoints = append(s.Endpoints, e)
	}
	return []discoveryv1.EndpointSlice{s}
}

func endpoint(specProduct string) *pylonv1alpha1.InferenceEndpoint {
	ep := &pylonv1alpha1.InferenceEndpoint{ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "models"}}
	if specProduct != "" {
		ep.Spec.GPU = &pylonv1alpha1.GPUSpec{Product: specProduct}
	}
	return ep
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	return s
}

func TestResolve(t *testing.T) {
	nodes := []client.Object{
		node("spark-1", "NVIDIA-GB10"),
		node("spark-2", "NVIDIA-GB10"),
		node("station-1", "NVIDIA-GB300"),
		node("cpu-1", ""),
	}
	tests := []struct {
		name        string
		specProduct string
		slices      []discoveryv1.EndpointSlice
		want        pylonv1alpha1.GPUStatus
	}{
		{
			name:        "spec wins and node products are still recorded",
			specProduct: "NVIDIA-H100",
			slices:      slices("spark-1"),
			want:        pylonv1alpha1.GPUStatus{Product: "NVIDIA-H100", Source: pylonv1alpha1.GPUSourceSpec, NodeProducts: []string{"NVIDIA-GB10"}},
		},
		{
			name:        "spec without endpoints",
			specProduct: "NVIDIA-H100",
			want:        pylonv1alpha1.GPUStatus{Product: "NVIDIA-H100", Source: pylonv1alpha1.GPUSourceSpec},
		},
		{
			name:   "single node",
			slices: slices("spark-1"),
			want:   pylonv1alpha1.GPUStatus{Product: "NVIDIA-GB10", Source: pylonv1alpha1.GPUSourceNodeLabels, NodeProducts: []string{"NVIDIA-GB10"}},
		},
		{
			name:   "same product on several nodes is listed once",
			slices: slices("spark-1", "spark-2", "spark-1"),
			want:   pylonv1alpha1.GPUStatus{Product: "NVIDIA-GB10", Source: pylonv1alpha1.GPUSourceNodeLabels, NodeProducts: []string{"NVIDIA-GB10"}},
		},
		{
			name:   "heterogeneous nodes are sorted and joined",
			slices: slices("station-1", "spark-1", "cpu-1"),
			want: pylonv1alpha1.GPUStatus{
				Product:      "NVIDIA-GB10,NVIDIA-GB300",
				Source:       pylonv1alpha1.GPUSourceNodeLabels,
				NodeProducts: []string{"NVIDIA-GB10", "NVIDIA-GB300"},
			},
		},
		{
			name: "no endpoints",
			want: pylonv1alpha1.GPUStatus{Source: pylonv1alpha1.GPUSourceUnknown},
		},
		{
			name:   "endpoints not scheduled yet",
			slices: slices("", ""),
			want:   pylonv1alpha1.GPUStatus{Source: pylonv1alpha1.GPUSourceUnknown},
		},
		{
			name:   "node without the label",
			slices: slices("cpu-1"),
			want:   pylonv1alpha1.GPUStatus{Source: pylonv1alpha1.GPUSourceUnknown},
		},
		{
			name:   "node that no longer exists is skipped",
			slices: slices("gone", "spark-2"),
			want:   pylonv1alpha1.GPUStatus{Product: "NVIDIA-GB10", Source: pylonv1alpha1.GPUSourceNodeLabels, NodeProducts: []string{"NVIDIA-GB10"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(nodes...).Build()
			got, err := (&Resolver{Reader: c}).Resolve(context.Background(), endpoint(tt.specProduct), tt.slices)
			require.NoError(t, err)
			assert.Equal(t, tt.want, *got)
		})
	}
}

func TestResolveReadError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("cache not synced")
		},
	}).Build()
	_, err := (&Resolver{Reader: c}).Resolve(context.Background(), endpoint(""), slices("spark-1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `reading node "spark-1"`)
}

func TestResolveReadsMetadataOnly(t *testing.T) {
	var got []client.Object
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(node("spark-1", "NVIDIA-GB10")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				got = append(got, obj)
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()
	_, err := (&Resolver{Reader: c}).Resolve(context.Background(), endpoint(""), slices("spark-1"))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.IsType(t, &metav1.PartialObjectMetadata{}, got[0])
}

func TestNodeNames(t *testing.T) {
	assert.Empty(t, NodeNames(nil))
	s := append(slices("b", "a", ""), slices("a", "c")...)
	assert.Equal(t, []string{"a", "b", "c"}, NodeNames(s))
}

func TestNodeMetadata(t *testing.T) {
	n := NodeMetadata()
	assert.Equal(t, "Node", n.GetObjectKind().GroupVersionKind().Kind)
	assert.Equal(t, "v1", n.APIVersion)
}
