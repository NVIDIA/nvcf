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
	"crypto/tls"
	"crypto/x509"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // G108: pprof endpoint is intentionally exposed for debugging
	"os"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
)

const defaultShutdownEndpoint = "/shutdown"

type Server interface {
	Setup() error
	Run() error
}

type BaseServerConfig struct {
	ServiceName      string
	TLS              auth.TLSConfigOptions
	Tracing          tracing.OTELConfig
	SwaggerFile      string
	Version          string
	ShutdownEndpoint string
}

func (cfg *BaseServerConfig) AddServerFlags(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	cmd.Flags().BoolVarP(&cfg.TLS.Enabled, "tls.enabled", "", false, "Enable TLS")
	cmd.Flags().StringVarP(&cfg.TLS.CertFile, "tls.cert-file", "", "", "Path to the server TLS Cert file")
	cmd.Flags().StringVarP(&cfg.TLS.KeyFile, "tls.key-file", "", "", "Path to the server TLS Key file")
	cmd.Flags().StringArrayVarP(&cfg.TLS.ClientCACertFiles, "tls.client-ca-cert-file", "", []string{},
		"(repeated) path to the client CA TLS cert file used by server to verify client certs; will be used in addition to the system cert pool")
	cmd.Flags().BoolVarP(&cfg.Tracing.Enabled, "tracing.enabled", "", false, "Enable Tracing")
	cmd.Flags().StringVarP(&cfg.Tracing.Endpoint, "tracing.endpoint", "", "",
		"Tracing OTEL endpoint.")
	cmd.Flags().StringVarP(&cfg.Tracing.AccessToken, "tracing.access-token", "l", "", "Access token for accessing tracing APIs")
	cmd.Flags().BoolVarP(&cfg.Tracing.Insecure, "tracing.insecure", "", false, "Connect to trace endpoint without a certificate. Generally used for developer mode")
	cmd.Flags().StringVarP(&cfg.SwaggerFile, "swagger-file", "", "", "Swagger file location")
	cmd.Flags().StringVarP(&cfg.Version, "version", "v", "1.0.0", "Service version")
	return true
}

// SetupTLSConfig sets up the certificates and cert-pool based on the tls files provided
func (cfg *BaseServerConfig) SetupTLSConfig() (tls.Certificate, *x509.CertPool, error) {
	if !cfg.TLS.Enabled {
		return tls.Certificate{}, nil, nil
	}

	if cfg.TLS.CertFile == "" || cfg.TLS.KeyFile == "" {
		return tls.Certificate{}, nil, errors.ErrCertAndKeyRequired
	}
	cert, err := os.ReadFile(cfg.TLS.CertFile)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	key, err := os.ReadFile(cfg.TLS.KeyFile)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tlsCert, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certPool := x509.NewCertPool()
	ok := certPool.AppendCertsFromPEM(cert)
	if !ok {
		return tls.Certificate{}, nil, errors.ErrBadCerts
	}
	return tlsCert, certPool, nil
}

// StandardTracer - Setup standard OTEL based tracer
func StandardTracer(cfg *BaseServerConfig) (trace.TracerProvider, error) {
	cfg.Tracing.Attributes.ServiceName = cfg.ServiceName
	cfg.Tracing.Attributes.ServiceVersion = cfg.Version
	return tracing.SetupOTELTracer(&cfg.Tracing)
}

// SetAdminRoutes sets up common routes for all services.
func (cfg *BaseServerConfig) SetAdminRoutes(mux *http.ServeMux, shutdownHandler http.Handler) {
	mux.Handle("/metrics", promhttp.Handler())

	// Setup shutdown handler
	// This handler can be called during preStop lifecycle stage of a pod termination
	if cfg.ShutdownEndpoint == "" {
		cfg.ShutdownEndpoint = defaultShutdownEndpoint
	}
	mux.Handle(cfg.ShutdownEndpoint, shutdownHandler)

	if cfg.SwaggerFile != "" {
		mux.HandleFunc(
			"/openapiv2/swagger.json", func(writer http.ResponseWriter, request *http.Request) {
				http.ServeFile(writer, request, cfg.SwaggerFile)
			},
		)
	}
}
