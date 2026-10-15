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

package clientmetrics

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// roundTripFunc adapts a function to http.RoundTripper for tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// newTestRecorder builds a Recorder backed by a MeterProvider whose OTel
// Prometheus exporter registers into the returned registry, so a test can
// gather it like a real scrape.
func newTestRecorder(t *testing.T) (*Recorder, *prometheus.Registry) {
	t.Helper()
	return newTestRecorderWithDefaults(t, nil)
}

func newTestRecorderWithDefaults(t *testing.T, defaults []attribute.KeyValue) (*Recorder, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	exporter, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		t.Fatalf("creating exporter: %v", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	rec, err := NewRecorder(mp, defaults)
	if err != nil {
		t.Fatalf("creating recorder: %v", err)
	}
	return rec, reg
}

func gatherNames(t *testing.T, reg *prometheus.Registry) map[string]bool {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	names := map[string]bool{}
	for _, f := range families {
		names[f.GetName()] = true
	}
	return names
}

func TestRoundTripper_RecordsDurationThroughScrape(t *testing.T) {
	rec, reg := newTestRecorder(t)

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    req,
		}, nil
	})
	rt := NewTransport(inner, rec, "icms")

	req, err := http.NewRequest(http.MethodPost, "https://icms.example.com/v2/heartbeat", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	_ = resp.Body.Close()

	names := gatherNames(t, reg)
	if !names["http_client_request_duration_seconds"] {
		t.Fatalf("expected http_client_request_duration_seconds in scrape, got: %v", names)
	}

	// Assert the peer.service and status labels are present with a single sample.
	families, _ := reg.Gather()
	var found bool
	for _, f := range families {
		if f.GetName() != "http_client_request_duration_seconds" {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["peer_service"] == "icms" && labels["http_response_status_code"] == "200" {
				if got := m.GetHistogram().GetSampleCount(); got != 1 {
					t.Fatalf("expected sample count 1, got %d", got)
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("did not find series with peer_service=icms and status=200")
	}
}

func TestRoundTripper_RecordsBodySizes(t *testing.T) {
	rec, reg := newTestRecorder(t)

	body := "response-payload"
	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       req,
		}, nil
	})
	rt := NewTransport(inner, rec, "icms")

	reqBody := "request-payload"
	req, _ := http.NewRequest(http.MethodPost, "https://icms.example.com/x", strings.NewReader(reqBody))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	_ = resp.Body.Close()

	names := gatherNames(t, reg)
	if !names["http_client_request_body_size_bytes"] {
		t.Fatalf("expected request body size metric, got: %v", names)
	}
	if !names["http_client_response_body_size_bytes"] {
		t.Fatalf("expected response body size metric, got: %v", names)
	}
}

func TestRoundTripper_RecordsErrorType(t *testing.T) {
	rec, reg := newTestRecorder(t)

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errTimeout{}
	})
	rt := NewTransport(inner, rec, "reval")

	req, _ := http.NewRequest(http.MethodPost, "https://reval.example.com/v1/render", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatalf("expected error from round trip")
	}

	families, _ := reg.Gather()
	var sawErrorType bool
	for _, f := range families {
		if f.GetName() != "http_client_request_duration_seconds" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "error_type" && l.GetValue() == "timeout" {
					sawErrorType = true
				}
			}
		}
	}
	if !sawErrorType {
		t.Fatalf("expected error_type=timeout label on failed request")
	}
}

func TestRecorder_DefaultAttributesOnEverySeries(t *testing.T) {
	defaults := []attribute.KeyValue{
		attribute.String("nvca.nca_id", "nca-1"),
		attribute.String("nvca.cluster_name", "cluster-a"),
	}
	rec, reg := newTestRecorderWithDefaults(t, defaults)

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
	})
	rt := NewTransport(inner, rec, "icms")
	req, _ := http.NewRequest(http.MethodPost, "https://icms.example.com/x", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("round trip: %v", err)
	}

	families, _ := reg.Gather()
	var sawNCA, sawCluster bool
	for _, f := range families {
		if f.GetName() != "http_client_request_duration_seconds" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "nvca_nca_id" && l.GetValue() == "nca-1" {
					sawNCA = true
				}
				if l.GetName() == "nvca_cluster_name" && l.GetValue() == "cluster-a" {
					sawCluster = true
				}
			}
		}
	}
	if !sawNCA || !sawCluster {
		t.Fatalf("expected default labels on series (nca=%v cluster=%v)", sawNCA, sawCluster)
	}
}

func TestRoundTripper_PerRequestURLTemplateFromContext(t *testing.T) {
	rec, reg := newTestRecorder(t)

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
	})
	// Static option provides a default; the per-request context value overrides it.
	rt := NewTransport(inner, rec, "icms", WithURLTemplate("/static/default"))

	req, _ := http.NewRequest(http.MethodPost, "https://icms.example.com/v1/nvca/clusters/abc-123/heartbeat", nil)
	ctx := ContextWithURLTemplate(req.Context(), "/v1/nvca/clusters/{clusterId}/heartbeat")
	if _, err := rt.RoundTrip(req.WithContext(ctx)); err != nil {
		t.Fatalf("round trip: %v", err)
	}

	families, _ := reg.Gather()
	var sawTemplate bool
	for _, f := range families {
		if f.GetName() != "http_client_request_duration_seconds" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "url_template" {
					if l.GetValue() != "/v1/nvca/clusters/{clusterId}/heartbeat" {
						t.Fatalf("expected per-request template to win, got %q", l.GetValue())
					}
					sawTemplate = true
				}
			}
		}
	}
	if !sawTemplate {
		t.Fatalf("expected url_template label from context")
	}
}

// TestNewTransport_MeterGatedPassthrough verifies a nil Recorder yields the
// inner transport unchanged (zero overhead, no behavioural change).
func TestNewTransport_MeterGatedPassthrough(t *testing.T) {
	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
	})
	got := NewTransport(inner, nil, "icms")
	// With no recorder, NewTransport must return the exact inner transport.
	gotFn, ok := got.(roundTripFunc)
	if !ok {
		t.Fatalf("expected inner transport returned unchanged, got %T", got)
	}
	// Sanity: the returned transport still works.
	req, _ := http.NewRequest(http.MethodGet, "https://x.example.com/", nil)
	if _, err := gotFn.RoundTrip(req); err != nil {
		t.Fatalf("passthrough round trip: %v", err)
	}
}

// errTimeout is a net.Error-like timeout used to exercise error classification.
type errTimeout struct{}

func (errTimeout) Error() string   { return "i/o timeout" }
func (errTimeout) Timeout() bool   { return true }
func (errTimeout) Temporary() bool { return true }

var _ error = errTimeout{}
