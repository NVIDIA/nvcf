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

package servers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	// By default, it sets `GOMEMLIMIT` to 90% of cgroup's memory limit.
	_ "github.com/KimMachineGun/automemlimit"
	// By default, it sets `GOMAXPROCS` to match the Linux container CPU quota.
	_ "go.uber.org/automaxprocs"

	"github.com/go-kit/kit/log" //nolint:staticcheck
	kitgrpc "github.com/go-kit/kit/transport/grpc"
	grpcvalidator "github.com/grpc-ecosystem/go-grpc-middleware/validator"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/oklog/oklog/pkg/group"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/servers/utils"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
)

const (
	GRPCMaxConcurrentStreams = 1000000
)

// RegisterServerFunc : register grpc server function
type RegisterServerFunc func(s *grpc.Server)

// RegisterHandlerFunc : register single handler function
type RegisterHandlerFunc func(ctx context.Context, mux *runtime.ServeMux, conn *grpc.ClientConn) error

// HTTPEndpointOverrideFunc : override for grpc-gateway mux - useful for overriding some endpoints and let grpc-gateway handle the rest of the endpoints by default
type HTTPEndpointOverrideFunc func(mux *runtime.ServeMux) *runtime.ServeMux

// PreServeCallbackFunc : callback to customize any settings of the server before it is served
type PreServeCallbackFunc func(s *grpc.Server) error

type CustomErrorHandlerFunc func(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler,
	w http.ResponseWriter, r *http.Request, err error)

type ServerFuncPair struct {
	Execute   func() error
	Interrupt func(error)
}

// GRPCConfig holds basic config required to setup a GRPC server
type GRPCConfig struct {
	*BaseServerConfig
	GRPCAddr  string
	HTTPAddr  string
	AdminAddr string
	// protobuf generated server registration function
	RegisterServer RegisterServerFunc `json:"-"`
	// REST gateway generated handler registration function
	RegisterHandler RegisterHandlerFunc `json:"-"`
	// use the provided mux for http instead of the default grpc-gateway mux
	HTTPEndpointOverride HTTPEndpointOverrideFunc `json:"-"`
	// pre-serve callback function to customize server setting before it is served
	PreServeCallback PreServeCallbackFunc `json:"-"`
	// additional GRPC server options
	ExtraServerOpts []grpc.ServerOption `json:"-"`
	// additional HTTP connection dial options
	ExtraHTTPDialOpts []grpc.DialOption `json:"-"`
	// additional admin http handlers
	AdditionalHttpHandlers map[string]http.Handler `json:"-"`
	// allow additional headers from requests
	AdditionalHeaders []string `json:"-"`
	// custom error handler function
	CustomErrorHandler  CustomErrorHandlerFunc `json:"-"`
	TerminationCallback func()                 `json:"-"`
	// set explicit http endpoints to serve health information
	HttpHealthEndpointOverride []string `json:"-"`
	// add additional servers
	AdditionalServers []ServerFuncPair
}

// grpcServer implements the Server interface
type grpcServer struct {
	config                 *GRPCConfig
	grpcServer             *grpc.Server
	httpServer             *http.Server
	healthServer           *health.Server
	tracer                 trace.TracerProvider
	logger                 log.Logger
	runtimeMu              sync.Mutex
	terminationCallbackRan bool
	grpcServerStopped      bool
	httpServerShutdown     bool
}

type Option func(server *grpcServer)

// NewGRPCServer - factory method for grpc based services
func NewGRPCServer(config *GRPCConfig, opts ...Option) Server {
	if config == nil {
		config = &GRPCConfig{}
	}
	if config.BaseServerConfig == nil {
		config.BaseServerConfig = &BaseServerConfig{}
	}

	s := &grpcServer{
		config: config,
		logger: log.NewNopLogger(),
	}

	for _, opt := range opts {
		opt(s)
	}
	return s
}

func WithLogger(logger log.Logger) Option {
	return func(g *grpcServer) {
		g.logger = logger
	}
}

func WithTracer(tracer trace.TracerProvider) Option {
	return func(g *grpcServer) {
		g.tracer = tracer
		otel.SetTracerProvider(tracer)
	}
}

