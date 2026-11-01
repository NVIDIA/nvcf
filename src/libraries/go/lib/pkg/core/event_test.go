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

package core

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// mockMetav1Object implements metav1.Object for testing
type mockMetav1Object struct {
	metav1.ObjectMeta
}

func TestEvent_String_WithMetav1Object(t *testing.T) {
	pod := &corev1.Pod{}
	pod.Name = "my-pod"
	pod.Namespace = "my-namespace"

	evt := &Event{
		Kind:          "Pod",
		ObjectMetaKey: "my-namespace/my-pod",
		Object:        pod,
	}
	s := evt.String()
	assert.Contains(t, s, "Pod")
	assert.Contains(t, s, "my-namespace/my-pod")
}

func TestEvent_String_WithTimeObject(t *testing.T) {
	now := time.Now()
	evt := &Event{
		Kind:          "Tick",
		ObjectMetaKey: "timer",
		Object:        &now,
	}
	s := evt.String()
	assert.Contains(t, s, "Tick")
	assert.Contains(t, s, "timer")
}

func TestEvent_String_WithObjectUpdate_MetaObject(t *testing.T) {
	pod := &corev1.Pod{}
	pod.Name = "new-pod"
	pod.Namespace = "ns"

	evt := &Event{
		Kind: "Update",
		Object: &ObjectUpdate{
			NewObj: pod,
			OldObj: nil,
		},
	}
	s := evt.String()
	assert.Contains(t, s, "Update")
	assert.Contains(t, s, "ObjectUpdate")
}

func TestEvent_String_WithObjectUpdate_UnknownType(t *testing.T) {
	evt := &Event{
		Kind: "Update",
		Object: &ObjectUpdate{
			NewObj: "plain-string",
			OldObj: nil,
		},
	}
	s := evt.String()
	assert.Contains(t, s, "Unknown ObjectUpdate")
}

func TestEvent_String_WithUnknownObject(t *testing.T) {
	evt := &Event{
		Kind:   "Unknown",
		Object: 42,
	}
	s := evt.String()
	assert.Contains(t, s, "Unknown")
}

func TestEvent_String_FormattingVariations(t *testing.T) {
	t.Run("with_complex_namespace_and_name", func(t *testing.T) {
		pod := &corev1.Pod{}
		pod.Name = "my-complex-pod-12345"
		pod.Namespace = "kube-system"
		evt := &Event{
			Kind:          "Reconcile",
			ObjectMetaKey: "kube-system/my-complex-pod-12345",
			Object:        pod,
		}
		s := evt.String()
		assert.Contains(t, s, "kube-system/my-complex-pod-12345")
		assert.Contains(t, s, "Reconcile")
	})

	t.Run("with_non_metav1_struct", func(t *testing.T) {
		type customObject struct {
			Field string
		}
		evt := &Event{
			Kind:          "Custom",
			ObjectMetaKey: "custom-key",
			Object:        customObject{Field: "value"},
		}
		s := evt.String()
		assert.Contains(t, s, "Custom")
		assert.Contains(t, s, "Unknown")
	})

	t.Run("with_nil_object", func(t *testing.T) {
		evt := &Event{
			Kind:          "Delete",
			ObjectMetaKey: "deleted-key",
			Object:        nil,
		}
		s := evt.String()
		assert.Contains(t, s, "Delete")
		assert.Contains(t, s, "Unknown")
	})
}

func TestEvent_String_Format(t *testing.T) {
	now := time.Now()
	evt := &Event{Kind: "K", Object: &now, ObjectMetaKey: "meta"}
	s := evt.String()
	// should start with "("
	assert.True(t, strings.HasPrefix(s, "("), fmt.Sprintf("unexpected format: %s", s))
}
