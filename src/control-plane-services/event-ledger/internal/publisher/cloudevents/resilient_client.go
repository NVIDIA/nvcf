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

package cloudevents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/middleware"
)

type ResilienceConfig struct {
	RetryInterval    time.Duration
	MaxRetryAttempts int
	RetryBatchSize   int
	InstanceID       string
	LeaseDuration    time.Duration
}

func DefaultResilienceConfig() ResilienceConfig {
	// Generate a unique instance ID
	hostname, _ := os.Hostname()
	instanceID := fmt.Sprintf("%s-%d", hostname, os.Getpid())

	return ResilienceConfig{
		RetryInterval:    30 * time.Second,
		MaxRetryAttempts: 5,
		RetryBatchSize:   100,
		InstanceID:       instanceID,
		LeaseDuration:    90 * time.Second, // Leader lease duration (must be >= processing timeout)
	}
}

// ResilientCloudEventsClient wraps CloudEventsStorageClient with Cassandra fallback
type ResilientCloudEventsClient struct {
	cloudEventsClient *CloudEventsStorageClient
	resilienceHandler data_access.CloudEventsResilienceHandler
	logger            *otelzap.Logger
	config            ResilienceConfig
	stopCh            chan struct{}
	stopOnce          sync.Once
	metrics           *middleware.CloudEventsMetrics
}

// NewResilientCloudEventsClient creates a new resilient CloudEvents client with Cassandra fallback.
func NewResilientCloudEventsClient(
	cfg config.CloudEventsConfig,
	resilienceHandler data_access.CloudEventsResilienceHandler,
	logger *otelzap.Logger,
	metrics *middleware.CloudEventsMetrics,
) (*ResilientCloudEventsClient, error) {

	// Create the underlying CloudEvents client
	cloudEventsClient, err := NewCloudEventsStorageClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create CloudEvents client: %w", err)
	}

	resilienceConfig := DefaultResilienceConfig()

	client := &ResilientCloudEventsClient{
		cloudEventsClient: cloudEventsClient,
		resilienceHandler: resilienceHandler,
		logger:            logger,
		config:            resilienceConfig,
		stopCh:            make(chan struct{}),
		metrics:           metrics,
	}

	// Start the retry worker (all instances participate in leader election)
	go client.retryWorker()

	logger.Info("resilient CloudEvents client created",
		zap.String("instance_id", resilienceConfig.InstanceID))

	return client, nil
}

// StoreBatch attempts to send events to CloudEvents, falls back to Cassandra on failure
func (c *ResilientCloudEventsClient) StoreBatch(ctx context.Context, batch []types.StageTransitionEvent) error {
	// Try to send to CloudEvents first
	err := c.cloudEventsClient.StoreBatch(ctx, batch)
	if err == nil {
		c.logger.DebugContext(ctx, "successfully sent batch to CloudEvents", zap.Int("batch_size", len(batch)))
		return nil
	}

	// CloudEvents failed, store in Cassandra for later retry
	c.logger.WarnContext(ctx, "CloudEvents send failed, storing in Cassandra for retry",
		zap.Error(err), zap.Int("batch_size", len(batch)))

	// Serialize for storage
	eventData, marshalErr := json.Marshal(batch)
	if marshalErr != nil {
		return fmt.Errorf("CloudEvents failed: %w (also failed to marshal for fallback: %w)", err, marshalErr)
	}

	// Store in Cassandra
	if storeErr := c.resilienceHandler.StoreFailedCloudEvent(ctx, "v1", eventData); storeErr != nil {
		c.logger.ErrorContext(ctx, "failed to store CloudEvents in Cassandra fallback", zap.Error(storeErr))
		// Return the original CloudEvents error, not the Cassandra error
		return fmt.Errorf("CloudEvents failed: %w (also failed to store fallback: %w)", err, storeErr)
	}

	// Record fallback metric
	c.metrics.EventsFallbackCounter.Add(ctx, int64(len(batch)),
		metric.WithAttributes(
			attribute.String("event_type", "v1"),
		))

	c.logger.DebugContext(ctx, "stored failed CloudEvents in Cassandra for retry")
	return nil
}

