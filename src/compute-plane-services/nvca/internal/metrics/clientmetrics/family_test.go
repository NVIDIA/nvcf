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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/clientmetrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

// newScrapeRecorder returns a Recorder wired to a fresh registry, plus a scrape
// function returning the current Prometheus exposition.
func newScrapeRecorder(t *testing.T, defaultAttrs ...attribute.KeyValue) (*clientmetrics.Recorder, func() string) {
	t.Helper()
	reg := prometheus.NewRegistry()
	exporter, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		t.Fatalf("exporter: %v", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	rec, err := clientmetrics.NewRecorder(mp, defaultAttrs)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	return rec, func() string {
		srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
		defer srv.Close()
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatalf("scrape: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return string(body)
	}
}

// bucketFor returns the count in the le="<bound>" bucket of metric.
func bucketFor(t *testing.T, text, metric, bound string) string {
	t.Helper()
	prefix := metric + "_bucket{"
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) && strings.Contains(line, `le="`+bound+`"`) {
			fields := strings.Fields(line)
			return fields[len(fields)-1]
		}
	}
	t.Fatalf("no %s bucket le=%q in scrape:\n%s", metric, bound, text)
	return ""
}

// TestDurationBucketsResolveSubSecondLatency guards the bucket choice. With the
// OTel SDK default boundaries (0, 5, 10, ... 10000, shaped for milliseconds) a
// seconds-valued instrument puts every realistic latency in the first bucket and
// quantile queries become meaningless. Sub-second observations must be
// distinguishable from each other.
func TestDurationBucketsResolveSubSecondLatency(t *testing.T) {
	rec, scrape := newScrapeRecorder(t)

	// 12ms and 800ms must land in different buckets.
	for _, d := range []time.Duration{12 * time.Millisecond, 800 * time.Millisecond} {
		rec.RecordHTTPRequest(context.Background(), clientmetrics.HTTPObservation{
			Duration: d, RequestBodySize: -1, ResponseBodySize: -1,
		})
	}
	text := scrape()

	const m = "http_client_request_duration_seconds"
	// 12ms <= 0.025 but 800ms is not, so the le="0.025" bucket holds exactly one.
	if got := bucketFor(t, text, m, "0.025"); got != "1" {
		t.Errorf("le=0.025 bucket = %s, want 1 (12ms only); buckets do not resolve sub-second latency", got)
	}
	if got := bucketFor(t, text, m, "1"); got != "2" {
		t.Errorf("le=1 bucket = %s, want 2 (both observations)", got)
	}
}

// TestSizeBucketsResolveLargePayloads guards against body-size boundaries that
// top out below NVCA's real payloads, which would collapse them all into +Inf.
// The two sizes are deliberately far apart and each is asserted in a bucket the
// other cannot reach, so swapping request and response attribution fails.
func TestSizeBucketsResolveLargePayloads(t *testing.T) {
	rec, scrape := newScrapeRecorder(t)
	rec.RecordHTTPRequest(context.Background(), clientmetrics.HTTPObservation{
		Duration: time.Millisecond, RequestBodySize: 512, ResponseBodySize: 40000,
	})
	text := scrape()

	if got := bucketFor(t, text, "http_client_response_body_size_bytes", "100000"); got != "1" {
		t.Errorf("40000-byte response not resolved below le=100000 (got %s)", got)
	}
	if got := bucketFor(t, text, "http_client_request_body_size_bytes", "1000"); got != "1" {
		t.Errorf("512-byte request not resolved below le=1000 (got %s)", got)
	}
	// Attribution: the 512-byte request must NOT appear in a response bucket that
	// only it could reach, and the 40000-byte response must NOT be in a request
	// bucket. Either failure means the two instruments are crossed.
	if got := bucketFor(t, text, "http_client_response_body_size_bytes", "1000"); got != "0" {
		t.Errorf("response size bucket le=1000 = %s, want 0: request size recorded as response", got)
	}
	if got := bucketFor(t, text, "http_client_request_body_size_bytes", "100000"); got != "1" {
		t.Errorf("request size bucket le=100000 = %s, want 1 (the 512-byte request)", got)
	}
	if got := bucketFor(t, text, "http_client_request_body_size_bytes", "100"); got != "0" {
		t.Errorf("request size bucket le=100 = %s, want 0", got)
	}
}

// TestInstrumentsGenericFamily is the extension point: a caller declares a
// Family and records through it without any change to the Recorder.
func TestInstrumentsGenericFamily(t *testing.T) {
	rec, scrape := newScrapeRecorder(t, attribute.String("nvca.nca_id", "nca-x"))

	custom := clientmetrics.Family{
		Duration: clientmetrics.InstrumentSpec{
			Name:        "custom.client.operation.duration",
			Unit:        "s",
			Description: "test family",
			Buckets:     clientmetrics.DurationBucketsSeconds,
		},
	}
	insts, err := rec.Instruments(custom)
	if err != nil {
		t.Fatalf("Instruments: %v", err)
	}
	insts.Record(context.Background(), clientmetrics.Observation{
		Duration:     20 * time.Millisecond,
		RequestSize:  -1,
		ResponseSize: -1,
		Attrs:        []attribute.KeyValue{attribute.String("peer.service", "custom")},
	})

	text := scrape()
	if !strings.Contains(text, "custom_client_operation_duration_seconds_count{") {
		t.Fatalf("custom family not exported:\n%s", text)
	}
	// Default labels are applied by the Recorder, not by each decorator.
	if !strings.Contains(text, `nvca_nca_id="nca-x"`) {
		t.Errorf("default attributes not applied to custom family")
	}
	if !strings.Contains(text, `peer_service="custom"`) {
		t.Errorf("peer_service not applied to custom family")
	}
}

