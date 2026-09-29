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

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/gpu"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/prober"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/registration"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
)

// Step names, in the order DefaultSteps runs them.
const (
	StepBackend              = "backend"
	StepHealth               = "health"
	StepGPU                  = "gpu"
	StepTransport            = "transport"
	StepRegistrationObserver = "registration-observer"
)

// EventReasonGPUProductChanged is the Event reason when the effective GPU
// product changes.
const EventReasonGPUProductChanged = "GPUProductChanged"

// DefaultSteps is the reconcile pipeline:
//
//  1. backend: read the Service and its EndpointSlices.
//  2. health: compute Ready.
//  3. gpu: compute status.gpu.
//  4. transport: replicate the cluster credential and trust bundle, create or
//     update the transport Deployment from Ready and the effective GPU
//     product, record ReconcileContext.Transport, and set the fast negatives
//     of TransportReady and Registered (ScaledToZero, TransportPodsNotRunning).
//  5. registration-observer: scrape the transport pods' metrics and compute
//     TransportReady, Registered, status.registration and status.servers
//     when step 4 set no fast negative.
//
// reader lists InferenceEndpoints for the transport step's watches.
func DefaultSteps(reader client.Reader, p *prober.Prober, g *gpu.Resolver, t *transport.Reconciler, o *registration.Observer) []Step {
	return []Step{
		&backendStep{reader: reader},
		&healthStep{prober: p},
		&gpuStep{resolver: g},
		&transportStep{reconciler: t, reader: reader},
		&registrationStep{observer: o},
	}
}

// backendStep reads the Service named in the spec and its EndpointSlices.
type backendStep struct {
	reader client.Reader
}

func (s *backendStep) Name() string { return StepBackend }

func (s *backendStep) Run(ctx context.Context, rc *ReconcileContext) error {
	ep := rc.Endpoint
	svc := &corev1.Service{}
	err := s.reader.Get(ctx, client.ObjectKey{Namespace: ep.Namespace, Name: ep.Spec.Service.Name}, svc)
	switch {
	case apierrors.IsNotFound(err):
		rc.Backend = Backend{}
		return nil
	case err != nil:
		return fmt.Errorf("reading Service %q: %w", ep.Spec.Service.Name, err)
	}
	var slices discoveryv1.EndpointSliceList
	if err := s.reader.List(ctx, &slices, client.InNamespace(ep.Namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: svc.Name}); err != nil {
		return fmt.Errorf("listing EndpointSlices of Service %q: %w", svc.Name, err)
	}
	rc.Backend = Backend{Service: svc, EndpointSlices: slices.Items}
	return nil
}

// healthStep computes Ready with the prober.
type healthStep struct {
	prober *prober.Prober
}

func (s *healthStep) Name() string { return StepHealth }

func (s *healthStep) Run(ctx context.Context, rc *ReconcileContext) error {
	res := s.prober.Probe(ctx, rc.Endpoint, rc.Backend.Service, rc.Backend.EndpointSlices)
	rc.SetReady(res.Status, res.Reason, res.Message)
	return nil
}

// gpuStep computes status.gpu. It never affects conditions: a failed node
// read keeps the previous value and does not fail the reconcile.
type gpuStep struct {
	resolver *gpu.Resolver
}

func (s *gpuStep) Name() string { return StepGPU }

func (s *gpuStep) Run(ctx context.Context, rc *ReconcileContext) error {
	status, err := s.resolver.Resolve(ctx, rc.Endpoint, rc.Backend.EndpointSlices)
	if err != nil {
		log.FromContext(ctx).Error(err, "Resolving the GPU product failed; keeping the previous value")
		return nil
	}
	previous := ""
	if rc.Endpoint.Status.GPU != nil {
		previous = rc.Endpoint.Status.GPU.Product
	}
	if status.Product != previous {
		rc.Event(corev1.EventTypeNormal, EventReasonGPUProductChanged,
			"Effective GPU product changed from %q to %q (source %s)", previous, status.Product, status.Source)
	}
	rc.Endpoint.Status.GPU = status
	return nil
}
