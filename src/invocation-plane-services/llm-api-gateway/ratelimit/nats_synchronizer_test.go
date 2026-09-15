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

package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type fakeNATSJetStream struct {
	published         []fakePublishedMessage
	streamInfoErr     error
	consumerInfoErr   error
	addedStream       *nats.StreamConfig
	updatedStream     *nats.StreamConfig
	addedConsumer     *nats.ConsumerConfig
	updatedConsumer   *nats.ConsumerConfig
	addedConsumerTo   string
	updatedConsumerTo string
	subscribeSubject  string
	subscribeQueue    string
	subscribeHandler  nats.MsgHandler
}

type fakeNATSDrainer struct {
	drained bool
	closed  bool
}

type fakeNATSMessage struct {
	data   []byte
	acked  bool
	ackErr error
}

type fakePublishedMessage struct {
	subject string
	data    []byte
}

func (f *fakeNATSJetStream) Publish(subject string, data []byte, _ ...nats.PubOpt) (*nats.PubAck, error) {
	f.published = append(f.published, fakePublishedMessage{subject: subject, data: data})
	return &nats.PubAck{}, nil
}

func (f *fakeNATSJetStream) StreamInfo(_ string, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	if f.streamInfoErr != nil {
		return nil, f.streamInfoErr
	}
	return &nats.StreamInfo{}, nil
}

func (f *fakeNATSJetStream) AddStream(cfg *nats.StreamConfig, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	f.addedStream = cfg
	return &nats.StreamInfo{Config: *cfg}, nil
}

func (f *fakeNATSJetStream) UpdateStream(cfg *nats.StreamConfig, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	f.updatedStream = cfg
	return &nats.StreamInfo{Config: *cfg}, nil
}

func (f *fakeNATSJetStream) ConsumerInfo(_ string, _ string, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	if f.consumerInfoErr != nil {
		return nil, f.consumerInfoErr
	}
	return &nats.ConsumerInfo{}, nil
}

func (f *fakeNATSJetStream) AddConsumer(
	stream string,
	cfg *nats.ConsumerConfig,
	_ ...nats.JSOpt,
) (*nats.ConsumerInfo, error) {
	f.addedConsumerTo = stream
	f.addedConsumer = cfg
	return &nats.ConsumerInfo{Config: *cfg}, nil
}

func (f *fakeNATSJetStream) UpdateConsumer(
	stream string,
	cfg *nats.ConsumerConfig,
	_ ...nats.JSOpt,
) (*nats.ConsumerInfo, error) {
	f.updatedConsumerTo = stream
	f.updatedConsumer = cfg
	return &nats.ConsumerInfo{Config: *cfg}, nil
}

func (f *fakeNATSJetStream) QueueSubscribe(
	subj,
	queue string,
	cb nats.MsgHandler,
	_ ...nats.SubOpt,
) (*nats.Subscription, error) {
	f.subscribeSubject = subj
	f.subscribeQueue = queue
	f.subscribeHandler = cb
	return &nats.Subscription{}, nil
}

func (f *fakeNATSDrainer) Drain() error {
	f.drained = true
	return nil
}

func (f *fakeNATSDrainer) Close() {
	f.closed = true
}

func (f *fakeNATSMessage) Ack() error {
	if f.ackErr != nil {
		return f.ackErr
	}
	f.acked = true
	return nil
}

func (f *fakeNATSMessage) Data() []byte {
	return f.data
}

