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

package consumer

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/semaphore"
)

var tracer = otel.Tracer("nats-consumer")

type GlobalNatsConsumer struct {
	primaryRegion     string
	functionVersionId string

	conn *nats.Conn

	primaryConsumer    jetstream.Consumer
	secondaryConsumers map[string]jetstream.Consumer

	workLimiter         *semaphore.Weighted
	notifyWorkCompleted *sync.Cond

	currentlyConnectedRegionsLock sync.Mutex
	currentlyConnectedRegions     map[string]struct{}

	maxPullBatchSize atomic.Int64
}

type ProvisionRegionFunc func(ctx context.Context, region string) error

func NewNatsConsumer(ctx context.Context, js jetstream.JetStream, provisionRegionFunc ProvisionRegionFunc, functionVersionId string, primaryRegion string, secondaryRegions []string, maxRequestConcurrency int) (*GlobalNatsConsumer, error) {
	consumer, err := getRegionalNatsConsumer(ctx, js, primaryRegion, functionVersionId, provisionRegionFunc)
	if err != nil {
		return nil, fmt.Errorf("failed to create consumer for primary region %s: %w", primaryRegion, err)
	}

	c := &GlobalNatsConsumer{
		primaryRegion:       primaryRegion,
		primaryConsumer:     consumer,
		functionVersionId:   functionVersionId,
		conn:                js.Conn(),
		workLimiter:         semaphore.NewWeighted(int64(maxRequestConcurrency)),
		notifyWorkCompleted: sync.NewCond(&sync.Mutex{}),
		secondaryConsumers:  make(map[string]jetstream.Consumer),
	}
	c.maxPullBatchSize.Store(int64(maxRequestConcurrency))
	for _, region := range secondaryRegions {
		consumer, err := getRegionalNatsConsumer(ctx, js, region, functionVersionId, provisionRegionFunc)
		if err != nil {
			// other regions are best effort. they may have gone down, etc.
			zap.L().Error("failed to create consumer for secondary region", zap.String("region", region), zap.Error(err))
			continue
		}
		c.secondaryConsumers[region] = consumer
	}

	c.currentlyConnectedRegions = map[string]struct{}{primaryRegion: {}}
	for region := range c.secondaryConsumers {
		c.currentlyConnectedRegions[region] = struct{}{}
	}

	return c, nil
}

func (c *GlobalNatsConsumer) GetCurrentlyConnectedRegions() map[string]struct{} {
	c.currentlyConnectedRegionsLock.Lock()
	defer c.currentlyConnectedRegionsLock.Unlock()
	return maps.Clone(c.currentlyConnectedRegions)
}

