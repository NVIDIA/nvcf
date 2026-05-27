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

package gateway

import (
	"context"
	"fmt"
	"net/url"
	"os"

	gatewayConfig "ai-api-gateway-service/gateway_config"
	"ai-api-gateway-service/internal/reloadableconfig"
	"ai-api-gateway-service/router"

	"github.com/goccy/go-json"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/servers"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/tracing"
)

type Config struct {
	OTELExporterOTLPEndpoint     string `mapstructure:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	TracingAccessToken           string `mapstructure:"TRACING_ACCESS_TOKEN"`
	SecretsPath                  string `mapstructure:"SECRETS_PATH"`
	MappingPath                  string `mapstructure:"MAPPING_PATH"`
	NvcfApiEndpoint              string `mapstructure:"NVCF_API_ENDPOINT"`
	PrivateModelNameRegexPattern string `mapstructure:"PRIVATE_MODEL_NAME_REGEX_PATTERN"`
	PodIP                        string `mapstructure:"POD_IP"`
	AWSRegion                    string `mapstructure:"AWS_REGION"`
	ShadowMaxConcurrent          int    `mapstructure:"SHADOW_MAX_CONCURRENT"`
}

type NVCFGateway struct {
	servers.Server
}

func swapRouterWhenNotified(ctx context.Context, mappings reloadableconfig.ReloadableConfig[gatewayConfig.GatewayConfig], config Config, router *router.SwappableRouter) {
	go func() {
		for {
			err := mappings.Get().WaitForNotification(ctx)
			if err != nil {
				return
			}

			// swap router with new mappings
			newMux, err := buildChiMux(mappings.Get(), config)
			if err != nil {
				zap.L().Error("Failed to build ChiMux, skipping router swap", zap.Error(err))
				continue
			}
			router.SetNewMux(newMux)
			zap.L().Info("Finished swapping the new ChiMux")
		}
	}()
}

func NewNVCFGateway(logger *logs.ZapLogger, config Config) (*NVCFGateway, error) {
	if config.SecretsPath == "" {
		config.SecretsPath = "vault/secrets.json"
	}
	if config.NvcfApiEndpoint == "" {
		return nil, fmt.Errorf("NVCF_API_ENDPOINT is required")
	}
	if config.PrivateModelNameRegexPattern == "" {
		config.PrivateModelNameRegexPattern = "^(_|stg/|playground_|nvdev/|internal/|private/)|(-staging|-turbo$)"
	}
	if config.MappingPath == "" {
		return nil, fmt.Errorf("MAPPING_PATH is required")
	}

	if config.TracingAccessToken == "" {
		secrets, _ := os.ReadFile(config.SecretsPath)
		var secretsMap map[string]any
		_ = json.Unmarshal(secrets, &secretsMap)
		if token, ok := secretsMap["tracingAccessToken"].(string); ok {
			config.TracingAccessToken = token
		}
	}

	otelUrl, err := url.Parse(config.OTELExporterOTLPEndpoint)
	if err != nil {
		return nil, err
	}

	mappings, err := gatewayConfig.SetupConfigWithConfigPath(config.MappingPath)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	router, err := buildRouter(mappings.Get(), config)
	if err != nil {
		cancel()
		return nil, err
	}
	http2Server := createHttp2Server("0.0.0.0:10081", router)
	http2ServerPair := servers.ServerFuncPair{
		Execute: func() error {
			zap.L().Info("", zap.String("transport", "http/2"), zap.String("addr", http2Server.Addr))
			return http2Server.ListenAndServe()
		},
		Interrupt: func(error) {
			cancel()
			_ = http2Server.Shutdown(context.Background())
		},
	}
	swapRouterWhenNotified(ctx, mappings, config, router)

	hostname, _ := os.Hostname()
	return &NVCFGateway{
		Server: servers.NewGRPCServer(&servers.GRPCConfig{
			HTTPAddr:  "0.0.0.0:10080",
			GRPCAddr:  "0.0.0.0:10085",
			AdminAddr: "0.0.0.0:10083",
			BaseServerConfig: &servers.BaseServerConfig{
				ServiceName: "gdn-nvcf-ai-api-gateway-service",
				Tracing: tracing.OTELConfig{
					Enabled:     otelUrl.Host != "",
					Endpoint:    otelUrl.Host,
					Insecure:    otelUrl.Scheme == "http",
					AccessToken: config.TracingAccessToken,
					Attributes: tracing.Attributes{
						Extra: map[string]string{
							"host.id": hostname,
							"host.ip": config.PodIP,
							"host.dc": config.AWSRegion,
						},
					},
				},
			},
		},
			servers.WithLogger(logger),
			servers.WithRegisterServer(func(_ *grpc.Server) {
				// This gateway serves HTTP only; no gRPC services are registered.
			}),
			servers.WithHttpHealthEndpoints(healthPath),
			servers.WithAdditionalServers(http2ServerPair),
		),
	}, nil
}
