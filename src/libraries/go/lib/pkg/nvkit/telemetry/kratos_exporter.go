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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	cloudevent "github.com/cloudevents/sdk-go/v2/event"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/metrics"
)

const (
	methodLabel                  = "method"
	sendSyncLabel                = "sendSync"
	sendAsyncLabel               = "sendAsync"
	sendSyncFailedLabel          = "sendSyncFailed"
	sendAsyncFailedLabel         = "sendAsyncFailed"
	sendAsyncListLabel           = "sendAsyncList"
	sendAsyncListFailedLabel     = "sendAsyncListFailed"
	sendBatchLabel               = "sendBatch"
	sendBatchFailedLabel         = "sendBatchFailed"
	createWorkerTasksLabel       = "createWorkerTasks"
	createWorkerTasksFailedLabel = "createWorkerTasksFailed"
)

var (
	metricsSubsystem     = "telemetry"
	defaultEventsCounter = metrics.CreateCounterVec(
		prometheus.DefaultRegisterer,
		"events_total",
		[]string{methodLabel},
		metricsSubsystem,
	)
	defaultBytesCounter = metrics.CreateCounterVec(
		prometheus.DefaultRegisterer,
		"bytes_total",
		[]string{methodLabel},
		metricsSubsystem,
	)
	defaultLatencyHistogram = metrics.CreateHistogramVec(
		prometheus.DefaultRegisterer,
		"latency_seconds",
		[]string{methodLabel},
		metricsSubsystem,
	)
)

// Exporter describes the functionality that is required to send cloud events
type Exporter interface {
	SendSync(ctx context.Context, events []cloudevent.Event) error
	SendAsync(ctx context.Context, event cloudevent.Event) error
	SendAsyncList(ctx context.Context, events []cloudevent.Event) error
}

// KratosExporter is a Kratos client that can post events to the Kratos data platform
type KratosExporter struct {
	cfg              *KratosExporterConfig
	endpoint         string
	client           *clients.HTTPClient
	cancelFunc       context.CancelFunc
	eventChannel     chan cloudevent.Event
	wg               sync.WaitGroup
	taskChannel      chan func()
	batchQueue       *batchQueue
	currBatch        *writeBatch
	batchMutex       sync.Mutex
	eventsCounter    *prometheus.CounterVec
	bytesCounter     *prometheus.CounterVec
	latencyHistogram *prometheus.HistogramVec
}

type writeBatch struct {
	time   time.Time
	events []cloudevent.Event
	size   int
	bytes  int64
	ready  chan struct{}
	done   chan struct{}
	timer  *time.Timer
	err    error
}

func newBatch(now time.Time, timeout time.Duration) *writeBatch {
	return &writeBatch{
		time:   now,
		events: []cloudevent.Event{},
		ready:  make(chan struct{}),
		done:   make(chan struct{}),
		timer:  time.NewTimer(timeout),
	}
}

func (b *writeBatch) add(event cloudevent.Event, maxSize int, maxBytes int64) bool {
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return false
	}
	eventSize := int64(len(eventBytes))
	if b.size > 0 && (b.bytes+eventSize) > maxBytes {
		return false
	}

	b.events = append(b.events, event)
	b.size++
	b.bytes += eventSize
	return true
}

func (b *writeBatch) full(maxSize int, maxBytes int64) bool {
	return b.size >= maxSize || b.bytes >= maxBytes
}

func (b *writeBatch) trigger() {
	close(b.ready)
}

func (b *writeBatch) complete(err error) {
	b.err = err
	close(b.done)
}

type batchQueue struct {
	queue  []*writeBatch
	mutex  sync.Mutex
	cond   *sync.Cond
	closed bool
}

func newBatchQueue(initialSize int) *batchQueue {
	bq := &batchQueue{
		queue: make([]*writeBatch, 0, initialSize),
	}
	bq.cond = sync.NewCond(&bq.mutex)
	return bq
}

func (b *batchQueue) Put(batch *writeBatch) bool {
	b.cond.L.Lock()
	defer b.cond.L.Unlock()
	defer b.cond.Broadcast()

	if b.closed {
		return false
	}
	b.queue = append(b.queue, batch)
	return true
}

