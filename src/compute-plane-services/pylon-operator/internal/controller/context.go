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
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
)

// Step is one ordered phase of a reconcile. Steps share a ReconcileContext:
// earlier steps fill in facts that later steps read, and every step writes
// only the status fields it owns. The reconciler runs the steps in order,
// stops at the first error, and then patches status once.
//
// The transport reconciler and the registration observer plug in as further
// steps; see DefaultSteps.
type Step interface {
	// Name identifies the step in logs and errors.
	Name() string
	// Run performs the step. It must not write the spec.
	Run(ctx context.Context, rc *ReconcileContext) error
}

// ManagerSetup is implemented by steps that need field indexes or watches of
// their own, for example a step that owns Deployments. SetupWithManager is
// called before the controller is built; b is the controller's builder.
type ManagerSetup interface {
	SetupWithManager(ctx context.Context, mgr ctrl.Manager, b *builder.Builder) error
}

// Forgetter is implemented by steps that keep per-endpoint state in memory.
// The reconciler calls Forget when the endpoint no longer exists.
type Forgetter interface {
	Forget(key types.NamespacedName)
}

// Backend is the Service named by the endpoint and its EndpointSlices.
type Backend struct {
	// Service is nil when the Service does not exist.
	Service *corev1.Service
	// EndpointSlices carry the kubernetes.io/service-name label of Service.
	EndpointSlices []discoveryv1.EndpointSlice
}

// ReconcileContext is the state shared by the steps of one reconcile.
type ReconcileContext struct {
	// Endpoint is the working copy. Steps write Endpoint.Status only.
	Endpoint *pylonv1alpha1.InferenceEndpoint
	// Backend is filled in by the backend step.
	Backend Backend
	// Transport is filled in by the transport step: the transport
	// Deployment's name and pod labels, its desired and ready replica counts,
	// whether it is scaled to zero, and the problem, if any, that kept it
	// from being created or updated. Nil when the transport step did not run
	// or failed. The registration observer reads it; see
	// TransportFastNegative.
	Transport *transport.State
	// Config is the operator configuration.
	Config config.Config

	requeueAfter time.Duration
	events       []pendingEvent
}

type pendingEvent struct {
	eventType string
	reason    string
	message   string
}

func newReconcileContext(ep *pylonv1alpha1.InferenceEndpoint, cfg config.Config) *ReconcileContext {
	return &ReconcileContext{Endpoint: ep, Config: cfg, requeueAfter: cfg.ProbeInterval}
}

// RequeueAfter asks for the next reconcile no later than d from now. The
// shortest request of all steps wins; the default is the probe interval.
func (rc *ReconcileContext) RequeueAfter(d time.Duration) {
	if d > 0 && (rc.requeueAfter <= 0 || d < rc.requeueAfter) {
		rc.requeueAfter = d
	}
}

// Event queues a Kubernetes Event on the endpoint. Events are emitted only
// after the status patch succeeds, so a failed reconcile does not repeat them.
func (rc *ReconcileContext) Event(eventType, reason, messageFmt string, args ...any) {
	rc.events = append(rc.events, pendingEvent{eventType: eventType, reason: reason, message: fmt.Sprintf(messageFmt, args...)})
}

// SetReady sets the Ready condition.
func (rc *ReconcileContext) SetReady(status metav1.ConditionStatus, reason pylonv1alpha1.ReadyReason, message string) {
	rc.setCondition(pylonv1alpha1.ConditionReady, status, string(reason), message)
}

// SetTransportReady sets the TransportReady condition.
func (rc *ReconcileContext) SetTransportReady(status metav1.ConditionStatus, reason pylonv1alpha1.TransportReadyReason, message string) {
	rc.setCondition(pylonv1alpha1.ConditionTransportReady, status, string(reason), message)
}

// SetRegistered sets the Registered condition.
func (rc *ReconcileContext) SetRegistered(status metav1.ConditionStatus, reason pylonv1alpha1.RegisteredReason, message string) {
	rc.setCondition(pylonv1alpha1.ConditionRegistered, status, string(reason), message)
}

// TransportFastNegative reports the TransportReady and Registered verdict the
// transport step reached without scraping Pylon: ScaledToZero, or
// TransportPodsNotRunning. When ok is true the transport step has already set
// both conditions and later steps must leave them; when it is false the
// registration observer owns them.
func (rc *ReconcileContext) TransportFastNegative() (verdict transport.FastNegative, ok bool) {
	if rc.Transport == nil {
		return transport.FastNegative{}, false
	}
	return rc.Transport.FastNegative()
}

// Condition returns the current value of a condition, or nil.
func (rc *ReconcileContext) Condition(t pylonv1alpha1.ConditionType) *metav1.Condition {
	return meta.FindStatusCondition(rc.Endpoint.Status.Conditions, string(t))
}

// setCondition updates one condition and leaves every other condition in the
// list untouched. meta.SetStatusCondition keeps lastTransitionTime unless the
// status changes.
func (rc *ReconcileContext) setCondition(t pylonv1alpha1.ConditionType, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&rc.Endpoint.Status.Conditions, metav1.Condition{
		Type:               string(t),
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: rc.Endpoint.Generation,
	})
}