func WithRegisterHandler(handlerFunc RegisterHandlerFunc) Option {
	return func(g *grpcServer) {
		g.config.RegisterHandler = handlerFunc
	}
}

func WithRegisterServer(serverFunc RegisterServerFunc) Option {
	return func(g *grpcServer) {
		g.config.RegisterServer = serverFunc
	}
}

func WithHTTPEndpointOverride(httpEndpointFunc HTTPEndpointOverrideFunc) Option {
	return func(g *grpcServer) {
		g.config.HTTPEndpointOverride = httpEndpointFunc
	}
}

func WithPreServeCallback(cbFunc PreServeCallbackFunc) Option {
	return func(g *grpcServer) {
		g.config.PreServeCallback = cbFunc
	}
}

func WithExtraServerOpts(serverOpts []grpc.ServerOption) Option {
	return func(g *grpcServer) {
		g.config.ExtraServerOpts = serverOpts
	}
}

func WithExtraHTTPDialOpts(dialOpts []grpc.DialOption) Option {
	return func(g *grpcServer) {
		g.config.ExtraHTTPDialOpts = dialOpts
	}
}

func WithAdditionalHTTPHandlers(handlers map[string]http.Handler) Option {
	return func(g *grpcServer) {
		g.config.AdditionalHttpHandlers = handlers
	}
}

func WithAdditionalRequestHeaders(headers []string) Option {
	return func(g *grpcServer) {
		g.config.AdditionalHeaders = headers
	}
}

func WithHttpHealthEndpoints(healthEndpoints ...string) Option {
	return func(g *grpcServer) {
		g.config.HttpHealthEndpointOverride = healthEndpoints
	}
}

func WithAdditionalServers(additionalServers ...ServerFuncPair) Option {
	return func(g *grpcServer) {
		g.config.AdditionalServers = additionalServers
	}
}

func WithTerminationCallback(terminationCallbackFunc func()) Option {
	return func(g *grpcServer) {
		g.config.TerminationCallback = terminationCallbackFunc
	}
}

func WithCustomErrorHandler(customErrorHandler CustomErrorHandlerFunc) Option {
	return func(g *grpcServer) {
		g.config.CustomErrorHandler = customErrorHandler
	}
}

func (g *grpcServer) setGRPCServer(server *grpc.Server) {
	g.runtimeMu.Lock()
	defer g.runtimeMu.Unlock()

	g.grpcServer = server
}

func (g *grpcServer) setHTTPServer(server *http.Server) {
	g.runtimeMu.Lock()
	defer g.runtimeMu.Unlock()

	g.httpServer = server
}

func (g *grpcServer) runTerminationCallback() {
	g.runtimeMu.Lock()
	if g.config.TerminationCallback == nil || g.terminationCallbackRan {
		g.runtimeMu.Unlock()
		return
	}
	callback := g.config.TerminationCallback
	g.terminationCallbackRan = true
	g.runtimeMu.Unlock()

	_ = g.logger.Log("Shutdown started", "termination-callback")
	callback()
	_ = g.logger.Log("Shutdown completed", "termination-callback")
}

func (g *grpcServer) shutdownHTTPServer(ctx context.Context) (bool, error) {
	g.runtimeMu.Lock()
	if g.httpServer == nil {
		g.runtimeMu.Unlock()
		return false, nil
	}
	if g.httpServerShutdown {
		g.runtimeMu.Unlock()
		return true, nil
	}
	httpServer := g.httpServer
	g.httpServerShutdown = true
	g.runtimeMu.Unlock()

	_ = g.logger.Log("Shutdown started", "gRPC/HTTP")
	err := httpServer.Shutdown(ctx)
	_ = g.logger.Log("Shutdown completed", "gRPC/HTTP")
	return true, err
}

func (g *grpcServer) gracefulStopGRPCServer() {
	g.runtimeMu.Lock()
	if g.grpcServer == nil || g.grpcServerStopped {
		g.runtimeMu.Unlock()
		return
	}
	grpcServer := g.grpcServer
	g.grpcServerStopped = true
	g.runtimeMu.Unlock()

	_ = g.logger.Log("Shutdown started", "gRPC")
	grpcServer.GracefulStop()
	_ = g.logger.Log("Shutdown completed", "gRPC")
}