func (b *batchQueue) Get() *writeBatch {
	b.cond.L.Lock()
	defer b.cond.L.Unlock()

	for len(b.queue) == 0 && !b.closed {
		b.cond.Wait()
	}

	if len(b.queue) == 0 {
		return nil
	}

	batch := b.queue[0]
	b.queue[0] = nil
	b.queue = b.queue[1:]

	return batch
}

func (b *batchQueue) Close() {
	b.cond.L.Lock()
	defer b.cond.L.Unlock()
	defer b.cond.Broadcast()

	b.closed = true
}

// SendSync sends the events synchronously, with a configurable batching mechanism
func (k *KratosExporter) SendSync(ctx context.Context, events []cloudevent.Event) error {
	var err error
	startTime := time.Now()
	label := sendSyncLabel
	defer func() {
		k.eventsCounter.WithLabelValues(label).Add(float64(len(events)))
		k.latencyHistogram.WithLabelValues(label).Observe(time.Since(startTime).Seconds())
	}()

	if len(events) == 0 {
		err = nverrors.ErrEmptyEventsList
		label = sendSyncFailedLabel
		return err
	}

	// Early size check to fail fast before batching
	reqBytes, err := json.Marshal(events)
	if err != nil {
		label = sendSyncFailedLabel
		return err
	}
	if len(reqBytes) > defaultMaxRequestSizeInBytes {
		err = nverrors.ErrEventsSizeExceedsLimit
		label = sendSyncFailedLabel
		return err
	}

	batches := k.batchEvents(events)

	// Wait for all batches to complete
	var lastErr error
	for batch := range batches {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			label = sendSyncFailedLabel
			return err
		case <-batch.done:
			if batch.err != nil {
				lastErr = batch.err
			}
		}
	}

	if lastErr != nil {
		label = sendSyncFailedLabel
	}
	return lastErr
}

func (k *KratosExporter) batchEvents(events []cloudevent.Event) map[*writeBatch]struct{} {
	k.batchMutex.Lock()
	defer k.batchMutex.Unlock()

	batches := make(map[*writeBatch]struct{})
	batchSize := k.cfg.MaxBatchSize
	batchBytes := int64(k.cfg.MaxBatchSizeInBytes)

	// Initialize batch queue if needed
	if k.batchQueue == nil {
		k.batchQueue = newBatchQueue(k.cfg.SyncBatchQueueSize)
		k.spawn(k.processBatches)
	}
	for i := range events {
	assignEvent:
		batch := k.currBatch
		if batch == nil {
			batch = k.newWriteBatch()
			k.currBatch = batch
		}

		if !batch.add(events[i], batchSize, batchBytes) {
			batch.trigger()
			k.batchQueue.Put(batch)
			k.currBatch = nil
			batches[batch] = struct{}{}
			goto assignEvent
		}

		if batch.full(batchSize, batchBytes) {
			batch.trigger()
			k.batchQueue.Put(batch)
			k.currBatch = nil
			batches[batch] = struct{}{}
		} else {
			batches[batch] = struct{}{}
		}
	}

	return batches
}

func (k *KratosExporter) newWriteBatch() *writeBatch {
	batch := newBatch(time.Now(), time.Duration(k.cfg.SyncBatchTimeoutMs)*time.Millisecond)
	k.spawn(func() { k.awaitBatch(batch) })
	return batch
}

// awaitBatch is spawned as a goroutine for each batch and waits for the batch to be put in the queue
// A write batch is put in the queue if size or bytes exceeds the configured limits
// or the timer expires
func (k *KratosExporter) awaitBatch(batch *writeBatch) {
	select {
	case <-batch.timer.C:
		k.batchMutex.Lock()
		if k.currBatch == batch {
			k.batchQueue.Put(batch)
			k.currBatch = nil
		}
		k.batchMutex.Unlock()
	case <-batch.ready:
		batch.timer.Stop()
	}
}

