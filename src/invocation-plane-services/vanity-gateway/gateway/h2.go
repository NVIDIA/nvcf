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
	config "ai-api-gateway-service/gateway_config"
	"ai-api-gateway-service/middleware"
	"ai-api-gateway-service/router"
	"fmt"
	"math"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/hellofresh/health-go/v5"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

const (
	maxRequestSize = 25 * 1024 * 1024 // 25 MB
	healthPath     = "/health"
	// max nvcf api timeout is one hour
	requestTimeout = 1*time.Hour + 10*time.Second
)

func buildRouter(mappings *config.GatewayConfig, serverConfig Config) (*router.SwappableRouter, error) {
	r, err := buildChiMux(mappings, serverConfig)
	if err != nil {
		return nil, err
	}

	swappableRouter := router.NewSwappableRouter(r)
	return swappableRouter, nil
}

// buildChiMux constructs the router from an already-validated GatewayConfig.
// Callers must ensure mappings have been validated (SetupConfigWithConfigPath
// handles this via WithPostLoadFunc on both initial load and reload).
func buildChiMux(mappings *config.GatewayConfig, serverConfig Config) (*chi.Mux, error) {
	// Compile the regex pattern
	re, err := regexp.Compile(serverConfig.PrivateModelNameRegexPattern)
	if err != nil {
		zap.L().Error("Error compiling regex")
		return nil, err
	}
	transport := middleware.TracedRoundTripper(&http.Transport{
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConnsPerHost: 64,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	})
	healthManager, err := healthManager(serverConfig.NvcfApiEndpoint, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to create health manager: %w", err)
	}

	vanityDirector, err := NewVanityDirector(serverConfig.NvcfApiEndpoint, transport)
	if err != nil {
		return nil, err
	}

	shadowMaxConcurrent := serverConfig.ShadowMaxConcurrent
	if shadowMaxConcurrent <= 0 {
		shadowMaxConcurrent = 256
	}
	shadower := NewTrafficShadower(shadowMaxConcurrent, requestTimeout)

	openAIDirector, err := NewOpenAIDirectorV2(mappings, re, vanityDirector, shadower)
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(middleware.RejectSpoofedShadowRequests(shadowHeader))
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(requestTimeout))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*.nvidia.com", "vscode-file://vscode-app"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowCredentials: true,
		AllowedHeaders:   []string{"*"},
	}))

	hostRouter := &middleware.HostRouter{}
	serverTelemetry := middleware.ServerTelemetryMiddleware()

	registerOpenAI(hostRouter, mappings, openAIDirector, healthManager, serverTelemetry)
	registerVanity(hostRouter, mappings, vanityDirector, healthManager, serverTelemetry)

	r.Use(hostRouter.Handler)
	r.With(serverTelemetry).Get(healthPath, healthManager.HandlerFunc)
	return r, nil
}

func createHttp2Server(addr string, swappableRouter *router.SwappableRouter) *http.Server {
	return &http.Server{
		Addr: addr,
		Handler: h2c.NewHandler(swappableRouter, &http2.Server{
			MaxConcurrentStreams: math.MaxInt32,
		}),
	}
}

// general pass-through vanity domains
func registerVanity(hostRouter *middleware.HostRouter, mappings *config.GatewayConfig, vanityDirector *VanityDirector, healthManager *health.Health, serverTelemetry func(http.Handler) http.Handler) {
	for _, vanity := range mappings.Vanity {
		r := chi.NewRouter()
		r.Use(serverTelemetry)
		for _, mapping := range vanity.Paths {
			target := VanityExecRequest{
				FunctionID:        mapping.FunctionID,
				FunctionVersionID: mapping.FunctionVersionID,
				PathOverride:      mapping.OutgoingPathOverride,
				UsePexec:          mapping.UsePexec,
				CustomHeaders:     mapping.CustomHeaders,
				EOL:               mapping.EOL,
				OfflineMessage:    mapping.OfflineMessage,
			}
			r.Post(mapping.Path, func(writer http.ResponseWriter, request *http.Request) {
				middleware.AddFunctionIDMetricAttribute(request.Context(), target.FunctionID)
				vanityDirector.ServeExec(target, writer, request)
			})
		}
		r.Get(healthPath, healthManager.HandlerFunc)
		r.Get("/v1/status/{requestId}", vanityDirector.ServePolling)
		hostRouter.Register(vanity.Host, chimiddleware.New(r))
	}
}

// openai specific domain
func registerOpenAI(hostRouter *middleware.HostRouter, mappings *config.GatewayConfig, openAIDirector *OpenAIDirector, healthManager *health.Health, serverTelemetry func(http.Handler) http.Handler) {
	r := chi.NewRouter()
	r.Use(serverTelemetry)
	r.Method(http.MethodPost, "/v1/chat/completions", chimiddleware.RequestSize(maxRequestSize)(http.HandlerFunc(openAIDirector.ServeChatCompletions)))
	r.Method(http.MethodPost, "/v1/completions", chimiddleware.RequestSize(maxRequestSize)(http.HandlerFunc(openAIDirector.ServeCompletions)))
	r.Method(http.MethodPost, "/v1/embeddings", chimiddleware.RequestSize(maxRequestSize)(http.HandlerFunc(openAIDirector.ServeEmbeddings)))
	r.Method(http.MethodPost, "/v1/responses", chimiddleware.RequestSize(maxRequestSize)(http.HandlerFunc(openAIDirector.ServeResponses)))
	r.Method(http.MethodPost, "/v1/images/generations", chimiddleware.RequestSize(maxRequestSize)(http.HandlerFunc(openAIDirector.ServeImageGenerations)))
	r.Method(http.MethodPost, "/v1/images/edits", chimiddleware.RequestSize(maxRequestSize)(http.HandlerFunc(openAIDirector.ServeImageEdits)))
	r.Method(http.MethodPost, "/v1/images/variations", chimiddleware.RequestSize(maxRequestSize)(http.HandlerFunc(openAIDirector.ServeImageVariations)))
	r.Get("/v1/models", openAIDirector.ListModels)
	r.Get("/v1/models/{model}", openAIDirector.GetModel)
	r.Get("/v1/models/{company}/{model}", openAIDirector.GetModel)

	r.Get(healthPath, healthManager.HandlerFunc)

	// special domain for openai
	hostRouter.Register(mappings.OpenAI.Host, chimiddleware.New(r))
}
