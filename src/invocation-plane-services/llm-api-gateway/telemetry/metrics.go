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

package telemetry

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/must"
)

const (
	metricPrefix = "llm_api_gateway_"

	dropReasonSameCluster      = "same_cluster"
	dropReasonOldMessage       = "old_message"
	dropReasonRemoteApplyOff   = "remote_apply_disabled"
	synchronizerDropOldMessage = "old_message"
)

var DurationBuckets = []float64{
	0.005,
	0.01,
	0.025,
	0.05,
	0.075,
	0.1,
	0.25,
	0.5,
	0.75,
	1,
	2.5,
	5,
	7.5,
	10,
	25,
	50,
	75,
}

func Meter() otelmetric.Meter {
	return otel.GetMeterProvider().Meter(ServiceName())
}

func InitializeMetrics() {
	_ = HTTPRequestsTotal()
	_ = HTTPServerRequestDuration()
	_ = HTTPActiveRequests()
	_ = UpstreamRequestsTotal()
	_ = UpstreamRequestDuration()
	_ = LLMTokens()
	_ = ProviderTime()
	_ = StreamFirstToken()
	_ = StreamDuration()
	_ = PubSubPublishFailures()
	_ = PubSubConsumeFailures()
	_ = PubSubConsumeDuration()
	_ = RateLimitEventReplicationLag()
	_ = RateLimitEventsReceived()
	_ = RateLimitEventsDropped()
	_ = RateLimitEventsApplied()
	_ = RateLimitEventsFailedApply()
	_ = RateLimitEventsDryRunWouldApply()
	_ = RateLimitSynchronizerPublishDuration()
	_ = RateLimitSynchronizerQueueWait()
	_ = RateLimitSynchronizerQueueLength()
	_ = RateLimitSynchronizerEventsDropped()
}

func Add(
	counter otelmetric.Int64Counter,
	delta int64,
	attrs ...attribute.KeyValue,
) {
	AddWithContext(context.Background(), counter, delta, attrs...)
}

func AddWithContext(
	ctx context.Context,
	counter otelmetric.Int64Counter,
	delta int64,
	attrs ...attribute.KeyValue,
) {
	counter.Add(ctx, delta, otelmetric.WithAttributes(attrs...))
}

func AddUpDownWithContext(
	ctx context.Context,
	counter otelmetric.Int64UpDownCounter,
	delta int64,
	attrs ...attribute.KeyValue,
) {
	counter.Add(ctx, delta, otelmetric.WithAttributes(attrs...))
}

func Record(
	histogram otelmetric.Float64Histogram,
	value float64,
	attrs ...attribute.KeyValue,
) {
	RecordWithContext(context.Background(), histogram, value, attrs...)
}

func RecordWithContext(
	ctx context.Context,
	histogram otelmetric.Float64Histogram,
	value float64,
	attrs ...attribute.KeyValue,
) {
	histogram.Record(ctx, value, otelmetric.WithAttributes(attrs...))
}

func HTTPRequestsTotal() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"http_requests_total",
		otelmetric.WithDescription("Total inbound HTTP requests."),
	))
}

func HTTPServerRequestDuration() otelmetric.Float64Histogram {
	return must.Get(Meter().Float64Histogram(
		metricPrefix+"http_request_duration_seconds",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Duration of inbound HTTP requests."),
		otelmetric.WithExplicitBucketBoundaries(DurationBuckets...),
	))
}

func HTTPActiveRequests() otelmetric.Int64UpDownCounter {
	return must.Get(Meter().Int64UpDownCounter(
		metricPrefix+"http_active_requests",
		otelmetric.WithDescription("Current in-flight inbound HTTP requests."),
	))
}

func UpstreamRequestsTotal() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"upstream_requests_total",
		otelmetric.WithDescription("Total outbound upstream requests."),
	))
}

func UpstreamRequestDuration() otelmetric.Float64Histogram {
	return must.Get(Meter().Float64Histogram(
		metricPrefix+"upstream_request_duration_seconds",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Duration of outbound upstream requests."),
		otelmetric.WithExplicitBucketBoundaries(DurationBuckets...),
	))
}

func LLMTokens() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"llm_tokens_total",
		otelmetric.WithDescription("LLM token counts reported by upstream providers."),
	))
}

func ProviderTime() otelmetric.Float64Histogram {
	return must.Get(Meter().Float64Histogram(
		metricPrefix+"provider_time_seconds",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Provider-reported timing phases."),
		otelmetric.WithExplicitBucketBoundaries(DurationBuckets...),
	))
}

func StreamFirstToken() otelmetric.Float64Histogram {
	return must.Get(Meter().Float64Histogram(
		metricPrefix+"stream_first_token_seconds",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Time from stream request start to first token."),
		otelmetric.WithExplicitBucketBoundaries(DurationBuckets...),
	))
}

