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

package transport

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
)

// reconcileService creates the pool export Service of ep or brings an
// existing one back to the rendered form. A Service of the same name that ep
// does not control is left alone and reported as a Problem.
func (r *Reconciler) reconcileService(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint) (*Problem, error) {
	desired := Service(ep)
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("service", desired.Name, "pool", PoolName(ep)))

	existing := &corev1.Service{}
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("reading Service %q: %w", desired.Name, err)
		}
		// An AlreadyExists here means the cache has not seen the Service
		// yet; the error requeues the endpoint, which then reads it.
		if err := r.client.Create(ctx, desired); err != nil {
			return nil, fmt.Errorf("creating Service %q: %w", desired.Name, err)
		}
		log.FromContext(ctx).Info("Created the pool export Service")
		return nil, nil
	}
	if !metav1.IsControlledBy(existing, ep) {
		return &Problem{
			EventReason: EventReasonTransportObjectConflict,
			Message: fmt.Sprintf("Service %q exists and is not controlled by this InferenceEndpoint; the transport is not exported to the Global Discovery Service",
				desired.Name),
		}, nil
	}
	return nil, r.updateService(ctx, existing, desired)
}

// updateService writes the parts of desired that differ from existing: the
// rendered labels, the owned annotations, the type, the selector and the
// ports. Other labels and annotations and the fields the API server fills
// in, such as the cluster IP, are left alone.
func (r *Reconciler) updateService(ctx context.Context, existing, desired *corev1.Service) error {
	s := existing.DeepCopy()
	var changed []string
	if setMissing(&s.Labels, desired.Labels) {
		changed = append(changed, "labels")
	}
	annotationsChanged := setMissing(&s.Annotations, desired.Annotations)
	for _, k := range ownedServiceAnnotations {
		if _, ok := desired.Annotations[k]; !ok {
			if _, ok := s.Annotations[k]; ok {
				delete(s.Annotations, k)
				annotationsChanged = true
			}
		}
	}
	if annotationsChanged {
		changed = append(changed, "annotations")
	}
	if s.Spec.Type != desired.Spec.Type {
		s.Spec.Type = desired.Spec.Type
		changed = append(changed, "type")
	}
	if !equality.Semantic.DeepEqual(s.Spec.Selector, desired.Spec.Selector) {
		s.Spec.Selector = desired.Spec.Selector
		changed = append(changed, "selector")
	}
	if !slices.EqualFunc(s.Spec.Ports, desired.Spec.Ports, sameServicePort) {
		s.Spec.Ports = desired.Spec.Ports
		changed = append(changed, "ports")
	}
	if len(changed) == 0 {
		return nil
	}
	// A merge patch without resourceVersion: it writes only fields the
	// operator owns.
	if err := r.client.Patch(ctx, s, client.MergeFrom(existing)); err != nil {
		return fmt.Errorf("updating Service %q: %w", existing.Name, err)
	}
	log.FromContext(ctx).Info("Updated the pool export Service", "changed", changed)
	return nil
}

// sameServicePort compares the fields Service renders. NodePort and
// AppProtocol are not rendered and so not compared.
func sameServicePort(a, b corev1.ServicePort) bool {
	return a.Name == b.Name && a.Protocol == b.Protocol && a.Port == b.Port && a.TargetPort == b.TargetPort
}

// setMissing sets every entry of want in *m that is missing or different,
// creating the map when needed, and reports whether it changed anything.
func setMissing(m *map[string]string, want map[string]string) bool {
	changed := false
	for k, v := range want {
		if cur, ok := (*m)[k]; ok && cur == v {
			continue
		}
		if *m == nil {
			*m = map[string]string{}
		}
		(*m)[k] = v
		changed = true
	}
	return changed
}
