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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
)

// maxEventMessageLen bounds recorded event messages so a verbose or multi-line
// condition message can't bloat the Event object stored in etcd. Matches the
// +kubebuilder:validation:MaxLength=1024 already enforced on metav1.Condition.Message.
const maxEventMessageLen = 1024

// recordEvent emits a Kubernetes event on ms. It is a no-op if the reconciler has
// no event recorder configured, so callers don't need to guard against a nil recorder.
func (r *Reconciler) recordEvent(ms *v1alpha1.MiniService, eventType, reason, messageFmt string, args ...any) {
	if r.eventRecorder == nil {
		return
	}
	r.eventRecorder.Eventf(ms, eventType, reason, messageFmt, args...)
}

// emitConditionEvents records a Warning event for each MiniService status condition that
// transitioned to False during this reconcile, and a Normal recovery event for each
// condition that transitioned back to True. Conditions whose Status and Reason are
// unchanged are skipped, so an unresolved abnormal state is not re-announced on every
// reconcile.
func (r *Reconciler) emitConditionEvents(ms *v1alpha1.MiniService, oldConditions, newConditions []metav1.Condition) {
	for _, cond := range newConditions {
		oldCond := meta.FindStatusCondition(oldConditions, cond.Type)
		if oldCond != nil && oldCond.Status == cond.Status && oldCond.Reason == cond.Reason {
			continue
		}

		switch cond.Status {
		case metav1.ConditionFalse:
			r.recordEvent(ms, corev1.EventTypeWarning, cond.Reason, "%s", sanitizeEventMessage(cond.Message))
		case metav1.ConditionTrue:
			if oldCond != nil && oldCond.Status == metav1.ConditionFalse {
				r.recordEvent(ms, corev1.EventTypeNormal, cond.Reason, "%s condition recovered", cond.Type)
			}
		}
	}
}

// sanitizeEventMessage collapses a condition message to a single line and truncates it,
// so it stays a concise, etcd-friendly event message rather than a raw multi-line error dump.
func sanitizeEventMessage(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxEventMessageLen {
		msg = msg[:maxEventMessageLen-3] + "..."
	}
	return msg
}
