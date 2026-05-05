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

package ratelimitsync

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	orionpubsub "github.com/NVIDIA/nvcf/llm-api-gateway/pubsub"
	"github.com/NVIDIA/nvcf/llm-api-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/llm-api-gateway/util"
)

type PublisherRuntime struct {
	Synchronizer ratelimit.Synchronizer
	start        func() error
	stop         func()
}

func (r *PublisherRuntime) Start() error {
	if r == nil || r.start == nil {
		return nil
	}
	return r.start()
}

func (r *PublisherRuntime) Stop() {
	if r == nil || r.stop == nil {
		return
	}
	r.stop()
}

func NewPublisherRuntime(cfg *config.Config) (*PublisherRuntime, error) {
	switch strings.ToLower(cfg.RateLimitSync.Transport) {
	case "", "none":
		return &PublisherRuntime{
			Synchronizer: ratelimit.NewSynchronizer(),
			start:        func() error { return nil },
			stop:         func() {},
		}, nil
	case "nats":
		return newNATSPublisherRuntime(cfg)
	case "pubsub":
		return newPubSubPublisherRuntime(cfg)
	default:
		return nil, fmt.Errorf(
			"unsupported rate limit sync transport %q",
			cfg.RateLimitSync.Transport,
		)
	}
}

func RunWorker(ctx context.Context, cfg *config.Config) error {
	run, err := workerRunnerFor(cfg.RateLimitSync.Transport)
	if err != nil {
		return err
	}

	if !cfg.Olric.Enabled {
		return fmt.Errorf("OLRIC_ENABLED must be true for the rate limit sync worker")
	}

	limiter, olricNode, err := newWorkerRateLimiter(ctx, cfg)
	if err != nil {
		return err
	}
	defer util.ShutdownOlricNode(context.Background(), olricNode, cfg.Olric.ShutdownTimeout)

	return run(ctx, cfg, limiter)
}

// workerRunner is the function signature shared by the NATS and Pub/Sub worker
// implementations. Returning one from workerRunnerFor lets RunWorker dispatch
// on transport once instead of repeating a switch before and after startup.
type workerRunner func(ctx context.Context, cfg *config.Config, limiter ratelimit.RateLimiter) error

func workerRunnerFor(transport string) (workerRunner, error) {
	switch strings.ToLower(transport) {
	case "nats":
		return runNATSWorker, nil
	case "pubsub":
		return runPubSubWorker, nil
	default:
		return nil, fmt.Errorf(`RATE_LIMIT_SYNC_TRANSPORT must be "nats" or "pubsub" for the rate limit sync worker, got %q`, transport)
	}
}

func newWorkerRateLimiter(
	ctx context.Context,
	cfg *config.Config,
) (ratelimit.RateLimiter, *util.OlricNode, error) {
	node, err := util.NewOlricNode(ctx, cfg.Olric)
	if err != nil {
		return nil, nil, fmt.Errorf("start olric node: %w", err)
	}

	limiter, err := ratelimit.NewRateLimiter(
		ratelimit.NewOlricStore(node.DMap),
		ratelimit.WithFailOpen(cfg.RateLimiter.FailOpen),
	)
	if err != nil {
		util.ShutdownOlricNode(context.Background(), node, cfg.Olric.ShutdownTimeout)
		return nil, nil, fmt.Errorf("create rate limiter: %w", err)
	}

	return limiter, node, nil
}

func newNATSPublisherRuntime(cfg *config.Config) (*PublisherRuntime, error) {
	natsCfg, err := validatedNATSConfig(cfg)
	if err != nil {
		return nil, err
	}

	conn, js, err := connectNATS(cfg)
	if err != nil {
		return nil, err
	}

	synchronizer := ratelimit.NewNATSSynchronizer(
		js,
		conn,
		natsCfg,
		cfg.RateLimitSync.ClusterName,
	)
	stop := newStop(func() {
		synchronizer.Stop()
	})

	return &PublisherRuntime{
		Synchronizer: synchronizer,
		start: func() error {
			if err := ratelimit.EnsureNATSStream(js, natsCfg); err != nil {
				stop()
				return err
			}
			synchronizer.Start()
			return nil
		},
		stop: stop,
	}, nil
}

func newPubSubPublisherRuntime(cfg *config.Config) (*PublisherRuntime, error) {
	pubsubCfg, err := validatedPubSubPublisherConfig(cfg)
	if err != nil {
		return nil, err
	}

	client, err := orionpubsub.NewClient(context.Background(), pubsubCfg)
	if err != nil {
		return nil, fmt.Errorf("create Pub/Sub client: %w", err)
	}

	topic, err := orionpubsub.FindOrCreateTopic(context.Background(), client, pubsubCfg)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("create Pub/Sub topic: %w", err)
	}

	synchronizer := ratelimit.NewPubSubSynchronizer(
		client,
		orionpubsub.NewPublisher(topic, cfg.Server.Region),
		cfg.RateLimitSync.ClusterName,
	)
	stop := newStop(func() {
		synchronizer.Stop()
	})

	return &PublisherRuntime{
		Synchronizer: synchronizer,
		start: func() error {
			synchronizer.Start()
			return nil
		},
		stop: stop,
	}, nil
}

