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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/carlmjohnson/versioninfo"
	"github.com/quic-go/quic-go/http3"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/clients"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/servers"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/tracing"

	"nvcf-grpc-proxy/nvcf/pb"
	"nvcf-grpc-proxy/proxy/consts"
	"nvcf-grpc-proxy/proxy/credentials"
	"nvcf-grpc-proxy/proxy/geo"
	"nvcf-grpc-proxy/proxy/hacks"
	"nvcf-grpc-proxy/proxy/invocation"
	"nvcf-grpc-proxy/proxy/ratelimit"
	"nvcf-grpc-proxy/proxy/reloadableTls"
	"nvcf-grpc-proxy/proxy/worker"
)

const serviceName = "gdn-nvcf-grpc-proxy-service"

type Config struct {
	OTELExporterOTLPEndpoint string `mapstructure:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	TracingAccessToken       string `mapstructure:"TRACING_ACCESS_TOKEN"`
	NatsPrivateNKey          string `mapstructure:"NATS_PRIVATE_NKEY"`
	NVCFFqdn                 string `mapstructure:"NVCF_FQDN_GRPC"`
	NatsFqdn                 string `mapstructure:"NATS_FQDN"`
	SSAFqdn                  string `mapstructure:"SSA_FQDN"`
	SelfWorkerFqdn           string `mapstructure:"SELF_WORKER_FQDN"`
	SecretsPath              string `mapstructure:"SECRETS_PATH"`
	PublicCertPath           string `mapstructure:"PUBLIC_CERT_PATH"`
	PrivateKeyPath           string `mapstructure:"PRIVATE_KEY_PATH"`
	PodIP                    string `mapstructure:"POD_IP"`
	AWSRegion                string `mapstructure:"AWS_REGION"`
	LocalTest                bool   `mapstructure:"LOCAL_TEST"`
	GeoDisabled              bool   `mapstructure:"GEO_DISABLED"`
	GeoSsaAddr               string `mapstructure:"GEO_SSA_ADDR"`
	GeoGlpsAddr              string `mapstructure:"GEO_GLPS_ADDR"`
	GeoTableS3Region         string `mapstructure:"GEO_TABLE_S3_REGION"`
	GeoTableS3BucketName     string `mapstructure:"GEO_TABLE_S3_BUCKET_NAME"`
	RoutingTableValidityDays int    `mapstructure:"GEO_ROUTING_TABLE_VALIDITY_DAYS"`
	RateLimitAddr            string `mapstructure:"RATE_LIMIT_ADDR"`
	RateLimitEnabled         bool   `mapstructure:"RATE_LIMIT_ENABLED"`
	EnableHTTP1Connect       bool   `mapstructure:"ENABLE_HTTP1_CONNECT"`
	EnableHTTP3Connect       bool   `mapstructure:"ENABLE_HTTP3_CONNECT"`
	ClusterName              string `mapstructure:"CLUSTER_NAME"`
	JetstreamPlacementTag    string `mapstructure:"JETSTREAM_PLACEMENT_TAG"`
}

type NVCFProxy struct {
	servers.Server
	director    *StreamDirector
	http3Server *http3.Server
}

func (n *NVCFProxy) Close() error {
	zap.L().Info("http3 server shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), consts.Timeout)
	defer cancel()
	if err := n.http3Server.Shutdown(ctx); err != nil {
		zap.L().Warn("http3 graceful close failed", zap.Error(err))
	}
	if err := n.http3Server.Close(); err != nil {
		zap.L().Warn("http3 close failed", zap.Error(err))
	}
	return n.director.Close()
}

type rateLimiter interface {
	IsRateLimited(ctx context.Context, ncaId, functionId, functionVersionId string, isSyncCheck bool) bool
}

func NewNVCFProxy(logger *logs.ZapLogger, config Config) (*NVCFProxy, error) {
	ctx := context.Background()
	if config.AWSRegion == "" {
		config.AWSRegion = "ncp"
	}
	if config.SecretsPath == "" {
		config.SecretsPath = "/vault/secrets/secrets.json"
	}

	if config.ClusterName == "" {
		config.ClusterName = "nvcf-grpc-proxy-service"
	}

	if config.JetstreamPlacementTag == "" {
		config.JetstreamPlacementTag = "aws-region:" + config.AWSRegion
	}

	secrets, _ := os.ReadFile(config.SecretsPath)
	var secretsMap map[string]any
	_ = json.Unmarshal(secrets, &secretsMap)
	if config.TracingAccessToken == "" {
		if token, ok := secretsMap["tracingAccessToken"].(string); ok {
			config.TracingAccessToken = token
		}
	}
	if config.NatsPrivateNKey == "" {
		if token, ok := secretsMap["natsPrivateNKey"].(string); ok {
			config.NatsPrivateNKey = token
		}
	}

	otelUrl, err := url.Parse(config.OTELExporterOTLPEndpoint)
	if err != nil {
		return nil, err
	}
	nvcfClient, err := NewNVCFClient(config)
	if err != nil {
		return nil, err
	}
	if config.LocalTest {
		nvcfClient = hacks.MockedNVCFClient{}
		config.SelfWorkerFqdn = "http://localhost:10080"
	}
	connectPaths, err := getConnectPaths(config)
	if err != nil {
		return nil, err
	}
	nc, err := invocation.NewNatsConnection(config.NatsFqdn, config.NatsPrivateNKey, serviceName, config.SSAFqdn, config.SecretsPath)
	if err != nil {
		return nil, err
	}

	var geoLookup interface {
		LookupRegions(ctx context.Context, clientIP net.IP) []string
	}
	if !config.GeoDisabled {
		geoLookup, err = geo.NewGeoIPLookupService(ctx, config.GeoSsaAddr, config.GeoGlpsAddr, config.GeoTableS3Region, config.GeoTableS3BucketName, config.SecretsPath, config.RoutingTableValidityDays)
		if err != nil {
			return nil, err
		}
	} else {
		geoLookup = geo.DisabledGeoIPLookupService{}
	}

	var rateLimit rateLimiter
	if config.RateLimitEnabled {
		rateLimitClient, err := ratelimit.NewRateLimitClient(config.SSAFqdn, config.SecretsPath, config.RateLimitAddr)
		if err != nil {
			return nil, err
		}
		rateLimit, err = ratelimit.NewRateLimitService(rateLimitClient)
		if err != nil {
			return nil, err
		}
	} else {
		rateLimit = &ratelimit.NoOpRateLimitService{}
	}

	invoker, err := invocation.NewFunctionInvoker(nc, nvcfClient, connectPaths, config.JetstreamPlacementTag, config.AWSRegion, geoLookup, rateLimit)
	if err != nil {
		return nil, err
	}
	director := NewStreamDirector(invoker)
	http3Server, err := setupH3(director, config)
	if err != nil {
		return nil, err
	}
	cancelH3Interrupt := make(chan struct{})
	http3ServerPair := servers.ServerFuncPair{
		Execute: func() error {
			zap.L().Info("", zap.String("transport", "http/3"), zap.String("addr", http3Server.Addr))
			select {
			case <-cancelH3Interrupt:
				return nil
			case err := <-lo.Async(http3Server.ListenAndServe):
				return err
			}
		},
		Interrupt: func(error) {
			zap.L().Info("skipping http3 server interrupt")
			close(cancelH3Interrupt)
		},
	}
	http1Server, err := setupH1(director)
	if err != nil {
		return nil, err
	}
	cancelH1Interrupt := make(chan struct{})
	http1ServerPair := servers.ServerFuncPair{
		Execute: func() error {
			zap.L().Info("", zap.String("transport", "http/1"), zap.String("addr", http1Server.Addr))
			select {
			case <-cancelH1Interrupt:
				return nil
			case err := <-lo.Async(http1Server.ListenAndServe):
				return err
			}
		},
		Interrupt: func(error) {
			zap.L().Info("skipping http1 server interrupt")
			close(cancelH1Interrupt)
		},
	}

	healthManager, err := healthManager(config.NVCFFqdn, nc)
	if err != nil {
		return nil, err
	}

	http2Server := createHttp2Server(director, "0.0.0.0:10081", healthManager)
	http2ServerPair := servers.ServerFuncPair{
		Execute: func() error {
			zap.L().Info("", zap.String("transport", "http/2"), zap.String("addr", http2Server.Addr))
			return http2Server.ListenAndServe()
		},
		Interrupt: func(error) {
			_ = http2Server.Shutdown(ctx)
		},
	}

	serverPairs := []servers.ServerFuncPair{http2ServerPair}
	if config.EnableHTTP1Connect {
		serverPairs = append(serverPairs, http1ServerPair)
	}
	if config.EnableHTTP3Connect {
		serverPairs = append(serverPairs, http3ServerPair)
	}

	hostname, _ := os.Hostname()
	return &NVCFProxy{
		director:    director,
		http3Server: http3Server,
		Server: servers.NewGRPCServer(&servers.GRPCConfig{
			HTTPAddr:  "0.0.0.0:10080",
			GRPCAddr:  "0.0.0.0:10085",
			AdminAddr: "0.0.0.0:10083",
			BaseServerConfig: &servers.BaseServerConfig{
				ServiceName: serviceName,
				Version:     getVersion(),
				Tracing: tracing.OTELConfig{
					Enabled:     config.OTELExporterOTLPEndpoint != "",
					Endpoint:    otelUrl.Host,
					Insecure:    otelUrl.Scheme == "http",
					AccessToken: config.TracingAccessToken,
					Attributes: tracing.Attributes{
						Extra: map[string]string{
							"host.id":      hostname,
							"host.ip":      config.PodIP,
							"host.dc":      config.AWSRegion,
							"cluster_name": config.ClusterName,
						},
					},
					SpanProcessorWrapper: func(processor trace.SpanProcessor) trace.SpanProcessor {
						return FilterSpanProcessor{
							SpanProcessor: processor,
							Filter: func(span trace.ReadOnlySpan) bool {
								return span.Name() != worker.TcpSessionSpanName
							},
						}
					},
				},
			},
		},
			servers.WithLogger(logger),
			servers.WithRegisterServer(func(server *grpc.Server) {}),
			servers.WithHttpHealthEndpoints("/health"),
			servers.WithAdditionalServers(serverPairs...),
		),
	}, nil
}

func getVersion() string {
	if version := os.Getenv("VERSION"); version != "" {
		return version
	}
	return versioninfo.Revision
}

func getConnectPaths(config Config) (invocation.ConnectPaths, error) {
	proxyPaths := invocation.ConnectPaths{}
	if config.EnableHTTP1Connect {
		if config.PodIP != "" {
			proxyPaths.HTTP1 = fmt.Sprintf("http://%s:10086/v1/proxy", config.PodIP)
		}
	}
	if config.EnableHTTP3Connect {
		parsedUrl, err := url.Parse(config.SelfWorkerFqdn)
		if err != nil {
			return proxyPaths, err
		}
		if config.PodIP != "" {
			ip := net.ParseIP(config.PodIP)
			if ip == nil {
				return proxyPaths, fmt.Errorf("failed to parse pod IP: %s", config.PodIP)
			}
			if ip.To4() == nil {
				return proxyPaths, fmt.Errorf("pod IP must be representable as IPV4: %s", config.PodIP)
			}
			parsedUrl.Host = strings.ReplaceAll(ip.To4().String(), ".", "-") + "." + parsedUrl.Host
		}
		proxyPath, err := url.JoinPath(parsedUrl.String(), "/v1/proxy")
		if err != nil {
			return proxyPaths, err
		}
		proxyPaths.HTTP3 = proxyPath
	}
	if proxyPaths.HTTP1 == "" && proxyPaths.HTTP3 == "" {
		return proxyPaths, fmt.Errorf("no proxy paths configured")
	}
	return proxyPaths, nil
}

func setupTLS(config Config) (*tls.Config, error) {
	if config.PublicCertPath != "" && config.PrivateKeyPath != "" {
		zap.L().Info("reading TLS certs from file", zap.String("public", config.PublicCertPath), zap.String("private", config.PrivateKeyPath))
		return reloadableTls.NewTLSReloader(config.PublicCertPath, config.PrivateKeyPath, "", &tls.Config{})
	}
	zap.L().Info("generating local TLS certs")
	leafCert, leafPrivateKey := hacks.GeneratePk()
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{leafCert.Raw},
			PrivateKey:  leafPrivateKey,
		}},
	}, nil
}

func NewNVCFClient(config Config) (pb.ProxyClient, error) {
	nvcfFqdn, err := grpcSafeUrl(config.NVCFFqdn)
	if err != nil {
		return nil, err
	}
	tlsEnabled := nvcfFqdn.Scheme == "https"

	// Create the base client config
	clientConfig := clients.GRPCClientConfig{
		BaseClientConfig: &clients.BaseClientConfig{
			Addr: nvcfFqdn.Host,
			TLS: auth.TLSConfigOptions{
				Enabled: tlsEnabled,
			},
		},
	}

	// Check for fixed bearer token
	tokenKey := "nvcfApiToken"
	if _, err := credentials.ReadTokenFromFile(config.SecretsPath, tokenKey); err == nil {
		// Create bearer token credentials with auto file watcher
		zap.L().Info("Using fixed bearer token authentication for NVCF client",
			zap.String("nvcf_host", nvcfFqdn.Host),
			zap.Bool("tls_enabled", tlsEnabled))

		// Create bearer token credentials with automatic file watching
		bearerCredentials, err := credentials.NewBearerTokenCredentials(
			config.SecretsPath,
			tokenKey,
			!tlsEnabled,
		)
		if err != nil {
			return nil, err
		}

		// Get standard dial options
		dialOpts, err := clientConfig.DialOptions()
		if err != nil {
			return nil, err
		}

		// Add our credential
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(bearerCredentials))
		clientConfig.DialOptOverrides = dialOpts
	} else {
		zap.L().Info("Fixed bearer token not found, falling back to OAuth2")

		// Configure OAuth2
		clientConfig.BaseClientConfig.AuthnCfg = &auth.AuthnConfig{
			OIDCConfig: &auth.ProviderConfig{
				Host:            config.SSAFqdn,
				CredentialsFile: config.SecretsPath,
				Scopes:          []string{"proxy:invoke_function"},
			},
			RefreshConfig:            &auth.RefreshConfig{Interval: int64((5 * time.Minute).Seconds())},
			DisableTransportSecurity: !tlsEnabled,
		}
	}

	conn, err := clientConfig.Dial()
	if err != nil {
		return nil, err
	}
	return pb.NewProxyClient(conn), nil
}

func grpcSafeUrl(uri string) (*url.URL, error) {
	nvcfFqdn, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	if nvcfFqdn.Port() == "" {
		port := ":443"
		if nvcfFqdn.Scheme == "http" {
			port = ":80"
		}
		nvcfFqdn, err = url.Parse(uri + port)
		if err != nil {
			return nil, err
		}
	}
	return nvcfFqdn, nil
}
