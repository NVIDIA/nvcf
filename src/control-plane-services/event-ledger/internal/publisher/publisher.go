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

package publisher

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/interfaces"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/constants"
)

// // EventPublisher defines the interface for publishing single events.
// type EventPublisher interface {
//	Publish(ctx context.Context, event types.StageTransitionEvent)
//	PublishV2(ectx context.Context, vent types.DeploymentStageTransitionEvent)
// }
//
// // BatchStorageClient interface for clients saving events into storage systems
// type BatchStorageClient interface {
//	StoreBatch(bctx context.Context, atch []types.StageTransitionEvent) error
//	StoreBatchV2(bctx context.Context, atch []types.DeploymentStageTransitionEvent) error
// }

// BatchedPublisher implements the core functionality of batching and worker management
type BatchedPublisher struct {
	config *config.BatchedPublisherConfig

	// channel used to exchange events between the publisher instance and the worker goroutine
	eventCh         chan types.StageTransitionEvent
	eventV2Ch       chan types.DeploymentStageTransitionEvent
	storageClients  []interfaces.BatchStorageClient
	logger          *otelzap.Logger
	wg              sync.WaitGroup
	metricsItemsIn  metric.Int64Counter
	metricsItemsOut metric.Int64Counter
	metricsAttrV1   metric.MeasurementOption
	metricsAttrV2   metric.MeasurementOption
	// The stop flag is mostly used for tests so we can stopping or starting twice.
	// It is meant for the goroutine starting/stopping the publisher.
	// There are no locks or atomics used.
	stopped bool
}

// NewBasePublisher creates a new BasePublisher and starts the worker goroutine
func NewBatchedPublisher(
	config config.BatchedPublisherConfig,
	storageClients []interfaces.BatchStorageClient,
	logger *otelzap.Logger,
) (*BatchedPublisher, error) {
	if config.QueueSize <= 0 {
		return nil, fmt.Errorf("publisher queue size must be positive: %d", config.QueueSize)
	}
	if config.BatchSize <= 0 {
		return nil, fmt.Errorf("publisher batch size must be positive: %d", config.BatchSize)
	}
	if config.BatchIntervalSeconds <= 0 {
		return nil, fmt.Errorf(
			"publisher batch interval seconds must be positive: %d",
			config.BatchIntervalSeconds)
	}

	meter := otel.GetMeterProvider().Meter(constants.ApiSvcName)

	itemsIn, err := meter.Int64Counter(
		"fnds_publisher_events_in",
		metric.WithDescription("Events published"),
		metric.WithUnit("events"),
	)
	if err != nil {
		return nil, err
	}
	itemsOut, err := meter.Int64Counter(
		"fnds_publisher_events_out",
		metric.WithDescription("Events processed"),
		metric.WithUnit("events"),
	)
	if err != nil {
		return nil, err
	}

	publisher := &BatchedPublisher{
		config:          &config,
		eventCh:         make(chan types.StageTransitionEvent, config.QueueSize),
		eventV2Ch:       make(chan types.DeploymentStageTransitionEvent, config.QueueSize),
		storageClients:  storageClients,
		logger:          logger,
		stopped:         false,
		metricsItemsIn:  itemsIn,
		metricsItemsOut: itemsOut,
		metricsAttrV1:   metric.WithAttributeSet(attribute.NewSet(attribute.String("version", "1"))),
		metricsAttrV2:   metric.WithAttributeSet(attribute.NewSet(attribute.String("version", "2"))),
	}

	publisher.wg.Add(2) // Two workers now - one for each event type
	go publisher.run()
	go publisher.runV2()

	logger.Warn("batched publisher created",
		zap.Int("queue_size", config.QueueSize),
		zap.Int("batch_size", config.BatchSize),
		zap.Int64("batch_interval_seconds", config.BatchIntervalSeconds))

	return publisher, nil
}

// Publish adds an event to the publisher's queue
func (p *BatchedPublisher) Publish(ctx context.Context, event types.StageTransitionEvent) {
	// Can it happen that, the service is stopping but there are still requests being processed?
	// this means that they will try to Publish an event and the channel will be closed,
	// creating a runtime panic.
	// Tracing handled by external library
	traceCtx := ctx
	traceLogger := logging.NewTraceLogger(traceCtx, p.logger)
	traceLogger.DebugContext(traceCtx, "publishing stage transition event", zap.String("event", event.Event))
	select {
	case p.eventCh <- event:
		p.metricsItemsIn.Add(traceCtx, 1, p.metricsAttrV1)
		// Event successfully queued
	default:
		// Queue is full, log and drop the event
		traceLogger.ErrorContext(traceCtx, "publisher queue full, dropping event", zap.String("event", event.Event))
	}
}