// Setup configures all base service related configuration
func (g *grpcServer) Setup() error {
	var err error
	// Setup OTEL tracer if it's not already been provided with servers.WithTracer option
	if g.tracer == nil {
		g.tracer, err = StandardTracer(g.config.BaseServerConfig)
		if err != nil {
			return err
		}
	}

	// Setup Shutdown handler
	shutdownHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = g.logger.Log("Received shutdown signal", "Starting graceful shutdown")
		g.runTerminationCallback()
		if _, err := g.shutdownHTTPServer(context.Background()); err != nil {
			fmt.Printf("Error shutting down http server")
		}
		g.gracefulStopGRPCServer()
	})

	// Set default admin routes
	mux := http.DefaultServeMux
	g.config.SetAdminRoutes(mux, shutdownHandler)

	// Set default health server and health check endpoints
	g.healthServer = health.NewServer()
	healthClientCfg := &clients.GRPCClientConfig{
		BaseClientConfig: &clients.BaseClientConfig{
			Addr: g.config.GRPCAddr,
			TLS: auth.TLSConfigOptions{
				Enabled:            g.config.TLS.Enabled,
				InsecureSkipVerify: true,
			},
		},
	}
	healthConn, err := healthClientCfg.Dial()
	if err != nil {
		return err
	}
	healthClient := grpc_health_v1.NewHealthClient(healthConn)
	healthCheckHandler := g.setupHealthCheckHandler(healthClient)

	healthEndpoints := []string{"/ping"}
	if len(g.config.HttpHealthEndpointOverride) > 0 {
		healthEndpoints = g.config.HttpHealthEndpointOverride
	}
	for _, healthEndpoint := range healthEndpoints {
		mux.Handle(healthEndpoint, healthCheckHandler)
	}

	return nil
}

