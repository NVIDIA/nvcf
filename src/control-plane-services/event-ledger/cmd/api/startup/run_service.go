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

package startup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	golibversion "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/klauspost/compress/gzhttp"
	"github.com/lestrrat-go/httprc"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/cmd/api/service"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/credentials"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/interfaces"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/middleware"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/tracing"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/policy"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/publisher"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/publisher/cloudevents"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/registrations"
)

// registerUnauthenticatedRoutes registers routes that must be reachable before
// (and regardless of) auth middleware: /health for liveness/readiness probes,
// and /info for build-version discovery. /info is wrapped with tracing and
// request logging via infoMiddleware; /health probes are left uninstrumented to
// avoid span and log spam.
func registerUnauthenticatedRoutes(router *mux.Router, server *service.Server, infoMiddleware func(http.Handler) http.Handler) {
	router.HandleFunc("/health", server.Health)
	router.Handle("/info", infoMiddleware(golibversion.Handler()))
}

func runService(cfg config.Config) error {
	ctx := context.Background()

	logger, undoLogger := logging.SetupLoggingFromConfig(&cfg.Logging, &cfg.Telemetry)
	defer undoLogger()

	// Replace zap's global logger so libraries/components using zap.L() (like nvkit) can also output logs correctly.
	zap.ReplaceGlobals(logger.Logger)

	// Log HTTP configuration being used
	logger.Warn("http transport configuration",
		zap.Int("max_idle_conns", cfg.HTTP.MaxIdleConns),
		zap.Int("max_idle_conns_per_host", cfg.HTTP.MaxIdleConnsPerHost),
		zap.Int("idle_conn_timeout_sec", cfg.HTTP.IdleConnTimeoutSec),
		zap.Int("tls_handshake_timeout_sec", cfg.HTTP.TLSHandshakeTimeoutSec),
		zap.Int("expect_continue_timeout_sec", cfg.HTTP.ExpectContinueTimeoutSec),
	)

	registrations.RegisterDBProviders(logger)

	undoTracingProvider := tracing.ApplyTracing(ctx, &cfg.Tracing, &cfg.Telemetry, logger)
	defer undoTracingProvider()

	// Initialize metrics
	middleware.SetupGlobalOtelMetrics(logger)
	metricsMiddleware := middleware.CreateHttpMetricsMiddleWare(logger)
	middleware.ServeMetrics(logger, cfg.Service.InternalPort, cfg.Profiling.Enabled)

	// Resolve the secrets file path, defaulting to the well-known Vault Agent path.
	secretsPath := cfg.SecretsPath
	if secretsPath == "" {
		secretsPath = "/vault/secrets/secrets.json"
	}

	// If Cassandra credentials are present in the secrets file, populate the config
	// before InitConns so the database client picks them up.
	if username, err := credentials.ReadTokenFromFile(secretsPath, "username"); err == nil {
		cfg.Database.CassandraConfig.Username = username
	}
	if password, err := credentials.ReadTokenFromFile(secretsPath, "password"); err == nil {
		cfg.Database.CassandraConfig.Password = password
	}

	conns, err := service.InitConns(cfg, logger)
	if err != nil {
		return err
	}

	storageClients := make([]interfaces.BatchStorageClient, 0, 1)

	// The CloudEvents client is optional; it can be disabled via --disable-cloudevent
	// (e.g. for local development where no CloudEvents endpoint is available).
	if cfg.Publisher.Cloudevents.Enabled {
		// Type assert DbHandler to CloudEventsResilienceHandler
		// Initialize CloudEvents metrics
		cloudEventsMetrics, err := middleware.CreateCloudEventsMetrics(logger)
		if err != nil {
			return err
		}

		resilienceHandler, ok := conns.DbHandler.(data_access.CloudEventsResilienceHandler)
		if !ok {
			return fmt.Errorf("database handler does not implement CloudEventsResilienceHandler interface")
		}

		resilientCloudEventsClient, err := cloudevents.NewResilientCloudEventsClient(
			cfg.Publisher.Cloudevents,
			resilienceHandler,
			logger,
			cloudEventsMetrics,
		)
		if err != nil {
			return err
		}
		defer resilientCloudEventsClient.Stop()

		storageClients = append(storageClients, resilientCloudEventsClient)
	} else {
		logger.Warn("CloudEvents client disabled via --disable-cloudevent")
	}
	if len(storageClients) == 0 {
		logger.Warn("no publisher storage clients configured; stage transition events will not be exported")
	}

	publisher, err := publisher.NewBatchedPublisher(cfg.Publisher.BatchedPublisher, storageClients, logger)
	if err != nil {
		return err
	}
	defer publisher.Stop()

	server := service.NewServer(conns, logger, publisher, cfg.Telemetry.ServiceVersion, &cfg.HTTP, cfg.Pagination, cfg.Stats)
	router := mux.NewRouter()
	router.Use(middleware.EnableCORS)
	router.Use(middleware.BodyLimitMiddleware(10 * 1024 * 1024)) // 10MB limit
	router.Use(metricsMiddleware)

	spanNameFormatter := func(operation string, r *http.Request) string {
		return r.Method + " " + operation // e.g., "GET /api/resource"
	}
	tracingMW := otelmux.Middleware("deployment-stages", otelmux.WithSpanNameFormatter(spanNameFormatter))
	loggerMW := logging.LoggerMiddleware(logger)

	// /info runs through tracing and request logging (RED metrics already apply
	// on the base router). /health is left uninstrumented to avoid probe span and
	// log spam.
	registerUnauthenticatedRoutes(router, server, func(h http.Handler) http.Handler {
		return tracingMW(loggerMW(h))
	})

	authRouter := router.PathPrefix("").Subrouter()
	authRouter.Use(tracingMW)
	authRouter.Use(loggerMW)

	var requireLocalScopeCheck = false

	if cfg.Auth.Enabled {
		// Validate auth configuration
		if err := config.ValidateAuthConfig(cfg.Auth, cfg.SelfManaged); err != nil {
			logger.Error("invalid auth configuration", zap.Error(err))
			return err
		}
		if err := config.ValidateEndpointAuthConfig(cfg); err != nil {
			logger.Error("invalid endpoint authentication configuration", zap.Error(err))
			return err
		}

		// Validate JWK URL format
		_, err := url.Parse(cfg.Auth.JWKSetUrl)
		if err != nil {
			logger.Error("invalid jwk-set-url format", zap.Error(err), zap.String("url", cfg.Auth.JWKSetUrl))
			return fmt.Errorf("invalid jwk-set-url format: %w", err)
		}

		cacheDuration := time.Duration(cfg.Auth.CacheRefreshInterval) * time.Second
		cacheDurationString := cacheDuration.String()
		logger.Sugar().Warnf("cache duration: %s", cacheDurationString)

		logger.Warn("initializing jwk cache")
		// Create error sink to log JWKS cache refresh errors
		errSink := httprc.ErrSinkFunc(func(err error) {
			logger.Error("jwks cache refresh error", zap.Error(err))
		})
		jwkCache := jwk.NewCache(ctx, jwk.WithErrSink(errSink), jwk.WithRefreshWindow(cacheDuration))

		switch cfg.Auth.Provider {
		case "jwt":
			logger.Warn("auth enabled using jwt provider")

			logger.Warn("initializing jwt middleware",
				zap.String("jwk_url", cfg.Auth.JWKSetUrl),
				zap.Duration("cache_duration", cacheDuration))

			jwtOpts := middleware.NewJWTParserOptions(cfg.Auth.JWKSetUrl, jwt.SigningMethodRS256, cacheDuration, &cfg.HTTP)
			jwtOpts.Issuer = cfg.Auth.Issuer
			jwtOpts.Audience = cfg.Auth.Audience
			jwtOpts.TenantClaim = cfg.Auth.TenantClaim
			jwtOpts.RequireExpiration = true

			requireLocalScopeCheck = true

			jwtMiddleware := middleware.NewParseJWTMiddleware(jwtOpts, jwkCache)
			authRouter.Use(jwtMiddleware)
		case "policy":
			logger.Warn("auth enabled using policy provider")

			policyAuthConfig := cfg.Auth.Policy.WithDefaults()
			policyConfig := &policy.PolicyConfig{
				Namespace:    policyAuthConfig.Namespace,
				PolicyFQDN:   policyAuthConfig.PolicyFQDN,
				SubjectField: policyAuthConfig.SubjectField,
				APIKeyField:  policyAuthConfig.APIKeyField,
			}

			cacheDurationString := time.Duration(cfg.Auth.CacheRefreshInterval) * time.Second
			cacheCfg := policy.AuthzCacheConfig{
				CacheDuration: cacheDurationString.String(),
				CacheSize:     100,
			}

			var policyClient policy.Authorizer

			// Use a static (no-auth) client in self-managed deployments.
			// Otherwise fall back to the OAuth2 client-credentials flow.
			if cfg.SelfManaged {
				logger.Warn("self-managed mode: using api-keys client for policy evaluator")

				policyClient = policy.NewApiKeysClient(
					cfg.Auth.Policy.PolicyEvaluatorAddr,
					policyConfig,
					middleware.GetSharedHTTPClient(&cfg.HTTP),
				)
			} else {
				logger.Warn("using OAuth2 for policy evaluator",
					zap.String("token_issuer", cfg.Auth.Policy.TokenIssuerAddr))

				oidcConfig := &auth.ProviderConfig{
					Host:            cfg.Auth.Policy.TokenIssuerAddr,
					Scopes:          []string{"pdp-evaluate"},
					CredentialsFile: cfg.Auth.Policy.CredsFile,
				}

				baseClientConfig := &clients.BaseClientConfig{
					Type: "http",
					Addr: cfg.Auth.Policy.PolicyEvaluatorAddr,
					TLS:  auth.TLSConfigOptions{},
					AuthnCfg: &auth.AuthnConfig{
						OIDCConfig: oidcConfig,
						RefreshConfig: &auth.RefreshConfig{
							CredentialsRefreshInterval: cfg.Auth.Policy.CredentialsRefreshInterval,
						},
					},
				}

				policyClient, err = policy.NewAuthzClient(
					baseClientConfig,
					policyConfig,
					policy.WithCachingEnabled(cacheCfg),
				)
				if err != nil {
					logger.Error("failed to create policy authz client", zap.Error(err))
					return fmt.Errorf("failed to create policy authz client: %w", err)
				}
			}

			var jwtOpts *middleware.JWTParserOptions
			if cfg.Auth.JWKSetUrl != "" {
				opts := middleware.NewJWTParserOptions(cfg.Auth.JWKSetUrl, nil, cacheDuration, &cfg.HTTP)
				opts.Issuer = cfg.Auth.Issuer
				opts.Audience = cfg.Auth.Audience
				opts.TenantClaim = cfg.Auth.TenantClaim
				jwtOpts = &opts
			}

			requireLocalScopeCheck = cfg.SelfManaged

			authRouter.Use(middleware.NewAuthMiddleware(
				policyClient,
				"nv-cloud-functions",
				jwtOpts,
				jwkCache,
				cfg.SelfManaged,
				logger,
			))
		default:
			// This should never be reached since ValidateAuthConfig handles invalid providers
			logger.Error("auth is enabled but no valid auth provider was provided")
			return config.ErrInvalidAuthProvider
		}
	} else {
		logger.Warn("auth is disabled; this is not recommended for production")
	}

	authRouter.Use(middleware.MaybeRequirePathTenant(requireLocalScopeCheck))

	authRouter.Handle("/status",
		middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(server.StatusHandler(cfg.Auth)),
	).Methods("GET", "OPTIONS")

	// Remove when legacy clients migrate to V3.
	if !cfg.DeprecateEndpoints {
		authRouter.Handle("/v1/ledger/versions/{functionVersionId}/instances/{instanceId}",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.WriteScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.PostStageTransitionEvent)),
		).Methods("POST", "OPTIONS")

		authRouter.Handle("/v1/ledger/versions/{functionVersionId}/instances/{instanceId}",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ListStageTransitionEvents)),
		).Methods("GET", "OPTIONS")

		authRouter.Handle("/v1/ledger/versions/{functionVersionId}/instances/{instanceId}/events/{event}",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.GetInstanceEvent)),
		).Methods("GET", "OPTIONS")

		authRouter.Handle("/v1/ledger/versions/{functionVersionId}/instances/{instanceId}",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ArchiveScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ArchiveInstanceStageTransitionEvents)),
		).Methods("DELETE", "OPTIONS")

		authRouter.Handle("/v1/ledger/versions/{functionVersionId}/instances",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ListInstances)),
		).Methods("GET", "OPTIONS")

		authRouter.Handle("/v1/ledger/versions/{functionVersionId}/instances",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ArchiveScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ArchiveFunctionVersionStageTransitionEvents)),
		).Methods("DELETE", "OPTIONS")

		authRouter.Handle("/v1/ledger/versions/{functionVersionId}/stats",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.GetDeploymentStats)),
		).Methods("GET", "OPTIONS")
	}

	// Cross account endpoints for KAS
	// 1. Events
	// 1.1. Create event
	// authRouter.Handle("/v1/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/instances/{instanceId}",
	//	middleware.MaybeRequireScopes(logger, cfg.Authn.Enabled, "admin:create")(http.HandlerFunc(server.PostStageTransitionEvent)),
	// ).Methods("POST", "OPTIONS")

	// 1.2. List events
	if !cfg.DeprecateEndpoints {
		authRouter.Handle("/v1/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/instances/{instanceId}/events",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ListStageTransitionEvents)),
		).Methods("GET", "OPTIONS")

		// 1.3. Get single event
		authRouter.Handle("/v1/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/instances/{instanceId}/events/{event}",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.GetInstanceEvent)),
		).Methods("GET", "OPTIONS")

		// 1.4. Delete all events (deletes the instance too)
		authRouter.Handle("/v1/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/instances/{instanceId}/events",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminArchiveScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ArchiveInstanceStageTransitionEvents)),
		).Methods("DELETE", "OPTIONS")

		// 2. Instances
		// 2.1. List instances
		authRouter.Handle("/v1/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/instances",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ListInstances)),
		).Methods("GET", "OPTIONS")

		// 2.2. Delete all instances
		authRouter.Handle("/v1/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/instances",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminArchiveScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ArchiveFunctionVersionStageTransitionEvents)),
		).Methods("DELETE", "OPTIONS")

		// 3. Function Version event stats
		authRouter.Handle("/v1/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/stats",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.GetDeploymentStats)),
		).Methods("GET", "OPTIONS")
	}

	// =======================================================
	// V2 - Deployment-based Routes
	// =======================================================

	// Remove when legacy clients migrate to V3.
	if !cfg.DeprecateEndpoints {
		// Create Event (Deployment)
		authRouter.Handle("/v2/ledger/versions/{functionVersionId}/deployments/{deploymentId}/instances/{instanceId}",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.WriteScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.PostDeploymentStageTransitionEvent)),
		).Methods("POST", "OPTIONS")

		// Get Events (Deployment)
		authRouter.Handle("/v2/ledger/versions/{functionVersionId}/deployments/{deploymentId}/instances/{instanceId}",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ListDeploymentStageTransitionEvents)),
		).Methods("GET", "OPTIONS")

		// Get Single Event (Deployment)
		authRouter.Handle("/v2/ledger/versions/{functionVersionId}/deployments/{deploymentId}/instances/{instanceId}/events/{event}",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.GetDeploymentInstanceEvent)),
		).Methods("GET", "OPTIONS")

		// List Instances (Deployment)
		authRouter.Handle("/v2/ledger/versions/{functionVersionId}/deployments/{deploymentId}/instances",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ListDeploymentInstances)),
		).Methods("GET", "OPTIONS")

		// DELETE instance Events (Deployment)
		authRouter.Handle("/v2/ledger/versions/{functionVersionId}/deployments/{deploymentId}/instances/{instanceId}",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ArchiveScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ArchiveDeploymentInstanceStageTransitionEvents)),
		).Methods("DELETE", "OPTIONS")

		// DELETE all instance events for deployment (Deployment)
		authRouter.Handle("/v2/ledger/versions/{functionVersionId}/deployments/{deploymentId}/instances",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ArchiveScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ArchiveDeploymentFunctionVersionStageTransitionEvents)),
		).Methods("DELETE", "OPTIONS")

		// Get Stats (Deployment)
		authRouter.Handle("/v2/ledger/versions/{functionVersionId}/deployments/{deploymentId}/stats",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.GetDeploymentDeploymentStats)),
		).Methods("GET", "OPTIONS")
	}

	// =======================================================
	// V3 - Kubernetes Events & CloudEvents (wip) Routes
	// =======================================================

	// K8s event/OTLP receiver endpoint (with gzip decompression support)
	wrapper, err := gzhttp.NewWrapper(gzhttp.AllowCompressedRequests(true))
	if err != nil {
		logger.Error("failed to wrap k8s events receiver handler with gzip decompression", zap.Error(err))
		os.Exit(1)
	}

	authRouter.Handle("/v3/ledger/k8s-events",
		middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.WriteScopes, middleware.RequireAnyScopes)(wrapper(http.HandlerFunc(server.PostK8sEventV3))),
	).Methods("POST", "OPTIONS")

	// CloudEvents receiver endpoint
	authRouter.Handle("/v3/ledger/cloudevents",
		middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.WriteScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.PostCloudEventV3)),
	).Methods("POST", "OPTIONS")

	// V3 Stats endpoint - retrieve aggregated stats for a namespace
	authRouter.Handle("/v3/ledger/namespace/{namespace}/stats",
		middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.GetStatsV3)),
	).Methods("GET", "OPTIONS")

	// V3 Events endpoint - retrieve all events for a specific namespace and context (context via query params)
	authRouter.Handle("/v3/ledger/namespace/{namespace}/events",
		middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.ReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.GetEventsV3)),
	).Methods("GET", "OPTIONS")

	// Cross Account Endpoints for KAS (V2)
	// Remove when legacy clients migrate to V3.
	if !cfg.DeprecateEndpoints {
		// 1. Events
		// 1.1. List events
		authRouter.Handle("/v2/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/deployments/{deploymentId}/instances/{instanceId}/events",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ListDeploymentStageTransitionEvents)),
		).Methods("GET", "OPTIONS")

		// 1.2. Get single event
		authRouter.Handle("/v2/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/deployments/{deploymentId}/instances/{instanceId}/events/{event}",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.GetDeploymentInstanceEvent)),
		).Methods("GET", "OPTIONS")

		// 1.3. Delete all events (deletes the instance too)
		authRouter.Handle("/v2/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/deployments/{deploymentId}/instances/{instanceId}/events",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminArchiveScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ArchiveDeploymentInstanceStageTransitionEvents)),
		).Methods("DELETE", "OPTIONS")

		// 2. Instances
		// 2.1. List instances
		authRouter.Handle("/v2/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/deployments/{deploymentId}/instances",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ListDeploymentInstances)),
		).Methods("GET", "OPTIONS")

		// 2.2. Delete all instances
		authRouter.Handle("/v2/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/deployments/{deploymentId}/instances",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminArchiveScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.ArchiveDeploymentFunctionVersionStageTransitionEvents)),
		).Methods("DELETE", "OPTIONS")

		// 3. Deployment stats
		authRouter.Handle("/v2/fnds/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/deployments/{deploymentId}/stats",
			middleware.MaybeRequireScopes(logger, requireLocalScopeCheck, middleware.AdminReadScopes, middleware.RequireAnyScopes)(http.HandlerFunc(server.GetDeploymentDeploymentStats)),
		).Methods("GET", "OPTIONS")
	}

	// zap error stdlib logger
	zapErrorLogger := logging.NewLoggerWithZapWriter(logger.Logger)

	// Wrap the router with RecoveryHandler middleware
	recoveryHandler := handlers.RecoveryHandler(
		handlers.PrintRecoveryStack(true),
		handlers.RecoveryLogger(zapErrorLogger),
	)(router)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Service.ApiPort),
		Handler:           recoveryHandler,
		ErrorLog:          zapErrorLogger,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	// Run our server in a goroutine
	go func() {
		log := logger.Sugar()
		log.Warnf("api server started on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("unable to listen and serve: %s\n", err.Error())
			}
		}
	}()

	// Handle interrupts
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	// Graceful shutdown
	logger.Warn("server is shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		errMsg := fmt.Sprintf("server shutdown failed: %s", err.Error())
		logger.Error(errMsg)
	} else {
		logger.Warn("server exited properly")
	}

	return nil
}
