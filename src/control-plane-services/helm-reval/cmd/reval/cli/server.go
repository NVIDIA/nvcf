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
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
	"github.com/spf13/viper"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/authorizers"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/httpapi"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/authz"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/service"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry/logging"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry/tracing"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/reval"
)

const (
	serviceName = "reval"
)

func runServer(cfg *config.RevalConfig, v *viper.Viper, factory AuthorizerFactory) error {
	initContext := context.Background()

	logger, loggerAtomicLevel, undoReplace := logging.InitializeLogger(cfg)
	defer func() { _ = logger.Sync() }()
	defer undoReplace()
	logger.Info("Starting ReVal server", zap.String("gitCommit", cfg.Telemetry.GitCommit))

	meterProvider := metrics.SetupGlobalOtelMetrics(logger)
	defer func() { _ = meterProvider.Shutdown(initContext) }()
	// Create a Meter for our application
	meter := meterProvider.Meter(config.ApiSvcName)

	metricsServer := metrics.NewMetricsServer(logger, cfg.HTTP.MetricsPort)

	if cfg.Tracing.Enabled {
		undoTracingProvider, _ := tracing.ApplyTracing(initContext, &cfg.Tracing, &cfg.Telemetry, logger)
		defer undoTracingProvider()
	}

	initContext, span := otel.Tracer(serviceName).Start(initContext, "reval.init")
	defer span.End()

	var auths []authorizers.Authorizer
	if factory != nil {
		var err error
		auths, err = factory(initContext, v, cfg, logger)
		if err != nil {
			logger.Error("failed to build authorizer chain", zap.Error(err))
			return err
		}
		logger.Info("authorizer chain initialized via custom factory")
	} else {
		// Default: self-hosted JWKS JWT and/or remote introspection (OIDC).
		var err error
		auths, err = authorizers.BuildChain(initContext, &cfg.Auth, logger)
		if err != nil {
			logger.Error("failed to build authorizer chain", zap.Error(err))
			return err
		}
		logger.Info("authentication active",
			zap.Bool("jwks", cfg.Auth.JWT.Enabled),
			zap.Bool("oidc_introspect", cfg.Auth.OIDC.Enabled))
	}

	authzMiddleware := authorizers.EvaluateMiddleware(
		authorizers.Chain(auths), logger, authz.ServeUnauthorized)

	managementHttpServer := serveManagementRoutes(logger, loggerAtomicLevel, cfg.HTTP)

	hopts := reval.HandlerOptions{
		SkipValidateObjects: cfg.SkipValidateObjects,
		SkipValidateImages:  cfg.SkipValidateImages,
		PreserveLabels:      cfg.PreserveLabels,
		PreserveAnnotations: cfg.PreserveAnnotations,
	}
	handler, err := reval.NewHandler(logger, config.ApiSvcName, hopts)
	if err != nil {
		logger.Error("Failed to create reval handler", zap.Error(err))
		return err
	}
	service := service.NewHttpService(handler, meter)

	router := chi.NewRouter()
	router.NotFound(httpapi.ServeNotFound)

	oldGrpcMetricsMiddleware := metrics.CreateOldGrpcMetricsMiddleWare(logger, meter)

	middlewares := chi.Chain(
		metrics.CreateHttpMetricsMiddleWare(logger, meter),
		tracing.NewOtelTraceMiddleware(),
		logging.NewZapLoggerMiddleware(logger),
		authzMiddleware,
		render.SetContentType(render.ContentTypeJSON),
		// This middleware is the last one in order to recover after panic
		// and close the rest of middlewares with a 500 response
		chiMiddleware.Recoverer,
	)

	v1 := chi.NewRouter()
	{
		v1.With(oldGrpcMetricsMiddleware("Validate"), middlewares.Handler).Post("/validate", service.Validate)
		v1.With(oldGrpcMetricsMiddleware("Render"), middlewares.Handler).Post("/render", service.Render)

		router.Mount("/v1", v1)
	}

	mainHttpServer := &http.Server{
		Addr:     getAddr(cfg.HTTP.ApiPort, cfg.HTTP.Local),
		Handler:  router,
		ErrorLog: logging.NewLoggerWithZapWriter(logger),
	}
	logger.Info("serving traffic routes", zap.String("addr", mainHttpServer.Addr))

	// We measure the initialization time of the server until this point.
	// And a span is created to represent that.
	span.End()

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	gracefulHandle(shutdownCtx, logger, metricsServer, managementHttpServer, mainHttpServer)

	return nil
}

func serveManagementRoutes(logger *zap.Logger, loggerAtomicLevel *zap.AtomicLevel, cfg config.HTTPConfig) *http.Server {
	router := chi.NewRouter()
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, err := w.Write([]byte("OK"))
		if err != nil {
			logger.Error("failed to write healthz response", zap.Error(err))
		}
	})

	router.Get("/log_level", loggerAtomicLevel.ServeHTTP)

	httpServer := &http.Server{
		Addr:     getAddr(cfg.ManagementPort, cfg.Local),
		Handler:  router,
		ErrorLog: logging.NewLoggerWithZapWriter(logger),
	}
	logger.Info("serving management routes", zap.String("addr", httpServer.Addr))

	return httpServer
}

func getAddr(port uint16, local bool) string {
	var host string
	if local {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// gracefulHandle starts each server in its own goroutine and blocks until ctx
// is cancelled (e.g. via signal.NotifyContext in production or a test cancel
// func in unit tests). Once the context is done it concurrently shuts every
// server down with a 10-second drain deadline.
func gracefulHandle(ctx context.Context, logger *zap.Logger, servers ...*http.Server) {
	for _, server := range servers {
		go func(server *http.Server) {
			if err := server.ListenAndServe(); err != nil {
				if !errors.Is(err, http.ErrServerClosed) {
					logger.Fatal("unable to listen and serve", zap.Error(err))
				}
			}
		}(server)
	}

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(server *http.Server) {
			defer wg.Done()
			logger.Info("shutting down http server", zap.String("addr", server.Addr))
			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Error("error shutting down server", zap.Error(err))
			}
		}(server)
	}
	wg.Wait()
}