// PublishV2 adds a deployment event to the publisher's V2 queue
func (p *BatchedPublisher) PublishV2(ctx context.Context, event types.DeploymentStageTransitionEvent) {
	// Tracing handled by external library
	traceCtx := ctx
	traceLogger := logging.NewTraceLogger(traceCtx, p.logger)
	traceLogger.DebugContext(traceCtx, "publishing deployment stage transition event", zap.String("event", event.Event))

	select {
	case p.eventV2Ch <- event:
		p.metricsItemsIn.Add(traceCtx, 1, p.metricsAttrV2)
		// Event successfully queued
	default:
		// Queue is full, log and drop the event
		traceLogger.ErrorContext(traceCtx, "publisher v2 queue full, dropping event", zap.String("event", event.Event))
	}
}

// Stop signals the publisher to stop processing and waits for it to finish gracefully
func (p *BatchedPublisher) Stop() {
	// No mutex here, assuming it is only called from the same goroutine
	if p.stopped {
		return
	}

	close(p.eventCh)
	close(p.eventV2Ch)

	p.wg.Wait() // Wait for both run goroutines to complete
	p.stopped = true
	p.logger.Info("publisher stopped")
}

// run is the main processing loop for the publisher
func (p *BatchedPublisher) run() {
	defer p.wg.Done()

	tickerDuration := time.Duration(p.config.BatchIntervalSeconds) * time.Second
	ticker := time.NewTicker(tickerDuration)
	defer ticker.Stop()

	var batch []types.StageTransitionEvent
	resetBatch := func() {
		batch = make([]types.StageTransitionEvent, 0, p.config.BatchSize)
	}
	resetBatch()

	writeBatch := func(msg string) {
		// Tracing handled by external library
		traceCtx := logging.AttachLoggerToContext(context.Background(), p.logger)

		traceLogger := logging.GetLogger(traceCtx)
		traceLogger.InfoContext(traceCtx, msg, zap.Int("batch_size", len(batch)))
		for _, client := range p.storageClients {
			if err := client.StoreBatch(traceCtx, batch); err != nil {
				traceLogger.ErrorContext(traceCtx, "failed to write batch", zap.Error(err))
			}
		}
		resetBatch()
	}

	p.logger.Warn("batchedpublisher worker started")

	for {
		select {
		case event, ok := <-p.eventCh:
			if !ok {
				// Channel closed, final batch processing
				p.logger.Warn("publisher event channel closed")
				writeBatch("writing final batch")
				return
			}
			p.metricsItemsOut.Add(context.Background(), 1, p.metricsAttrV1)
			batch = append(batch, event)
			if len(batch) >= p.config.BatchSize {
				writeBatch("publisher batch full, writing batch")
				ticker.Reset(tickerDuration)
			}
		case <-ticker.C:
			if len(batch) > 0 {
				writeBatch("publisher batch interval reached, writing batch")
			}
		}
	}
}

// runV2 is the main processing loop for the V2 publisher
func (p *BatchedPublisher) runV2() {
	defer p.wg.Done()

	tickerDuration := time.Duration(p.config.BatchIntervalSeconds) * time.Second
	ticker := time.NewTicker(tickerDuration)
	defer ticker.Stop()

	var batch []types.DeploymentStageTransitionEvent
	resetBatch := func() {
		batch = make([]types.DeploymentStageTransitionEvent, 0, p.config.BatchSize)
	}
	resetBatch()

	writeBatch := func(msg string) {
		// Tracing handled by external library
		traceCtx := logging.AttachLoggerToContext(context.Background(), p.logger)

		traceLogger := logging.GetLogger(traceCtx)
		traceLogger.InfoContext(traceCtx, msg, zap.Int("batch_size", len(batch)))
		for _, client := range p.storageClients {
			if err := client.StoreBatchV2(traceCtx, batch); err != nil {
				traceLogger.ErrorContext(traceCtx, "failed to write v2 batch", zap.Error(err))
			}
		}
		resetBatch()
	}

	p.logger.Warn("batchedpublisher v2 worker started")

	for {
		select {
		case event, ok := <-p.eventV2Ch:
			if !ok {
				// Channel closed, final batch processing
				p.logger.Warn("publisher v2 event channel closed")
				writeBatch("writing final v2 batch")
				return
			}
			p.metricsItemsOut.Add(context.Background(), 1, p.metricsAttrV2)
			batch = append(batch, event)
			if len(batch) >= p.config.BatchSize {
				writeBatch("publisher v2 batch full, writing batch")
				ticker.Reset(tickerDuration)
			}
		case <-ticker.C:
			if len(batch) > 0 {
				writeBatch("publisher v2 batch interval reached, writing batch")
			}
		}
	}
}
