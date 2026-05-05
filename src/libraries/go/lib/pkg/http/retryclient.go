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

package http

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/hashicorp/go-retryablehttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const ClientTimeoutEnvKey string = "NV_HTTP_CLIENT_TIMEOUT"

type features struct {
	appName       string
	clientTimeout time.Duration

	reqHeaders map[string]string

	co ClientOptions
}

// Retryable client options.
type ClientOptions struct {
	RetryWaitMin time.Duration
	RetryWaitMax time.Duration
	RetryMax     int
	CheckRetry   retryablehttp.CheckRetry
	Backoff      retryablehttp.Backoff
	ErrorHandler retryablehttp.ErrorHandler
	PrepareRetry retryablehttp.PrepareRetry
}

type Option func(*features)

// WithAppVersionUserAgent when called will add a
// User-Agent header to each client request with the
// provided appName plus the version.
func WithAppVersionUserAgent(appName string) Option {
	return func(f *features) {
		f.appName = appName
	}
}

// WithClientTimeout specifies the HTTP client's timeout
// it's recommended to set the timeout to something 30 seconds
// or greater due to the retryable client timeout being
// included in this value
func WithClientTimeout(timeout time.Duration) Option {
	return func(f *features) {
		f.clientTimeout = timeout
	}
}

// WithRequestHeader overwrites request header for all requests
func WithRequestHeader(key string, value string) Option {
	return func(f *features) {
		if f.reqHeaders == nil {
			f.reqHeaders = map[string]string{}
		}
		f.reqHeaders[key] = value
	}
}

// WithClientOptions sets retryablehttp.Client fields corresponding to those in co.
func WithClientOptions(co ClientOptions) Option {
	return func(f *features) { f.co = co }
}

// NewRetryableClient creates a retryablehttp.Client with the provided options
func NewRetryableClient(ctx context.Context, opts ...Option) *http.Client {
	log := core.GetLogger(ctx)
	defaultClientTimeout := 5 * time.Minute
	if v, ok := os.LookupEnv(ClientTimeoutEnvKey); ok && v != "" {
		timeout, err := time.ParseDuration(v)
		if err != nil {
			log.WithError(err).Warnf("failed to parse duration from env var %s with value %s", ClientTimeoutEnvKey, v)
		} else {
			log.Debugf("parsed %v client HTTP timeout from env var %s", timeout, ClientTimeoutEnvKey)
			defaultClientTimeout = timeout
		}
	}

	f := &features{
		clientTimeout: defaultClientTimeout,
	}
	for _, o := range opts {
		o(f)
	}

	rhc := newRetryableClient(ctx, f)

	httpClient := rhc.StandardClient()
	httpClient.Timeout = f.clientTimeout
	httpClient.Transport = otelhttp.NewTransport(httpClient.Transport)

	if f.appName != "" {
		httpClient.Transport = version.NewTransport(httpClient.Transport, f.appName)
	}

	if len(f.reqHeaders) > 0 {
		httpClient.Transport = newClientRequestHeadersTransport(httpClient.Transport, f.reqHeaders)
	}

	return httpClient
}

func newRetryableClient(ctx context.Context, f *features) *retryablehttp.Client {
	rhc := retryablehttp.NewClient()

	rhc.Logger = leveledLogger{logger: core.GetLogger(ctx)}
	if f.co.RetryWaitMin != 0 {
		rhc.RetryWaitMin = f.co.RetryWaitMin
	}
	if f.co.RetryWaitMax != 0 {
		rhc.RetryWaitMax = f.co.RetryWaitMax
	}
	if f.co.RetryMax != 0 {
		rhc.RetryMax = f.co.RetryMax
	}
	if f.co.CheckRetry != nil {
		rhc.CheckRetry = f.co.CheckRetry
	}
	if f.co.Backoff != nil {
		rhc.Backoff = f.co.Backoff
	}
	if f.co.ErrorHandler != nil {
		rhc.ErrorHandler = f.co.ErrorHandler
	}
	if f.co.PrepareRetry != nil {
		rhc.PrepareRetry = f.co.PrepareRetry
	}

	return rhc
}
