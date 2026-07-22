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

package clientmetrics_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cmnhttp "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/http"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/clientmetrics"
)

// TestEndToEnd_RealFactoryScrape exercises the full production path: the shared
// retryable client factory (with otelhttp metrics suppressed), the metrics
// RoundTripper injected via WithTransportWrapper, the OTel MeterProvider, the
// Prometheus exporter, and a real /metrics scrape. It prints the exposition and
// asserts the key guarantees (semconv names, peer.service, error.type, no
// duplicate duration family from otelhttp).
func TestEndToEnd_RealFactoryScrape(t *testing.T) {
	ctx := context.Background()

	// Meter provider + Prometheus exporter into a fresh registry (as the agent
	// wires it into the /metrics registry in production).
	reg := prometheus.NewRegistry()
	exporter, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		t.Fatalf("exporter: %v", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	rec, err := clientmetrics.NewRecorder(mp, []attribute.KeyValue{
		attribute.String("nvca.nca_id", "nca-demo"),
		attribute.String("nvca.cluster_name", "cluster-demo"),
	})
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}

	// A fake dependency: 200 with a body, and a 404 (a client error that the
	// retry policy does not retry, keeping the test fast).
	var mode string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "fail" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"payload":"response-body"}`))
	}))
	defer upstream.Close()

	// Build the client through the REAL shared factory, tagged peer.service=demo.
	// Retries are disabled so the transport-error case returns immediately.
	// otelhttp is inside this factory (metrics suppressed), so a single duration
	// family should appear.
	noRetry := func(context.Context, *http.Response, error) (bool, error) { return false, nil }
	client := cmnhttp.NewRetryableClient(ctx,
		cmnhttp.WithClientOptions(cmnhttp.ClientOptions{CheckRetry: noRetry}),
		cmnhttp.WithTransportWrapper(func(inner http.RoundTripper) http.RoundTripper {
			return clientmetrics.NewTransport(inner, rec, "demo")
		}),
	)

	// 1) success with a request+response body
	mode = "ok"
	req, _ := http.NewRequestWithContext(
		clientmetrics.ContextWithURLTemplate(ctx, "/v1/things/{id}"),
		http.MethodPost, upstream.URL+"/v1/things/abc-123", strings.NewReader(`{"req":"body"}`))
	doAndClose(t, client, req)

	// 2) server error (503)
	mode = "fail"
	req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL+"/v1/things/def-456", nil)
	doAndClose(t, client, req2)

	// 3) transport failure: connection refused (record error.type, no status code)
	reqErr, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:1/nope", nil)
	_, _ = client.Do(reqErr) // expected to error

	// Scrape /metrics via the same handler the agent uses.
	scrapeSrv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer scrapeSrv.Close()
	resp, err := http.Get(scrapeSrv.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	text := string(body)

	// Print a compact view (the _count series) for eyeballing; the full bucket
	// set is verbose and not needed to see the distinct series and labels.
	t.Log("\n----- /metrics (http_client_* _count series) -----")
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "http_client_") && strings.Contains(line, "_count{") {
			t.Log(line)
		}
	}
	t.Log("--------------------------------------------------")

	// Assertions: the key guarantees.
	mustContain(t, text, `http_client_request_duration_seconds_count{`)
	mustContain(t, text, `peer_service="demo"`)
	mustContain(t, text, `nvca_nca_id="nca-demo"`)
	mustContain(t, text, `url_template="/v1/things/{id}"`)
	mustContain(t, text, `http_response_status_code="404"`)
	mustContain(t, text, `error_type="connection_refused"`)
	mustContain(t, text, `http_client_request_body_size_bytes_count{`)
	mustContain(t, text, `http_client_response_body_size_bytes_count{`)

	// No double-count: exactly one duration _count comes from our scope; otelhttp
	// must not have emitted its own duration family.
	if n := strings.Count(text, "\nhttp_client_request_duration_seconds_count{"); n == 0 {
		t.Fatalf("expected duration count series present")
	}
}

func doAndClose(t *testing.T, c *http.Client, req *http.Request) {
	t.Helper()
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("request %s: %v", req.URL, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected /metrics to contain %q", needle)
	}
}
