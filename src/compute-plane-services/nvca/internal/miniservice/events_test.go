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
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
)

// drainEvents reads any events currently buffered on rec.Events without blocking.
func drainEvents(t *testing.T, rec *record.FakeRecorder) []string {
	t.Helper()
	var events []string
	for {
		select {
		case e := <-rec.Events:
			events = append(events, e)
		default:
			return events
		}
	}
}

func newEventTestMiniService() *v1alpha1.MiniService {
	return &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ms", Namespace: "ns"},
	}
}

func TestEmitConditionEvents(t *testing.T) {
	t.Run("unset to false emits a warning event", func(t *testing.T) {
		rec := record.NewFakeRecorder(10)
		r := &Reconciler{eventRecorder: rec}
		ms := newEventTestMiniService()

		newConds := []metav1.Condition{{
			Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.MiniServiceStatusReasonDegradedWorker,
			Message: "worker pod worker-0 crash looping",
		}}

		r.emitConditionEvents(ms, nil, newConds)

		events := drainEvents(t, rec)
		require.Len(t, events, 1)
		assert.Equal(t, "Warning DegradedWorker worker pod worker-0 crash looping", events[0])
	})

	t.Run("unchanged false condition does not re-emit", func(t *testing.T) {
		rec := record.NewFakeRecorder(10)
		r := &Reconciler{eventRecorder: rec}
		ms := newEventTestMiniService()

		cond := metav1.Condition{
			Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.MiniServiceStatusReasonObjectsFailed,
			Message: "objects failed to deploy",
		}

		r.emitConditionEvents(ms, []metav1.Condition{cond}, []metav1.Condition{cond})

		assert.Empty(t, drainEvents(t, rec))
	})

	t.Run("false to false with a new reason emits a new warning event", func(t *testing.T) {
		rec := record.NewFakeRecorder(10)
		r := &Reconciler{eventRecorder: rec}
		ms := newEventTestMiniService()

		oldConds := []metav1.Condition{{
			Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.MiniServiceStatusReasonWaitingObjectReadiness,
			Message: "waiting on objects",
		}}
		newConds := []metav1.Condition{{
			Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.MiniServiceStatusReasonPendingTimeout,
			Message: "objects timed out pending",
		}}

		r.emitConditionEvents(ms, oldConds, newConds)

		events := drainEvents(t, rec)
		require.Len(t, events, 1)
		assert.Equal(t, "Warning ObjectsTimedOutPending objects timed out pending", events[0])
	})

	t.Run("false to true emits a normal recovery event", func(t *testing.T) {
		rec := record.NewFakeRecorder(10)
		r := &Reconciler{eventRecorder: rec}
		ms := newEventTestMiniService()

		oldConds := []metav1.Condition{{
			Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
			Status: metav1.ConditionFalse,
			Reason: v1alpha1.MiniServiceStatusReasonDegradedWorker,
		}}
		newConds := []metav1.Condition{{
			Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
			Status: metav1.ConditionTrue,
			Reason: "ObjectsReady",
		}}

		r.emitConditionEvents(ms, oldConds, newConds)

		events := drainEvents(t, rec)
		require.Len(t, events, 1)
		assert.Equal(t, "Normal ObjectsReady ObjectsHealthy condition recovered", events[0])
	})

	t.Run("unset to true does not emit a recovery event", func(t *testing.T) {
		rec := record.NewFakeRecorder(10)
		r := &Reconciler{eventRecorder: rec}
		ms := newEventTestMiniService()

		newConds := []metav1.Condition{{
			Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
			Status: metav1.ConditionTrue,
			Reason: "ObjectsReady",
		}}

		r.emitConditionEvents(ms, nil, newConds)

		assert.Empty(t, drainEvents(t, rec))
	})

	t.Run("nil event recorder does not panic", func(t *testing.T) {
		r := &Reconciler{}
		ms := newEventTestMiniService()
		newConds := []metav1.Condition{{
			Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
			Status: metav1.ConditionFalse,
			Reason: v1alpha1.MiniServiceStatusReasonObjectsFailed,
		}}

		assert.NotPanics(t, func() { r.emitConditionEvents(ms, nil, newConds) })
	})
}

func TestSanitizeEventMessage(t *testing.T) {
	t.Run("collapses newlines and extra whitespace", func(t *testing.T) {
		msg := "line one\nline two\n\tline three"
		assert.Equal(t, "line one line two line three", sanitizeEventMessage(msg))
	})

	t.Run("truncates overly long messages", func(t *testing.T) {
		msg := strings.Repeat("a", maxEventMessageLen+50)
		got := sanitizeEventMessage(msg)
		assert.LessOrEqual(t, len(got), maxEventMessageLen)
		assert.True(t, strings.HasSuffix(got, "..."))
	})

	t.Run("truncates without splitting a multi-byte rune", func(t *testing.T) {
		// "e" is a 3-byte rune (U+00e9 encoded as UTF-8 wouldn't apply here, use a
		// genuine multi-byte character so the naive byte cut lands mid-rune).
		msg := strings.Repeat("a", maxEventMessageLen-4) + "中文" // two 3-byte CJK runes
		got := sanitizeEventMessage(msg)
		assert.True(t, utf8.ValidString(got))
		assert.LessOrEqual(t, len(got), maxEventMessageLen)
	})
}
