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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsNamespaceStuckTerminating(t *testing.T) {
	namespaceName := "foo-ns"
	namespace := &corev1.Namespace{}
	namespace.Name = namespaceName
	namespace.Status.Phase = corev1.NamespaceActive
	defaultTimeConfig := (&TimeConfig{}).Complete()
	stuckTimeout := defaultTimeConfig.NamespaceStuckTimeout
	var isStuck bool
	var reasons []string

	// No deletion timestamp.
	reasons, isStuck = IsNamespaceStuckTerminating(namespace, defaultTimeConfig)
	assert.False(t, isStuck)
	assert.Empty(t, reasons)

	// Terminated but timestamp not after timeout.
	namespace.Status.Phase = corev1.NamespaceTerminating
	now := time.Now()
	namespace.DeletionTimestamp = &metav1.Time{Time: now}
	reasons, isStuck = IsNamespaceStuckTerminating(namespace, defaultTimeConfig)
	assert.False(t, isStuck)
	assert.Empty(t, reasons)

	// Terminated and timestamp after timeout, no conditions.
	now = time.Now()
	namespace.DeletionTimestamp = &metav1.Time{Time: now.Add(-1*stuckTimeout - 1*time.Minute)}
	reasons, isStuck = IsNamespaceStuckTerminating(namespace, defaultTimeConfig)
	assert.True(t, isStuck)
	assert.Equal(t, []string{"unknown"}, reasons)

	// Terminated and timestamp not after timeout, with conditions.
	now = time.Now()
	namespace.DeletionTimestamp = &metav1.Time{Time: now}
	namespace.Status.Conditions = []corev1.NamespaceCondition{
		{
			Type:               corev1.NamespaceDeletionDiscoveryFailure,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Time{Time: now.Add(-1*stuckTimeout - 1*time.Minute)},
		},
		{
			Type:               corev1.NamespaceDeletionContentFailure,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Time{Time: now.Add(-1*stuckTimeout - 1*time.Minute)},
		},
		{
			Type:   corev1.NamespaceDeletionGVParsingFailure,
			Status: corev1.ConditionTrue,
		},
		{
			Type:               corev1.NamespaceContentRemaining,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Time{Time: now.Add(-1*stuckTimeout - 1*time.Minute)},
		},
		{
			Type:               corev1.NamespaceFinalizersRemaining,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Time{Time: now.Add(-1*stuckTimeout - 1*time.Minute)},
		},
	}
	reasons, isStuck = IsNamespaceStuckTerminating(namespace, defaultTimeConfig)
	assert.True(t, isStuck)
	assert.Equal(t, []string{
		"discovery failed on deletion",
		"content failed to be deleted",
		"gvk parse failed on deletion",
		"content remaining after deletion timeout",
		"finalizers remaining after deletion timeout",
	}, reasons)
}

func TestIsMiniServiceNamespaceName(t *testing.T) {
	assert.False(t, IsMiniServiceNamespaceName("foo-sr"))
	assert.True(t, IsMiniServiceNamespaceName("sr-foo"))
}
