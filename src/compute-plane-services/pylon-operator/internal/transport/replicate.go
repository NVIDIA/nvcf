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
	"bytes"
	"context"
	"fmt"
	"maps"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
)

// notCreatedSuffix ends the message of a missing source.
const notCreatedSuffix = "; the transport Deployment is not created or updated until it exists"

// replicate copies the cluster credential, and the trust bundle when
// configured, from the operator namespace into the endpoint's namespace. It
// returns a Problem when a source is missing or a copy's name is taken.
//
// Nothing here logs, wraps into an error or puts into a message the content
// of either object: only namespaces, names and keys.
func (r *Reconciler) replicate(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint) (*Problem, error) {
	if p, err := r.replicateCredential(ctx, ep); p != nil || err != nil {
		return p, err
	}
	if r.config.TrustBundleConfigMap == "" {
		return nil, nil
	}
	return r.replicateTrustBundle(ctx, ep)
}

func (r *Reconciler) replicateCredential(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint) (*Problem, error) {
	key := client.ObjectKey{Namespace: r.config.OperatorNamespace, Name: r.config.ClusterCredentialSecret}
	src := &corev1.Secret{}
	if err := r.client.Get(ctx, key, src); err != nil {
		if apierrors.IsNotFound(err) {
			return &Problem{
				EventReason: EventReasonClusterCredentialMissing,
				Message:     fmt.Sprintf("Cluster credential Secret %s not found%s", key, notCreatedSuffix),
			}, nil
		}
		return nil, fmt.Errorf("reading cluster credential Secret %s: %w", key, err)
	}
	token := src.Data[CredentialKey]
	if len(token) == 0 {
		return &Problem{
			EventReason: EventReasonClusterCredentialMissing,
			Message:     fmt.Sprintf("Cluster credential Secret %s has no %s key%s", key, CredentialKey, notCreatedSuffix),
		}, nil
	}
	if ep.Namespace == key.Namespace {
		// The pods mount the source itself.
		return nil, nil
	}
	replica := &corev1.Secret{
		ObjectMeta: replicaMeta(ep, key.Name),
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{CredentialKey: token},
	}
	return r.ensureReplica(ctx, ep, "Secret", replica, &corev1.Secret{}, func(obj client.Object) bool {
		s := obj.(*corev1.Secret)
		if len(s.Data) == 1 && bytes.Equal(s.Data[CredentialKey], token) {
			return false
		}
		s.Data = map[string][]byte{CredentialKey: token}
		return true
	})
}

func (r *Reconciler) replicateTrustBundle(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint) (*Problem, error) {
	key := client.ObjectKey{Namespace: r.config.OperatorNamespace, Name: r.config.TrustBundleConfigMap}
	src := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, key, src); err != nil {
		if apierrors.IsNotFound(err) {
			return &Problem{
				EventReason: EventReasonTrustBundleMissing,
				Message:     fmt.Sprintf("Trust bundle ConfigMap %s not found%s", key, notCreatedSuffix),
			}, nil
		}
		return nil, fmt.Errorf("reading trust bundle ConfigMap %s: %w", key, err)
	}
	if src.Data[TrustBundleCAKey] == "" && len(src.BinaryData[TrustBundleCAKey]) == 0 {
		return &Problem{
			EventReason: EventReasonTrustBundleMissing,
			Message:     fmt.Sprintf("Trust bundle ConfigMap %s has no %s key%s", key, TrustBundleCAKey, notCreatedSuffix),
		}, nil
	}
	if ep.Namespace == key.Namespace {
		return nil, nil
	}
	replica := &corev1.ConfigMap{
		ObjectMeta: replicaMeta(ep, key.Name),
		Data:       maps.Clone(src.Data),
		BinaryData: maps.Clone(src.BinaryData),
	}
	return r.ensureReplica(ctx, ep, "ConfigMap", replica, &corev1.ConfigMap{}, func(obj client.Object) bool {
		cm := obj.(*corev1.ConfigMap)
		if maps.Equal(cm.Data, src.Data) && maps.EqualFunc(cm.BinaryData, src.BinaryData, bytes.Equal) {
			return false
		}
		cm.Data, cm.BinaryData = maps.Clone(src.Data), maps.Clone(src.BinaryData)
		return true
	})
}

// ensureReplica creates desired, or brings the existing copy up to date:
// sync copies the source's data into it and reports whether anything
// changed. Every endpoint that uses a copy is one of its owners and none is
// its controller, so the copy is garbage-collected with the last endpoint in
// its namespace. A same-named object without the managed-by label is left
// alone and reported as a Problem.
func (r *Reconciler) ensureReplica(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint, kind string,
	desired, existing client.Object, sync func(client.Object) bool) (*Problem, error) {
	key := client.ObjectKeyFromObject(desired)
	logger := log.FromContext(ctx).WithValues("kind", kind, "replica", key.String())
	err := r.client.Get(ctx, key, existing)
	switch {
	case apierrors.IsNotFound(err):
		desired.SetOwnerReferences([]metav1.OwnerReference{ownerRef(ep)})
		if err := r.client.Create(ctx, desired); err != nil {
			return nil, fmt.Errorf("creating %s %s: %w", kind, key, err)
		}
		logger.Info("Replicated from the operator namespace")
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading %s %s: %w", kind, key, err)
	}
	if existing.GetLabels()[config.ManagedByLabel] != config.ManagedBy {
		return &Problem{
			EventReason: EventReasonTransportObjectConflict,
			Message: fmt.Sprintf("%s %s exists and is not managed by %s; the transport Deployment is not created or updated",
				kind, key, config.ManagedBy),
		}, nil
	}
	dataChanged := sync(existing)
	ownerAdded := !ownedBy(existing, ep)
	if ownerAdded {
		existing.SetOwnerReferences(append(existing.GetOwnerReferences(), ownerRef(ep)))
	}
	if !dataChanged && !ownerAdded {
		return nil, nil
	}
	if err := r.client.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("updating %s %s: %w", kind, key, err)
	}
	logger.Info("Updated the replica", "dataChanged", dataChanged, "ownerAdded", ownerAdded)
	return nil, nil
}

// replicaMeta is the metadata of a copy in the endpoint's namespace.
func replicaMeta(ep *pylonv1alpha1.InferenceEndpoint, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: ep.Namespace,
		Labels:    map[string]string{config.ManagedByLabel: config.ManagedBy},
	}
}

// ownerRef is a non-controller owner reference to ep.
func ownerRef(ep *pylonv1alpha1.InferenceEndpoint) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: pylonv1alpha1.GroupVersion.String(),
		Kind:       endpointKind,
		Name:       ep.Name,
		UID:        ep.UID,
	}
}

func ownedBy(obj client.Object, ep *pylonv1alpha1.InferenceEndpoint) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == ep.UID && ref.Kind == endpointKind {
			return true
		}
	}
	return false
}