// StoreBatchV2 attempts to send V2 events to CloudEvents, falls back to Cassandra on failure
func (c *ResilientCloudEventsClient) StoreBatchV2(ctx context.Context, batch []types.DeploymentStageTransitionEvent) error {
	// Try to send to CloudEvents first
	err := c.cloudEventsClient.StoreBatchV2(ctx, batch)
	if err == nil {
		c.logger.DebugContext(ctx, "successfully sent v2 batch to CloudEvents", zap.Int("batch_size", len(batch)))
		return nil
	}

	// CloudEvents failed, store in Cassandra for later retry
	c.logger.WarnContext(ctx, "CloudEvents v2 send failed, storing in Cassandra for retry",
		zap.Error(err), zap.Int("batch_size", len(batch)))

	// Serialize for storage
	eventData, marshalErr := json.Marshal(batch)
	if marshalErr != nil {
		return fmt.Errorf("CloudEvents v2 failed: %w (also failed to marshal for fallback: %w)", err, marshalErr)
	}

	// Store in Cassandra
	if storeErr := c.resilienceHandler.StoreFailedCloudEvent(ctx, "v2", eventData); storeErr != nil {
		c.logger.ErrorContext(ctx, "failed to store CloudEvents v2 in Cassandra fallback", zap.Error(storeErr))
		// Return the original CloudEvents error, not the Cassandra error
		return fmt.Errorf("CloudEvents v2 failed: %w (also failed to store fallback: %w)", err, storeErr)
	}

	// Record fallback metric
	c.metrics.EventsFallbackCounter.Add(ctx, int64(len(batch)),
		metric.WithAttributes(
			attribute.String("event_type", "v2"),
		))

	c.logger.DebugContext(ctx, "stored failed CloudEvents v2 in Cassandra for retry")
	return nil
}

// retryWorker participates in leader election and processes retries if leader
func (c *ResilientCloudEventsClient) retryWorker() {
	ticker := time.NewTicker(c.config.RetryInterval)
	defer ticker.Stop()

	isLeader := false

	c.logger.Info("CloudEvents retry worker started",
		zap.String("instance", c.config.InstanceID),
		zap.Duration("interval", c.config.RetryInterval))

	for {
		select {
		case <-ticker.C:
			// Try to become or renew leadership
			ctx := logging.AttachLoggerToContext(context.Background(), c.logger)
			becameLeader, err := c.resilienceHandler.TryBecomeLeader(ctx, c.config.InstanceID, c.config.LeaseDuration)
			if err != nil {
				c.logger.Error("failed to check/renew leadership", zap.Error(err))
				isLeader = false
				continue
			}

			// Log leadership changes
			if becameLeader && !isLeader {
				c.logger.Info("became CloudEvents retry leader", zap.String("instance", c.config.InstanceID))
			} else if !becameLeader && isLeader {
				c.logger.Info("lost CloudEvents retry leadership", zap.String("instance", c.config.InstanceID))
			}

			isLeader = becameLeader

			// Only process failed events if we are the leader
			if isLeader {
				c.processFailedEvents()
			}

		case <-c.stopCh:
			c.logger.Info("CloudEvents retry worker stopped", zap.String("instance", c.config.InstanceID))
			return
		}
	}
}

