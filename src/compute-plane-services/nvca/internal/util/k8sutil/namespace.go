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

package k8sutil

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func IsNamespaceStuckTerminating(
	ns *corev1.Namespace,
	k8sTimeConfig *TimeConfig,
) (reasons []string, stuck bool) {
	if ns.Status.Phase != corev1.NamespaceTerminating {
		return nil, false
	}

	stuckTimeout := k8sTimeConfig.NamespaceStuckTimeout
	now := time.Now()
	for _, cond := range ns.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		switch cond.Type {
		case corev1.NamespaceDeletionDiscoveryFailure:
			stuck = true
			reasons = append(reasons, "discovery failed on deletion")
		case corev1.NamespaceDeletionContentFailure:
			// Finalizers can be stuck terminating for a long time, so we need to
			// check if the timeout has been reached.
			if cond.LastTransitionTime.Add(stuckTimeout).Before(now) {
				stuck = true
				reasons = append(reasons, "content failed to be deleted")
			}
		case corev1.NamespaceDeletionGVParsingFailure:
			stuck = true
			reasons = append(reasons, "gvk parse failed on deletion")
		case corev1.NamespaceContentRemaining:
			if cond.LastTransitionTime.Add(stuckTimeout).Before(now) {
				stuck = true
				reasons = append(reasons, "content remaining after deletion timeout")
			}
		case corev1.NamespaceFinalizersRemaining:
			if cond.LastTransitionTime.Add(stuckTimeout).Before(now) {
				stuck = true
				reasons = append(reasons, "finalizers remaining after deletion timeout")
			}
		}
	}

	if !stuck && ns.DeletionTimestamp != nil &&
		ns.DeletionTimestamp.Add(stuckTimeout).Before(now) {
		stuck = true
		reasons = append(reasons, "unknown")
	}

	return reasons, stuck
}

// IsMiniServiceNamespaceName returns true if namespace appears to be a
// helm function/task namespace name.
func IsMiniServiceNamespaceName(namespace string) bool {
	return strings.HasPrefix(namespace, "sr-")
}
