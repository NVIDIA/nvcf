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

// Package eventledger publishes CloudEvents to the Event Ledger on a best
// effort basis. Publishing never blocks the caller: events that cannot be
// delivered within the send timeout, or that arrive while too many sends are
// in flight, are dropped and counted.
package eventledger

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	cehttp "github.com/cloudevents/sdk-go/v2/protocol/http"
	"go.uber.org/zap"

	"nvcf-grpc-proxy/proxy/metrics"
)

const (
	ingestPath  = "/v3/ledger/cloudevents"
	sendTimeout = 10 * time.Second
	retryDelay  = 250 * time.Millisecond
	maxRetries  = 3
	maxInflight = 1000
)

// Publisher sends events to the Event Ledger. A nil *Publisher drops every
// event, so callers do not need to check whether publishing is enabled.
type Publisher struct {
	client    cloudevents.Client
	transport *transport

	inflight chan struct{}
	wg       sync.WaitGroup
}

// New returns a Publisher that posts to the ledger at addr. See newTransport
// for how it authenticates.
func New(addr, secretsPath, oauth2Fqdn string) (*Publisher, error) {
	transport, err := newTransport(secretsPath, oauth2Fqdn)
	if err != nil {
		return nil, fmt.Errorf("failed to create event ledger transport: %w", err)
	}
	client, err := cloudevents.NewClientHTTP(
		cehttp.WithTarget(addr+ingestPath),
		cehttp.WithRoundTripper(transport),
	)
	if err != nil {
		transport.Close()
		return nil, fmt.Errorf("failed to create event ledger client: %w", err)
	}
	return &Publisher{
		client:    client,
		transport: transport,
		inflight:  make(chan struct{}, maxInflight),
	}, nil
}

// Publish sends event in the background. The send keeps ctx's trace but not
// its cancellation, so it outlives the request that produced the event.
func (p *Publisher) Publish(ctx context.Context, event cloudevents.Event) {
	if p == nil {
		return
	}
	select {
	case p.inflight <- struct{}{}:
	default:
		metrics.EventLedgerPublishCounter.WithLabelValues(event.Type(), "dropped").Inc()
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() { <-p.inflight }()
		p.send(context.WithoutCancel(ctx), event)
	}()
}

func (p *Publisher) send(ctx context.Context, event cloudevents.Event) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	// The ledger accepts only structured mode (application/cloudevents+json).
	ctx = cloudevents.WithEncodingStructured(ctx)

	retry := backoff.WithContext(backoff.WithMaxRetries(
		backoff.NewExponentialBackOff(backoff.WithInitialInterval(retryDelay)), maxRetries), ctx)
	if err := backoff.Retry(func() error { return p.sendOnce(ctx, event) }, retry); err != nil {
		metrics.EventLedgerPublishCounter.WithLabelValues(event.Type(), "failed").Inc()
		metrics.EventLedgerSendDurationSeconds.WithLabelValues("failed").Observe(time.Since(start).Seconds())
		zap.L().Warn("failed to publish event to event ledger",
			zap.String("event_type", event.Type()),
			zap.String("event_id", event.ID()),
			zap.Error(err))
		return
	}
	metrics.EventLedgerPublishCounter.WithLabelValues(event.Type(), "sent").Inc()
	metrics.EventLedgerSendDurationSeconds.WithLabelValues("sent").Observe(time.Since(start).Seconds())
}

// sendOnce makes one delivery attempt. Rejections other than 429 and 5xx are
// permanent.
func (p *Publisher) sendOnce(ctx context.Context, event cloudevents.Event) error {
	result := p.client.Send(ctx, event)
	if cloudevents.IsACK(result) {
		return nil
	}
	var httpResult *cehttp.Result
	if errors.As(result, &httpResult) &&
		httpResult.StatusCode != http.StatusTooManyRequests && httpResult.StatusCode < http.StatusInternalServerError {
		return backoff.Permanent(result)
	}
	return result
}

// Close waits for in-flight sends, which each end within the send timeout,
// then releases the transport. Publish must not be called after Close.
func (p *Publisher) Close() {
	if p == nil {
		return
	}
	p.wg.Wait()
	p.transport.Close()
}
