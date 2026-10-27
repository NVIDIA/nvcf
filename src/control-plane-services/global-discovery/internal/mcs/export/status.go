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

package export

import (
	"regexp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// conditionReason is the pattern metav1.Condition requires of reasons.
var conditionReason = regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`)

// ConditionFromGDS converts a GDS condition. Reasons the Kubernetes API
// would reject become "Unknown"; a missing transition time is left zero so
// that meta.SetStatusCondition stamps it.
func ConditionFromGDS(c *gdsv1.Condition) metav1.Condition {
	out := metav1.Condition{Type: c.GetType(), Reason: c.GetReason(), Message: c.GetMessage()}
	switch c.GetStatus() {
	case gdsv1.ConditionStatus_CONDITION_STATUS_TRUE:
		out.Status = metav1.ConditionTrue
	case gdsv1.ConditionStatus_CONDITION_STATUS_FALSE:
		out.Status = metav1.ConditionFalse
	default:
		out.Status = metav1.ConditionUnknown
	}
	if !conditionReason.MatchString(out.Reason) || len(out.Reason) > 1024 {
		out.Reason = "Unknown"
	}
	if len(out.Message) > 32768 {
		out.Message = out.Message[:32768]
	}
	if t := c.GetLastTransitionAt(); t != nil {
		out.LastTransitionTime = metav1.NewTime(t.AsTime())
	}
	return out
}

// gdsCondition returns the condition of type t in a GDS cluster status, or
// nil.
func gdsCondition(st *gdsv1.ClusterStatus, t string) *metav1.Condition {
	for _, c := range st.GetConditions() {
		if c.GetType() == t {
			out := ConditionFromGDS(c)
			return &out
		}
	}
	return nil
}

// equalConditions compares condition lists ignoring order.
func equalConditions(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	byType := make(map[string]metav1.Condition, len(a))
	for _, c := range a {
		byType[c.Type] = c
	}
	for _, c := range b {
		o, ok := byType[c.Type]
		if !ok || o.Status != c.Status || o.Reason != c.Reason || o.Message != c.Message ||
			o.ObservedGeneration != c.ObservedGeneration || !o.LastTransitionTime.Equal(&c.LastTransitionTime) {
			return false
		}
	}
	return true
}