func runNATSWorker(
	ctx context.Context,
	cfg *config.Config,
	limiter ratelimit.RateLimiter,
) error {
	natsCfg, err := validatedNATSConfig(cfg)
	if err != nil {
		return err
	}

	conn, js, err := connectNATS(cfg)
	if err != nil {
		return err
	}

	unsubscribe := func() error { return nil }
	stop := newStop(func() {
		_ = unsubscribe()
		if err := conn.Drain(); err != nil {
			conn.Close()
		}
	})
	defer stop()

	if err := ratelimit.EnsureNATSStream(js, natsCfg); err != nil {
		return err
	}
	if err := ratelimit.EnsureNATSConsumer(js, natsCfg); err != nil {
		return err
	}

	unsubscribe, err = ratelimit.SubscribeNATS(
		ctx,
		js,
		natsCfg,
		func(eventCtx context.Context, event *ratelimit.RateLimitEventWireFormat) error {
			return ratelimit.ApplySynchronizedEvent(
				eventCtx,
				limiter,
				cfg.RateLimitSync.ClusterName,
				cfg.RateLimitSync.ApplyRemote,
				event,
			)
		},
	)
	if err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}

func runPubSubWorker(
	ctx context.Context,
	cfg *config.Config,
	limiter ratelimit.RateLimiter,
) error {
	pubsubCfg, err := validatedPubSubWorkerConfig(cfg)
	if err != nil {
		return err
	}

	client, err := orionpubsub.NewClient(context.Background(), pubsubCfg)
	if err != nil {
		return fmt.Errorf("create Pub/Sub client: %w", err)
	}
	defer client.Close()

	subscription, err := orionpubsub.FindOrCreateSubscription(ctx, client, pubsubCfg)
	if err != nil {
		return fmt.Errorf("create Pub/Sub subscription: %w", err)
	}

	consumer := orionpubsub.NewConsumer(
		subscription,
		orionpubsub.JSONReceiver(
			ratelimit.PubSubConsumer(
				limiter,
				cfg.RateLimitSync.ClusterName,
				cfg.RateLimitSync.ApplyRemote,
			),
		),
	)

	if err := consumer.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}

	return nil
}

func validatedNATSConfig(cfg *config.Config) (ratelimit.NATSSyncConfig, error) {
	if cfg.RateLimitSync.ClusterName == "" {
		return ratelimit.NATSSyncConfig{}, fmt.Errorf(
			"RATE_LIMIT_SYNC_CLUSTER_NAME must be set when rate limit sync is enabled",
		)
	}
	if cfg.RateLimitSync.NATS.URL == "" {
		return ratelimit.NATSSyncConfig{}, fmt.Errorf(
			"RATE_LIMIT_SYNC_NATS_URL must be set for NATS rate limit sync",
		)
	}
	if cfg.RateLimitSync.NATS.Subject == "" {
		return ratelimit.NATSSyncConfig{}, fmt.Errorf(
			"RATE_LIMIT_SYNC_NATS_SUBJECT must be set for NATS rate limit sync",
		)
	}

	natsCfg, err := ratelimit.NewNATSSyncConfig(
		cfg.RateLimitSync.NATS.Subject,
		cfg.RateLimitSync.ClusterName,
	)
	if err != nil {
		return ratelimit.NATSSyncConfig{}, err
	}
	return natsCfg, nil
}

func validatedPubSubPublisherConfig(cfg *config.Config) (orionpubsub.Config, error) {
	if cfg.RateLimitSync.ClusterName == "" {
		return orionpubsub.Config{}, fmt.Errorf(
			"RATE_LIMIT_SYNC_CLUSTER_NAME must be set when rate limit sync is enabled",
		)
	}
	if cfg.RateLimitSync.PubSub.ProjectID == "" {
		return orionpubsub.Config{}, fmt.Errorf(
			"RATE_LIMIT_SYNC_PUBSUB_PROJECT_ID must be set for Pub/Sub rate limit sync",
		)
	}
	if cfg.RateLimitSync.PubSub.Topic == "" {
		return orionpubsub.Config{}, fmt.Errorf(
			"RATE_LIMIT_SYNC_PUBSUB_TOPIC must be set for Pub/Sub rate limit sync",
		)
	}

	return orionpubsub.Config{
		Create:       cfg.RateLimitSync.PubSub.Create,
		ProjectID:    cfg.RateLimitSync.PubSub.ProjectID,
		Topic:        cfg.RateLimitSync.PubSub.Topic,
		Subscription: cfg.RateLimitSync.PubSub.Subscription,
		Endpoint:     cfg.RateLimitSync.PubSub.Endpoint,
		EmulatorHost: cfg.RateLimitSync.PubSub.EmulatorHost,
	}, nil
}

func validatedPubSubWorkerConfig(cfg *config.Config) (orionpubsub.Config, error) {
	pubsubCfg, err := validatedPubSubPublisherConfig(cfg)
	if err != nil {
		return orionpubsub.Config{}, err
	}

	if cfg.RateLimitSync.PubSub.Subscription == "" {
		return orionpubsub.Config{}, fmt.Errorf(
			"RATE_LIMIT_SYNC_PUBSUB_SUBSCRIPTION must be set for Pub/Sub rate limit sync",
		)
	}

	return pubsubCfg, nil
}

func connectNATS(cfg *config.Config) (*nats.Conn, nats.JetStreamContext, error) {
	conn, err := nats.Connect(
		cfg.RateLimitSync.NATS.URL,
		nats.Name("llm-api-gateway-rate-limit-sync-"+cfg.RateLimitSync.ClusterName),
		nats.Timeout(cfg.RateLimitSync.NATS.ConnectTimeout),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to NATS: %w", err)
	}

	js, err := conn.JetStream(nats.MaxWait(cfg.RateLimitSync.NATS.ConnectTimeout))
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("create NATS JetStream context: %w", err)
	}

	return conn, js, nil
}

func newStop(stop func()) func() {
	var once sync.Once

	return func() {
		once.Do(stop)
	}
}
