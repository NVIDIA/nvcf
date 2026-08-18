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

package nvca

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestLedgerEventSpamKey_IncludesInstanceID(t *testing.T) {
	base := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns"},
		InvolvedObject: corev1.ObjectReference{
			Kind:       "ICMSRequest",
			Namespace:  "ns",
			Name:       "req-1",
			UID:        "uid-1",
			APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
		},
		Type:   corev1.EventTypeNormal,
		Source: corev1.EventSource{Component: "nvca"},
	}
	a := base.DeepCopy()
	a.Annotations = map[string]string{types.LedgerAnnotationInstanceID: "0-sr-a"}
	b := base.DeepCopy()
	b.Annotations = map[string]string{types.LedgerAnnotationInstanceID: "1-sr-a"}
	none := base.DeepCopy()

	assert.NotEqual(t, ledgerEventSpamKey(a), ledgerEventSpamKey(b),
		"different instance-ids must not share a spam budget")
	assert.NotEqual(t, ledgerEventSpamKey(a), ledgerEventSpamKey(none),
		"instance-level and request-level events must not share a spam budget")
	assert.Equal(t, ledgerEventSpamKey(none), ledgerEventSpamKey(base.DeepCopy()),
		"request-level events (no instance-id) share one key")
}

func TestLedgerEventAggregatorKey_IncludesInstanceID(t *testing.T) {
	base := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns"},
		InvolvedObject: corev1.ObjectReference{
			Kind:       "ICMSRequest",
			Namespace:  "ns",
			Name:       "req-1",
			UID:        "uid-1",
			APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
		},
		Type:    corev1.EventTypeNormal,
		Reason:  "InstanceStatusUpdate",
		Message: "0-sr-a is running",
		Source:  corev1.EventSource{Component: "nvca"},
	}
	a := base.DeepCopy()
	a.Annotations = map[string]string{types.LedgerAnnotationInstanceID: "0-sr-a"}
	b := base.DeepCopy()
	b.Message = "1-sr-a is running"
	b.Annotations = map[string]string{types.LedgerAnnotationInstanceID: "1-sr-a"}

	aggA, localA := ledgerEventAggregatorKey(a)
	aggB, localB := ledgerEventAggregatorKey(b)
	assert.NotEqual(t, aggA, aggB, "different instance-ids must not share an aggregate group")
	assert.NotEqual(t, localA, localB, "local keys remain message-based")
}

func TestLedgerEventAggregateMaxIntervalSeconds(t *testing.T) {
	assert.Equal(t, 299, ledgerEventAggregateMaxIntervalSeconds(5*time.Minute))
	assert.Equal(t, 299, ledgerEventAggregateMaxIntervalSeconds(0),
		"zero interval falls back to default heartbeat - 1s")
	assert.Equal(t, 1, ledgerEventAggregateMaxIntervalSeconds(time.Second))
	assert.Equal(t, 1, ledgerEventAggregateMaxIntervalSeconds(500*time.Millisecond))
}

func TestNewLedgerEventCorrelatorOptions(t *testing.T) {
	opts := NewLedgerEventCorrelatorOptions(5 * time.Minute)
	assert.Equal(t, 299, opts.MaxIntervalInSeconds)
	assert.NotNil(t, opts.KeyFunc)
	assert.NotNil(t, opts.SpamKeyFunc)
}
