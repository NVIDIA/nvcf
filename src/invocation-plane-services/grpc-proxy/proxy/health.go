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

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/clients"
	"github.com/hellofresh/health-go/v5"
	"github.com/nats-io/nats.go"
	pb "google.golang.org/grpc/health/grpc_health_v1"
)

func healthManager(nvcfApiHost string, nc *nats.Conn) (*health.Health, error) {
	nvcfFqdn, err := grpcSafeUrl(nvcfApiHost)
	if err != nil {
		return nil, err
	}
	tlsEnabled := nvcfFqdn.Scheme == "https"
	grpcClientConfig := clients.GRPCClientConfig{BaseClientConfig: &clients.BaseClientConfig{
		Addr: nvcfFqdn.Host,
		TLS: auth.TLSConfigOptions{
			Enabled: tlsEnabled,
		},
	}}
	conn, err := grpcClientConfig.Dial()
	if err != nil {
		return nil, err
	}
	client := pb.NewHealthClient(conn)
	return health.New(health.WithComponent(health.Component{
		Name: "grpc proxy",
	}), health.WithChecks(health.Config{
		Name:    "nvcf grpc api",
		Timeout: 5 * time.Second,
		Check: func(ctx context.Context) error {
			response, err := client.Check(ctx, &pb.HealthCheckRequest{})
			if err != nil {
				return err
			}
			if response.Status != pb.HealthCheckResponse_SERVING {
				return fmt.Errorf("invalid nvcf api health response %s", response.Status.String())
			}
			return nil
		},
	}, health.Config{
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
