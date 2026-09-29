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

// Package transport reconciles the transport of an InferenceEndpoint: one
// Pylon Deployment in the endpoint's namespace, controlled by the endpoint so
// that deleting it garbage-collects the Deployment, and the cluster
// credential Secret (tier T0) and router trust bundle ConfigMap replicated
// from the operator namespace into the endpoint's namespace for the pods to
// mount.
package transport

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
)

// Event reasons of a Problem.
const (
	// EventReasonClusterCredentialMissing: the cluster credential Secret is
	// missing from the operator namespace or has no cluster-token key.
	EventReasonClusterCredentialMissing = "ClusterCredentialMissing"
	// EventReasonTrustBundleMissing: the trust bundle ConfigMap is missing
	// from the operator namespace or has no ca.crt key.
	EventReasonTrustBundleMissing = "TrustBundleMissing"
	// EventReasonTransportObjectConflict: an object the transport needs
	// exists under the same name but is not the operator's.
	EventReasonTransportObjectConflict = "TransportObjectConflict"
)

// Problem is why the transport step could not create or update the
// Deployment.
type Problem struct {
	// EventReason is the reason of the Warning Event the step emits.
	EventReason string
	// Message names the object at fault. It never contains credential
	// material.
	Message string
}

// State is what the transport step wrote and observed in one reconcile. The
// registration observer reads it from the ReconcileContext.
type State struct {
	// DeploymentName names the transport Deployment in the endpoint's
	// namespace, whether or not it exists.
	DeploymentName string
	// PodLabels select the transport pods in the endpoint's namespace.
	PodLabels map[string]string
	// DesiredReplicas is spec.replicas of the Deployment after this
	// reconcile: the configured count, or zero when scaled to zero. It is
	// zero when no Deployment exists.
	DesiredReplicas int32
	// ReadyReplicas is status.readyReplicas of the Deployment as read at the
	// start of this reconcile, before any update. It is zero when no
	// Deployment exists.
	ReadyReplicas int32
	// ScaledToZero is true while Ready is False with reason
	// ModelNameMismatch.
	ScaledToZero bool
	// Problem is set when the Deployment could not be created or updated.
	Problem *Problem
}

// FastNegative is a TransportReady and Registered verdict reached without
// scraping Pylon.
type FastNegative struct {
	TransportReady pylonv1alpha1.TransportReadyReason
	Registered     pylonv1alpha1.RegisteredReason
	Message        string
}

// FastNegative returns the verdicts that precede any scrape result, in the
// design's order: ScaledToZero first, then TransportPodsNotRunning when the
// Deployment could not be created or updated or has no ready pods although
// it wants some. ok is false otherwise, and the registration observer decides
// from the pods' metrics.
func (s State) FastNegative() (verdict FastNegative, ok bool) {
	switch {
	case s.ScaledToZero:
		msg := fmt.Sprintf("Transport Deployment %q is scaled to zero while Ready is False with reason %s",
			s.DeploymentName, pylonv1alpha1.ReadyReasonModelNameMismatch)
		if s.Problem != nil {
			msg += "; " + s.Problem.Message
		}
		return FastNegative{
			TransportReady: pylonv1alpha1.TransportReadyReasonScaledToZero,
			Registered:     pylonv1alpha1.RegisteredReasonScaledToZero,
			Message:        msg,
		}, true
	case s.Problem != nil:
		return FastNegative{
			TransportReady: pylonv1alpha1.TransportReadyReasonTransportPodsNotRunning,
			Registered:     pylonv1alpha1.RegisteredReasonTransportPodsNotRunning,
			Message:        s.Problem.Message,
		}, true
	case s.DesiredReplicas > 0 && s.ReadyReplicas == 0:
		return FastNegative{
			TransportReady: pylonv1alpha1.TransportReadyReasonTransportPodsNotRunning,
			Registered:     pylonv1alpha1.RegisteredReasonTransportPodsNotRunning,
			Message:        fmt.Sprintf("Transport Deployment %q has no ready pods (0 of %d)", s.DeploymentName, s.DesiredReplicas),
		}, true
	}
	return FastNegative{}, false
}

// Reconciler creates and updates transport Deployments and replicates the
// credential and trust bundle they mount.
type Reconciler struct {
	client client.Client
	config config.Config
}

// New returns a Reconciler. In the operator c reads through the manager's
// cache, which config.ManagerOptions narrows to the objects read here.
func New(c client.Client, cfg config.Config) *Reconciler {
	return &Reconciler{client: c, config: cfg}
}

// Config returns the configuration the Reconciler renders with.
func (r *Reconciler) Config() config.Config {
	return r.config
}

