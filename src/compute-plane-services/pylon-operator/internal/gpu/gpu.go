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

// Package gpu resolves the effective GPU type of an InferenceEndpoint from its
// spec or from the nvidia.com/gpu.product label of the nodes behind its
// Service. It reads Node metadata only, never pods or pod templates.
package gpu

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
)

// ProductLabel is the node label GPU Feature Discovery writes.
const ProductLabel = "nvidia.com/gpu.product"

// Resolver derives status.gpu. Reader must serve Node metadata; the
// controller's cache does so through a metadata-only Node informer.
type Resolver struct {
	Reader client.Reader
}

// Resolve returns the effective GPU status of ep. slices are the Service's
// EndpointSlices; endpoints count whether ready or not. spec.gpu.product wins
// over the derived value, and the node products are recorded either way.
func (r *Resolver) Resolve(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint, slices []discoveryv1.EndpointSlice) (*pylonv1alpha1.GPUStatus, error) {
	products, err := r.nodeProducts(ctx, NodeNames(slices))
	if err != nil {
		return nil, err
	}
	status := &pylonv1alpha1.GPUStatus{NodeProducts: products}
	switch {
	case ep.Spec.GPU != nil && ep.Spec.GPU.Product != "":
		status.Product = ep.Spec.GPU.Product
		status.Source = pylonv1alpha1.GPUSourceSpec
	case len(products) > 0:
		status.Product = strings.Join(products, ",")
		status.Source = pylonv1alpha1.GPUSourceNodeLabels
	default:
		status.Source = pylonv1alpha1.GPUSourceUnknown
	}
	return status, nil
}

func (r *Resolver) nodeProducts(ctx context.Context, nodes []string) ([]string, error) {
	seen := map[string]struct{}{}
	for _, name := range nodes {
		node := NodeMetadata()
		if err := r.Reader.Get(ctx, client.ObjectKey{Name: name}, node); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("reading node %q: %w", name, err)
		}
		if product := node.GetLabels()[ProductLabel]; product != "" {
			seen[product] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// NodeMetadata returns an empty metadata-only Node, the object type the
// controller watches and reads Nodes as.
func NodeMetadata() *metav1.PartialObjectMetadata {
	node := &metav1.PartialObjectMetadata{}
	node.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))
	return node
}

// NodeNames returns the distinct, sorted node names of the endpoints in
// slices, ready or not.
func NodeNames(slices []discoveryv1.EndpointSlice) []string {
	seen := map[string]struct{}{}
	for i := range slices {
		for _, e := range slices[i].Endpoints {
			if e.NodeName != nil && *e.NodeName != "" {
				seen[*e.NodeName] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
