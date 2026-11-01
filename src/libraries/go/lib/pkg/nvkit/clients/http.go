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

package clients

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"

	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
)

const (
	defaultNumRetries = 2
	maxAllowedRetries = 10
)

type HTTPClientConfig struct {
	*BaseClientConfig
	NumRetries int
}

type HTTPClient struct {
	// origClient holds the http-client setup during initialization as a reference during any updates
	// This is required so that any updates to the client (like authz update) can be done on top of the origClient.
	// Updating the existing client fields in such use-case creates multiple authn-context and has unintentional authn background refreshes getting triggered.
	origClient *http.Client
	// client holds the http-client.
	// This is the client used to perform the actual http requests.
	// This client will be refreshed if authn is enabled and if credentials are updated at runtime.
	client atomic.Pointer[http.Client]
	// Config holds the config provided during initialization of the client
	Config *HTTPClientConfig `mapstructure:"client"`
}

// DefaultHTTPClient configures a default http client
func DefaultHTTPClient(cfg *HTTPClientConfig, spanFormatter func(operation string, r *http.Request) string) (*HTTPClient, error) {
	zap.S().Infof("Creating HTTP Client for %+v (authn_enabled: %+v)", cfg.Addr, cfg.isAuthNEnabled())
	rootCAPool, err := cfg.TLS.LoadRootCAPool()
	if err != nil {
		return nil, err
	}
	clientCAPool, err := cfg.TLS.LoadClientCAPool()
	if err != nil {
		return nil, err
	}
	certificates, err := cfg.TLS.Certificate()
	if err != nil {
		return nil, err
	}
	var tlsCerts []tls.Certificate
	if certificates != nil {
		tlsCerts = append(tlsCerts, *certificates)
	}

	var spanFormatOption otelhttp.Option
	if spanFormatter != nil {
		spanFormatOption = otelhttp.WithSpanNameFormatter(spanFormatter)
	}

	client := &http.Client{
		Transport: otelhttp.NewTransport(
			&http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: tlsCerts,
					RootCAs:      rootCAPool,
					ClientAuth:   tls.VerifyClientCertIfGiven,
					ClientCAs:    clientCAPool,
					MinVersion:   tls.VersionTLS12,
				},
			},
			spanFormatOption,
			otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace { return otelhttptrace.NewClientTrace(ctx) }),
		),
	}

	httpClient := &HTTPClient{Config: cfg, origClient: client}
	httpClient.client.Store(client)
	if cfg.NumRetries > 0 {
		if cfg.NumRetries > maxAllowedRetries {
			zap.L().Error("error, number of retries exceeded the upper limit")
			return nil, errors.ErrExceedMaxAllowedRetries
		}

		// Add retry capabilities
		retryHttpClient := retryablehttp.NewClient()
		retryHttpClient.HTTPClient = client
		retryHttpClient.Logger = newRetryLogger()
		retryHttpClient.RetryMax = defaultNumRetries

		if cfg.NumRetries != defaultNumRetries {
			retryHttpClient.RetryMax = cfg.NumRetries
		}

		httpClient.origClient = httpClient.client.Load()
		httpClient.client.Store(retryHttpClient.StandardClient())
	}

	// If AuthN is enabled, then get the http-client with authentication middleware.
	if cfg.isAuthNEnabled() {
		client, err = cfg.AuthnCfg.HttpClientWithAuth(httpClient.client.Load())
		if err != nil {
			return nil, err
		}
		httpClient.origClient = httpClient.client.Load()
		httpClient.client.Store(client)

		// Setup credentials watcher to watch out for runtime updates of credentials.
		go cfg.AuthnCfg.SetupCredentialsFileWatcher(httpClient)
	}

	return httpClient, nil
}

// Client returns the associated http client.
// If Authn config is provided, then a http.Client with authn injection capabilities is returned.
func (c *HTTPClient) Client(ctx context.Context) *http.Client {
	client := c.client.Load()
	return client
}

// Update implements the auth.AuthnRefresher interface for credential file watching.
// This method handles credential refresh with proper error handling and logging.
// Note: The credentials change check is already performed in auth/authn.go before calling this method.
func (c *HTTPClient) Update(creds *auth.ClientCredentials) {
	if !c.Config.isAuthNEnabled() {
		return
	}

	// Update the AuthnConfig with the new credentials
	c.Config.AuthnCfg.UpdateCredentials(creds)

	// Refresh the auth client with updated credentials
	newClient := c.Config.AuthnCfg.RefreshHttpAuthNClient(c.origClient)
	if newClient == nil {
		zap.L().Error("Failed to refresh HTTP auth client: RefreshHttpAuthNClient returned nil", zap.String("client_id", creds.ClientID))
		return
	}

	// Thread-safe update of the client
	c.client.Store(newClient)

	zap.L().Info("Successfully updated HTTP client with new authentication credentials",
		zap.String("client_id", creds.ClientID))
}

func (c *HTTPClient) Get(ctx context.Context, url string) (resp *http.Response, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	// Thread-safe access to the client
	client := c.client.Load()

	return client.Do(req) //nolint:gosec // G704: false positive - URL is validated before use
}

func (c *HTTPClient) Put(ctx context.Context, url string, body io.Reader) (resp *http.Response, err error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", url, body)
	if err != nil {
		return nil, err
	}

	// Thread-safe access to the client
	client := c.client.Load()

	return client.Do(req) //nolint:gosec // G704: false positive - URL is validated before use
}

func (c *HTTPClient) Post(ctx context.Context, url string, body io.Reader) (resp *http.Response, err error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return nil, err
	}

	// Thread-safe access to the client
	client := c.client.Load()

	return client.Do(req) //nolint:gosec // G704: false positive - URL is validated before use
}

func (c *HTTPClient) Do(ctx context.Context, req *http.Request) (resp *http.Response, err error) {
	req = req.WithContext(ctx)

	// Thread-safe access to the client
	client := c.client.Load()

	return client.Do(req) //nolint:gosec // G704: false positive - URL is validated before use
}

// AddClientFlags add the http client flags with the client prefix
func (cfg *HTTPClientConfig) AddClientFlags(cmd *cobra.Command, clientName string) bool {
	if cmd == nil || cfg == nil || clientName == "" {
		return false
	}
	cfg.BaseClientConfig = &BaseClientConfig{}
	return cfg.BaseClientConfig.AddClientFlags(cmd, clientName)
}

func (cfg *HTTPClientConfig) isAuthNEnabled() bool {
	return cfg.AuthnCfg != nil && cfg.AuthnCfg.OIDCConfig != nil && cfg.AuthnCfg.OIDCConfig.Host != ""
}

// NewHTTPClient returns a new HTTPClient type
func NewHTTPClient(client *http.Client, config *HTTPClientConfig) *HTTPClient {
	httpClient := &HTTPClient{
		Config:     config,
		origClient: client,
	}
	httpClient.client.Store(client)
	return httpClient
}