// Reconcile brings the transport of ep to its desired state and reports it.
// ep is the reconcile's working copy: the Ready condition and status.gpu are
// those computed earlier in the same reconcile.
//
// The Deployment is created before the backend exists, since Pylon waits for
// the upstream. It is neither created nor updated while the credential or
// trust bundle cannot be replicated, except that it is still scaled to zero
// on a model name mismatch.
func (r *Reconciler) Reconcile(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint) (State, error) {
	scaled := scaledToZero(ep)
	replicas := r.config.TransportReplicas
	if scaled {
		replicas = 0
	}
	desired := Deployment(ep, r.config, replicas)
	st := State{DeploymentName: desired.Name, PodLabels: PodLabels(ep), ScaledToZero: scaled}
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("deployment", desired.Name))

	existing := &appsv1.Deployment{}
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return st, fmt.Errorf("reading Deployment %q: %w", desired.Name, err)
		}
		existing = nil
	}
	if existing != nil && !metav1.IsControlledBy(existing, ep) {
		st.Problem = &Problem{
			EventReason: EventReasonTransportObjectConflict,
			Message:     fmt.Sprintf("Deployment %q exists and is not controlled by this InferenceEndpoint", desired.Name),
		}
		return st, nil
	}

	problem, err := r.replicate(ctx, ep)
	if err != nil {
		return st, err
	}
	if problem != nil {
		st.Problem = problem
		if existing != nil {
			st.ReadyReplicas = existing.Status.ReadyReplicas
			st.DesiredReplicas = ptr.Deref(existing.Spec.Replicas, 1)
			if scaled && st.DesiredReplicas != 0 {
				// Scaling down needs neither credential nor trust bundle, and
				// a wrong model must never be advertised.
				scaledDown := existing.DeepCopy()
				scaledDown.Spec.Replicas = ptr.To[int32](0)
				if err := r.patch(ctx, existing, scaledDown); err != nil {
					return st, err
				}
				log.FromContext(ctx).Info("Scaled the transport Deployment to zero", "reason", pylonv1alpha1.ReadyReasonModelNameMismatch)
				st.DesiredReplicas = 0
			}
		}
		return st, nil
	}

	if existing == nil {
		if err := r.client.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Invisible to the cache, so it lacks the managed-by label.
				st.Problem = &Problem{
					EventReason: EventReasonTransportObjectConflict,
					Message:     fmt.Sprintf("Deployment %q exists and is not managed by %s", desired.Name, config.ManagedBy),
				}
				return st, nil
			}
			return st, fmt.Errorf("creating Deployment %q: %w", desired.Name, err)
		}
		log.FromContext(ctx).Info("Created the transport Deployment", "replicas", replicas)
		st.DesiredReplicas = replicas
		return st, nil
	}

	st.ReadyReplicas = existing.Status.ReadyReplicas
	st.DesiredReplicas = replicas
	if !equality.Semantic.DeepEqual(existing.Spec.Selector, desired.Spec.Selector) {
		// The selector is immutable, so the Deployment is replaced. Its
		// deletion requeues the endpoint through the owner watch.
		if err := r.client.Delete(ctx, existing, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
			return st, fmt.Errorf("deleting Deployment %q to change its selector: %w", desired.Name, err)
		}
		log.FromContext(ctx).Info("Deleted the transport Deployment to change its selector")
		st.ReadyReplicas = 0
		return st, nil
	}
	return st, r.update(ctx, existing, desired)
}

// update writes the parts of desired that differ from existing. The template
// is compared by SpecHashAnnotation only, so defaults the API server adds
// never cause a write, and an unchanged spec never rolls the pods.
func (r *Reconciler) update(ctx context.Context, existing, desired *appsv1.Deployment) error {
	d := existing.DeepCopy()
	var changed []string
	if d.Spec.Template.Annotations[SpecHashAnnotation] != desired.Spec.Template.Annotations[SpecHashAnnotation] {
		d.Spec.Template = *desired.Spec.Template.DeepCopy()
		changed = append(changed, "template")
	}
	if ptr.Deref(d.Spec.Replicas, 1) != *desired.Spec.Replicas {
		d.Spec.Replicas = ptr.To(*desired.Spec.Replicas)
		changed = append(changed, "replicas")
	}
	labelsChanged := false
	for k, v := range desired.Labels {
		if d.Labels[k] != v {
			if d.Labels == nil {
				d.Labels = map[string]string{}
			}
			d.Labels[k] = v
			labelsChanged = true
		}
	}
	if labelsChanged {
		changed = append(changed, "labels")
	}
	if len(changed) == 0 {
		return nil
	}
	if err := r.patch(ctx, existing, d); err != nil {
		return err
	}
	log.FromContext(ctx).Info("Updated the transport Deployment", "changed", changed, "replicas", *desired.Spec.Replicas)
	return nil
}

// patch sends the difference from base to modified as a merge patch. The
// patch carries no resourceVersion: it writes only fields the operator owns,
// so it does not conflict with the Deployment controller's status writes.
func (r *Reconciler) patch(ctx context.Context, base, modified *appsv1.Deployment) error {
	if err := r.client.Patch(ctx, modified, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("updating Deployment %q: %w", base.Name, err)
	}
	return nil
}

// scaledToZero reports whether Ready is False with reason ModelNameMismatch.
func scaledToZero(ep *pylonv1alpha1.InferenceEndpoint) bool {
	c := meta.FindStatusCondition(ep.Status.Conditions, string(pylonv1alpha1.ConditionReady))
	return c != nil && c.Status == metav1.ConditionFalse && c.Reason == string(pylonv1alpha1.ReadyReasonModelNameMismatch)
}
