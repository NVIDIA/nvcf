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
	"fmt"
	"sync"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	pubsubpb "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var subscriptionMutex sync.Mutex

type SubscriptionOption func(*gcppubsub.Subscriber)

func WithReceiveSettings(settings gcppubsub.ReceiveSettings) SubscriptionOption {
	return func(sub *gcppubsub.Subscriber) {
		sub.ReceiveSettings = settings
	}
}

func FindOrCreateSubscription(
	ctx context.Context,
	client *gcppubsub.Client,
	cfg Config,
	opts ...SubscriptionOption,
) (*gcppubsub.Subscriber, error) {
	subscriptionMutex.Lock()
	defer subscriptionMutex.Unlock()

	sub := client.Subscriber(cfg.Subscription)
	exists, err := subscriptionExists(ctx, client, sub.String())
	if err != nil {
		return nil, err
	}

	if !exists && cfg.Create {
		topic, err := FindOrCreateTopic(ctx, client, cfg)
		if err != nil {
			return nil, err
		}

		_, err = client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
			Name:  sub.String(),
			Topic: topic.String(),
		})
		if err != nil {
			return nil, fmt.Errorf("create subscription %q: %w", cfg.Subscription, err)
		}
	}

	for _, opt := range opts {
		opt(sub)
	}

	return sub, nil
}

func subscriptionExists(
	ctx context.Context,
	client *gcppubsub.Client,
	subscription string,
) (bool, error) {
	_, err := client.SubscriptionAdminClient.GetSubscription(
		ctx,
		&pubsubpb.GetSubscriptionRequest{Subscription: subscription},
	)
	if err == nil {
		return true, nil
	}
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	return false, fmt.Errorf("get subscription %q: %w", subscription, err)
}
