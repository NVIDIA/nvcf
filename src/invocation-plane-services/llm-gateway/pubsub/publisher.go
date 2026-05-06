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

package pubsub

import (
	"context"
	"encoding/json"
	"fmt"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/llm-api-gateway/telemetry"
)

type Publisher interface {
	PublishJSON(ctx context.Context, message any, orderingKey string) (string, error)
	PublishProto(ctx context.Context, message proto.Message, orderingKey string) (string, error)
	Stop()
}

type Topic interface {
	Publish(ctx context.Context, msg *gcppubsub.Message) *gcppubsub.PublishResult
	Stop()
	ID() string
}

type publisher struct {
	topic  Topic
	region string
}

func NewPublisher(topic Topic, region string) Publisher {
	return &publisher{topic: topic, region: region}
}

func (p *publisher) PublishJSON(
	ctx context.Context,
	message any,
	orderingKey string,
) (string, error) {
	data, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("marshal message: %w", err)
	}
	return p.publish(ctx, data, orderingKey)
}

func (p *publisher) PublishProto(
	ctx context.Context,
	message proto.Message,
	orderingKey string,
) (string, error) {
	data, err := proto.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("marshal message: %w", err)
	}
	return p.publish(ctx, data, orderingKey)
}

func (p *publisher) publish(ctx context.Context, data []byte, orderingKey string) (string, error) {
	attr := attribute.String("topic", p.topic.ID())
	telemetry.Add(telemetry.PubSubPublishFailures(), 0, attr)

	result := p.topic.Publish(ctx, &gcppubsub.Message{
		Data:        data,
		OrderingKey: orderingKey,
		Attributes: map[string]string{
			"region": p.region,
		},
	})

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-result.Ready():
		id, err := result.Get(ctx)
		if err != nil {
			telemetry.Add(telemetry.PubSubPublishFailures(), 1, attr)
			return "", err
		}
		return id, nil
	}
}

func (p *publisher) Stop() {
	p.topic.Stop()
}
