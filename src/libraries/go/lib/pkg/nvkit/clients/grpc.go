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

package clients

import (
	"time"

	middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	retry "github.com/grpc-ecosystem/go-grpc-middleware/retry"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

var DefaultKeepaliveParams = keepalive.ClientParameters{
	Time:                5 * time.Minute,  // client ping server if no activity for this long
	Timeout:             10 * time.Second, // wait for ping ack before considering connection dead
	PermitWithoutStream: true,             // allow pings when no active streams
}

type GRPCClientConfig struct {
	*BaseClientConfig
	// Overrides all client dial options if set.
	DialOptOverrides []grpc.DialOption `mapstructure:"dialOpts,omitempty"`
	// Client metadata to store any client specific information
	Metadata map[string]interface{} `mapstructure:"metadata,omitempty"`
	// Keepalive configuration. If nil, default keepalive parameters will be applied.
	Keepalive *keepalive.ClientParameters `mapstructure:"keepalive,omitempty"`
}

func (cfg *GRPCClientConfig) AddClientFlags(cmd *cobra.Command, clientName string) bool {
	if cmd == nil || cfg == nil || clientName == "" {
		return false
	}
	cfg.BaseClientConfig = &BaseClientConfig{}
	return cfg.BaseClientConfig.AddClientFlags(cmd, clientName)
}

// DialOptions : generates common dial options for this client based on values in the config.
func (cfg *GRPCClientConfig) DialOptions() ([]grpc.DialOption, error) {
	// Return overrides if they're present
	if len(cfg.DialOptOverrides) > 0 {
		return cfg.DialOptOverrides, nil
	}

	// Build dial options from flags
	var opts []grpc.DialOption
	var unaryInterceptors []grpc.UnaryClientInterceptor
	var streamInterceptors []grpc.StreamClientInterceptor

	// Add TLS support
	tlsConfig, err := cfg.TLS.ClientTLSConfig()
	if err != nil {
		return nil, err
	}
	tlsOpts := grpc.WithTransportCredentials(insecure.NewCredentials())
	if tlsConfig != nil {
		tlsOpts = grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	}
	opts = append(opts, tlsOpts)

	// Add Auth interceptor
	rpcCred, err := cfg.AuthnCfg.GRPCClientWithAuth()
	if err != nil {
		return nil, err
	}
	if rpcCred != nil {
		authnOpts := grpc.WithPerRPCCredentials(rpcCred)
		opts = append(opts, authnOpts)
	}

	// Add retry interceptor
	retryOpts := []retry.CallOption{
		retry.WithBackoff(retry.BackoffLinear(100 * time.Millisecond)),
		retry.WithCodes(codes.Internal, codes.Aborted, codes.Unavailable),
	}
	unaryInterceptors = append(unaryInterceptors,
		retry.UnaryClientInterceptor(retryOpts...))
	streamInterceptors = append(streamInterceptors,
		retry.StreamClientInterceptor(retryOpts...))

	// Build interceptor chain
	opts = append(opts, grpc.WithUnaryInterceptor(middleware.ChainUnaryClient(unaryInterceptors...)))
	opts = append(opts, grpc.WithStreamInterceptor(middleware.ChainStreamClient(streamInterceptors...)))

	// Add OpenTelemetry stats handler
	opts = append(opts, grpc.WithStatsHandler(otelgrpc.NewClientHandler()))

	// add keepalive for connections by default
	keepaliveParams := DefaultKeepaliveParams
	if cfg.Keepalive != nil {
		keepaliveParams = *cfg.Keepalive
	}
	opts = append(opts, grpc.WithKeepaliveParams(keepaliveParams))

	return opts, nil
}

// Dial : creates a connection to the gRPC service specified. Does not attempt to validate this connection until a
//
//	request is made unless a dial option of WithBlock() is specified in DialOptOverrides.
func (cfg *GRPCClientConfig) Dial() (*grpc.ClientConn, error) {
	dialOpts, err := cfg.DialOptions()
	if err != nil {
		return nil, err
	}

	conn, err := grpc.Dial(cfg.Addr, dialOpts...) //nolint:staticcheck // SA1019: grpc.Dial is deprecated but still supported throughout 1.x
	if err != nil {
		return nil, err
	}
	return conn, nil
}
