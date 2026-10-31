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

package proxy

import (
	"context"
	"fmt"
	"time"

	"github.com/hellofresh/health-go/v5"
	"github.com/nats-io/nats.go"
)

// healthManager backs the readiness probe on /health. It checks only this
// pod's NATS connection. nvcf-api is deliberately not checked: every replica
// shares it, so gating on it would remove all replicas during an nvcf-api
// outage and cut gRPC streams that no longer need it.
func healthManager(nc *nats.Conn) (*health.Health, error) {
	return health.New(health.WithComponent(health.Component{
		Name: "grpc proxy",
	}), health.WithChecks(health.Config{
		Name:    "nats",
		Timeout: 5 * time.Second,
		Check: func(context.Context) error {
			status := nc.Status()
			if status != nats.CONNECTED {
				return fmt.Errorf("nats connection not connected: %s", status)
			}
			return nil
		},
	}))
}
