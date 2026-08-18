/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package mscontroller

import (
	"context"

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/transporttls"
	nvcav1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
)

func (r *Reconciler) prepareTransportTLSForWorkloads(
	ctx context.Context,
	ms *nvcav1alpha1.MiniService,
	objs []client.Object,
) error {
	if err := r.cfg.Workload.Validate(); err != nil {
		return reconcile.TerminalError(err)
	}
	if r.cfg.Workload.TransportTLS == nil {
		return nil
	}
	cfg := transporttls.NormalizeConfig(*r.cfg.Workload.TransportTLS)
	if cfg.TrustMode != transporttls.TrustModeBundle {
		return nil
	}

	var podSpecs []*corev1.PodSpec
	for _, obj := range objs {
		podSpec := objectPodSpec(obj)
		if podSpec == nil || !transporttls.PodSpecHasLLMWorker(podSpec) {
			continue
		}
		podSpecs = append(podSpecs, podSpec)
	}
	if len(podSpecs) == 0 {
		return nil
	}

	if err := transporttls.ValidateConfig(cfg); err != nil {
		return reconcile.TerminalError(err)
	}
	if err := r.ensureTransportTLSConfigMap(ctx, ms, cfg); err != nil {
		return err
	}
	for _, podSpec := range podSpecs {
		if err := transporttls.InjectIntoPodSpec(podSpec, cfg); err != nil {
			return reconcile.TerminalError(err)
		}
	}
	return nil
}

func (r *Reconciler) ensureTransportTLSConfigMap(
	ctx context.Context,
	ms *nvcav1alpha1.MiniService,
	cfg nvcaconfig.TransportTLSConfig,
) error {
	desiredData := transporttls.DesiredConfigMapData(cfg)
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: ms.Spec.Namespace, Name: cfg.TrustBundleConfigMapName}
	err := r.Client.Get(ctx, key, cm)
	if apierrors.IsNotFound(err) {
		return r.Client.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            cfg.TrustBundleConfigMapName,
				Namespace:       ms.Spec.Namespace,
				OwnerReferences: transportTLSOwnerReferences(ms),
			},
			Data: desiredData,
		})
	}
	if err != nil {
		return err
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	changed := false
	for key, value := range desiredData {
		if cm.Data[key] != value {
			cm.Data[key] = value
			changed = true
		}
	}
	var ownerRefsChanged bool
	cm.OwnerReferences, ownerRefsChanged = normalizeMiniServiceOwnerReferences(cm.OwnerReferences)
	if ownerRefsChanged {
		changed = true
	}
	if shouldOwnTransportTLSConfigMap(ms) {
		var ownerRefChanged bool
		cm.OwnerReferences, ownerRefChanged = ensureOwnerReference(cm.OwnerReferences, ms)
		if ownerRefChanged {
			changed = true
		}
	} else if hasOwnerReference(cm.OwnerReferences, ms) {
		cm.OwnerReferences = removeOwnerReference(cm.OwnerReferences, ms)
		changed = true
	}
	if !changed {
		return nil
	}
	return r.Client.Update(ctx, cm)
}

func objectPodSpec(obj client.Object) *corev1.PodSpec {
	switch typed := obj.(type) {
	case *corev1.Pod:
		return &typed.Spec
	default:
		// MiniService currently renders workers as Pods. Keep this boundary
		// isolated so future pod-template object kinds can opt into the same
		// transport trust mutation deliberately.
		return nil
	}
}

func transportTLSOwnerReferences(ms *nvcav1alpha1.MiniService) []metav1.OwnerReference {
	if !shouldOwnTransportTLSConfigMap(ms) {
		return nil
	}
	return []metav1.OwnerReference{transportTLSOwnerReference(ms)}
}

func shouldOwnTransportTLSConfigMap(ms *nvcav1alpha1.MiniService) bool {
	return ms.Namespace == "" || ms.Namespace == ms.Spec.Namespace
}

func transportTLSOwnerReference(ms *nvcav1alpha1.MiniService) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: nvcav1alpha1.SchemeGroupVersion.String(),
		Kind:       miniServiceKind,
		Name:       ms.Name,
		UID:        ms.UID,
	}
}

func ensureOwnerReference(refs []metav1.OwnerReference, ms *nvcav1alpha1.MiniService) ([]metav1.OwnerReference, bool) {
	desiredRef := transportTLSOwnerReference(ms)
	for i := range refs {
		if !matchesOwnerReference(refs[i], ms) {
			continue
		}
		if ownerReferencesEqual(refs[i], desiredRef) {
			return refs, false
		}
		refs[i] = desiredRef
		return refs, true
	}
	return append(refs, desiredRef), true
}

func normalizeMiniServiceOwnerReferences(refs []metav1.OwnerReference) ([]metav1.OwnerReference, bool) {
	changed := false
	for i := range refs {
		if !isMiniServiceOwnerReference(refs[i]) {
			continue
		}
		if refs[i].Controller != nil || refs[i].BlockOwnerDeletion != nil {
			refs[i].Controller = nil
			refs[i].BlockOwnerDeletion = nil
			changed = true
		}
	}
	return refs, changed
}

func hasOwnerReference(refs []metav1.OwnerReference, ms *nvcav1alpha1.MiniService) bool {
	for _, ref := range refs {
		if matchesOwnerReference(ref, ms) {
			return true
		}
	}
	return false
}

func removeOwnerReference(refs []metav1.OwnerReference, ms *nvcav1alpha1.MiniService) []metav1.OwnerReference {
	filteredRefs := refs[:0]
	for _, ref := range refs {
		if matchesOwnerReference(ref, ms) {
			continue
		}
		filteredRefs = append(filteredRefs, ref)
	}
	return filteredRefs
}

func matchesOwnerReference(ref metav1.OwnerReference, ms *nvcav1alpha1.MiniService) bool {
	return isMiniServiceOwnerReference(ref) &&
		ref.Name == ms.Name &&
		ref.UID == ms.UID
}

func isMiniServiceOwnerReference(ref metav1.OwnerReference) bool {
	return ref.APIVersion == nvcav1alpha1.SchemeGroupVersion.String() &&
		ref.Kind == miniServiceKind
}

func ownerReferencesEqual(a, b metav1.OwnerReference) bool {
	return a.APIVersion == b.APIVersion &&
		a.Kind == b.Kind &&
		a.Name == b.Name &&
		a.UID == b.UID &&
		ptr.Deref(a.Controller, false) == ptr.Deref(b.Controller, false) &&
		ptr.Deref(a.BlockOwnerDeletion, false) == ptr.Deref(b.BlockOwnerDeletion, false)
}
