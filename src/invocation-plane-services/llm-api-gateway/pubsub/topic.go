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
	"time"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	pubsubpb "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

const defaultRetentionDuration = 24 * time.Hour

func FindOrCreateTopic(
	ctx context.Context,
	client *gcppubsub.Client,
	cfg Config,
) (*gcppubsub.Publisher, error) {
	publisher := client.Publisher(cfg.Topic)
	if !cfg.Create {
		return publisher, nil
	}

	_, err := client.TopicAdminClient.GetTopic(
		ctx,
		&pubsubpb.GetTopicRequest{Topic: publisher.String()},
	)
	if err == nil {
		return publisher, nil
	}
	if status.Code(err) != codes.NotFound {
		return nil, fmt.Errorf("get topic %q: %w", cfg.Topic, err)
	}

	_, err = client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{
		Name:                     publisher.String(),
		MessageRetentionDuration: durationpb.New(defaultRetentionDuration),
	})
	if err != nil {
		return nil, fmt.Errorf("create topic %q: %w", cfg.Topic, err)
	}

	return publisher, nil
}
