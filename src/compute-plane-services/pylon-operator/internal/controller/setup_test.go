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

package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/gpu"
)

func conflict() error {
	return apierrors.NewConflict(schema.GroupResource{Group: "pylon.nvidia.com", Resource: "inferenceendpoints"}, testName, fmt.Errorf("modified"))
}

func notFound() error {
	return apierrors.NewNotFound(schema.GroupResource{Group: "pylon.nvidia.com", Resource: "inferenceendpoints"}, testName)
}

func endpointNamed(namespace, name, svc string) *pylonv1alpha1.InferenceEndpoint {
	ep := inferenceEndpoint(1)
	ep.Namespace, ep.Name, ep.Spec.Service.Name = namespace, name, svc
	return ep
}

func requests(keys ...string) []reconcile.Request {
	out := make([]reconcile.Request, 0, len(keys))
	for _, k := range keys {
		ns, name, _ := strings.Cut(k, "/")
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	}
	return out
}

func mappingObjects() []client.Object {
	return []client.Object{
		endpointNamed("models", "a", "svc-1"),
		endpointNamed("models", "b", "svc-1"),
		endpointNamed("models", "c", "svc-2"),
		endpointNamed("team-b", "d", "svc-1"),
		endpointSlice("models", "svc-1-x", "svc-1", "spark-1", true),
		endpointSlice("models", "svc-1-y", "svc-1", "spark-1", false),
		endpointSlice("team-b", "svc-1-z", "svc-1", "spark-1", true),
		endpointSlice("models", "svc-2-x", "svc-2", "spark-2", true),
		endpointSlice("models", "orphan", "", "spark-1", true),
	}
}

func TestEndpointsForService(t *testing.T) {
	f := newFixture(t, withObjects(mappingObjects()...))
	got := f.reconciler.EndpointsForService(context.Background(), service("models", "svc-1"))
	assert.ElementsMatch(t, requests("models/a", "models/b"), got)
	assert.Empty(t, f.reconciler.EndpointsForService(context.Background(), service("models", "unrelated")))
}

func TestEndpointsForEndpointSlice(t *testing.T) {
	f := newFixture(t, withObjects(mappingObjects()...))
	got := f.reconciler.EndpointsForEndpointSlice(context.Background(), endpointSlice("team-b", "s", "svc-1", "n", true))
	assert.ElementsMatch(t, requests("team-b/d"), got)
	assert.Nil(t, f.reconciler.EndpointsForEndpointSlice(context.Background(), endpointSlice("models", "s", "", "n", true)))
}

func TestEndpointsForNode(t *testing.T) {
	f := newFixture(t, withObjects(mappingObjects()...))
	got := f.reconciler.EndpointsForNode(context.Background(), gpuNode("spark-1"))
	assert.ElementsMatch(t, requests("models/a", "models/b", "team-b/d"), got)
	got = f.reconciler.EndpointsForNode(context.Background(), gpuNode("spark-2"))
	assert.ElementsMatch(t, requests("models/c"), got)
	assert.Empty(t, f.reconciler.EndpointsForNode(context.Background(), gpuNode("idle")))
}

func gpuNode(name string) *metav1.PartialObjectMetadata {
	n := gpu.NodeMetadata()
	n.Name = name
	return n
}

func TestMappingListErrors(t *testing.T) {
	f := newFixture(t, withObjects(mappingObjects()...), withInterceptors(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return fmt.Errorf("cache not synced")
		},
	}))
	ctx := context.Background()
	assert.Nil(t, f.reconciler.EndpointsForService(ctx, service("models", "svc-1")))
	assert.Nil(t, f.reconciler.EndpointsForEndpointSlice(ctx, endpointSlice("models", "s", "svc-1", "n", true)))
	assert.Nil(t, f.reconciler.EndpointsForNode(ctx, gpuNode("spark-1")))
}

func TestGPUProductLabelChanged(t *testing.T) {
	p := GPUProductLabelChanged()
	labelled := func(v string) client.Object {
		n := gpuNode("spark-1")
		if v != "" {
			n.Labels = map[string]string{gpu.ProductLabel: v, "other": "x"}
		}
		return n
	}
	assert.False(t, p.Create(event.CreateEvent{Object: labelled("A")}))
	assert.False(t, p.Delete(event.DeleteEvent{Object: labelled("A")}))
	assert.False(t, p.Generic(event.GenericEvent{Object: labelled("A")}))
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: labelled("A"), ObjectNew: labelled("A")}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: labelled("A"), ObjectNew: labelled("B")}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: labelled(""), ObjectNew: labelled("B")}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: labelled("A"), ObjectNew: labelled("")}))
	assert.False(t, p.Update(event.UpdateEvent{ObjectNew: labelled("A")}))
}

func TestIndexers(t *testing.T) {
	assert.Equal(t, []string{"svc-1"}, IndexServiceName(endpointNamed("models", "a", "svc-1")))
	assert.Nil(t, IndexServiceName(endpointNamed("models", "a", "")))
	assert.Nil(t, IndexServiceName(&corev1.Service{}))

	assert.Equal(t, []string{"spark-1"}, IndexEndpointSliceNodes(endpointSlice("models", "s", "svc", "spark-1", false)))
	assert.Nil(t, IndexEndpointSliceNodes(&corev1.Service{}))
}

func TestNewScheme(t *testing.T) {
	s, err := NewScheme()
	require.NoError(t, err)
	for _, obj := range []client.Object{&pylonv1alpha1.InferenceEndpoint{}, &discoveryv1.EndpointSlice{}, &corev1.Node{}} {
		_, _, err := s.ObjectKinds(obj)
		assert.NoError(t, err)
	}
}

func TestDefaultStepsOrder(t *testing.T) {
	var names []string
	for _, s := range DefaultSteps(nil, nil, nil, nil, nil) {
		names = append(names, s.Name())
	}
	assert.Equal(t, []string{StepBackend, StepHealth, StepGPU, StepTransport, StepRegistrationObserver}, names)
}