// processBatches is a worker that processes batches from the queue and sent to telemetry
func (k *KratosExporter) processBatches() {
	for {
		batch := k.batchQueue.Get()

		if batch == nil {
			return
		}

		err := k.sendSyncWithRetries(context.Background(), func() error {
			return k.sendSync(context.Background(), batch.events)
		})

		batch.complete(err)
	}
}

func (k *KratosExporter) spawn(f func()) {
	k.wg.Add(1)
	go func() {
		defer k.wg.Done()
		f()
	}()
}

func (k *KratosExporter) sendSyncWithRetries(ctx context.Context, operation func() error) error {
	var lastErr error
	maxRetries := k.cfg.SyncMaxRetries // additional retries after the first attempt

	// Always make at least one attempt, then up to maxRetries additional retries
	totalAttempts := maxRetries + 1

	for attempt := 0; attempt < totalAttempts; attempt++ {
		if ctx.Err() != nil {
			return fmt.Errorf("context err: %w", ctx.Err())
		}

		err := operation()
		if err == nil {
			return nil // success
		}

		lastErr = err

		// If we've exhausted all attempts, break and return error below
		if attempt >= totalAttempts-1 {
			break
		}

		// Exponential back-off before next retry
		delay := time.Duration(math.Pow(2, float64(attempt))) * time.Duration(k.cfg.SyncBaseDelayInSeconds) * time.Second
		zap.L().Warn("Retrying SendSync", zap.Int("attempt", attempt+1), zap.Int("maxRetries", maxRetries), zap.Duration("delay", delay), zap.Error(err))

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		case <-timer.C:
		}
	}

	return fmt.Errorf("SendSync failed after %d retries: %w", maxRetries, lastErr)
}

// SendAsync adds an event to the buffer which is flushed periodically
// If the buffer is full, it drops the events with warning
func (k *KratosExporter) SendAsync(ctx context.Context, event cloudevent.Event) error {
	startTime := time.Now()
	label := sendAsyncLabel
	defer func() {
		k.eventsCounter.WithLabelValues(label).Inc()
		k.latencyHistogram.WithLabelValues(label).Observe(time.Since(startTime).Seconds())
	}()
	select {
	case k.eventChannel <- event:
	default:
		zap.L().Warn("dropping event as internal buffer is full", zap.Int("len", len(k.eventChannel)),
			zap.Int("cap", cap(k.eventChannel)))
		label = sendAsyncFailedLabel
	}
	return nil
}

// SendAsyncMultiple adds events to buffer which are flushed periodically
// If the buffer is full, it drops the events with warning
func (k *KratosExporter) SendAsyncList(ctx context.Context, events []cloudevent.Event) error {
	startTime := time.Now()
	label := sendAsyncListLabel
	defer func() {
		k.eventsCounter.WithLabelValues(label).Add(float64(len(events)))
		k.latencyHistogram.WithLabelValues(label).Observe(time.Since(startTime).Seconds())
	}()

	for _, event := range events {
		select {
		case k.eventChannel <- event:
		default:
			zap.L().Warn("dropping event as internal buffer is full", zap.Int("len", len(k.eventChannel)),
				zap.Int("cap", cap(k.eventChannel)))
			label = sendAsyncListFailedLabel
		}
	}
	return nil
}

// sendSync sends a cloud event to the Kratos data platform synchronously without storing events in buffer.
func (k *KratosExporter) sendSync(ctx context.Context, events []cloudevent.Event) error {
	startTime := time.Now()
	inputLength := len(events)
	if inputLength == 0 {
		return nverrors.ErrEmptyEventsList
	}

	reqBytes, err := json.Marshal(events)
	if err != nil {
		zap.L().Error("error marshalling events", zap.Error(err))
		return err
	}

	// Check if the size exceeds 1MB (1MB = 1000000 bytes)
	if len(reqBytes) > defaultMaxRequestSizeInBytes {
		zap.L().Error("error total size of events exceeds 1MB", zap.Error(err))
		return nverrors.ErrEventsSizeExceedsLimit
	}

	req, err := k.buildRequest(ctx, events)
	if err != nil {
		zap.L().Error("error building request", zap.Error(err))
		return err
	}

	err = k.sendRequest(ctx, req)
	if err != nil {
		zap.L().Error("error sending request", zap.Error(err))
		k.bytesCounter.WithLabelValues(sendSyncFailedLabel).Add(float64(len(reqBytes)))
		return err
	}
	k.bytesCounter.WithLabelValues(sendSyncLabel).Add(float64(len(reqBytes)))

	endTime := time.Now()
	zap.L().Debug("Events sent to Kratos",
		zap.Int("eventCount", len(events)),
		zap.Int("size", len(reqBytes)),
		zap.Duration("totalTime", endTime.Sub(startTime)))
	return nil
}

