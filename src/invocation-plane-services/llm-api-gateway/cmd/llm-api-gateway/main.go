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

package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	zlog "github.com/rs/zerolog/log"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/server"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const defaultGatewayShutdownTimeout = 5 * time.Second

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to load configuration")
	}
	telemetry.SetServiceName(cfg.Telemetry.ServiceName)

	observability, err := telemetry.Init(context.Background(), telemetry.RuntimeConfig{
		MetricsPort: cfg.Telemetry.MetricsPort,
	})
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to initialize open telemetry")
	}
	defer func() {
		if shutdownErr := observability.Shutdown(context.Background()); shutdownErr != nil {
			zlog.Error().Err(shutdownErr).Msg("failed to shutdown open telemetry")
		}
	}()

	inferenceProvider, err := provider.NewStargateProvider(cfg.Stargate)
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to initialize inference provider")
	}

	var authClient *nvcf.GRPCClient
	if cfg.NVCF.GRPCAddr != "" {
		authClient, err = nvcf.NewClient(nvcf.Config{
			Addr:               cfg.NVCF.GRPCAddr,
			SecretsPath:        cfg.NVCF.SecretsPath,
			OAuth2ProviderHost: cfg.NVCF.OAuth2ProviderHost,
			Insecure:           cfg.NVCF.GRPCInsecure,
			Timeout:            cfg.NVCF.GRPCTimeout,
		})
		if err != nil {
			zlog.Fatal().Err(err).Msg("failed to initialize nvcf grpc auth client")
		}
	}

	e, err := server.New(cfg, inferenceProvider, authClient)
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to initialize gateway")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runGateway(
		ctx,
		cfg.Server.Addr,
		shutdownTimeout(cfg.Server.WriteTimeout),
		e.Start,
		e.Shutdown,
	); err != nil {
		zlog.Fatal().Err(err).Msg("gateway exited unexpectedly")
	}
}

func runGateway(
	ctx context.Context,
	addr string,
	timeout time.Duration,
	start func(string) error,
	shutdown func(context.Context) error,
) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- start(addr)
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if err := shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}

		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func shutdownTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultGatewayShutdownTimeout
	}
	return timeout
}
