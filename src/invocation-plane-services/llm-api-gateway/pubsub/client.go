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
	"os"
	"sync"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"google.golang.org/api/option"
)

type Config struct {
	ProjectID    string
	Topic        string
	Subscription string
	Endpoint     string
	EmulatorHost string
	Create       bool
}

var clientInitMutex sync.Mutex

func NewClient(ctx context.Context, cfg Config) (*gcppubsub.Client, error) {
	var opts []option.ClientOption
	opts = append(opts, option.WithTelemetryDisabled())

	if cfg.Endpoint != "" {
		opts = append(opts, option.WithEndpoint(cfg.Endpoint))
	}

	if cfg.EmulatorHost != "" {
		clientInitMutex.Lock()
		defer clientInitMutex.Unlock()

		orig, ok := os.LookupEnv("PUBSUB_EMULATOR_HOST")
		defer func() {
			if ok {
				_ = os.Setenv("PUBSUB_EMULATOR_HOST", orig)
				return
			}
			_ = os.Unsetenv("PUBSUB_EMULATOR_HOST")
		}()

		if err := os.Setenv("PUBSUB_EMULATOR_HOST", cfg.EmulatorHost); err != nil {
			return nil, fmt.Errorf("set PUBSUB_EMULATOR_HOST: %w", err)
		}
	}

	return gcppubsub.NewClient(ctx, cfg.ProjectID, opts...)
}
