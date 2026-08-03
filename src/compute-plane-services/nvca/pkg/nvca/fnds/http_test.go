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
	"io"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"fmt"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/clientmetrics"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"golang.org/x/time/rate"
)

func TestHTTPClient(t *testing.T) {
	mux := http.NewServeMux()
	wantErr := new(error)
	wantCode := new(int)
	mux.HandleFunc("/foo", func(w http.ResponseWriter, r *http.Request) {
		if *wantErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte((*wantErr).Error()))
			return
		}
		if *wantCode != 0 {
			w.WriteHeader(*wantCode)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Test rate limiting
	httpClient := newHTTPClient(1*time.Second, 5, 2)

	// Create some good requests but expect rate limiting.
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		resp, err := httpClient.Get(srv.URL + "/foo")
		if assert.Error(ct, err) {
			assert.Contains(ct, err.Error(), "rate limited")
			assert.Nil(ct, resp)
		}
	}, 2*time.Second, 10*time.Millisecond)

	// Test circuit breaking.
	httpClient = newHTTPClient(1*time.Second, rate.Limit(100), 100)

	for range 5 {
		resp, err := httpClient.Get(srv.URL + "/foo")
		if assert.NoError(t, err) {
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		}
	}

	// Break circuit on consecutive errors.
	*wantErr = fmt.Errorf("some error")
	for range 6 {
		resp, err := httpClient.Get(srv.URL + "/foo")
		if assert.NoError(t, err) {
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		}
	}
	resp, err := httpClient.Get(srv.URL + "/foo")
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "circuit breaker is open")
		assert.Nil(t, resp)
	}

	// Wait for circuit to be half-closed.
	*wantErr = nil
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		resp, err := httpClient.Get(srv.URL + "/foo")
		if assert.NoError(ct, err) {
			assert.Equal(ct, http.StatusOK, resp.StatusCode)
		}
	}, 2*time.Second, 100*time.Millisecond)

	// Break circuit on unexpected code.
	*wantCode = http.StatusNoContent
	resp, err = httpClient.Get(srv.URL + "/foo")
	if assert.NoError(t, err) {
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	}
	resp, err = httpClient.Get(srv.URL + "/foo")
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "circuit breaker is open")
		assert.Nil(t, resp)
	}
}

type fndsSentinelTransport struct{ inner http.RoundTripper }

func (s *fndsSentinelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.inner.RoundTrip(req)
}

// TestNewHTTPClient_AppliesTransportWrappers verifies the wrapper hook is applied
// as the outermost transport layer, which is how the FNDS client is instrumented.
func TestNewHTTPClient_AppliesTransportWrappers(t *testing.T) {
	var called bool
	sentinel := &fndsSentinelTransport{}
	c := NewHTTPClient(func(inner http.RoundTripper) http.RoundTripper {
		called = true
		sentinel.inner = inner
		return sentinel
	})
	if !called {
		t.Fatal("expected the transport wrapper to be invoked")
	}
	if c.Transport != http.RoundTripper(sentinel) {
		t.Fatalf("expected the wrapper's return value to be the outermost transport, got %T", c.Transport)
	}
}

// TestNewHTTPClient_NilWrapperIsIgnored keeps the no-wrapper path unchanged.
func TestNewHTTPClient_NilWrapperIsIgnored(t *testing.T) {
	plain := NewHTTPClient()
	withNil := NewHTTPClient(nil)
	if plain.Transport == nil || withNil.Transport == nil {
		t.Fatal("transport must be set in both cases")
	}
	if _, ok := withNil.Transport.(*fndsSentinelTransport); ok {
		t.Fatal("a nil wrapper must not wrap the transport")
	}
}

// TestNewHTTPClient_NoDoubleCount pins the suppression contract: when a metrics
// transport wrapper is installed, that wrapper is the single source for the
// http.client.* family and otelhttp must not also emit into it. A live global
// meter provider is installed so otelhttp would emit if suppression regressed.
func TestNewHTTPClient_NoDoubleCount(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer backend.Close()

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

	c := NewHTTPClient(func(inner http.RoundTripper) http.RoundTripper {
		return clientmetrics.NewTransport(inner, rec, clientmetrics.PeerServiceFNDS)
	})
	resp, err := c.Get(backend.URL + "/v1/stages")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer srv.Close()
	s, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	body, _ := io.ReadAll(s.Body)
	_ = s.Body.Close()
	scrape := string(body)

	const otelhttpScope = `otel_scope_name="go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"`
	if strings.Contains(scrape, otelhttpScope) {
		t.Fatalf("otelhttp emitted client metrics for the FNDS client: suppression regressed\n%s", scrape)
	}

	got := strings.Count(scrape, "\nhttp_client_request_duration_seconds_count{")
	if got != 1 {
		t.Fatalf("expected exactly 1 FNDS duration observation, got %d\n%s", got, scrape)
	}
	if !strings.Contains(scrape, `peer_service="fnds"`) {
		t.Errorf("expected peer_service=fnds\n%s", scrape)
	}
}