// Run is responsible for running the service as configured
func (g *grpcServer) Run() error {
	// Configure TLS
	tlsCert, certPool, err := g.config.SetupTLSConfig()
	if err != nil {
		return err
	}

	var svrGroup group.Group
	{
		// The admin listener mounts the http.DefaultServeMux, and serves up
		// stuff like the Prometheus metrics route, the Go debug and profiling
		// routes, and so on.
		adminListener, err := net.Listen("tcp", g.config.AdminAddr)
		if err != nil {
			_ = g.logger.Log("transport", "admin/HTTP", "during", "Listen", "err", err)
			return err
		}
		mux := http.DefaultServeMux
		for path, handler := range g.config.AdditionalHttpHandlers {
			mux.Handle(path, handler)
		}
		svrGroup.Add(func() error {
			_ = g.logger.Log("transport", "admin/HTTP", "addr", g.config.AdminAddr)
			adminServer := &http.Server{
				ReadHeaderTimeout: 30 * time.Second,
				Handler:           mux,
			}
			return adminServer.Serve(adminListener)
		}, func(err error) {
			_ = g.logger.Log("transport", "gRPC", "addr", g.config.AdminAddr, "err", fmt.Sprintf("%v - Shutting down", err))
			g.runTerminationCallback()
			adminListener.Close()
		})
	}
	{
		// The gRPC listener mounts the Go kit gRPC server we created.
		grpcListener, err := net.Listen("tcp", g.config.GRPCAddr)
		if err != nil {
			_ = g.logger.Log("transport", "gRPC", "during", "Listen", "err", err)
			return err
		}
		svrGroup.Add(func() error {
			_ = g.logger.Log("transport", "gRPC", "addr", g.config.GRPCAddr)
			unaryInterceptors := []grpc.UnaryServerInterceptor{
				grpcvalidator.UnaryServerInterceptor(),
				grpc.UnaryServerInterceptor(kitgrpc.Interceptor),
			}

			srvOpts := []grpc.ServerOption{
				grpc.ChainUnaryInterceptor(unaryInterceptors...),
				grpc.StatsHandler(otelgrpc.NewServerHandler()),
			}
			tlsCreds := insecure.NewCredentials()
			if g.config.TLS.Enabled {
				srvOpts = append(srvOpts, grpc.Creds(credentials.NewServerTLSFromCert(&tlsCert)))
			} else {
				srvOpts = append(srvOpts, grpc.Creds(tlsCreds))
			}
			srvOpts = append(srvOpts, g.config.ExtraServerOpts...)
			// we add the Go Kit gRPC Interceptor to our gRPC service as it is used by
			// the here demonstrated zipkin tracing middleware.
			baseServer := grpc.NewServer(srvOpts...)
			g.config.RegisterServer(baseServer)
			g.setGRPCServer(baseServer)
			grpc_health_v1.RegisterHealthServer(baseServer, g.healthServer)
			if g.config.PreServeCallback != nil {
				if err := g.config.PreServeCallback(baseServer); err != nil {
					return err
				}
			}
			return baseServer.Serve(grpcListener)
		}, func(err error) {
			_ = g.logger.Log("transport", "gRPC", "addr", g.config.GRPCAddr, "err", fmt.Sprintf("%v - Shutting down", err))
			g.healthServer.Shutdown()
			// Gracefully stop the gRPC server. This will block until all active RPCs are served.
			// Ideally, a /shutdown on the server should be triggered before where it has enough time to process any shutdown activities.
			// However, we will try to serve any active connections even if a `/shutdown` was not triggered before
			g.gracefulStopGRPCServer()
			grpcListener.Close()
		})
	}
	{
		httpListener, err := net.Listen("tcp", g.config.HTTPAddr)
		if err != nil {
			_ = g.logger.Log("transport", "grpc/HTTP", "during", "Listen", "err", err)
			return err
		}
		svrGroup.Add(func() error {
			ctx := context.Background()
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			transportCreds := insecure.NewCredentials()
			if g.config.TLS.Enabled {
				transportCreds = credentials.NewClientTLSFromCert(certPool, "")
			}
			dOpts := []grpc.DialOption{grpc.WithTransportCredentials(transportCreds)}
			dOpts = append(dOpts, g.config.ExtraHTTPDialOpts...)
			conn, err := grpc.DialContext( //nolint:staticcheck // SA1019: grpc.DialContext is deprecated but still supported throughout 1.x
				ctx,
				fmt.Sprintf("0.0.0.0%s", g.config.GRPCAddr),
				dOpts...,
			)
			if err != nil {
				return fmt.Errorf("failed to dial to grpc server: %+v", err)
			}

			outgoingHeaderMatcher := func(key string) (string, bool) {
				if strings.HasPrefix(key, utils.HeaderNVPrefix) {
					return key[len(utils.HeaderNVPrefix):], true
				}
				return runtime.DefaultHeaderMatcher(key)
			}
			// Register gRPC gateway server endpoints
			// Note: Make sure the gRPC server is running properly and accessible
			errorHandlerFunc := g.errorHandler
			if g.config.CustomErrorHandler != nil {
				errorHandlerFunc = g.config.CustomErrorHandler
			}
			gwMux := runtime.NewServeMux(
				runtime.WithErrorHandler(errorHandlerFunc),
				runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
					// set the marshaller to use the proto definitions in the response
					// without this the responses are camelCase but this is miss leading
					// because policy values should be snake_case
					MarshalOptions: protojson.MarshalOptions{
						UseProtoNames:   true,
						EmitUnpopulated: true,
					},
				}),
				runtime.WithOutgoingHeaderMatcher(outgoingHeaderMatcher),
				runtime.WithMetadata(func(c context.Context, req *http.Request) metadata.MD {
					md := utils.GetMetadataFromRequest(req)
					for _, header := range g.config.AdditionalHeaders {
						md.Set(header, req.Header.Get(header))
					}
					return md
				}),
			)
			if g.config.RegisterHandler != nil {
				err = g.config.RegisterHandler(ctx, gwMux, conn)
				if err != nil {
					return fmt.Errorf("failed to register: %+v", err)
				}
				_ = g.logger.Log("transport", "gRPC/HTTP", "addr", g.config.HTTPAddr)
			}
			if g.config.HTTPEndpointOverride != nil {
				gwMux = g.config.HTTPEndpointOverride(gwMux)
			}
			httpSrv := &http.Server{
				Addr:              g.config.HTTPAddr,
				Handler:           gwMux,
				ReadHeaderTimeout: 30 * time.Second,
			}
			g.setHTTPServer(httpSrv)
			if g.config.TLS.Enabled {
				//nolint:gosec // G402: InsecureSkipVerify is intentional for development/testing
				httpSrv.TLSConfig = &tls.Config{
					Certificates:       []tls.Certificate{tlsCert},
					InsecureSkipVerify: true,
				}
				err = httpSrv.ServeTLS(httpListener, "", "")
			} else {
				err = httpSrv.Serve(httpListener)
			}
			return err
		}, func(err error) {
			_ = g.logger.Log("transport", "gRPC/HTTP", "addr", g.config.HTTPAddr, "err", fmt.Sprintf("%v - Shutting down", err))
			_ = g.logger.Log("Shutting down gRPC/HTTP", "exit after signal")
			shutDown, err := g.shutdownHTTPServer(context.Background())
			if err != nil {
				_ = g.logger.Log("Shutdown of gRPC/HTTP server errored", err)
				return
			}
			if !shutDown {
				httpListener.Close()
			}
		})
	}
	{
		// This function just sits and waits for ctrl-C.
		cancelInterrupt := make(chan struct{})
		svrGroup.Add(func() error {
			c := make(chan os.Signal, 1)
			signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
			select {
			case sig := <-c:
				return fmt.Errorf("received signal %s", sig)
			case <-cancelInterrupt:
				return nil
			}
		}, func(error) {
			close(cancelInterrupt)
		})
	}

	for _, serverFuncPair := range g.config.AdditionalServers {
		svrGroup.Add(serverFuncPair.Execute, serverFuncPair.Interrupt)
	}

	defer tracing.Shutdown()
	return svrGroup.Run()
}

