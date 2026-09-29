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

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/gpu"
)

const (
	// ControllerName names the controller in logs and metrics.
	ControllerName = "inferenceendpoint"

	// ServiceNameIndex indexes InferenceEndpoints by spec.service.name.
	ServiceNameIndex = "spec.service.name"
	// EndpointSliceNodeIndex indexes EndpointSlices by endpoints[].nodeName.
	EndpointSliceNodeIndex = "endpoints.nodeName"
)

// NewScheme returns a scheme with the client-go types and pylon v1alpha1.
func NewScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := pylonv1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// IndexServiceName is the ServiceNameIndex extractor.
func IndexServiceName(obj client.Object) []string {
	ep, ok := obj.(*pylonv1alpha1.InferenceEndpoint)
	if !ok || ep.Spec.Service.Name == "" {
		return nil
	}
	return []string{ep.Spec.Service.Name}
}

// IndexEndpointSliceNodes is the EndpointSliceNodeIndex extractor.
func IndexEndpointSliceNodes(obj client.Object) []string {
	slice, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return nil
	}
	return gpu.NodeNames([]discoveryv1.EndpointSlice{*slice})
}

// SetupWithManager registers the field indexes and watches and adds the
// controller to mgr. Steps implementing ManagerSetup add their own.
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	indexer := mgr.GetFieldIndexer()
	if err := indexer.IndexField(ctx, &pylonv1alpha1.InferenceEndpoint{}, ServiceNameIndex, IndexServiceName); err != nil {
		return fmt.Errorf("indexing %s: %w", ServiceNameIndex, err)
	}
	if err := indexer.IndexField(ctx, &discoveryv1.EndpointSlice{}, EndpointSliceNodeIndex, IndexEndpointSliceNodes); err != nil {
		return fmt.Errorf("indexing %s: %w", EndpointSliceNodeIndex, err)
	}

	b := ctrl.NewControllerManagedBy(mgr).
		Named(ControllerName).
		For(&pylonv1alpha1.InferenceEndpoint{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(r.EndpointsForService)).
		Watches(&discoveryv1.EndpointSlice{}, handler.EnqueueRequestsFromMapFunc(r.EndpointsForEndpointSlice)).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.EndpointsForNode),
			builder.OnlyMetadata, builder.WithPredicates(GPUProductLabelChanged()))
	for _, s := range r.steps {
		if ms, ok := s.(ManagerSetup); ok {
			if err := ms.SetupWithManager(ctx, mgr, b); err != nil {
				return fmt.Errorf("setting up step %s: %w", s.Name(), err)
			}
		}
	}
	return b.Complete(r)
}

// EndpointsForService maps a Service to the InferenceEndpoints naming it.
func (r *Reconciler) EndpointsForService(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.endpointsReferencing(ctx, obj.GetNamespace(), obj.GetName())
}

// EndpointsForEndpointSlice maps an EndpointSlice to the InferenceEndpoints
// naming its Service.
func (r *Reconciler) EndpointsForEndpointSlice(ctx context.Context, obj client.Object) []reconcile.Request {
	svc := obj.GetLabels()[discoveryv1.LabelServiceName]
	if svc == "" {
		return nil
	}
	return r.endpointsReferencing(ctx, obj.GetNamespace(), svc)
}

// EndpointsForNode maps a Node to the InferenceEndpoints whose Services have
// an endpoint on it.
func (r *Reconciler) EndpointsForNode(ctx context.Context, obj client.Object) []reconcile.Request {
	var slices discoveryv1.EndpointSliceList
	if err := r.client.List(ctx, &slices, client.MatchingFields{EndpointSliceNodeIndex: obj.GetName()}); err != nil {
		log.FromContext(ctx).Error(err, "Listing EndpointSlices for a node failed", "node", obj.GetName())
		return nil
	}
	seen := map[types.NamespacedName]struct{}{}
	var out []reconcile.Request
	for i := range slices.Items {
		svc := slices.Items[i].Labels[discoveryv1.LabelServiceName]
		if svc == "" {
			continue
		}
		key := types.NamespacedName{Namespace: slices.Items[i].Namespace, Name: svc}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, r.endpointsReferencing(ctx, key.Namespace, key.Name)...)
	}
	return out
}

func (r *Reconciler) endpointsReferencing(ctx context.Context, namespace, service string) []reconcile.Request {
	var list pylonv1alpha1.InferenceEndpointList
	if err := r.client.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{ServiceNameIndex: service}); err != nil {
		log.FromContext(ctx).Error(err, "Listing InferenceEndpoints for a Service failed", "namespace", namespace, "service", service)
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return out
}

// GPUProductLabelChanged passes Node updates that change the
// nvidia.com/gpu.product label. Node creation and deletion move endpoints,
// which the EndpointSlice watch already reports.
func GPUProductLabelChanged() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			return e.ObjectOld.GetLabels()[gpu.ProductLabel] != e.ObjectNew.GetLabels()[gpu.ProductLabel]
		},
	}
}
