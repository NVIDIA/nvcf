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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// fakePassiveClock is a minimal clock.PassiveClock whose time only advances
// when Step is called, so aggregation-window behavior is deterministic.
type fakePassiveClock struct{ now time.Time }

func (c *fakePassiveClock) Now() time.Time                   { return c.now }
func (c *fakePassiveClock) Since(ts time.Time) time.Duration { return c.now.Sub(ts) }
func (c *fakePassiveClock) Step(d time.Duration)             { c.now = c.now.Add(d) }

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
	tests := []struct {
		name     string
		interval time.Duration
		want     int
	}{
		{name: "five minute heartbeat", interval: 5 * time.Minute, want: 299},
		{name: "zero falls back to default heartbeat - 1s", interval: 0, want: 299},
		{name: "one second has no safe window", interval: time.Second, want: 0},
		{name: "sub-second has no safe window", interval: 500 * time.Millisecond, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ledgerEventAggregateMaxIntervalSeconds(tt.interval))
		})
	}
}

func TestNewLedgerEventCorrelatorOptions(t *testing.T) {
	tests := []struct {
		name            string
		interval        time.Duration
		wantMaxInterval int
		wantMaxEvents   int
	}{
		{
			name:            "usable heartbeat sets a sub-heartbeat window",
			interval:        5 * time.Minute,
			wantMaxInterval: 299,
			wantMaxEvents:   0, // client-go default (10)
		},
		{
			name:            "sub-second heartbeat disables aggregation",
			interval:        500 * time.Millisecond,
			wantMaxInterval: 0, // client-go default (10m) - unused because aggregation is disabled
			wantMaxEvents:   ledgerAggregationDisabledMaxEvents,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewLedgerEventCorrelatorOptions(tt.interval)
			assert.Equal(t, tt.wantMaxInterval, opts.MaxIntervalInSeconds)
			assert.Equal(t, tt.wantMaxEvents, opts.MaxEvents)
			assert.NotNil(t, opts.KeyFunc)
			assert.NotNil(t, opts.SpamKeyFunc)
		})
	}
}

// TestLedgerCorrelator_PreservesAnnotationsUnderRapidHeartbeats drives a real
// client-go correlator with a sub-second heartbeat and ten status Events for a
// single instance at 500ms cadence. Aggregation must stay disabled so the
// ledger instance-id annotation survives every Event (EventAggregate would
// otherwise drop annotations once the aggregate threshold is hit).
func TestLedgerCorrelator_PreservesAnnotationsUnderRapidHeartbeats(t *testing.T) {
	clk := &fakePassiveClock{now: time.Unix(1700000000, 0)}
	opts := NewLedgerEventCorrelatorOptions(500 * time.Millisecond)
	opts.Clock = clk
	correlator := record.NewEventCorrelatorWithOptions(opts)

	const instanceID = "0-sr-a"
	for i := 0; i < 10; i++ {
		ev := &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   "ns",
				Annotations: map[string]string{types.LedgerAnnotationInstanceID: instanceID},
			},
			InvolvedObject: corev1.ObjectReference{
				Kind:       "ICMSRequest",
				Namespace:  "ns",
				Name:       "req-1",
				UID:        "uid-1",
				APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
			},
			Type:    corev1.EventTypeNormal,
			Reason:  "InstanceStatusUpdate",
			Message: fmt.Sprintf("%s transition %d", instanceID, i),
			Source:  corev1.EventSource{Component: "nvca"},
		}

		res, err := correlator.EventCorrelate(ev)
		require.NoError(t, err)
		require.NotNil(t, res)
		require.False(t, res.Skip, "distinct per-instance heartbeats must not be spam-filtered")
		require.NotNil(t, res.Event)
		assert.Equal(t, instanceID, res.Event.Annotations[types.LedgerAnnotationInstanceID],
			"aggregation must not strip the ledger instance-id annotation")

		clk.Step(500 * time.Millisecond)
	}
}