func (g *grpcServer) setupHealthCheckHandler(healthCheckClient grpc_health_v1.HealthClient) http.HandlerFunc {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := grpc_health_v1.HealthCheckRequest{}
		resp, err := healthCheckClient.Check(context.Background(), &req)
		w.Header().Set("Cache-Control", "no-cache")
		if err != nil {
			zap.L().Error("error checking health status", zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
		} else if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
			zap.L().Error("failed health check", zap.String("health check response", resp.Status.String()))
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("OK")); err != nil {
				zap.L().Error("error writing health status OK", zap.Error(err))
			}
		}
	})
	return handler
}

func (g *grpcServer) errorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	setHeaderFunc := func(header string) {
		val := r.Header.Get(header)
		if val != "" {
			w.Header().Set(header, val)
		}
	}
	for _, header := range utils.StandardHeaders {
		setHeaderFunc(header)
	}

	// Encode NVError using custom error marshaller
	runtime.DefaultHTTPErrorHandler(ctx, mux, &errors.NVErrorMarshaler{FallbackMarshaler: marshaler}, w, r, err)
}

// SetupTLSConfig sets up the certificates and cert-pool based on the tls files provided
func (gc *GRPCConfig) SetupTLSConfig() (tls.Certificate, *x509.CertPool, error) {
	return gc.BaseServerConfig.SetupTLSConfig()
}

// AddServerFlags if a helper function to add common flags to server command
// Returns true if addition of flags was successful
func (gc *GRPCConfig) AddServerFlags(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if gc.BaseServerConfig == nil {
		gc.BaseServerConfig = &BaseServerConfig{}
	}
	cmd.Flags().StringVarP(&gc.AdminAddr, "admin-addr", "a", ":8080", "Admin listen address - for metrics, debug, etc.")
	cmd.Flags().StringVarP(&gc.HTTPAddr, "http-addr", "p", ":8081", "gRPC/HTTP listen address")
	cmd.Flags().StringVarP(&gc.GRPCAddr, "grpc-addr", "g", ":8082", "gRPC listen address")
	gc.BaseServerConfig.AddServerFlags(cmd)
	return true
}
