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
	"context"
	"errors"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	natsqueue "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/nats"
)

// classifyQueuePollFailure maps a poll error onto the bounded reason label.
// Bounded deliberately: the label feeds a counter, so passing the error text
// through would be a cardinality leak. The distinction that matters
// operationally is broker-level failure (connection_closed, no_servers, auth)
// against stream-level failure (stream_not_found, consumer_not_found), because
// those are the two independent causes behind nvcf#1590 and they need different
// responses.
func classifyQueuePollFailure(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, nats.ErrConnectionClosed):
		return nvcametrics.QueuePollReasonConnectionClosed
	case errors.Is(err, nats.ErrNoServers):
		return nvcametrics.QueuePollReasonNoServers
	case errors.Is(err, nats.ErrAuthorization), errors.Is(err, nats.ErrAuthExpired):
		return nvcametrics.QueuePollReasonAuth
	case errors.Is(err, jetstream.ErrStreamNotFound):
		return nvcametrics.QueuePollReasonStreamNotFound
	case errors.Is(err, jetstream.ErrConsumerNotFound):
		return nvcametrics.QueuePollReasonConsumerNotFound
	case errors.Is(err, nats.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return nvcametrics.QueuePollReasonTimeout
	default:
		return nvcametrics.QueuePollReasonOther
	}
}

// natsConnectionObserver turns NATS connection lifecycle callbacks into
// metrics. It is the replacement for the pod restart as an operational signal:
// now that liveness only reacts to a permanently closed connection, a queue
// outage is visible here rather than as a CrashLoopBackOff.
type natsConnectionObserver struct {
	metrics *nvcametrics.Metrics
}

func newNATSConnectionObserver(m *nvcametrics.Metrics) *natsConnectionObserver {
	return &natsConnectionObserver{metrics: m}
}

// gaugeForConnState maps the transport's reported state onto the gauge values.
// The mapping is explicit because nats.Status' own iota order is different
// (CLOSED is 2 there, RECONNECTING is 3), so casting would silently publish
// closed connections as reconnecting ones.
func gaugeForConnState(state natsqueue.ConnState) float64 {
	switch state {
	case natsqueue.ConnStateConnected:
		return nvcametrics.NATSConnStateConnected
	case natsqueue.ConnStateReconnecting:
		return nvcametrics.NATSConnStateReconnecting
	case natsqueue.ConnStateClosed:
		return nvcametrics.NATSConnStateClosed
	default:
		// Includes "connecting": not yet usable, and reporting it as connected
		// is the failure this mapping exists to avoid.
		return nvcametrics.NATSConnStateDisconnected
	}
}

func (o *natsConnectionObserver) ConnectionStateChanged(state natsqueue.ConnState) {
	if o.metrics == nil || o.metrics.NATSConnectionState == nil {
		return
	}
	o.metrics.NATSConnectionState.
		WithLabelValues(o.metrics.WithDefaultLabelValues()...).
		Set(gaugeForConnState(state))
}

func (o *natsConnectionObserver) ReconnectSucceeded() {
	if o.metrics == nil || o.metrics.NATSReconnectsTotal == nil {
		return
	}
	o.metrics.NATSReconnectsTotal.WithLabelValues(o.metrics.WithDefaultLabelValues()...).Inc()
}
