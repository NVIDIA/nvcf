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
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	echo "github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/auth/statickeys"
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
		MetricsPort:        cfg.Telemetry.MetricsPort,
		TracingAccessToken: cfg.Telemetry.TracingAccessToken,
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

	authClient, authMode, err := newAuthClient(cfg, zlog.Logger)
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to initialize request authentication")
	}

	e, err := server.New(cfg, inferenceProvider, authClient)
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to initialize gateway")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	zlog.Info().
		Str("addr", cfg.Server.Addr).
		Str("auth_mode", string(authMode)).
		Bool("public_read_endpoints", cfg.PublicReadEndpointsEnabled()).
		Bool("tls", cfg.Server.TLSEnabled()).
		Msg("starting llm api gateway")

	if err := runGateway(
		ctx,
		cfg.Server.Addr,
		shutdownTimeout(cfg.Server.WriteTimeout),
		gatewayStart(echoStarter{e: e}, cfg.Server),
		e.Shutdown,
	); err != nil {
		zlog.Fatal().Err(err).Msg("gateway exited unexpectedly")
	}
}

// newAuthClient builds the caller authenticator for the configured mode. It
// fails closed: without NVCF_GRPC_ADDR or API_KEYS_PATH the gateway starts
// only when ALLOW_ANONYMOUS is set, and then with a warning. A nil client
// means anonymous access.
func newAuthClient(cfg *config.Config, logger zerolog.Logger) (nvcf.Client, config.AuthMode, error) {
	mode, err := cfg.AuthMode()
	if err != nil {
		return nil, "", err
	}

	switch mode {
	case config.AuthModeNVCF:
		grpcAuthClient, err := nvcf.NewClient(nvcf.Config{
			Addr:               cfg.NVCF.GRPCAddr,
			SecretsPath:        cfg.NVCF.SecretsPath,
			OAuth2ProviderHost: cfg.NVCF.OAuth2ProviderHost,
			Insecure:           cfg.NVCF.GRPCInsecure,
			Timeout:            cfg.NVCF.GRPCTimeout,
		})
		if err != nil {
			return nil, "", fmt.Errorf("initialize nvcf grpc auth client: %w", err)
		}
		return nvcf.NewCachedClient(grpcAuthClient), mode, nil
	case config.AuthModeStaticKeys:
		authorizer, err := statickeys.New(cfg.Auth.APIKeysPath)
		if err != nil {
			return nil, "", fmt.Errorf("initialize static api key authorizer: %w", err)
		}
		return authorizer, mode, nil
	case config.AuthModeAnonymous:
		logger.Warn().
			Str("auth_mode", string(mode)).
			Msg("ALLOW_ANONYMOUS is set and no authenticator is configured: " +
				"the gateway accepts unauthenticated requests")
		return nil, mode, nil
	default:
		return nil, "", fmt.Errorf("unsupported auth mode %q", mode)
	}
}

// gatewayStarter starts the listener, in plaintext or over TLS.
type gatewayStarter interface {
	Start(address string) error
	StartTLS(address, certFile, keyFile string, reloadInterval time.Duration) error
}

// echoStarter starts the gateway through server.Start and server.StartTLS.
// e.Start and e.StartTLS would drop the handler server.New installs.
type echoStarter struct {
	e *echo.Echo
}

func (s echoStarter) Start(address string) error {
	return server.Start(s.e, address)
}

func (s echoStarter) StartTLS(address, certFile, keyFile string, reloadInterval time.Duration) error {
	return server.StartTLS(s.e, address, certFile, keyFile, reloadInterval)
}

// gatewayStart picks the listener for runGateway. Any TLS file selects TLS,
// so a half-configured pair fails at startup instead of serving plaintext.
func gatewayStart(s gatewayStarter, serverCfg config.ServerConfig) func(string) error {
	if !serverCfg.TLSEnabled() {
		return s.Start
	}
	return func(addr string) error {
		return s.StartTLS(addr, serverCfg.TLSCertFile, serverCfg.TLSKeyFile, serverCfg.TLSReloadInterval)
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
