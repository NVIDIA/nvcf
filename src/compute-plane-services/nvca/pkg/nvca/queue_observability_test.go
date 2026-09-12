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
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	natsqueue "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/nats"
)

// TestClassifyQueuePollFailureSurvivesWrapping is the case that actually
// matters: every error the NATS client hands back is wrapped
// ("fetch jetstream batch: %w"), so a classifier written with == instead of
// errors.Is would compile, pass a naive test, and label every real failure
// "other" in production.
func TestClassifyQueuePollFailureSurvivesWrapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{
			"connection closed, wrapped as the client wraps it",
			fmt.Errorf("fetch jetstream messages: %w", nats.ErrConnectionClosed),
			nvcametrics.QueuePollReasonConnectionClosed,
		},
		{
			"no servers, double wrapped",
			fmt.Errorf("receive: %w", fmt.Errorf("connect: %w", nats.ErrNoServers)),
			nvcametrics.QueuePollReasonNoServers,
		},
		{
			"authorization violation",
			fmt.Errorf("fetch jetstream batch: %w", nats.ErrAuthorization),
			nvcametrics.QueuePollReasonAuth,
		},
		{
			"expired credentials classify as auth, not other",
			fmt.Errorf("fetch jetstream batch: %w", nats.ErrAuthExpired),
			nvcametrics.QueuePollReasonAuth,
		},
		{
			"stream deleted underneath us, the nvcf#1590 fault injection",
			fmt.Errorf("ensure consumer: %w", jetstream.ErrStreamNotFound),
			nvcametrics.QueuePollReasonStreamNotFound,
		},
		{
			"consumer went stale",
			fmt.Errorf("ensure consumer: %w", jetstream.ErrConsumerNotFound),
			nvcametrics.QueuePollReasonConsumerNotFound,
		},
		{
			"timeout",
			fmt.Errorf("fetch: %w", nats.ErrTimeout),
			nvcametrics.QueuePollReasonTimeout,
		},
		{
			"unrecognised errors fall into the bounded catch-all",
			errors.New("something nobody predicted"),
			nvcametrics.QueuePollReasonOther,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyQueuePollFailure(tc.err))
		})
	}
}

// TestNATSConnectionObserverTracksState covers the gauge that replaces the pod
// restart as the operational signal. State 3 is the one to alert on, because
// nats.go never reopens a closed connection.
func TestNATSConnectionObserverTracksState(t *testing.T) {
	m, _ := newTestMetrics(t)
	obs := newNATSConnectionObserver(m)

	gauge := func() float64 {
		return testutil.ToFloat64(m.NATSConnectionState.WithLabelValues(m.WithDefaultLabelValues()...))
	}
	reconnects := func() float64 {
		return testutil.ToFloat64(m.NATSReconnectsTotal.WithLabelValues(m.WithDefaultLabelValues()...))
	}

	obs.ConnectionStateChanged(natsqueue.ConnStateConnected)
	assert.Equal(t, float64(nvcametrics.NATSConnStateConnected), gauge(),
		"the initial connect must seed the gauge; nats.go fires no callback for it")
	assert.Zero(t, reconnects(), "the first connect is not a reconnect")

	obs.ConnectionStateChanged(natsqueue.ConnStateDisconnected)
	assert.Equal(t, float64(nvcametrics.NATSConnStateDisconnected), gauge())

	obs.ConnectionStateChanged(natsqueue.ConnStateReconnecting)
	assert.Equal(t, float64(nvcametrics.NATSConnStateReconnecting), gauge(),
		"reconnecting is a distinct state from disconnected, and the gauge documents all four")

	obs.ConnectionStateChanged(natsqueue.ConnStateConnected)
	obs.ReconnectSucceeded()
	assert.Equal(t, float64(nvcametrics.NATSConnStateConnected), gauge())
	assert.Equal(t, float64(1), reconnects(),
		"counting reconnects is what separates flapping from one long outage")

	obs.ConnectionStateChanged(natsqueue.ConnStateConnected)
	obs.ReconnectSucceeded()
	assert.Equal(t, float64(2), reconnects())

	obs.ConnectionStateChanged(natsqueue.ConnStateClosed)
	assert.Equal(t, float64(nvcametrics.NATSConnStateClosed), gauge(),
		"a permanently closed connection must be distinguishable from a retrying one")
}

// TestGaugeMappingDoesNotFollowNatsStatusOrder guards the one mapping mistake
// that would be invisible in production. nats.Status numbers CLOSED 2 and
// RECONNECTING 3; the metric numbers them the other way round. Casting instead
// of mapping would publish every dead connection as a recovering one, and the
// alert on state==3 would fire on healthy reconnects while missing the outage
// it exists for.
func TestGaugeMappingDoesNotFollowNatsStatusOrder(t *testing.T) {
	assert.Equal(t, float64(nvcametrics.NATSConnStateClosed),
		gaugeForConnState(natsqueue.ConnStateClosed))
	assert.Equal(t, float64(nvcametrics.NATSConnStateReconnecting),
		gaugeForConnState(natsqueue.ConnStateReconnecting))
	assert.NotEqual(t, float64(nats.CLOSED), gaugeForConnState(natsqueue.ConnStateClosed),
		"the metric must not inherit nats.Status' numbering")

	// "connecting" has never been usable, so it must not read as connected.
	assert.Equal(t, float64(nvcametrics.NATSConnStateDisconnected),
		gaugeForConnState(natsqueue.ConnStateConnecting))
}

// TestNATSConnectionObserverNilMetrics guards the paths where metrics are not
// configured. The observer is wired unconditionally in Agent.Start, so a nil
// registry must not panic the agent at startup.
func TestNATSConnectionObserverNilMetrics(t *testing.T) {
	obs := newNATSConnectionObserver(nil)
	assert.NotPanics(t, func() {
		obs.ConnectionStateChanged(natsqueue.ConnStateConnected)
		obs.ConnectionStateChanged(natsqueue.ConnStateClosed)
		obs.ReconnectSucceeded()
	})
}

// TestRecordQueuePollFailureSeparatesReasons pins the label set. The reason
// split is the point: broker-level failures and stream-level failures need
// different responses, and a single undifferentiated counter would not tell
// an operator which one they have.
func TestRecordQueuePollFailureSeparatesReasons(t *testing.T) {
	m, _ := newTestMetrics(t)

	m.RecordQueuePollFailure("creation", "L40S", nvcametrics.QueuePollReasonStreamNotFound)
	m.RecordQueuePollFailure("creation", "L40S", nvcametrics.QueuePollReasonStreamNotFound)
	m.RecordQueuePollFailure("termination", "none", nvcametrics.QueuePollReasonNoServers)

	count := func(queueType, gpu, reason string) float64 {
		return testutil.ToFloat64(m.QueuePollFailuresTotal.WithLabelValues(
			m.WithDefaultLabelValues(queueType, gpu, reason)...))
	}

	assert.Equal(t, float64(2), count("creation", "L40S", nvcametrics.QueuePollReasonStreamNotFound))
	assert.Equal(t, float64(1), count("termination", "none", nvcametrics.QueuePollReasonNoServers))
	assert.Zero(t, count("creation", "L40S", nvcametrics.QueuePollReasonAuth))
}

// TestRecordQueuePollFailureNilSafe mirrors the queue manager's habit of
// calling through a possibly-nil metrics handle.
func TestRecordQueuePollFailureNilSafe(t *testing.T) {
	var m *nvcametrics.Metrics
	assert.NotPanics(t, func() {
		m.RecordQueuePollFailure("creation", "none", nvcametrics.QueuePollReasonOther)
	})
}