// TestInstrumentsNilRecorderIsNoop keeps the "always safe to call" contract.
func TestInstrumentsNilRecorderIsNoop(t *testing.T) {
	var rec *clientmetrics.Recorder
	insts, err := rec.Instruments(clientmetrics.MessagingClientFamily)
	if err != nil {
		t.Fatalf("nil recorder Instruments: %v", err)
	}
	insts.Record(context.Background(), clientmetrics.Observation{Duration: time.Second})
	rec.RecordHTTPRequest(context.Background(), clientmetrics.HTTPObservation{Duration: time.Second})
}

// fakeQueue is a queue.Client stub.
type fakeQueue struct{ err error }

func (f fakeQueue) ReceiveMessage(context.Context, queue.ReceiveMessageInput) ([]queue.ReceiveMessageOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []queue.ReceiveMessageOutput{{MessageID: "a"}, {MessageID: "b"}}, nil
}
func (f fakeQueue) DeleteMessage(context.Context, queue.DeleteMessageInput) error { return f.err }
func (f fakeQueue) ChangeMessageVisibility(context.Context, queue.ChangeMessageVisibilityInput) error {
	return f.err
}
func (f fakeQueue) IsMessageNotFoundError(error) bool { return false }

// TestQueueClientDecorator proves a new transport type is one decorator over the
// shared Recorder: messaging semconv labels, NVCA default labels, and the
// messaging duration family, with no Recorder changes.
func TestQueueClientDecorator(t *testing.T) {
	rec, scrape := newScrapeRecorder(t, attribute.String("nvca.nca_id", "nca-q"))

	c, err := clientmetrics.NewQueueClient(fakeQueue{}, rec, clientmetrics.PeerServiceSQS, "aws_sqs")
	if err != nil {
		t.Fatalf("NewQueueClient: %v", err)
	}
	if _, err := c.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: "https://sqs/secret-account/q"},
	}); err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	text := scrape()
	if !strings.Contains(text, "messaging_client_operation_duration_seconds_count{") {
		t.Fatalf("messaging duration family not exported:\n%s", text)
	}
	for _, want := range []string{
		`peer_service="sqs"`,
		`messaging_system="aws_sqs"`,
		`messaging_operation_name="receive"`,
		`messaging_destination_name="CreationQueue"`,
		`nvca_nca_id="nca-q"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in scrape:\n%s", want, text)
		}
	}
	// Cardinality: the queue URL must never become a label value.
	if strings.Contains(text, "secret-account") {
		t.Errorf("queue URL leaked into labels")
	}
}

// TestQueueClientRecordsErrorType checks failures carry a bounded error.type.
func TestQueueClientRecordsErrorType(t *testing.T) {
	rec, scrape := newScrapeRecorder(t)
	c, err := clientmetrics.NewQueueClient(fakeQueue{err: context.DeadlineExceeded}, rec, clientmetrics.PeerServiceNATS, "nats")
	if err != nil {
		t.Fatalf("NewQueueClient: %v", err)
	}
	if err := c.DeleteMessage(context.Background(), queue.DeleteMessageInput{
		QueueInfo: queue.MessageQueueInfo{QueueType: queue.TerminationQueue},
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the inner error to pass through, got %v", err)
	}
	text := scrape()
	if !strings.Contains(text, `error_type="timeout"`) {
		t.Errorf("expected error_type=timeout in scrape:\n%s", text)
	}
}

// TestNewQueueClientNilRecorderReturnsInner keeps the decorator meter-gated.
func TestNewQueueClientNilRecorderReturnsInner(t *testing.T) {
	inner := fakeQueue{}
	got, err := clientmetrics.NewQueueClient(inner, nil, clientmetrics.PeerServiceSQS, "aws_sqs")
	if err != nil {
		t.Fatalf("NewQueueClient: %v", err)
	}
	if _, ok := got.(fakeQueue); !ok {
		t.Fatalf("expected the inner client unchanged when the recorder is nil, got %T", got)
	}
}

// TestUnknownBodySizesAreOmitted covers the two cases where a payload size is
// unknown rather than zero. Recording them as 0 would pull both size histograms
// toward their lowest bucket and misreport payload distribution; duration must
// still be recorded in both cases.
func TestUnknownBodySizesAreOmitted(t *testing.T) {
	rec, scrape := newScrapeRecorder(t)

	// Chunked response: the server flushes, so no Content-Length is sent and the
	// client sees ContentLength == -1.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk-one"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("chunk-two"))
	}))
	defer upstream.Close()

	client := &http.Client{
		Transport: clientmetrics.NewTransport(http.DefaultTransport, rec, "demo"),
	}

	// Streamed request body: io.NopCloser is not one of the types http.NewRequest
	// can measure, so ContentLength stays 0 with a non-nil Body, which Go defines
	// as unknown for client requests.
	req, err := http.NewRequest(http.MethodPost, upstream.URL, io.NopCloser(strings.NewReader("streamed")))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if req.ContentLength != 0 || req.Body == nil {
		t.Fatalf("precondition: want ContentLength==0 with non-nil Body, got %d body=%v",
			req.ContentLength, req.Body != nil)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.ContentLength != -1 {
		t.Fatalf("precondition: want a chunked response (ContentLength -1), got %d", resp.ContentLength)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	text := scrape()

	if !strings.Contains(text, "http_client_request_duration_seconds_count{") {
		t.Fatal("duration must be recorded even when both body sizes are unknown")
	}
	if strings.Contains(text, "http_client_request_body_size_bytes_count{") {
		t.Errorf("unknown request body size must be omitted, not recorded as 0:\n%s", text)
	}
	if strings.Contains(text, "http_client_response_body_size_bytes_count{") {
		t.Errorf("unknown response body size must be omitted, not recorded as 0:\n%s", text)
	}
}