// nats name formats
// request queue stream
// rq_$region_$functionVersionId
// request queue consumer name
// rq_$region_$functionVersionId_workers
// response queue
// rsq_$region_functionVersionId
func getRegionalNatsConsumer(ctx context.Context, js jetstream.JetStream, region string, functionVersionId string, provisionRegionFunc ProvisionRegionFunc) (jetstream.Consumer, error) {
	streamName := fmt.Sprintf("rq_%s_%s", region, functionVersionId)
	consumerName := fmt.Sprintf("%s_workers", streamName)
	var consumer jetstream.Consumer
	err := backoff.Retry(func() error {
		var err error
		consumer, err = js.Consumer(ctx, streamName, consumerName)
		if err != nil {
			zap.L().Debug("failed to connect to nats consumer", zap.String("streamName", streamName), zap.Error(err))
			var jsApiErr *jetstream.APIError
			if errors.As(err, &jsApiErr) && jsApiErr.ErrorCode == jetstream.JSErrCodeStreamNotFound {
				err = jetstream.ErrStreamNotFound
			}
			// some unknown error, pass it back up to retry
			if !errors.Is(err, jetstream.ErrStreamNotFound) && !errors.Is(err, jetstream.ErrConsumerNotFound) {
				return err
			}
			// error is because we are missing a stream or consumer. try creating them.
			err = provisionRegionFunc(ctx, region)
			if err != nil {
				return err
			}
			// try again immediately if provisioning was successful
			consumer, err = js.Consumer(ctx, streamName, consumerName)
		}
		return err
	}, backoff.WithContext(backoff.NewExponentialBackOff(backoff.WithMaxElapsedTime(5*time.Second)), ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to create consumer %s for stream %s: %w", consumerName, streamName, err)
	}
	return consumer, nil
}

// FetchWorkStream streams work as it becomes available.
//
// never request more work from the primary region than you can currently work on
// if work is pulled from other regions, nack work that you don't have capacity for
// if work is pulled from the primary region and other-region work took capacity that was available at pull time but is now taken, nack
// don't pull work from other regions unless there is work capacity
//
// workLimiter - guards working on work. acquire prior to work. release after work is completed.
// maxReq - the available work capacity at time of fetch. don't grab the work capacity though.
// notifyWorkCompleted - wakes up consumers that are waiting on available work capacity to start fetching again.
func (c *GlobalNatsConsumer) FetchWorkStream(ctx context.Context) (<-chan *WorkRequest, error) {
	reconnects := c.watchReconnects(ctx)

	primaryChan := make(chan *WorkRequest)
	go func() {
		_ = c.singleConsumer(ctx, c.primaryConsumer, c.primaryRegion, reconnects, primaryChan)
		c.currentlyConnectedRegionsLock.Lock()
		delete(c.currentlyConnectedRegions, c.primaryRegion)
		c.currentlyConnectedRegionsLock.Unlock()
		close(primaryChan)
	}()

	secondaryChan := make(chan *WorkRequest)
	wg := sync.WaitGroup{}
	for region, consumer := range c.secondaryConsumers {
		region := region // go loop var shadowing
		consumer := consumer
		wg.Add(1)
		go func() {
			_ = c.singleConsumer(ctx, consumer, region, reconnects, secondaryChan)
			c.currentlyConnectedRegionsLock.Lock()
			delete(c.currentlyConnectedRegions, region)
			c.currentlyConnectedRegionsLock.Unlock()
			wg.Done()
		}()
	}
	go func() {
		wg.Wait()
		close(secondaryChan)
	}()

	biasedChan := make(chan *WorkRequest)
	go func(primaryChan <-chan *WorkRequest, secondaryChan <-chan *WorkRequest) {
		for {
			var msg *WorkRequest
			var ok bool
			select {
			case msg, ok = <-primaryChan:
				if !ok {
					primaryChan = nil
				}
			default:
				select {
				case msg, ok = <-primaryChan:
					if !ok {
						primaryChan = nil
					}
				case msg, ok = <-secondaryChan:
					if !ok {
						secondaryChan = nil
					}
				}
			}
			if primaryChan == nil && secondaryChan == nil {
				break
			}
			// will be nil if we hit a loop that detected a closed stream
			if msg == nil {
				continue
			}
			// grab the work slot *after* biasing towards primary region
			if !c.workLimiter.TryAcquire(1) {
				zap.L().Debug("nacking message due to work limiter", zap.String("subject", msg.Subject()))
				_ = msg.Nak()
				continue
			}
			// after this point if we drop msg we need to Close it to release the workLimiter
			biasedChan <- msg
		}
		close(biasedChan)
	}(primaryChan, secondaryChan)

	return biasedChan, nil
}

// reconnectSignal hands out a channel that is closed on the next NATS reconnect.
type reconnectSignal struct {
	mu sync.Mutex
	ch chan struct{}
}

func newReconnectSignal() *reconnectSignal {
	return &reconnectSignal{ch: make(chan struct{})}
}

func (r *reconnectSignal) next() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ch
}

func (r *reconnectSignal) fire() {
	r.mu.Lock()
	defer r.mu.Unlock()
	close(r.ch)
	r.ch = make(chan struct{})
}

