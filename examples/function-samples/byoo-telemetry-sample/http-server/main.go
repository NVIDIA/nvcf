// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command byoo-telemetry-sample is an NVCF container function that emits one
// OTLP span, one OTLP log record, and a metrics snapshot for every inference
// request, so a Bring Your Own Observability (BYOO) setup can be checked end
// to end without a GPU or a model server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	listenAddr      = ":8000"
	maxRequestBytes = 1 << 20
	shutdownTimeout = 10 * time.Second

	headerRequestID   = "NVCF-REQID"
	headerTraceParent = "traceparent"
)

type echoRequest struct {
	Message string `json:"message"`
}

type echoResponse struct {
	Message string `json:"message"`
	TraceID string `json:"traceId"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config, warnings := loadConfig(os.Getenv)
	for _, warning := range warnings {
		logger.Warn(warning)
	}
	tel := newTelemetry(config, logger)
	logger.Info("telemetry configured",
		"service_name", config.serviceName,
		"logs_endpoint", config.logsEndpoint,
		"metrics_endpoint", config.metricsEndpoint,
		"traces_endpoint", config.tracesEndpoint)

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           newRouter(tel, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", server.Addr)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown failed", "error", err)
	}
	if err := tel.close(shutdownCtx); err != nil {
		logger.Warn("telemetry flush incomplete", "error", err)
	}
	logger.Info("stopped")
}

func newRouter(tel *telemetry, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) { handleEcho(w, r, tel, logger) })
	return mux
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func handleEcho(w http.ResponseWriter, r *http.Request, tel *telemetry, logger *slog.Logger) {
	start := time.Now()
	traceID, parentSpanID := parseTraceParent(r.Header.Get(headerTraceParent))
	if traceID == "" {
		traceID = randomHex(16)
	}
	rec := requestRecord{
		route:        "/echo",
		method:       r.Method,
		requestID:    r.Header.Get(headerRequestID),
		traceID:      traceID,
		spanID:       randomHex(8),
		parentSpanID: parentSpanID,
		start:        start,
	}

	status, body := echo(r)
	if resp, ok := body.(echoResponse); ok {
		resp.TraceID = traceID
		body = resp
		rec.message = resp.Message
	}
	writeJSON(w, status, body)

	rec.status = status
	rec.end = time.Now()
	level := slog.LevelInfo
	if status >= http.StatusBadRequest {
		level = slog.LevelWarn
	}
	logger.Log(r.Context(), level, "request handled",
		"route", rec.route,
		"status", status,
		"request_id", rec.requestID,
		"trace_id", rec.traceID,
		"span_id", rec.spanID,
		"duration_seconds", rec.end.Sub(rec.start).Seconds())
	tel.record(rec)
}

func echo(r *http.Request) (int, any) {
	if r.Method != http.MethodPost {
		return http.StatusMethodNotAllowed, map[string]string{"error": "use POST"}
	}
	var req echoRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxRequestBytes)).Decode(&req); err != nil {
		return http.StatusBadRequest, map[string]string{"error": "body must be JSON with a message field"}
	}
	return http.StatusOK, echoResponse{Message: req.Message}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
