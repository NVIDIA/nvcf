// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
)

const reviewMarker = "review-marker"

func TestSDKLoggerWithholdsValuesAndErrorText(t *testing.T) {
	logs := captureDefaultLog(t)
	logger := logr.New(newSDKSink(time.Minute, time.Now))

	logger.Error(errors.New(`parse "http://user:review-marker@collector.example": invalid port`), "parse url", "input", "http://user:review-marker@collector.example")
	logger.V(1).Info("parse headers", "input", "authorization=Bearer review-marker%ZZ")

	out := logs.String()
	if !strings.Contains(out, `"message":"parse url"`) || !strings.Contains(out, `"reason":"error"`) {
		t.Fatalf("log = %s, want the plain message and the reason", out)
	}
	if !strings.Contains(out, `"message":"parse headers"`) {
		t.Fatalf("log = %s, want the warning's plain message", out)
	}
	for _, leak := range []string{reviewMarker, "collector.example", "Bearer", "invalid port"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log = %s, contains %q", out, leak)
		}
	}
}

func TestSDKLoggerWithholdsMessagesThatAreNotPlainWords(t *testing.T) {
	logs := captureDefaultLog(t)
	logger := logr.New(newSDKSink(time.Minute, time.Now))

	logger.Error(errors.New("refused"), "dial collector.example:4317 failed")

	out := logs.String()
	if !strings.Contains(out, `"message":"withheld"`) || strings.Contains(out, "collector.example") {
		t.Fatalf("log = %s, want the message withheld", out)
	}
}

func TestSDKLoggerDropsInfoAndDebug(t *testing.T) {
	logs := captureDefaultLog(t)
	logger := logr.New(newSDKSink(time.Minute, time.Now))

	logger.V(4).Info("exporter started", "endpoint", "collector.example:4318")
	logger.V(8).Info("export done", "endpoint", "collector.example:4318")

	if out := logs.String(); out != "" {
		t.Fatalf("log = %s, want nothing below warning level", out)
	}
}

func TestSDKLoggerLogsEachMessageAtMostOncePerInterval(t *testing.T) {
	logs := captureDefaultLog(t)
	now := time.Unix(1_800_000_000, 0)
	logger := logr.New(newSDKSink(5*time.Minute, func() time.Time { return now }))

	logger.Error(errors.New("x"), "parse url")
	logger.Error(errors.New("x"), "parse url")
	logger.Error(errors.New("x"), "parse headers")
	if got := strings.Count(logs.String(), "\n"); got != 2 {
		t.Fatalf("lines = %d, want one per message\n%s", got, logs.String())
	}
	now = now.Add(5 * time.Minute)
	logger.Error(errors.New("x"), "parse url")
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], `"failures":2`) {
		t.Fatalf("lines = %v, want a third line counting 2 failures of parse url", lines)
	}
}

// recordingSink keeps everything the SDK logs, values included, so a test
// can prove none of it is printed.
type recordingSink struct {
	mu  sync.Mutex
	out strings.Builder
}

func (s *recordingSink) Init(logr.RuntimeInfo) {}
func (s *recordingSink) Enabled(int) bool      { return true }
func (s *recordingSink) Info(_ int, msg string, kv ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(&s.out, msg, kv)
}
func (s *recordingSink) Error(err error, msg string, kv ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(&s.out, err, msg, kv)
}
func (s *recordingSink) WithValues(...any) logr.LogSink { return s }
func (s *recordingSink) WithName(string) logr.LogSink   { return s }
func (s *recordingSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

func TestNewOTLPKeepsSDKDiagnosticsFromPrintingEnvironmentValues(t *testing.T) {
	logs := captureDefaultLog(t)
	inherited := &recordingSink{}
	otel.SetLogger(logr.New(inherited))
	t.Cleanup(func() { otel.SetLogger(logr.Discard()) })
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=Bearer review-marker%ZZ")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://user:review-marker@[::1")

	r, endpoint := httpReceiver(t)
	exporter := newOTLP(t, endpoint, ProtocolHTTP)
	exporter.Add(SubmitsAmbiguous, 1, nil)
	flush(t, exporter)

	for name, out := range map[string]string{"inherited SDK logger": inherited.String(), "log": logs.String()} {
		if strings.Contains(out, reviewMarker) {
			t.Errorf("%s = %s, contains the marker", name, out)
		}
	}
	if !strings.Contains(logs.String(), "telemetry SDK diagnostic") {
		t.Errorf("log = %s, want the SDK's parse failures reported without values", logs.String())
	}
	if r.metric(MetricPrefix+SubmitsAmbiguous) == nil {
		t.Fatal("no metrics delivered")
	}
}
