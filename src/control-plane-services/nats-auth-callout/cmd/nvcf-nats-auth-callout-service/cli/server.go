/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/logger"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/router"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/service"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/tracing"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/version"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func makeServerCmd() *cobra.Command {
	// Initialize config early to get default values for flags (without custom config)
	cfg, err := config.InitConfig("nvcf-nats-auth-callout-service")
	var defaultPort, defaultServiceName string
	if err == nil {
		defaultPort = cfg.Server.Port
		defaultServiceName = cfg.Service.Name
	}
	config.ResetConfig() // avoid conflicts with file load ordering compared to env vars
	// If config fails to load, use empty strings and let the config system handle defaults

	cmd := &cobra.Command{
		Use:        "server",
		Aliases:    nil,
		SuggestFor: nil,
		Short:      "Simple HTTP server with ping and healthz endpoints",
		Long: `Simple HTTP server command.

Usage:
  nvcf-nats-auth-callout-service server --help // for usage
  nvcf-nats-auth-callout-service server --port 8080
  nvcf-nats-auth-callout-service server --service-name my-service
  nvcf-nats-auth-callout-service server --config /path/to/config.yaml
  nvcf-nats-auth-callout-service server --secrets-file /path/to/secrets.yaml

Configuration can be provided via:
  1. Command line flags (highest priority)
  2. Environment variables with NVCF_NATS_AUTH_CALLOUT_SERVICE prefix:
     NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH=/path/to/config.yaml
     NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH=/path/to/secrets.json
     NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=9090
     NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME=my-service
     NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NATS__URL=nats://localhost:4222
     NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SEED=SUA...
     NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SIGNATURE=SAA...
     NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED=true
  3. Custom config file (--config flag or NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH)
  4. Secrets file (--secrets-file flag or NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH)
  5. Embedded defaults (lowest priority)

Examples:
  # Using command line flags
  nvcf-nats-auth-callout-service server --port 9090 --service-name my-service --nats-url nats://localhost:4222 --nkey-seed SUA... --nkey-signature SAA...

  # Using environment variables
  NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=9090 NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME=my-service NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NATS__URL=nats://localhost:4222 NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SEED=SUA... NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SIGNATURE=SAA... NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED=true nvcf-nats-auth-callout-service server
  
  # Using secrets file via flag
  nvcf-nats-auth-callout-service server --secrets-file /path/to/secrets.json
  
  # Using secrets file via environment variable
  NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH=/path/to/secrets.json nvcf-nats-auth-callout-service server

  # Using custom config file via flag
  nvcf-nats-auth-callout-service server --config /path/to/my-config.yaml
  
  # Using custom config file via environment variable
  NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH=/path/to/my-config.yaml nvcf-nats-auth-callout-service server

  # Config file format (YAML):
  server:
    port: "9090"
  service:
    name: "my-service"
    nats_url: "nats://localhost:4222"
    nkey_seed: "SUA..." # NKey seed for authentication
    nkey_signature: "SAA..." # Signing key seed for JWT signing
  tracing:
    enabled: true
`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(cmd)
		},
	}

	// Add flags with defaults from config
	cmd.Flags().StringP("port", "p", defaultPort, "server port")
	cmd.Flags().String("service-name", defaultServiceName, "service name")
	cmd.Flags().String("nats-url", "", "NATS server URL")
	cmd.Flags().String("nkey-seed", "", "NKey seed for authentication")
	cmd.Flags().String("nkey-signature", "", "Signing key seed for JWT signing")
	cmd.Flags().StringP("config", "c", "", "path to configuration file")
	cmd.Flags().StringP("secrets-file", "s", "", "path to secrets file")

	return cmd
}

