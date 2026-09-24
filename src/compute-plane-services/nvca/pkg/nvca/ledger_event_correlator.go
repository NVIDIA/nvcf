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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// defaultHeartbeatFallback matches config.defaultPeriodicInstanceStatusInterval.
// Used when the agent has not configured a periodic status interval yet.
const defaultHeartbeatFallback = 5 * time.Minute

// ledgerAggregationDisabledMaxEvents is a MaxEvents value large enough that the
// client-go aggregator never reaches its unique-event threshold, so ledger
// Events are never collapsed into an aggregate. This matters because
// EventAggregate builds the aggregate Event without copying Annotations, which
// would strip the FnDs ledger context. It is used when the heartbeat interval
// is too small to derive a safe sub-heartbeat aggregation window.
const ledgerAggregationDisabledMaxEvents = 1 << 30

// NewLedgerEventCorrelatorOptions builds client-go Event correlator options so
// multi-instance ICMSRequest Events do not share one spam budget or collapse
// into annotation-less aggregates.
//
// For a usable heartbeat interval, aggregation MaxInterval is set just below
// the periodic status heartbeat (same config source) so each re-report starts
// a fresh window. When no such window exists (interval <=1s), aggregation is
// disabled outright via MaxEvents: client-go treats MaxIntervalInSeconds==0 as
// its 10m default, so a zero here would re-enable aggregation and drop ledger
// annotations.
func NewLedgerEventCorrelatorOptions(heartbeatInterval time.Duration) record.CorrelatorOptions {
	opts := record.CorrelatorOptions{
		KeyFunc:     ledgerEventAggregatorKey,
		SpamKeyFunc: ledgerEventSpamKey,
	}
	if secs := ledgerEventAggregateMaxIntervalSeconds(heartbeatInterval); secs > 0 {
		opts.MaxIntervalInSeconds = secs
	} else {
		opts.MaxEvents = ledgerAggregationDisabledMaxEvents
	}
	return opts
}

// ledgerEventSpamKey mirrors client-go's default spam key (source + object +
// type) and appends the ledger instance-id annotation when present.
func ledgerEventSpamKey(event *corev1.Event) string {
	if event == nil {
		return ""
	}
	return strings.Join([]string{
		event.Source.Component,
		event.Source.Host,
		event.InvolvedObject.Kind,
		event.InvolvedObject.Namespace,
		event.InvolvedObject.Name,
		string(event.InvolvedObject.UID),
		event.InvolvedObject.APIVersion,
		event.Type,
		eventAnnotation(event, types.LedgerAnnotationInstanceID),
	}, "")
}

// ledgerEventAggregatorKey wraps EventAggregatorByReasonFunc and appends the
// ledger instance-id annotation to the aggregate group key so instances on the
// same ICMSRequest CR do not merge.
func ledgerEventAggregatorKey(event *corev1.Event) (string, string) {
	if event == nil {
		return "", ""
	}
	aggregateKey, localKey := record.EventAggregatorByReasonFunc(event)
	return aggregateKey + eventAnnotation(event, types.LedgerAnnotationInstanceID), localKey
}

func eventAnnotation(event *corev1.Event, key string) string {
	if event == nil || event.Annotations == nil {
		return ""
	}
	return event.Annotations[key]
}

// ledgerEventAggregateMaxIntervalSeconds returns the aggregation window in
// whole seconds just below the heartbeat interval so the aggregator resets
// between periodic reports. It returns 0 when the interval is <=1s to signal
// the caller that no safe sub-heartbeat window exists and aggregation should be
// disabled instead (a 0 passed to client-go would fall back to its 10m
// default). A non-positive interval falls back to the default heartbeat.
func ledgerEventAggregateMaxIntervalSeconds(heartbeatInterval time.Duration) int {
	if heartbeatInterval <= 0 {
		heartbeatInterval = defaultHeartbeatFallback
	}
	secs := int(heartbeatInterval / time.Second)
	if secs <= 1 {
		return 0
	}
	return secs - 1
}