// watchReconnects fires the returned signal every time the NATS connection
// reconnects, until ctx is done.
//
// Fetch does not re-issue its pull request after a reconnect. If a message
// reached the consumer while this worker was disconnected, the server drops
// the pull because nobody was listening on its reply subject, and the fetch
// sits idle until it misses two heartbeats. Requests queued in that window
// wait the whole time, so fetches re-pull as soon as the connection is back.
func (c *GlobalNatsConsumer) watchReconnects(ctx context.Context) *reconnectSignal {
	signal := newReconnectSignal()
	if c.conn == nil {
		return signal
	}
	statusChanged := c.conn.StatusChanged(nats.CONNECTED)
	go func() {
		defer c.conn.RemoveStatusListener(statusChanged)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-statusChanged:
				if !ok {
					return
				}
				signal.fire()
			}
		}
	}()
	return signal
}

// singleConsumer is a blocking call
func (c *GlobalNatsConsumer) singleConsumer(ctx context.Context, consumer jetstream.Consumer, region string, reconnects *reconnectSignal, msgChan chan<- *WorkRequest) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// this adds an internal retry per consumer to recover from normal events like stream leadership changes which produce errors in only one region at a time
		// without having to wait for the global connected region check to recreate all consumers
		// using maxRetries instead of a time cap so if the failure happens during a Fetch the time would not have already counted against the retries
		backoffConfig := backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(backoff.WithMaxElapsedTime(0)), 10), ctx)
		err := backoff.Retry(func() error {
			// msgs will close and drain by itself on cancellation
			msgs, fetchErrChan, err := c.fetchMax(ctx, consumer, reconnects)
			if err != nil {
				return err
			}
			for msg := range msgs {
				// creating a work request does not check the work limiter, so don't Close release unless we have reserved capacity later
				request, err := NewWorkRequest(msg, region, c.releaseWork)
				if err != nil {
					zap.L().Error("failed to create work request", zap.Error(err))
					continue
				}
				msgChan <- request
			}
			if err := <-fetchErrChan; err != nil {
				adjusted, adjustErr := c.adjustMaxBatchIfNeeded(err)
				if adjustErr != nil {
					return adjustErr
				}
				// don't print the fetch error if we successfully adjusted
				if adjusted {
					return err
				}
				zap.L().Error("failed to fetch work requests. exiting work consumer.", zap.String("region", region), zap.Error(err))
				return err
			}
			return nil
		}, backoffConfig)
		if err != nil {
			return err
		}
	}
}

// returns whether the batch size was adjusted, and if there was an error adjusting the batch size
func (c *GlobalNatsConsumer) adjustMaxBatchIfNeeded(err error) (bool, error) {
	// error from nats is a string "Exceeded MaxRequestBatch of %d" and is not typed
	// check for match and pull the max request batch size from the error
	renderedErr := err.Error()
	const exceededMaxRequestBatchPrefix = "nats: Exceeded MaxRequestBatch of "
	if strings.HasPrefix(renderedErr, exceededMaxRequestBatchPrefix) {
		maxReq := renderedErr[len(exceededMaxRequestBatchPrefix):]
		maxReqInt, err := strconv.ParseInt(maxReq, 10, 64)
		if err != nil {
			// an error here would indicate the nats server changed its message
			zap.L().Error("failed to convert max request batch size to int", zap.Error(err))
			return false, err
		}
		c.maxPullBatchSize.Store(maxReqInt)
		zap.L().Info("max request batch size exceeded, updating max pull batch size", zap.Int64("maxPullBatchSize", maxReqInt))
		return true, nil
	}
	return false, nil
}

func (c *GlobalNatsConsumer) fetchMax(ctx context.Context, consumer jetstream.Consumer, reconnects *reconnectSignal) (<-chan jetstream.Msg, <-chan error, error) {
	// wait until there is space available before optimistically pulling
	// we may end up having to nack if another region comes in first
	c.notifyWorkCompleted.L.Lock()
	maxReq := c.workLimiter.MaxAvailable()
	for maxReq <= 0 {
		c.notifyWorkCompleted.Wait()
		maxReq = c.workLimiter.MaxAvailable()
	}
	defer c.notifyWorkCompleted.L.Unlock()

	// don't pull more than the max batch size even if we have work space
	maxPullBatchSize := c.maxPullBatchSize.Load()
	if maxPullBatchSize < maxReq {
		maxReq = maxPullBatchSize
	}

	return fetchMax(ctx, consumer, maxReq, reconnects.next())
}