// processFailedEvents retrieves and retries failed CloudEvents
func (c *ResilientCloudEventsClient) processFailedEvents() {
	// Use a context with timeout at 90% of lease duration to ensure we finish before lease expires
	// This prevents another instance from taking over mid-processing
	processingTimeout := time.Duration(float64(c.config.LeaseDuration) * 0.9)
	// Attach logger to context to prevent panic in downstream calls
	loggerCtx := logging.AttachLoggerToContext(context.Background(), c.logger)
	ctx, cancel := context.WithTimeout(loggerCtx, processingTimeout)
	defer cancel()

	// Get the iterator for streaming failed events
	iterator := c.resilienceHandler.StreamFailedCloudEvents(ctx, c.config.RetryBatchSize)

	processedCount := 0

	// Process events using the iterator
	// The iterator will automatically handle pagination and streaming
	for failedEvent := range iterator {
		// Skip events that have exceeded max retry attempts
		// We keep them in Cassandra but don't retry them anymore
		if failedEvent.RetryCount >= c.config.MaxRetryAttempts {
			c.logger.Warn("skipping event that exceeded max retry attempts",
				zap.String("id", failedEvent.ID.String()),
				zap.Int("retry_count", failedEvent.RetryCount),
				zap.Int("max_attempts", c.config.MaxRetryAttempts))

			// Record metric
			c.metrics.EventsSkippedCounter.Add(ctx, 1,
				metric.WithAttributes(
					attribute.String("event_type", failedEvent.EventType),
				))
			continue
		}

		// Check if enough time has passed since last retry (exponential backoff)
		backoffDuration := c.calculateBackoff(failedEvent.RetryCount)
		if time.Since(failedEvent.LastRetryTimestamp) < backoffDuration {
			continue // Skip this event, not enough time has passed
		}

		// Retry the event
		if c.retryEvent(ctx, failedEvent) {
			processedCount++

			// Record successful retry metric
			c.metrics.EventsRetriedCounter.Add(ctx, 1,
				metric.WithAttributes(
					attribute.String("event_type", failedEvent.EventType),
				))

			// Success! Delete from Cassandra
			if err := c.resilienceHandler.DeleteFailedCloudEvent(ctx, failedEvent.ID); err != nil {
				c.logger.Error("failed to delete successfully retried CloudEvent",
					zap.String("id", failedEvent.ID.String()), zap.Error(err))
			} else {
				c.logger.Info("successfully retried CloudEvent",
					zap.String("id", failedEvent.ID.String()),
					zap.String("type", failedEvent.EventType))
			}
		} else {
			// Failed again, update retry count
			newRetryCount := failedEvent.RetryCount + 1
			if err := c.resilienceHandler.UpdateFailedCloudEventRetry(ctx, failedEvent.ID, newRetryCount); err != nil {
				c.logger.Error("failed to update retry count for CloudEvent",
					zap.String("id", failedEvent.ID.String()), zap.Error(err))
			}
		}
	}

	if processedCount > 0 {
		c.logger.Debug("processed failed CloudEvents", zap.Int("count", processedCount))
	}
}

// retryEvent attempts to resend a failed CloudEvent
func (c *ResilientCloudEventsClient) retryEvent(ctx context.Context, failedEvent data_access.FailedCloudEvent) bool {

	switch failedEvent.EventType {
	case "v1":
		var batch []types.StageTransitionEvent
		if err := json.Unmarshal([]byte(failedEvent.EventData), &batch); err != nil {
			c.logger.Error("failed to unmarshal v1 event data",
				zap.String("id", failedEvent.ID.String()), zap.Error(err))
			return false
		}

		// Send directly to CloudEvents client (bypassing our wrapper to avoid re-fallback)
		err := c.cloudEventsClient.StoreBatch(ctx, batch)
		if err != nil {
			c.logger.Debug("retry failed for v1 CloudEvent",
				zap.String("id", failedEvent.ID.String()), zap.Error(err))
			return false
		}
		return true

	case "v2":
		var batch []types.DeploymentStageTransitionEvent
		if err := json.Unmarshal([]byte(failedEvent.EventData), &batch); err != nil {
			c.logger.Error("failed to unmarshal v2 event data",
				zap.String("id", failedEvent.ID.String()), zap.Error(err))
			return false
		}

		// Send directly to CloudEvents client (bypassing our wrapper to avoid re-fallback)
		err := c.cloudEventsClient.StoreBatchV2(ctx, batch)
		if err != nil {
			c.logger.Debug("retry failed for v2 CloudEvent",
				zap.String("id", failedEvent.ID.String()), zap.Error(err))
			return false
		}
		return true

	default:
		c.logger.Error("unknown event type in retry",
			zap.String("type", failedEvent.EventType),
			zap.String("id", failedEvent.ID.String()))
		return false
	}
}

// calculateBackoff calculates exponential backoff duration based on retry count
func (c *ResilientCloudEventsClient) calculateBackoff(retryCount int) time.Duration {
	// Exponential backoff: 1min, 2min, 4min, 8min, 16min...
	backoffMinutes := 1 << retryCount // 2^retryCount
	if backoffMinutes > 60 {          // Cap at 60 minutes
		backoffMinutes = 60
	}
	return time.Duration(backoffMinutes) * time.Minute
}

// Stop gracefully stops the retry worker
func (c *ResilientCloudEventsClient) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
		c.logger.Info("resilient CloudEvents client stopped")
	})
}