func TestNewNATSSyncConfig(t *testing.T) {
	cfg, err := NewNATSSyncConfig("rate.limit.sync", "cluster/a")
	if err != nil {
		t.Fatalf("NewNATSSyncConfig() error = %v", err)
	}

	if cfg.Stream != "rate_limit_sync_rate_limit_sync" {
		t.Fatalf("stream = %q", cfg.Stream)
	}
	if cfg.Durable != "rate_limit_sync_cluster_a" {
		t.Fatalf("durable = %q", cfg.Durable)
	}
	if cfg.Queue != "rate_limit_sync_cluster_a" {
		t.Fatalf("queue = %q", cfg.Queue)
	}
	if cfg.DeliverSubject != "_INBOX.rate_limit_sync.rate_limit_sync.cluster_a" {
		t.Fatalf("deliver subject = %q", cfg.DeliverSubject)
	}
	if cfg.AckWait != defaultNATSAckWait {
		t.Fatalf("ack wait = %s, want %s", cfg.AckWait, defaultNATSAckWait)
	}
	if cfg.MaxAge != time.Duration(dropMessagesOlderThan)*time.Second {
		t.Fatalf("max age = %s", cfg.MaxAge)
	}
}

func TestEnsureNATSStreamCreatesDurableStream(t *testing.T) {
	js := &fakeNATSJetStream{
		streamInfoErr: nats.ErrStreamNotFound,
	}
	cfg, err := NewNATSSyncConfig("rate-limit-events", "cluster-a")
	if err != nil {
		t.Fatalf("NewNATSSyncConfig() error = %v", err)
	}

	if err := EnsureNATSStream(js, cfg); err != nil {
		t.Fatalf("EnsureNATSStream() error = %v", err)
	}

	if js.addedStream == nil {
		t.Fatal("expected stream to be created")
	}
	if js.addedStream.Retention != nats.InterestPolicy {
		t.Fatalf("stream retention = %v, want InterestPolicy", js.addedStream.Retention)
	}
	if len(js.addedStream.Subjects) != 1 || js.addedStream.Subjects[0] != cfg.Subject {
		t.Fatalf("stream subjects = %#v, want [%q]", js.addedStream.Subjects, cfg.Subject)
	}
	if js.addedConsumer != nil || js.updatedConsumer != nil {
		t.Fatal("did not expect consumer provisioning from EnsureNATSStream()")
	}
}

func TestEnsureNATSConsumerCreatesDurableConsumer(t *testing.T) {
	js := &fakeNATSJetStream{
		consumerInfoErr: nats.ErrConsumerNotFound,
	}
	cfg, err := NewNATSSyncConfig("rate-limit-events", "cluster-a")
	if err != nil {
		t.Fatalf("NewNATSSyncConfig() error = %v", err)
	}

	if err := EnsureNATSConsumer(js, cfg); err != nil {
		t.Fatalf("EnsureNATSConsumer() error = %v", err)
	}

	if js.addedConsumer == nil {
		t.Fatal("expected consumer to be created")
	}
	if js.addedConsumerTo != cfg.Stream {
		t.Fatalf("consumer stream = %q, want %q", js.addedConsumerTo, cfg.Stream)
	}
	if js.addedConsumer.Durable != cfg.Durable {
		t.Fatalf("consumer durable = %q, want %q", js.addedConsumer.Durable, cfg.Durable)
	}
	if js.addedConsumer.DeliverGroup != cfg.Queue {
		t.Fatalf("consumer deliver group = %q, want %q", js.addedConsumer.DeliverGroup, cfg.Queue)
	}
	if js.addedConsumer.DeliverSubject != cfg.DeliverSubject {
		t.Fatalf(
			"consumer deliver subject = %q, want %q",
			js.addedConsumer.DeliverSubject,
			cfg.DeliverSubject,
		)
	}
}

