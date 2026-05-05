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

	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/pool"
)

type protoMessage[T any] interface {
	*T
	proto.Message
}

func ProtoReceiver[T any, PT protoMessage[T]](
	receiver func(context.Context, *T, Message) error,
) Receiver {
	protoPool := pool.NewWithReleaser(
		func() *T {
			return new(T)
		},
		func(x *T) {
			proto.Reset(PT(x))
		},
	)

	return func(ctx context.Context, data []byte, msg Message) error {
		protoData := protoPool.Get()
		defer protoPool.Put(protoData)

		if err := proto.Unmarshal(data, PT(protoData)); err != nil {
			msg.Nack()
			return err
		}

		return receiver(ctx, protoData, msg)
	}
}

type jsonMessage[T any] interface {
	*T
}

func JSONReceiver[T any, JT jsonMessage[T]](
	receiver func(context.Context, T, Message) error,
) Receiver {
	return func(ctx context.Context, data []byte, msg Message) error {
		var jsonData T
		if err := json.Unmarshal(data, JT(&jsonData)); err != nil {
			msg.Nack()
			return err
		}
		return receiver(ctx, jsonData, msg)
	}
}