func (k *KratosExporter) buildRequest(ctx context.Context, events []cloudevent.Event) (*http.Request, error) {
	reqBytes, err := json.Marshal(events)
	if err != nil {
		zap.L().Error("error marshalling events", zap.Error(err))
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.endpoint, bytes.NewBuffer(reqBytes))
	if err != nil {
		zap.L().Error("error creating request with context", zap.Error(err))
		return nil, err
	}
	req.Header.Set("Content-Type", cloudevent.ApplicationCloudEventsBatchJSON)
	req.Header.Set("Accept", "*/*")
	return req, nil
}

func (k *KratosExporter) sendRequest(ctx context.Context, req *http.Request) error {
	resp, err := k.client.Client(ctx).Do(req)
	defer k.closeResponse(resp)
	if err != nil {
		zap.L().Error("Error sending data to kratos", zap.Error(err))
		return err
	}

	var respBytes []byte
	if respBytes, err = io.ReadAll(resp.Body); err != nil {
		zap.L().Error("Response read error", zap.Error(err), zap.Int("Status code", resp.StatusCode))
		return fmt.Errorf("cannot read response body: %+v", err)
	}
	if resp.StatusCode != http.StatusOK {
		zap.L().Error("Invalid response status", zap.Int("Status code", resp.StatusCode))
		zap.L().Debug("Response", zap.Any("Headers", resp.Header), zap.ByteString("Body", respBytes))
		return fmt.Errorf("invalid response status: %d", resp.StatusCode)
	}

	return nil
}

func (k *KratosExporter) closeResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
}

// Close closes the internal buffered channel and waits for all events in the channel to be flushed to Kratos data collector.
// When the pod receives a SIGKILL/SIGTERM the client should invoke the Close() method and then all the events in the buffer will be flushed out.
func (k *KratosExporter) Close() {
	zap.L().Info("Closing channel")
	k.cancelFunc()

	// Flush any remaining current batch and all batches in the queue
	k.flushAllBatches()
	close(k.eventChannel)
	k.wg.Wait()
}

// startWorkerPool starts go routines responsible for sending batch of events (task) to Kratos
func (k *KratosExporter) startWorkerPool() {
	zap.L().Debug("Starting worker pool")
	for i := 0; i < k.cfg.MaxWorkers; i++ {
		k.wg.Add(1)
		go func() {
			defer k.wg.Done()
			for task := range k.taskChannel {
				// Execute the task
				task()
			}
			zap.L().Info("Task channel is closed, returning from worker goroutine")
		}()
	}
}

// flushPeriodically creates a periodic job for flushing the events from the buffer
func (k *KratosExporter) flushPeriodically(ctx context.Context) {
	k.wg.Add(1)
	go func() {
		defer k.wg.Done()
		ticker := time.NewTicker(time.Duration(k.cfg.FlushInterval) * time.Second)
		defer ticker.Stop()
		zap.L().Debug("Started periodic job for flushing metering events")
		for {
			select {
			case <-ticker.C:
				k.createWorkerTasks(context.Background())
			case <-ctx.Done():
				zap.L().Info("Received a stop signal, cancelling the periodic flush job")
				k.flushAll(context.Background())
				return
			}
		}
	}()
}

