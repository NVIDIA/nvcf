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

package fnds

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"golang.org/x/time/rate"
)

var (
	// Duration during which no requests allowed.
	defaultCircuitBreakerTimeout = 2 * time.Minute
	// Number of requests allowed in one second.
	defaultRequestRateLimit = rate.Limit(25)
	// Number of requests allowed at once.
	defaultRequestBurst = 10
)

// NewHTTPClient returns an HTTP client that retries on HTTP 429/5xx errors,
// and will return errors for all client connections after multiple non-retryable codes
// or max-retried requests (circuit-breaker), or when too many requests were sent
// in a given time period.
//
// transportWrappers are applied as the outermost transport layers, so each
// observes one logical request and its final outcome. This is how the client is
// instrumented without this package depending on the metrics implementation.
// A nil wrapper is ignored.
func NewHTTPClient(transportWrappers ...func(http.RoundTripper) http.RoundTripper) *http.Client {
	return newHTTPClient(defaultCircuitBreakerTimeout, defaultRequestRateLimit, defaultRequestBurst, transportWrappers...)
}

func newHTTPClient(
	cbTimeout time.Duration,
	rateLimit rate.Limit,
	burst int,
	transportWrappers ...func(http.RoundTripper) http.RoundTripper,
) *http.Client {
	rhttpClient := retryablehttp.NewClient()
	cbRT := circuitBreakingRoundTripper{
		base: rhttpClient.StandardClient().Transport,
		okHTTPCodes: map[int]bool{
			http.StatusOK:       true,
			http.StatusAccepted: true,
		},
		// 20 requests per second, bursts of 5 requests allowed at once.
		lim: rate.NewLimiter(rateLimit, burst),
		cb: gobreaker.NewTwoStepCircuitBreaker[struct{}](gobreaker.Settings{
			Name: "fnds",
			// Allow a trickle of requests at a time after half-open.
			MaxRequests: 2,
			// Configured time until half-open.
			Timeout: cbTimeout,
			// A 60% error rate or more than 5 consecutive failures should trigger backoff.
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				if counts.Requests < 3 {
					return false
				}
				if counts.ConsecutiveFailures > 5 {
					return true
				}
				failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
				return failureRatio >= 0.6
			},
		}),
	}
	// otelhttp always provides client tracing. Its own HTTP client metrics are
	// suppressed only when a transport wrapper is present, so that wrapper is the
	// single source for the semconv HTTP client series and does not double-count.
	// Callers without a wrapper keep otelhttp's default metric behaviour.
	otelOpts := []otelhttp.Option{
		otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace {
			return otelhttptrace.NewClientTrace(ctx)
		}),
	}
	var hasWrapper bool
	for _, wrap := range transportWrappers {
		if wrap != nil {
			hasWrapper = true
			break
		}
	}
	if hasWrapper {
		otelOpts = append(otelOpts, otelhttp.WithMeterProvider(metricnoop.NewMeterProvider()))
	}

	var transport http.RoundTripper = otelhttp.NewTransport(cbRT, otelOpts...)
	for _, wrap := range transportWrappers {
		if wrap != nil {
			transport = wrap(transport)
		}
	}
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: transport,
	}
}

// circuitBreakingRoundTripper is a RoundTripper that understands RoundTrip() errors and HTTP response codes.
type circuitBreakingRoundTripper struct {
	base        http.RoundTripper
	okHTTPCodes map[int]bool
	lim         *rate.Limiter
	cb          *gobreaker.TwoStepCircuitBreaker[struct{}]
}

func (t circuitBreakingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	done, err := t.cb.Allow()
	if err != nil {
		return nil, err
	}

	if !t.lim.Allow() {
		return nil, fmt.Errorf("rate limited")
	}

	resp, err := t.base.RoundTrip(req)

	done(err == nil && t.okHTTPCodes[resp.StatusCode])

	return resp, err
}