func endFetchSpan(fetchSpan trace.Span, err error, messagesFetched int) {
	if err != nil {
		fetchSpan.RecordError(err)
		fetchSpan.SetStatus(codes.Error, err.Error())
	}
	fetchSpan.SetAttributes(attribute.Int("messagesFetched", messagesFetched))
	fetchSpan.End()
}

func (c *GlobalNatsConsumer) releaseWork() {
	c.workLimiter.Release(1)
	c.notifyWorkCompleted.L.Lock()
	c.notifyWorkCompleted.Broadcast()
	c.notifyWorkCompleted.L.Unlock()
}

// fetchMax will close and drain the returned stream on context cancellation.
// The fetch ends without an error when reconnected is closed, so the caller
// issues a new pull request right away.
func fetchMax(ctx context.Context, consumer jetstream.Consumer, maxReq int64, reconnected <-chan struct{}) (<-chan jetstream.Msg, <-chan error, error) {
	zap.L().Debug("sending nats fetch request", zap.Int64("maxReq", maxReq), zap.String("consumer", consumer.CachedInfo().Name))
	ctx, fetchSpan := tracer.Start(ctx, "Fetch", trace.WithSpanKind(trace.SpanKindClient), trace.WithNewRoot(), trace.WithAttributes(attribute.Int64("maxReq", maxReq), attribute.String("consumer", consumer.CachedInfo().Name)))
	// Detached from ctx: on ctx cancellation the fetch keeps running so the
	// drain below can nack whatever the server still delivers to it.
	// FetchContext sets the pull expiry to the deadline minus up to 1s, and
	// cannot be combined with FetchMaxWait.
	fetchCtx, cancelFetch := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute+time.Second)
	activeFetch, err := consumer.Fetch(int(maxReq), jetstream.FetchHeartbeat(5*time.Second), jetstream.FetchContext(fetchCtx))
	if err != nil {
		cancelFetch()
		return nil, nil, err
	}
	go func() {
		select {
		case <-reconnected:
			cancelFetch()
		case <-fetchCtx.Done():
		}
	}()

	retChan := make(chan jetstream.Msg, maxReq)
	errChan := make(chan error, 1) // buffered, so we can exit this goroutine and defer close the ret chan. the error chan is checked afterward.
	go func() {
		defer close(retChan)
		defer close(errChan)
		messagesFetched := 0
		for {
			// wait for one message to arrive
			var msg jetstream.Msg
			select {
			case msg = <-activeFetch.Messages():
			case <-ctx.Done():
				// drain and nack remainder of messages
				go func() {
					defer cancelFetch()
					for msg := range activeFetch.Messages() {
						if msg != nil {
							messagesFetched++
							zap.L().Debug("nacking message due to closed context drain", zap.String("subject", msg.Subject()))
							_ = msg.Nak()
						}
					}
					endFetchSpan(fetchSpan, activeFetch.Error(), messagesFetched)
				}()
				return
			}
			// msg channel was closed, finish processing the fetch response
			if msg == nil {
				break
			}
			messagesFetched++
			fetchSpan.AddEvent("message fetched")
			zap.L().Debug("message fetched", zap.String("consumer", consumer.CachedInfo().Name))
			select {
			case retChan <- msg:
			case <-ctx.Done():
				zap.L().Debug("nacking message due to closed context", zap.String("subject", msg.Subject()))
				_ = msg.Nak()
			}
		}
		err := activeFetch.Error() // may be nil
		switch {
		case errors.Is(err, context.Canceled):
			zap.L().Info("nats reconnected, re-issuing fetch request", zap.String("consumer", consumer.CachedInfo().Name))
			err = nil
		case errors.Is(err, context.DeadlineExceeded):
			// the fetch expired with no messages; same as the server's request timeout
			err = nil
		}
		cancelFetch()
		endFetchSpan(fetchSpan, err, messagesFetched)
		if err != nil {
			errChan <- err
		}
	}()

	return retChan, errChan, nil
}
