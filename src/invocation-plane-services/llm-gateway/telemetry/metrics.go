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

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
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
	_ = PubSubPublishFailures()
	_ = PubSubConsumeFailures()
	_ = PubSubConsumeDuration()
	_ = RateLimitEventReplicationLag()
	_ = RateLimitEventReceived()
	_ = RateLimitEventDroppedSameCluster()
	_ = RateLimitEventDroppedOldMessage()
	_ = RateLimitEventApplied()
	_ = RateLimitEventFailedToApply()
	_ = RateLimitEventDryRunWouldApply()
	_ = RateLimitPubsubPublishTime()
	_ = RateLimitPubsubQueueWaitTime()
	_ = RateLimitPublisherEventsDroppedOldMessage()
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

var (
	PubSubPublishFailures = sync.OnceValue(func() otelmetric.Int64Counter {
		return must.Get(Meter().Int64Counter(
			"orion.pubsub.publish_failures",
			otelmetric.WithDescription("Number of messages failing to be published to a topic"),
		))
	})

	PubSubConsumeFailures = sync.OnceValue(func() otelmetric.Int64Counter {
		return must.Get(Meter().Int64Counter(
			"orion.pubsub.consume_failures",
			otelmetric.WithDescription("Number of messages failing to be consumed from a subscription"),
		))
	})

	PubSubConsumeDuration = sync.OnceValue(func() otelmetric.Float64Histogram {
		return must.Get(Meter().Float64Histogram(
			"orion.pubsub.consume_duration",
			otelmetric.WithUnit("s"),
			otelmetric.WithDescription("Time taken to consume a message from a subscription"),
			otelmetric.WithExplicitBucketBoundaries(0, 0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 1, 5, 10, 60, 120),
		))
	})

	RateLimitEventReplicationLag = sync.OnceValue(func() otelmetric.Float64Histogram {
		return must.Get(Meter().Float64Histogram(
			"orion.pubsub.rate_limit_events.consumer.replication_lag",
			otelmetric.WithUnit("s"),
			otelmetric.WithDescription("Measure of lag between when a rate limit event is created and when it is processed"),
			otelmetric.WithExplicitBucketBoundaries(DurationBuckets...),
		))
	})

	RateLimitEventReceived = sync.OnceValue(func() otelmetric.Int64Counter {
		return must.Get(Meter().Int64Counter(
			"orion.pubsub.rate_limit_events.consumer.events_received",
			otelmetric.WithDescription("Number of rate limit events we received from pubsub"),
		))
	})

	RateLimitEventDroppedSameCluster = sync.OnceValue(func() otelmetric.Int64Counter {
		return must.Get(Meter().Int64Counter(
			"orion.pubsub.rate_limit_events.consumer.events_dropped_same_cluster",
			otelmetric.WithDescription("Number of rate limit events we dropped because they were from the same cluster"),
		))
	})

	RateLimitEventDroppedOldMessage = sync.OnceValue(func() otelmetric.Int64Counter {
		return must.Get(Meter().Int64Counter(
			"orion.pubsub.rate_limit_events.consumer.events_dropped_old_message",
			otelmetric.WithDescription("Number of rate limit events we dropped because they were too old"),
		))
	})

	RateLimitEventApplied = sync.OnceValue(func() otelmetric.Int64Counter {
		return must.Get(Meter().Int64Counter(
			"orion.pubsub.rate_limit_events.consumer.events_applied",
			otelmetric.WithDescription("Number of rate limit events we applied to the local cluster"),
		))
	})

	RateLimitEventFailedToApply = sync.OnceValue(func() otelmetric.Int64Counter {
		return must.Get(Meter().Int64Counter(
			"orion.pubsub.rate_limit_events.consumer.events_failed_apply",
			otelmetric.WithDescription("Number of rate limit events we failed to apply to the local cluster"),
		))
	})

	RateLimitEventDryRunWouldApply = sync.OnceValue(func() otelmetric.Int64Counter {
		return must.Get(Meter().Int64Counter(
			"orion.pubsub.rate_limit_events.consumer.events_dry_run_would_apply",
			otelmetric.WithDescription("Number of rate limit events we would apply if we weren't in dry run mode"),
		))
	})

	RateLimitPubsubPublishTime = sync.OnceValue(func() otelmetric.Float64Histogram {
		return must.Get(Meter().Float64Histogram(
			"orion.pubsub.rate_limit_events.synchronizer_publish_time",
			otelmetric.WithUnit("s"),
			otelmetric.WithDescription("Measure of how long it took to send a Rate Limit Event to pubsub"),
			otelmetric.WithExplicitBucketBoundaries(DurationBuckets...),
		))
	})

	RateLimitPubsubQueueWaitTime = sync.OnceValue(func() otelmetric.Float64Histogram {
		return must.Get(Meter().Float64Histogram(
			"orion.pubsub.rate_limit_events.synchronizer_queue_wait_time",
			otelmetric.WithUnit("s"),
			otelmetric.WithDescription("Measure of how long it took to queue a Rate Limit Event onto the channel"),
			otelmetric.WithExplicitBucketBoundaries(0, 0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10),
		))
	})

	RateLimitPublisherEventsDroppedOldMessage = sync.OnceValue(func() otelmetric.Int64Counter {
		return must.Get(Meter().Int64Counter(
			"orion.pubsub.rate_limit_events.synchronizer_events_dropped_old_message",
			otelmetric.WithDescription("Number of rate limit events we dropped in the publisher because they were too old"),
		))
	})
)
