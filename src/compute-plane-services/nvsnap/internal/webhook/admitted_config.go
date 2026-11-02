// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import corev1 "k8s.io/api/core/v1"

// AdmittedConfigAnnotation is the configuration identity (cacheURI) of a
// pod as admission received it, before the webhook's own patches. A group
// capture records a companion under it, and the companion's replacement is
// admitted by the identity of the spec it arrives with: the two agree
// whatever the webhook adds to running pods (env, probes), which re-hashing
// a running spec does not guarantee.
const AdmittedConfigAnnotation = "nvsnap.io/admitted-config"

// admittedConfigPatches stamps the pod with its admitted configuration
// identity, unless it has one (a pod is admitted once).
func (m *Mutator) admittedConfigPatches(pod *corev1.Pod) []PatchOp {
	if _, has := pod.Annotations[AdmittedConfigAnnotation]; has {
		return nil
	}
	u, ok := m.cacheURI(pod)
	if !ok {
		return nil
	}
	var patches []PatchOp
	if pod.Annotations == nil {
		patches = append(patches, PatchOp{Op: "add", Path: "/metadata/annotations", Value: map[string]string{}})
	}
	return append(patches, PatchOp{Op: "add", Path: "/metadata/annotations/" + jsonPointerEscape(AdmittedConfigAnnotation), Value: u})
}
