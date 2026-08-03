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

package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cmnauth "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/auth"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/clientmetrics"
)

// TestAuthTokenFetcher_NoDoubleCount pins the suppression contract on the shared
// auth client: when a metrics transport wrapper is installed, the wrapper is the
// single source for the http.client.* family and otelhttp must not also emit into
// it. A live global meter provider is installed for the duration of the test so
// otelhttp would emit if the suppression regressed, which is what makes the
// assertion able to fail.
func TestAuthTokenFetcher_NoDoubleCount(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok", "token_type": "Bearer", "expires_in": 900, "scope": "scope",
		})
	}))
	defer authSrv.Close()

	reg := prometheus.NewRegistry()
	exporter, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		t.Fatalf("exporter: %v", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))

	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	rec, err := clientmetrics.NewRecorder(mp, nil)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}

	f := cmnauth.NewTokenFetcher(authSrv.URL, "client", "secret", "scope",
		cmnauth.WithTransportWrapper(func(inner http.RoundTripper) http.RoundTripper {
			return clientmetrics.NewTransport(inner, rec, clientmetrics.PeerServiceAuth)
		}),
	)
	if _, err := f.FetchToken(context.Background()); err != nil {
		t.Fatalf("FetchToken: %v", err)
	}

	scrapeSrv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer scrapeSrv.Close()
	resp, err := http.Get(scrapeSrv.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	scrape := string(body)

	const otelhttpScope = `otel_scope_name="go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"`
	if strings.Contains(scrape, otelhttpScope) {
		t.Fatalf("otelhttp emitted client metrics for the auth client: suppression regressed\n%s", scrape)
	}
	if !strings.Contains(scrape, `peer_service="auth"`) {
		t.Fatalf("expected the auth token request to be recorded with peer_service=auth\n%s", scrape)
	}
	// The auth client serves one route, so it must not carry a url.template
	// borrowed from another client's context.
	if strings.Contains(scrape, "url_template=") {
		t.Errorf("auth series must not carry a url_template\n%s", scrape)
	}
}
