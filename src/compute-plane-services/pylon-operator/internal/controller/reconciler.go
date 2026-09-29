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

// Package controller holds the InferenceEndpoint reconciler. A reconcile runs
// an ordered list of steps on a shared context and patches status once.
package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/metrics"
)

// conflictRetryDelay is the requeue delay after an optimistic-lock conflict
// on the status patch.
const conflictRetryDelay = time.Second

// Reconciler reconciles InferenceEndpoint objects.
type Reconciler struct {
	client   client.Client
	recorder record.EventRecorder
	config   config.Config
	metrics  *metrics.Metrics
	steps    []Step
}

// Options configures NewReconciler.
type Options struct {
	// Client reads objects and patches status.
	Client client.Client
	// Recorder emits Kubernetes Events.
	Recorder record.EventRecorder
	// Config is the operator configuration.
	Config config.Config
	// Metrics records endpoint condition gauges. Nil disables recording.
	Metrics *metrics.Metrics
	// Steps is the reconcile pipeline, normally DefaultSteps.
	Steps []Step
}

// NewReconciler returns a Reconciler.
func NewReconciler(o Options) *Reconciler {
	return &Reconciler{
		client:   o.Client,
		recorder: o.Recorder,
		config:   o.Config,
		metrics:  o.Metrics,
		steps:    o.Steps,
	}
}

// +kubebuilder:rbac:groups=pylon.nvidia.com,resources=inferenceendpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=pylon.nvidia.com,resources=inferenceendpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=pylon.nvidia.com,resources=inferenceendpoints/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch

// The finalizers rule lets the transport Deployment's ownerReference set
// blockOwnerDeletion where the OwnerReferencesPermissionEnforcement admission
// plugin is enabled. Secrets and ConfigMaps are cached only under the
// configured names, and Pods only with the managed-by label of transport
// pods; see config.ManagerOptions.

// Reconcile runs the steps for one InferenceEndpoint, patches its status and
// emits Events for condition transitions. It requeues after the probe
// interval, or sooner when a step asks for it.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("clusterId", r.config.ClusterID)
	ctx = log.IntoContext(ctx, logger)

	ep := &pylonv1alpha1.InferenceEndpoint{}
	if err := r.client.Get(ctx, req.NamespacedName, ep); err != nil {
		if apierrors.IsNotFound(err) {
			r.forget(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("reading InferenceEndpoint: %w", err)
	}
	if !ep.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("model", ep.Spec.ModelName)
	ctx = log.IntoContext(ctx, logger)

	original := ep.DeepCopy()
	rc := newReconcileContext(ep, r.config)
	stepErr := r.runSteps(ctx, rc)
	if stepErr == nil {
		ep.Status.ObservedGeneration = ep.Generation
	}

	if err := r.patchStatus(ctx, original, ep); err != nil {
		switch {
		case apierrors.IsConflict(err):
			logger.V(1).Info("Status patch conflicted; retrying")
			return ctrl.Result{RequeueAfter: conflictRetryDelay}, nil
		case apierrors.IsNotFound(err):
			r.forget(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, errors.Join(stepErr, fmt.Errorf("patching status: %w", err))
	}

	r.emitConditionEvents(ep, original.Status.Conditions)
	for _, e := range rc.events {
		r.recorder.Event(ep, e.eventType, e.reason, e.message)
	}
	r.metrics.SetEndpointConditions(req.String(), ep.Status.Conditions)
	if rc.Transport != nil {
		r.metrics.SetTransportReplicasReady(req.String(), rc.Transport.ReadyReplicas)
	}

	if stepErr != nil {
		return ctrl.Result{}, stepErr
	}
	return ctrl.Result{RequeueAfter: rc.requeueAfter}, nil
}

// forget drops the metrics and the steps' in-memory state of an endpoint
// that no longer exists.
func (r *Reconciler) forget(key types.NamespacedName) {
	r.metrics.ForgetEndpoint(key.String())
	for _, s := range r.steps {
		if f, ok := s.(Forgetter); ok {
			f.Forget(key)
		}
	}
}

func (r *Reconciler) runSteps(ctx context.Context, rc *ReconcileContext) error {
	for _, s := range r.steps {
		if err := s.Run(ctx, rc); err != nil {
			return fmt.Errorf("step %s: %w", s.Name(), err)
		}
	}
	return nil
}

// patchStatus writes status through the status subresource. The merge patch
// carries the full conditions list, so it is sent with an optimistic lock:
// a concurrent writer causes a conflict and a retry instead of a lost update.
func (r *Reconciler) patchStatus(ctx context.Context, original, ep *pylonv1alpha1.InferenceEndpoint) error {
	if equality.Semantic.DeepEqual(original.Status, ep.Status) {
		return nil
	}
	patch := client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})
	return r.client.Status().Patch(ctx, ep, patch)
}

// emitConditionEvents emits a Normal Event when a condition becomes True and
// a Warning Event when it becomes False, including a change of reason while
// False. Transitions to Unknown emit nothing.
func (r *Reconciler) emitConditionEvents(ep *pylonv1alpha1.InferenceEndpoint, before []metav1.Condition) {
	for _, c := range ep.Status.Conditions {
		old := meta.FindStatusCondition(before, c.Type)
		if old != nil && old.Status == c.Status && old.Reason == c.Reason {
			continue
		}
		var eventType string
		switch c.Status {
		case metav1.ConditionTrue:
			eventType = corev1.EventTypeNormal
		case metav1.ConditionFalse:
			eventType = corev1.EventTypeWarning
		default:
			continue
		}
		r.recorder.Eventf(ep, eventType, c.Reason, "%s is %s: %s", c.Type, c.Status, c.Message)
	}
}
