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
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
)

// transportStep runs the transport reconciler, records its State on the
// ReconcileContext and sets the fast negatives of TransportReady and
// Registered. It owns the transport Deployments and watches the sources and
// copies of the cluster credential and trust bundle.
type transportStep struct {
	reconciler *transport.Reconciler
	reader     client.Reader
}

func (s *transportStep) Name() string { return StepTransport }

// Run emits a Warning Event when a Problem first appears or changes; the
// TransportReady message carries the Problem while it lasts.
func (s *transportStep) Run(ctx context.Context, rc *ReconcileContext) error {
	st, err := s.reconciler.Reconcile(ctx, rc.Endpoint)
	if err != nil {
		return err
	}
	rc.Transport = &st
	if st.Problem != nil {
		prev := rc.Condition(pylonv1alpha1.ConditionTransportReady)
		if prev == nil || !strings.Contains(prev.Message, st.Problem.Message) {
			rc.Event(corev1.EventTypeWarning, st.Problem.EventReason, "%s", st.Problem.Message)
		}
	}
	if verdict, ok := st.FastNegative(); ok {
		rc.SetTransportReady(metav1.ConditionFalse, verdict.TransportReady, verdict.Message)
		rc.SetRegistered(metav1.ConditionFalse, verdict.Registered, verdict.Message)
	}
	return nil
}

// SetupWithManager owns the transport Deployments, so a change of their
// status requeues the endpoint. It watches the credential Secret and, when
// configured, the trust bundle ConfigMap; config.ManagerOptions narrows both
// caches to those names.
func (s *transportStep) SetupWithManager(_ context.Context, _ ctrl.Manager, b *builder.Builder) error {
	cfg := s.reconciler.Config()
	b.Owns(&appsv1.Deployment{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(s.endpointsForReplicated(cfg.ClusterCredentialSecret)))
	if cfg.TrustBundleConfigMap != "" {
		b.Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(s.endpointsForReplicated(cfg.TrustBundleConfigMap)))
	}
	return nil
}

// endpointsForReplicated maps the object called name to InferenceEndpoints: a
// change of the source in the operator namespace requeues every endpoint, and
// a change of a copy the operator manages requeues the endpoints in the
// copy's namespace, which restore it.
func (s *transportStep) endpointsForReplicated(name string) handler.MapFunc {
	operatorNamespace := s.reconciler.Config().OperatorNamespace
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		if obj.GetName() != name {
			return nil
		}
		switch {
		case obj.GetNamespace() == operatorNamespace:
			return s.listEndpoints(ctx)
		case obj.GetLabels()[config.ManagedByLabel] == config.ManagedBy:
			return s.listEndpoints(ctx, client.InNamespace(obj.GetNamespace()))
		}
		return nil
	}
}

func (s *transportStep) listEndpoints(ctx context.Context, opts ...client.ListOption) []reconcile.Request {
	var list pylonv1alpha1.InferenceEndpointList
	if err := s.reader.List(ctx, &list, opts...); err != nil {
		log.FromContext(ctx).Error(err, "Listing InferenceEndpoints for a replicated object failed")
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return out
}
