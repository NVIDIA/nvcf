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
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nats-io/nats.go"
	zlog "github.com/rs/zerolog/log"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const defaultNATSAckWait = 30 * time.Second

type NATSPublisher interface {
	Publish(subject string, data []byte, opts ...nats.PubOpt) (*nats.PubAck, error)
}

type NATSJetStream interface {
	NATSPublisher
	StreamInfo(stream string, opts ...nats.JSOpt) (*nats.StreamInfo, error)
	AddStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error)
	UpdateStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error)
	ConsumerInfo(stream, consumer string, opts ...nats.JSOpt) (*nats.ConsumerInfo, error)
	AddConsumer(stream string, cfg *nats.ConsumerConfig, opts ...nats.JSOpt) (*nats.ConsumerInfo, error)
	UpdateConsumer(stream string, cfg *nats.ConsumerConfig, opts ...nats.JSOpt) (*nats.ConsumerInfo, error)
	QueueSubscribe(
		subj,
		queue string,
		cb nats.MsgHandler,
		opts ...nats.SubOpt,
	) (*nats.Subscription, error)
}

type NATSDrainer interface {
	Drain() error
	Close()
}

type NATSMessage interface {
	Ack() error
	Data() []byte
}

type NATSSyncConfig struct {
	Subject        string
	Stream         string
	Durable        string
	Queue          string
	DeliverSubject string
	AckWait        time.Duration
	MaxAge         time.Duration
}

type natsMessageAdapter struct {
	msg *nats.Msg
}

func (m natsMessageAdapter) Ack() error {
	return m.msg.Ack()
}

func (m natsMessageAdapter) Data() []byte {
	return m.msg.Data
}

var _ Synchronizer = (*natsSynchronizer)(nil)

func NewNATSSyncConfig(subject, clusterName string) (NATSSyncConfig, error) {
	if subject == "" {
		return NATSSyncConfig{}, fmt.Errorf("NATS subject must be configured")
	}
	if clusterName == "" {
		return NATSSyncConfig{}, fmt.Errorf("cluster name must be configured for NATS rate limit sync")
	}

	streamSuffix := sanitizeJetStreamName(subject)
	clusterSuffix := sanitizeJetStreamName(clusterName)

	return NATSSyncConfig{
		Subject:        subject,
		Stream:         "rate_limit_sync_" + streamSuffix,
		Durable:        "rate_limit_sync_" + clusterSuffix,
		Queue:          "rate_limit_sync_" + clusterSuffix,
		DeliverSubject: "_INBOX.rate_limit_sync." + streamSuffix + "." + clusterSuffix,
		AckWait:        defaultNATSAckWait,
		MaxAge:         time.Duration(dropMessagesOlderThan) * time.Second,
	}, nil
}

func NewNATSSynchronizer(
	publisher NATSPublisher,
	drainer NATSDrainer,
	cfg NATSSyncConfig,
	clusterName string,
) Synchronizer {
	s := &natsSynchronizer{
		publisher:   publisher,
		drainer:     drainer,
		cfg:         cfg,
		clusterName: clusterName,
	}
	telemetry.RegisterRateLimitSynchronizerQueueLength(func() int64 {
		if s.resultChan == nil {
			return -1
		}
		return int64(len(s.resultChan))
	})
	return s
}

func EnsureNATSStream(js NATSJetStream, cfg NATSSyncConfig) error {
	if _, err := js.StreamInfo(cfg.Stream); err != nil {
		if !errors.Is(err, nats.ErrStreamNotFound) {
			return fmt.Errorf("lookup NATS JetStream stream %q: %w", cfg.Stream, err)
		}
		if _, err := js.AddStream(natsStreamConfig(cfg)); err != nil {
			if !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
				return fmt.Errorf("create NATS JetStream stream %q: %w", cfg.Stream, err)
			}
			if _, err := js.UpdateStream(natsStreamConfig(cfg)); err != nil {
				return fmt.Errorf("update NATS JetStream stream %q after concurrent create: %w", cfg.Stream, err)
			}
		}
	} else if _, err := js.UpdateStream(natsStreamConfig(cfg)); err != nil {
		return fmt.Errorf("update NATS JetStream stream %q: %w", cfg.Stream, err)
	}

	return nil
}

func EnsureNATSConsumer(js NATSJetStream, cfg NATSSyncConfig) error {
	if _, err := js.ConsumerInfo(cfg.Stream, cfg.Durable); err != nil {
		if !errors.Is(err, nats.ErrConsumerNotFound) {
			return fmt.Errorf(
				"lookup NATS JetStream consumer %q on stream %q: %w",
				cfg.Durable,
				cfg.Stream,
				err,
			)
		}
		if _, err := js.AddConsumer(cfg.Stream, natsConsumerConfig(cfg)); err != nil {
			return fmt.Errorf(
				"create NATS JetStream consumer %q on stream %q: %w",
				cfg.Durable,
				cfg.Stream,
				err,
			)
		}
	} else if _, err := js.UpdateConsumer(cfg.Stream, natsConsumerConfig(cfg)); err != nil {
		return fmt.Errorf(
			"update NATS JetStream consumer %q on stream %q: %w",
			cfg.Durable,
			cfg.Stream,
			err,
		)
	}

	return nil
}