func TestNATSSynchronizerPublishesWireFormat(t *testing.T) {
	js := &fakeNATSJetStream{}
	drainer := &fakeNATSDrainer{}
	cfg, err := NewNATSSyncConfig("rate-limit-events", "cluster-a")
	if err != nil {
		t.Fatalf("NewNATSSyncConfig() error = %v", err)
	}
	synchronizer := NewNATSSynchronizer(js, drainer, cfg, "cluster-a")
	synchronizer.Start()

	err = synchronizer.Send(context.Background(), &RateLimitEvent{
		Key: "org:123",
		Result: &RateLimitResult{
			Requested: 42,
			RateLimit: RateLimit{
				Limit:  100,
				Period: time.Minute,
			},
		},
		RequestID:   "req-123",
		MustConsume: true,
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	synchronizer.Stop()

	if len(js.published) != 1 {
		t.Fatalf("published = %d, want 1", len(js.published))
	}
	if js.published[0].subject != "rate-limit-events" {
		t.Fatalf("subject = %q, want rate-limit-events", js.published[0].subject)
	}
	if !drainer.drained {
		t.Fatal("expected NATS connection to be drained on stop")
	}

	var event RateLimitEventWireFormat
	if err := json.Unmarshal(js.published[0].data, &event); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if event.Key != "org:123" {
		t.Fatalf("key = %q, want org:123", event.Key)
	}
	if event.Units != 42 {
		t.Fatalf("units = %d, want 42", event.Units)
	}
	if event.Rate != 100 {
		t.Fatalf("rate = %d, want 100", event.Rate)
	}
	if event.Period != time.Minute {
		t.Fatalf("period = %s, want 1m", event.Period)
	}
	if event.RequestID != "req-123" {
		t.Fatalf("request id = %q, want req-123", event.RequestID)
	}
	if event.ClusterName != "cluster-a" {
		t.Fatalf("cluster name = %q, want cluster-a", event.ClusterName)
	}
	if !event.MustConsume {
		t.Fatal("must consume = false, want true")
	}
}

func TestSubscribeNATSBindsClusterQueueConsumer(t *testing.T) {
	js := &fakeNATSJetStream{}
	cfg, err := NewNATSSyncConfig("rate-limit-events", "cluster-a")
	if err != nil {
		t.Fatalf("NewNATSSyncConfig() error = %v", err)
	}

	stop, err := SubscribeNATS(
		context.Background(),
		js,
		cfg,
		func(context.Context, *RateLimitEventWireFormat) error { return nil },
	)
	if err != nil {
		t.Fatalf("SubscribeNATS() error = %v", err)
	}
	t.Cleanup(func() {
		_ = stop()
	})

	if js.subscribeSubject != cfg.Subject {
		t.Fatalf("subscribe subject = %q, want %q", js.subscribeSubject, cfg.Subject)
	}
	if js.subscribeQueue != cfg.Queue {
		t.Fatalf("subscribe queue = %q, want %q", js.subscribeQueue, cfg.Queue)
	}
	if js.subscribeHandler == nil {
		t.Fatal("expected subscription handler to be registered")
	}
}

func TestHandleNATSMessageInvokesHandlerAndAck(t *testing.T) {
	payload, err := json.Marshal(&RateLimitEventWireFormat{RequestID: "req-456"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	msg := &fakeNATSMessage{data: payload}
	called := false
	handleNATSMessage(
		context.Background(),
		"rate-limit-events",
		msg,
		func(_ context.Context, event *RateLimitEventWireFormat) error {
			called = true
			if event.RequestID != "req-456" {
				t.Fatalf("request id = %q, want req-456", event.RequestID)
			}
			return nil
		},
	)

	if !called {
		t.Fatal("expected NATS handler to be invoked")
	}
	if !msg.acked {
		t.Fatal("expected NATS message to be acked")
	}
}

func TestHandleNATSMessageDoesNotAckOnHandlerError(t *testing.T) {
	payload, err := json.Marshal(&RateLimitEventWireFormat{RequestID: "req-789"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	msg := &fakeNATSMessage{data: payload}
	handleNATSMessage(
		context.Background(),
		"rate-limit-events",
		msg,
		func(context.Context, *RateLimitEventWireFormat) error {
			return errors.New("boom")
		},
	)

	if msg.acked {
		t.Fatal("expected NATS message to remain unacked")
	}
}
