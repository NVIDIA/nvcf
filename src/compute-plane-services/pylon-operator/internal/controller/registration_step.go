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

	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/registration"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
)

// registrationStep is the registration observer: TransportReady,
// Registered, status.registration and status.servers from the transport
// pods' metrics.
type registrationStep struct {
	observer *registration.Observer
}

func (s *registrationStep) Name() string { return StepRegistrationObserver }

// Run leaves both conditions to a fast negative of the transport step and
// then reports no connected router and no servers without scraping. Otherwise
// it scrapes the transport pods and asks for the next reconcile within the
// scrape interval, with jitter.
func (s *registrationStep) Run(ctx context.Context, rc *ReconcileContext) error {
	status := &rc.Endpoint.Status
	if _, ok := rc.TransportFastNegative(); ok {
		status.Registration = registration.Idle(status.Registration, rc.Config.ClusterID)
		status.Servers = nil
		return nil
	}
	var st transport.State
	if rc.Transport != nil {
		st = *rc.Transport
	}
	res, err := s.observer.Observe(ctx, rc.Endpoint, st, rc.Config.ClusterID)
	if err != nil {
		return err
	}
	rc.SetTransportReady(res.TransportReady.Status, res.TransportReady.Reason, res.TransportReady.Message)
	rc.SetRegistered(res.Registered.Status, res.Registered.Reason, res.Registered.Message)
	status.Registration = &res.Registration
	status.Servers = res.Servers
	if res.Rejected != "" {
		rc.Event(corev1.EventTypeWarning, registration.EventReasonRegistrationStreamRejected, "%s", res.Rejected)
	}
	rc.RequeueAfter(s.observer.NextScrape(rc.Config.ScrapeInterval))
	return nil
}

// Forget drops the observer's memory of a deleted endpoint.
func (s *registrationStep) Forget(key types.NamespacedName) {
	s.observer.Forget(key)
}