// createWorkerTasks removes all the events from eventChannel and adds batches of events to be sent to Kratos as
// a task to the taskChannel
func (k *KratosExporter) createWorkerTasks(ctx context.Context) {
	startTime := time.Now()
	batch := make([]cloudevent.Event, 0, k.cfg.MaxBatchSize)
	batchSize := 0
	for {
		select {
		case e, ok := <-k.eventChannel:
			if !ok {
				zap.L().Info("Event channel is closed")
				return
			}
			batch = append(batch, e)
			eventBytes, err := json.Marshal(e)
			if err != nil {
				zap.L().Warn("error marshalling event", zap.Any("event", e))
				k.eventsCounter.WithLabelValues(createWorkerTasksFailedLabel).Inc()
				continue
			}
			k.bytesCounter.WithLabelValues(createWorkerTasksLabel).Add(float64(len(eventBytes)))
			batchSize += len(eventBytes)
			if len(batch) == k.cfg.MaxBatchSize {
				batchCopy := make([]cloudevent.Event, len(batch))
				copy(batchCopy, batch)
				k.taskChannel <- func() {
					k.sendBatchWithRetries(ctx, batchCopy)
				}
				zap.L().Debug("Batch created and sent to task channel",
					zap.Int("length", len(batchCopy)),
					zap.Int("size", batchSize))
				batch = make([]cloudevent.Event, 0, k.cfg.MaxBatchSize)
				batchSize = 0
			}
		default:
			if len(batch) > 0 {
				batchCopy := make([]cloudevent.Event, len(batch))
				copy(batchCopy, batch)
				k.taskChannel <- func() {
					k.sendBatchWithRetries(ctx, batchCopy)
				}
				zap.L().Debug("Batch created and sent to task channel",
					zap.Int("length", len(batchCopy)),
					zap.Int("size", batchSize))
			}
			endTime := time.Now()
			zap.L().Debug("Create Worker Tasks finished", zap.Duration("totalTime", endTime.Sub(startTime)))
			return
		}
	}
}

// sendBatchWithRetries is used by async flush methods to send events to Kratos data collector with exponential
// backoff retry strategy.
func (k *KratosExporter) sendBatchWithRetries(ctx context.Context, events []cloudevent.Event) {
	if len(events) == 0 {
		return
	}
	zap.L().Debug("Batch flushed",
		zap.Int("length", len(events)))
	maxRetries := k.cfg.NumRetriesExponentialBackoff
	baseDelay := time.Duration(k.cfg.BaseDelayInSeconds) * time.Second
	var err error
	startTime := time.Now()
	defer func() {
		label := sendBatchLabel
		if err != nil {
			label = sendBatchFailedLabel
		}
		k.eventsCounter.WithLabelValues(label).Add(float64(len(events)))
		k.latencyHistogram.WithLabelValues(label).Observe(time.Since(startTime).Seconds())
	}()
	for i := 0; i < maxRetries; i++ {
		err = k.sendSync(ctx, events)
		if err == nil {
			return
		}
		zap.L().Warn("Retrying sending events", zap.Int("attempt", i))
		retry := math.Pow(2, float64(i))
		delay := time.Duration(retry) * baseDelay
		time.Sleep(delay)
	}
	if err != nil {
		zap.L().Warn("Exhausted the retry attempt", zap.Error(err))
		zap.L().Debug("Batch failed",
			zap.Int("length", len(events)))
	}
}