func runServer(cmd *cobra.Command) error {
	// Get custom config path from flag
	customConfigPath, _ := cmd.Flags().GetString("config")

	// Check environment variable if flag not provided (CLI flag > Environment variable > Default)
	if customConfigPath == "" {
		if envConfigPath := os.Getenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH"); envConfigPath != "" {
			customConfigPath = envConfigPath
		}
	}

	secretFilePath, _ := cmd.Flags().GetString("secrets-file")

	// Check environment variable if flag not provided (CLI flag > Environment variable > Default)
	if secretFilePath == "" {
		if envSecretFilePath := os.Getenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH"); envSecretFilePath != "" {
			secretFilePath = envSecretFilePath
		}
	}

	// Initialize configuration with custom config path (if provided)
	cfg, err := config.InitConfig("nvcf-nats-auth-callout-service", customConfigPath, secretFilePath)
	if err != nil {
		return fmt.Errorf("failed to initialize config: %w", err)
	}

	// Bind flags to configuration
	if err := config.BindFlag("server.port", cmd, "port"); err != nil {
		return fmt.Errorf("failed to bind port flag: %w", err)
	}
	if err := config.BindFlag("service.name", cmd, "service-name"); err != nil {
		return fmt.Errorf("failed to bind service-name flag: %w", err)
	}
	if err := config.BindFlag("service.nats_url", cmd, "nats-url"); err != nil {
		return fmt.Errorf("failed to bind nats-url flag: %w", err)
	}
	if err := config.BindFlag("service.nkey_seed", cmd, "nkey-seed"); err != nil {
		return fmt.Errorf("failed to bind nkey-seed flag: %w", err)
	}
	if err := config.BindFlag("service.nkey_signature", cmd, "nkey-signature"); err != nil {
		return fmt.Errorf("failed to bind nkey-signature flag: %w", err)
	}

	// Get current configuration with all sources (including bound flags)
	cfg, err = config.GetCurrentConfig()
	if err != nil {
		return fmt.Errorf("failed to get current config: %w", err)
	}

	// Validate configuration
	if err := config.ValidateConfig(cfg); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Initialize logger from configuration
	appLogger, err := logger.NewLogger(&cfg.Logging)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	defer appLogger.Sync()

	// Initialize multi-provider tracing
	ctx := context.Background()
	tracingConfig := tracing.TracingConfig{
		Provider: cfg.Tracing.Provider,
		Enabled:  cfg.Tracing.Enabled,
		Otel: tracing.OtelTracingConfig{
			Endpoint:      cfg.Tracing.Otel.Endpoint,
			HTTPEndpoint:  cfg.Tracing.Otel.HTTPEndpoint,
			Insecure:      cfg.Tracing.Otel.Insecure,
			Environment:   cfg.Tracing.Otel.Environment,
			SamplingRatio: cfg.Tracing.Otel.SamplingRatio,
			Headers: tracing.TracingHeadersConfig{
				Authorization: cfg.Tracing.Otel.Headers.Authorization,
				XAPIKey:       cfg.Tracing.Otel.Headers.XAPIKey,
			},
			TimeoutMs:          cfg.Tracing.Otel.TimeoutMs,
			RetryDelayMs:       cfg.Tracing.Otel.RetryDelayMs,
			MaxExportBatchSize: cfg.Tracing.Otel.MaxExportBatchSize,
			ExportTimeoutMs:    cfg.Tracing.Otel.ExportTimeoutMs,
			MaxQueueSize:       cfg.Tracing.Otel.MaxQueueSize,
			ScheduleDelayMs:    cfg.Tracing.Otel.ScheduleDelayMs,
			Compression:        cfg.Tracing.Otel.Compression,
		},
		Lightstep: tracing.LightstepTracingConfig{
			Endpoint:           cfg.Tracing.Lightstep.Endpoint,
			AccessToken:        cfg.Tracing.Lightstep.AccessToken,
			Environment:        cfg.Tracing.Lightstep.Environment,
			SamplingRatio:      cfg.Tracing.Lightstep.SamplingRatio,
			TimeoutMs:          cfg.Tracing.Lightstep.TimeoutMs,
			MaxExportBatchSize: cfg.Tracing.Lightstep.MaxExportBatchSize,
			ExportTimeoutMs:    cfg.Tracing.Lightstep.ExportTimeoutMs,
			MaxQueueSize:       cfg.Tracing.Lightstep.MaxQueueSize,
			ScheduleDelayMs:    cfg.Tracing.Lightstep.ScheduleDelayMs,
			Compression:        cfg.Tracing.Lightstep.Compression,
			Insecure:           cfg.Tracing.Lightstep.Insecure,
		},
	}

	tracingProvider, err := tracing.NewTracingProvider(appLogger, tracingConfig)
	if err != nil {
		appLogger.Error("Failed to create tracing provider", zap.Error(err))
		return fmt.Errorf("failed to create tracing provider: %w", err)
	}

	shutdown, err := tracingProvider.InitTracing(ctx, cfg.Service.Name, version.GetVersion())
	if err != nil {
		appLogger.Error("Failed to initialize tracing", zap.Error(err))
		return fmt.Errorf("failed to initialize tracing: %w", err)
	}
	defer func() {
		if err := shutdown(ctx); err != nil {
			if !errors.Is(err, context.Canceled) {
				appLogger.Error("Failed to shutdown tracing", zap.Error(err))
			}
		}
	}()

	appLogger.Info("Tracing provider initialized", zap.String("provider_info", tracingProvider.GetProviderInfo()))

	// Log configuration information
	appLogger.Info("configuration loaded",
		zap.String("port", cfg.Server.Port),
		zap.String("service_name", cfg.Service.Name),
		zap.String("log_level", cfg.Logging.Level),
		zap.String("log_format", cfg.Logging.Format),
		zap.String("log_output", cfg.Logging.Output),
		zap.Bool("tracing_enabled", cfg.Tracing.Enabled),
	)

	appLogger.Info("Starting server",
		zap.String("version", version.Version),
		zap.String("service_name", cfg.Service.Name),
		zap.Bool("tracing_enabled", cfg.Tracing.Enabled),
	)

	// Initialize service
	service, err := service.NewFromConfig(ctx, &cfg.Service, appLogger)
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}

	// Create router
	routerConfig := &router.Config{
		ServiceName:    cfg.Service.Name,
		TracingEnabled: cfg.Tracing.Enabled,
		Metrics: &router.MetricsConfig{
			Enabled: cfg.Metrics.Enabled,
			Port:    cfg.Metrics.Port,
		},
	}
	r := router.New(appLogger, routerConfig)

	// Create a context that listens for the interrupt signal from the OS
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start metrics server if enabled
	if cfg.Metrics.Enabled {
		go func() {
			appLogger.Info("Metrics server starting", zap.String("address", ":"+cfg.Metrics.Port))
			metricsHandler := r.GetMetricsHandler()
			if err := http.ListenAndServe(":"+cfg.Metrics.Port, metricsHandler); err != nil {
				appLogger.Error("Metrics server failed to start", zap.Error(err))
			}
		}()
	}

	// Start server in a goroutine
	go func() {
		appLogger.Info("Server starting", zap.String("address", ":"+cfg.Server.Port))
		if err := r.Engine().Run(":" + cfg.Server.Port); err != nil {
			appLogger.Error("Server failed to start", zap.Error(err))
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	<-ctx.Done()

	// Restore default behavior on the interrupt signal and notify user of shutdown
	stop()
	appLogger.Info("Server is shutting down gracefully, press Ctrl+C again to force")

	service.Stop()

	// The context is used to inform the server it has 5 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	appLogger.Info("Server shutdown complete")
	return nil
}
