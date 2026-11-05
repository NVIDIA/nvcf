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
	"net/http"
	"time"

	"github.com/dsx-ai-factory/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/dsx-ai-factory/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"github.com/hellofresh/health-go/v5"
	pb "google.golang.org/grpc/health/grpc_health_v1"
)

// natsConnState is the part of invocation.ConnState the probes read. Reading it
// never takes the NATS connection's lock, which nats.go holds through each
// reconnect attempt, so a probe answers within its timeout while NATS is
// reconnecting.
type natsConnState interface {
	Connected() bool
	Closed() bool
}

func healthManager(nvcfApiHost string, natsState natsConnState) (*health.Health, error) {
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
			if !natsState.Connected() {
				return fmt.Errorf("nats connection not connected")
			}
			return nil
		},
	}))
}

// livenessHandler serves /livez on the admin port. It fails only when the NATS
// connection is closed: nats.go never reopens a closed connection, so a restart
// is the only recovery. nvcf-api and a reconnecting NATS connection are left
// to readiness, so their outages do not restart the pod.
func livenessHandler(natsState natsConnState) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if natsState.Closed() {
			http.Error(w, "nats connection closed", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