// flushAll publishes all events present in the buffered events channel to Kratos data collector.
func (k *KratosExporter) flushAll(ctx context.Context) {
	zap.L().Debug("Flush all invoked")
	startTime := time.Now()
	batch := make([]cloudevent.Event, 0, k.cfg.MaxBatchSize)
	batchSize := 0
	for event := range k.eventChannel {
		batch = append(batch, event)
		eventBytes, err := json.Marshal(event)
		if err != nil {
			zap.L().Warn("error marshalling event", zap.Any("event", event))
			k.eventsCounter.WithLabelValues(sendAsyncFailedLabel).Inc()
			continue
		}
		batchSize += len(eventBytes)
		if len(batch) == k.cfg.MaxBatchSize {
			batchCopy := make([]cloudevent.Event, len(batch))
			copy(batchCopy, batch)
			k.taskChannel <- func() {
				k.sendBatchWithRetries(ctx, batchCopy)
			}
			zap.L().Debug("Batch created and sent to task channel",
				zap.Int("length", len(batchCopy)),
				zap.Int("size", batchSize))
			batch = make([]cloudevent.Event, 0, k.cfg.MaxBatchSize)
			batchSize = 0
		}
	}
	if len(batch) > 0 {
		batchCopy := make([]cloudevent.Event, len(batch))
		copy(batchCopy, batch)
		k.taskChannel <- func() {
			k.sendBatchWithRetries(ctx, batchCopy)
		}
		zap.L().Debug("Batch created and sent to task channel",
			zap.Int("length", len(batchCopy)),
			zap.Int("size", batchSize))
	}
	zap.L().Debug("All events flushed to taskChannel.")
	endTime := time.Now()
	zap.L().Debug("Flush all finished", zap.Duration("totalTime", endTime.Sub(startTime)))
	close(k.taskChannel)
}

func (k *KratosExporter) flushCurrentBatch() {
	k.batchMutex.Lock()
	defer k.batchMutex.Unlock()

	if k.currBatch != nil && len(k.currBatch.events) > 0 {
		k.currBatch.trigger()
		if k.batchQueue != nil {
			k.batchQueue.Put(k.currBatch)
		}
		k.currBatch = nil
	}
}

func (k *KratosExporter) flushAllBatches() {
	zap.L().Debug("Flush all batches invoked")

	// First, flush any remaining current batch
	k.flushCurrentBatch()

	// Process all remaining batches in the queue
	if k.batchQueue != nil {
		// Close the batch queue first to signal no more batches will be added
		k.batchQueue.Close()

		// Then drain all existing batches
		for {
			batch := k.batchQueue.Get()
			if batch == nil {
				break // Queue is empty and closed
			}

			// Process the batch synchronously to ensure it's sent before shutdown
			err := k.sendSyncWithRetries(context.Background(), func() error {
				return k.sendSync(context.Background(), batch.events)
			})

			batch.complete(err)

			if err != nil {
				zap.L().Warn("Failed to send batch during shutdown",
					zap.Int("eventCount", len(batch.events)),
					zap.Error(err))
			} else {
				zap.L().Debug("Successfully sent batch during shutdown",
					zap.Int("eventCount", len(batch.events)))
			}
		}
	}

	zap.L().Debug("All batches flushed and processed")
}

// NewKratosExporter  creates a new Kratos Telemetry Client
func NewKratosExporter(cfg *KratosExporterConfig) (*KratosExporter, error) {
	httpClientCfg := &clients.HTTPClientConfig{
		BaseClientConfig: cfg.ClientCfg,
		NumRetries:       cfg.NumRetries,
	}
	var internalHttpClient *clients.HTTPClient
	var err error

	if cfg.CustomHTTPClient != nil {
		internalHttpClient = clients.NewHTTPClient(cfg.CustomHTTPClient, httpClientCfg)
	} else {
		internalHttpClient, err = clients.DefaultHTTPClient(httpClientCfg, func(_ string, r *http.Request) string {
			return "kratos.telemetry"
		})
	}

	if err != nil {
		zap.L().Error("error creating kratos http client", zap.Error(err))
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/api/%s/topic/%s", cfg.ClientCfg.Addr, cfg.APIVersion, cfg.CollectorID)
	ctx, cancelFunc := context.WithCancel(context.Background())
	k := &KratosExporter{
		cfg:              cfg,
		endpoint:         endpoint,
		client:           internalHttpClient,
		cancelFunc:       cancelFunc,
		eventChannel:     make(chan cloudevent.Event, cfg.MaxEvents),
		taskChannel:      make(chan func(), cfg.MaxBufferSize),
		batchQueue:       newBatchQueue(cfg.SyncBatchQueueSize),
		eventsCounter:    defaultEventsCounter,
		bytesCounter:     defaultBytesCounter,
		latencyHistogram: defaultLatencyHistogram,
	}

	k.flushPeriodically(ctx)
	k.startWorkerPool()
	k.spawn(k.processBatches)

	return k, nil
}