func StreamDuration() otelmetric.Float64Histogram {
	return must.Get(Meter().Float64Histogram(
		metricPrefix+"stream_duration_seconds",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Total stream duration."),
		otelmetric.WithExplicitBucketBoundaries(DurationBuckets...),
	))
}

func PubSubPublishFailures() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"pubsub_publish_failures_total",
		otelmetric.WithDescription("Number of messages failing to be published."),
	))
}

func PubSubConsumeFailures() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"pubsub_consume_failures_total",
		otelmetric.WithDescription("Number of messages failing to be consumed."),
	))
}

func PubSubConsumeDuration() otelmetric.Float64Histogram {
	return must.Get(Meter().Float64Histogram(
		metricPrefix+"pubsub_consume_duration_seconds",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Time taken to consume a message."),
		otelmetric.WithExplicitBucketBoundaries(0, 0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 1, 5, 10, 60, 120),
	))
}

func RateLimitEventReplicationLag() otelmetric.Float64Histogram {
	return must.Get(Meter().Float64Histogram(
		metricPrefix+"rate_limit_event_replication_lag_seconds",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Lag between rate limit event creation and processing."),
		otelmetric.WithExplicitBucketBoundaries(DurationBuckets...),
	))
}

func RateLimitEventsReceived() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"rate_limit_events_received_total",
		otelmetric.WithDescription("Number of rate limit events received from sync transport."),
	))
}

func RateLimitEventsDropped() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"rate_limit_events_dropped_total",
		otelmetric.WithDescription("Number of received rate limit events dropped."),
	))
}

func RateLimitEventsApplied() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"rate_limit_events_applied_total",
		otelmetric.WithDescription("Number of rate limit events applied to the local limiter."),
	))
}

func RateLimitEventsFailedApply() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"rate_limit_events_failed_apply_total",
		otelmetric.WithDescription("Number of rate limit events that failed to apply locally."),
	))
}

func RateLimitEventsDryRunWouldApply() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"rate_limit_events_dry_run_would_apply_total",
		otelmetric.WithDescription("Number of rate limit events that would apply when remote application is disabled."),
	))
}

func RateLimitSynchronizerPublishDuration() otelmetric.Float64Histogram {
	return must.Get(Meter().Float64Histogram(
		metricPrefix+"rate_limit_synchronizer_publish_duration_seconds",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Time taken to publish a rate limit event."),
		otelmetric.WithExplicitBucketBoundaries(DurationBuckets...),
	))
}

func RateLimitSynchronizerQueueWait() otelmetric.Float64Histogram {
	return must.Get(Meter().Float64Histogram(
		metricPrefix+"rate_limit_synchronizer_queue_wait_seconds",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Time spent queueing a rate limit event."),
		otelmetric.WithExplicitBucketBoundaries(0, 0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10),
	))
}

var (
	queueLengthMu        sync.RWMutex
	queueLengthObservers []func() int64
)

func RateLimitSynchronizerQueueLength() otelmetric.Int64ObservableGauge {
	return must.Get(Meter().Int64ObservableGauge(
		metricPrefix+"rate_limit_synchronizer_queue_length",
		otelmetric.WithDescription("Current rate limit synchronizer queue length."),
		otelmetric.WithInt64Callback(func(_ context.Context, observer otelmetric.Int64Observer) error {
			observer.Observe(observedRateLimitSynchronizerQueueLength())
			return nil
		}),
	))
}

func RegisterRateLimitSynchronizerQueueLength(observer func() int64) {
	queueLengthMu.Lock()
	if observer == nil {
		queueLengthObservers = nil
	} else {
		queueLengthObservers = append(queueLengthObservers, observer)
	}
	queueLengthMu.Unlock()
	_ = RateLimitSynchronizerQueueLength()
}

func observedRateLimitSynchronizerQueueLength() int64 {
	queueLengthMu.RLock()
	observers := append([]func() int64(nil), queueLengthObservers...)
	queueLengthMu.RUnlock()

	if len(observers) == 0 {
		return -1
	}
	var total int64
	for _, observer := range observers {
		total += observer()
	}
	return total
}

func RateLimitSynchronizerEventsDropped() otelmetric.Int64Counter {
	return must.Get(Meter().Int64Counter(
		metricPrefix+"rate_limit_synchronizer_events_dropped_total",
		otelmetric.WithDescription("Number of rate limit events dropped before publishing."),
	))
}

func DropReasonSameCluster() attribute.KeyValue {
	return attribute.String("reason", dropReasonSameCluster)
}

func DropReasonOldMessage() attribute.KeyValue {
	return attribute.String("reason", dropReasonOldMessage)
}

func DropReasonRemoteApplyDisabled() attribute.KeyValue {
	return attribute.String("reason", dropReasonRemoteApplyOff)
}

func SynchronizerDropReasonOldMessage() attribute.KeyValue {
	return attribute.String("reason", synchronizerDropOldMessage)
}
