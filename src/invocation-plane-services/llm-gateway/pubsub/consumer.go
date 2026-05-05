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
	"time"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/llm-api-gateway/telemetry"
)

type Message interface {
	Ack()
	Nack()
}

type Subscription interface {
	Receive(ctx context.Context, callback func(ctx context.Context, msg *gcppubsub.Message)) error
	String() string
}

type Receiver = func(ctx context.Context, data []byte, msg Message) error

type Consumer interface {
	Run(ctx context.Context) error
}

type consumer struct {
	receiver     Receiver
	subscription Subscription
}

func NewConsumer(subscription Subscription, receiver Receiver) Consumer {
	return &consumer{
		subscription: subscription,
		receiver:     receiver,
	}
}

func (c *consumer) Run(ctx context.Context) error {
	return c.subscription.Receive(ctx, func(ctx context.Context, msg *gcppubsub.Message) {
		start := time.Now()
		defer func() {
			telemetry.RecordWithContext(
				ctx,
				telemetry.PubSubConsumeDuration(),
				time.Since(start).Seconds(),
				attribute.Stringer("subscription", c.subscription),
			)
		}()

		if err := c.receiver(ctx, msg.Data, msg); err != nil {
			telemetry.Add(
				telemetry.PubSubConsumeFailures(),
				1,
				attribute.Stringer("subscription", c.subscription),
			)
			msg.Nack()
			return
		}
		msg.Ack()
	})
}
