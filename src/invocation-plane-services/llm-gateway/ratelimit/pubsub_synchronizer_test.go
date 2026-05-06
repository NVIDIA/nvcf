//go:build ignore

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
	"io"
	"log"
	"sync"
	"testing"
	"time"

	pubsub "cloud.google.com/go/pubsub/v2"
	pubsubpb "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	zlog "github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	orionpubsub "github.com/NVIDIA/nvcf/llm-api-gateway/pubsub"
	"github.com/NVIDIA/nvcf/llm-api-gateway/testdata"
	oriontesting "github.com/NVIDIA/nvcf/llm-api-gateway/testing"
)

type subscriber struct {
	sub      *pubsub.Subscriber
	messages []*pubsub.Message
	mu       sync.Mutex
	wg       sync.WaitGroup
}

func newSubscriber(sub *pubsub.Subscriber) *subscriber {
	s := &subscriber{sub: sub}
	s.messages = []*pubsub.Message{}
	return s
}

func (s *subscriber) receive(ctx context.Context, m *pubsub.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	zlog.Debug().Interface("message", m).Msg("received message from subscription")
	s.messages = append(s.messages, m)
	m.Ack()
	s.wg.Done()
}

func TestPubSubSynchronizer(t *testing.T) {
	pubSub, err := oriontesting.CreatePubSubContainer(t)
	require.NoError(t, err)
	defer func() {
		if err := pubSub.Stop(context.Background(), ptr.To(time.Duration(0))); err != nil {
			log.Printf("error stopping pubsub emulator container: %v", err)
		}

		if err := pubSub.Terminate(context.Background()); err != nil {
			log.Printf("error terminating pubsub emulator container: %v", err)
		}
	}()

	// Don't want this to run all day.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := config.MustReadConfig(testdata.Path("testcontainers-data/orion/config.toml"))
	cfg.RateLimitSynchronization.PubSub.EmulatorHost = pubSub.HostPort

	client, err := orionpubsub.NewClient(ctx, cfg.RateLimitSynchronization.PubSub)
	require.NoError(t, err)

	topic, err := orionpubsub.FindOrCreateTopic(ctx, client, cfg.RateLimitSynchronization.PubSub)
	require.NoError(t, err)

	// Subscription used for validating sends to pubsub
	sub := client.Subscriber("test-sub")
	_, err = client.SubscriptionAdminClient.CreateSubscription(
		ctx,
		&pubsubpb.Subscription{
			Name:  sub.String(),
			Topic: topic.String(),
		},
	)
	require.NoError(t, err)
	s := newSubscriber(sub)

	var (
		publisher    = orionpubsub.NewPublisher(topic, "region-a")
		synchronizer = newPubSubSynchronizer(io.NopCloser(nil), publisher, "cluster-a")
	)
	synchronizer.Start()

	var (
		rateLimit = RateLimit{
			Limit:  1000,
			Period: 60 * time.Second,
		}
		rateLimitResult = &RateLimitResult{
			Requested: 256,
			RateLimit: rateLimit,
		}
	)

	rateLimitEvent := RateLimitEvent{
		Key:       "foo",
		Result:    rateLimitResult,
		RequestID: "req_00aaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	require.NoError(
		t,
		synchronizer.Send(context.Background(), &rateLimitEvent),
		"failed to send rate limit event",
	)

	recvCtx, cancel := context.WithCancel(ctx)
	var recvErr error

	s.wg.Add(1)
	//nolint:goroutinetracking
	go func() {
		recvErr = sub.Receive(recvCtx, s.receive)
	}()

	if recvErr != nil && !errors.Is(recvErr, context.Canceled) {
		log.Printf("error receiving messages from pubsub: %v", recvErr)
	}

	synchronizer.Stop() // wait for the message to be flushed
	s.wg.Wait()
	cancel()

	require.Len(t, s.messages, 1)
	assert.Equal(t, map[string]string{"region": "region-a"}, s.messages[0].Attributes)

	rateLimitEventResult := &RateLimitEventWireFormat{}
	err = json.Unmarshal(s.messages[0].Data, rateLimitEventResult)
	require.NoError(t, err)
	assert.Equal(t, "foo", rateLimitEventResult.Key)
	assert.Equal(t, int64(256), rateLimitEventResult.Units)
	assert.Equal(t, int64(1000), rateLimitEventResult.Rate)
	assert.Equal(t, time.Second*60, rateLimitEventResult.Period)
	assert.Equal(t, "req_00aaaaaaaaaaaaaaaaaaaaaaaaa", rateLimitEventResult.RequestID)
	assert.Equal(t, "cluster-a", rateLimitEventResult.ClusterName)
}