func SubscribeNATS(
	ctx context.Context,
	js NATSJetStream,
	cfg NATSSyncConfig,
	handler func(context.Context, *RateLimitEventWireFormat) error,
) (func() error, error) {
	subscription, err := js.QueueSubscribe(
		cfg.Subject,
		cfg.Queue,
		func(msg *nats.Msg) {
			handleNATSMessage(ctx, cfg.Subject, natsMessageAdapter{msg: msg}, handler)
		},
		nats.Bind(cfg.Stream, cfg.Durable),
		nats.ManualAck(),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"subscribe to NATS JetStream subject %q with consumer %q: %w",
			cfg.Subject,
			cfg.Durable,
			err,
		)
	}

	return subscription.Unsubscribe, nil
}

func handleNATSMessage(
	ctx context.Context,
	subject string,
	msg NATSMessage,
	handler func(context.Context, *RateLimitEventWireFormat) error,
) {
	start := time.Now()
	defer func() {
		telemetry.Record(
			telemetry.PubSubConsumeDuration(),
			time.Since(start).Seconds(),
		)
	}()

	var event RateLimitEventWireFormat
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		telemetry.Add(
			telemetry.PubSubConsumeFailures(),
			1,
		)
		zlog.Error().Err(err).Msg("failed to unmarshal NATS rate limit event")
		return
	}

	if err := handler(ctx, &event); err != nil {
		telemetry.Add(
			telemetry.PubSubConsumeFailures(),
			1,
		)
		zlog.Error().Err(err).Msg("failed to handle NATS rate limit event")
		return
	}

	if err := msg.Ack(); err != nil {
		telemetry.Add(
			telemetry.PubSubConsumeFailures(),
			1,
		)
		zlog.Error().Err(err).Msg("failed to ack NATS rate limit event")
	}
}

type natsSynchronizer struct {
	publisher   NATSPublisher
	drainer     NATSDrainer
	cfg         NATSSyncConfig
	clusterName string
	wg          sync.WaitGroup
	resultChan  chan *RateLimitEventWireFormat
}

func (s *natsSynchronizer) Send(_ context.Context, rle *RateLimitEvent) error {
	queueStart := time.Now()
	s.resultChan <- &RateLimitEventWireFormat{
		Key:         rle.Key,
		Units:       rle.Result.Requested,
		Rate:        rle.Result.RateLimit.Limit,
		Period:      rle.Result.RateLimit.Period,
		RequestID:   rle.RequestID,
		ClusterName: s.clusterName,
		CreatedAt:   time.Now().Unix(),
		MustConsume: rle.MustConsume,
	}
	telemetry.Record(
		telemetry.RateLimitSynchronizerQueueWait(),
		time.Since(queueStart).Seconds(),
	)

	return nil
}

func (s *natsSynchronizer) Start() {
	s.resultChan = make(chan *RateLimitEventWireFormat, publishResultBufferSize)
	for i := range numPublishResultProcessors {
		s.wg.Go(func() {
			s.processor(i)
		})
	}
}

func (s *natsSynchronizer) Stop() {
	if s.resultChan != nil {
		close(s.resultChan)
		s.wg.Wait()
	}

	if err := s.drainer.Drain(); err != nil {
		zlog.Error().Err(err).Msg("failed to drain NATS connection")
		s.drainer.Close()
	}
}

func (s *natsSynchronizer) processor(i int) {
	for data := range s.resultChan {
		lag := time.Since(time.Unix(data.CreatedAt, 0)).Seconds()
		if lag > dropMessagesOlderThan {
			zlog.Debug().
				Int("processor_id", i).
				Str("request_id", data.RequestID).
				Float64("lag_seconds", lag).
				Msg("dropping too-old NATS rate limit event")
			telemetry.Add(
				telemetry.RateLimitSynchronizerEventsDropped(),
				1,
				telemetry.SynchronizerDropReasonOldMessage(),
			)
			continue
		}

		payload, err := json.Marshal(data)
		if err != nil {
			zlog.Error().Err(err).Msg("failed to marshal NATS rate limit event")
			continue
		}

		publishStart := time.Now()
		if _, err := s.publisher.Publish(s.cfg.Subject, payload); err != nil {
			zlog.Error().Err(err).Msg("failed to publish NATS rate limit event")
			telemetry.Add(
				telemetry.PubSubPublishFailures(),
				1,
			)
			continue
		}
		telemetry.Record(
			telemetry.RateLimitSynchronizerPublishDuration(),
			time.Since(publishStart).Seconds(),
		)
	}
}

func sanitizeJetStreamName(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteByte('_')
		}
	}

	s := strings.Trim(b.String(), "_")
	if s == "" {
		return "default"
	}

	return s
}

func natsStreamConfig(cfg NATSSyncConfig) *nats.StreamConfig {
	return &nats.StreamConfig{
		Name:      cfg.Stream,
		Subjects:  []string{cfg.Subject},
		Retention: nats.InterestPolicy,
		MaxAge:    cfg.MaxAge,
		Storage:   nats.FileStorage,
		Discard:   nats.DiscardOld,
	}
}

func natsConsumerConfig(cfg NATSSyncConfig) *nats.ConsumerConfig {
	return &nats.ConsumerConfig{
		Durable:        cfg.Durable,
		DeliverPolicy:  nats.DeliverNewPolicy,
		AckPolicy:      nats.AckExplicitPolicy,
		AckWait:        cfg.AckWait,
		FilterSubject:  cfg.Subject,
		ReplayPolicy:   nats.ReplayInstantPolicy,
		DeliverSubject: cfg.DeliverSubject,
		DeliverGroup:   cfg.Queue,
	}
}
