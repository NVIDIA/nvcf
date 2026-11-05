// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/config"
)

const messagesStreamPing = "event: ping\ndata: {\"type\": \"ping\"}\n\n"

func TestMessagesFailsWhenUpstreamStreamEndsEarly(t *testing.T) {
	t.Parallel()

	streamStop := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	withoutStop := strings.TrimSuffix(messagesTestSSE, streamStop)
	cutEvent := "event: content_block_delta\ndata: {\"type\":\"content_bl"
	upstreamError := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n"
	largeEvent := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" +
		strings.Repeat("a", messagesUsageBufferLimit) + "\"}}\n\n"
	crLines := func(sse string) string { return strings.ReplaceAll(strings.ReplaceAll(sse, "\r\n", "\n"), "\n", "\r") }

	tests := []struct {
		name         string
		unary        bool
		contentType  string
		encoding     string
		upstreamBody string
		dropMidway   bool
		// stall keeps the upstream open until the gateway request timeout ends it.
		stall bool
		// wantAdded is what the gateway writes after the upstream bytes; empty means nothing.
		wantAdded string
		want502   bool
	}{
		{name: "ends with message_stop", upstreamBody: messagesTestSSE},
		{name: "message_stop then [DONE]", upstreamBody: messagesTestSSE + "data: [DONE]\n\n"},
		{name: "event over the usage buffer limit", upstreamBody: largeEvent + messagesTestSSE},
		{name: "CR line endings", upstreamBody: crLines(messagesTestSSE)},
		// "\r\r" already ends the last frame, so no ping seal comes first.
		{name: "clean end without message_stop, CR line endings", upstreamBody: crLines(withoutStop), wantAdded: "error", want502: true},
		// message_stop ends the stream, so a later read failure does not change it.
		{name: "connection drops after message_stop", upstreamBody: messagesTestSSE, dropMidway: true},
		{name: "gateway timeout after message_stop", upstreamBody: messagesTestSSE, stall: true},
		{name: "clean end without message_stop", upstreamBody: withoutStop, wantAdded: "error", want502: true},
		{name: "clean end mid event", upstreamBody: withoutStop + cutEvent, wantAdded: "\n" + messagesStreamPing + "error", want502: true},
		{name: "clean end mid event after CRLF line", upstreamBody: withoutStop + "event: content_block_delta\r\n", wantAdded: messagesStreamPing + "error", want502: true},
		{name: "upstream error event", upstreamBody: withoutStop + upstreamError, want502: true},
		// A cut upstream error event is not parsed by the client, so the gateway adds its own.
		{name: "connection drops in upstream error event", upstreamBody: withoutStop + "event: error\n", dropMidway: true, wantAdded: messagesStreamPing + "error", want502: true},
		{name: "clean end in upstream error data", upstreamBody: withoutStop + "event: error\ndata: {\"type\":\"err", wantAdded: "\n" + messagesStreamPing + "error", want502: true},
		{name: "connection drops", upstreamBody: withoutStop, dropMidway: true, wantAdded: "error", want502: true},
		{name: "connection drops mid event", upstreamBody: withoutStop + cutEvent, dropMidway: true, wantAdded: "\n" + messagesStreamPing + "error", want502: true},
		{name: "gateway timeout", upstreamBody: withoutStop, stall: true, wantAdded: "error", want502: true},
		{name: "compressed stream drops", encoding: "gzip", upstreamBody: "compressed bytes", dropMidway: true, want502: true},
		{name: "compressed stream ends", encoding: "gzip", upstreamBody: "compressed bytes"},
		{name: "unary connection drops", unary: true, contentType: "application/json", upstreamBody: `{"type":"message","content":[`, dropMidway: true, want502: true},
		{name: "unary complete", unary: true, contentType: "application/json", upstreamBody: `{"type":"message","content":[],"usage":{"input_tokens":7,"output_tokens":5}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				contentType := tt.contentType
				if contentType == "" {
					contentType = "text/event-stream"
				}
				w.Header().Set("Content-Type", contentType)
				if tt.encoding != "" {
					w.Header().Set("Content-Encoding", tt.encoding)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tt.upstreamBody)
				w.(http.Flusher).Flush()
				if tt.stall {
					<-r.Context().Done()
				}
				if tt.dropMidway {
					panic(http.ErrAbortHandler)
				}
			}))
			t.Cleanup(backend.Close)
			cfg := config.Default()
			if tt.stall {
				cfg.Stargate.RequestTimeout = 200 * time.Millisecond
			}
			e, _ := messagesTestAPI(t, backend.URL, cfg, nil, nil)
			handlerErr := captureHandlerError(e)

			body := messagesTestBody
			if !tt.unary {
				body = strings.Replace(body, `"max_tokens":64`, `"stream":true,"max_tokens":64`, 1)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, messagesHTTPRequest(body))
			require.Equal(t, http.StatusOK, rec.Code)

			// The client keeps its 200; logs, metrics and traces record a 502.
			if tt.want502 {
				var httpErr *echo.HTTPError
				require.ErrorAs(t, *handlerErr, &httpErr)
				require.Equal(t, http.StatusBadGateway, httpErr.Code)
			} else {
				require.NoError(t, *handlerErr)
			}

			got := rec.Body.String()
			require.True(t, strings.HasPrefix(got, tt.upstreamBody), "upstream bytes must pass through unchanged")
			added := strings.TrimPrefix(got, tt.upstreamBody)
			wantAdded := tt.wantAdded
			if strings.HasSuffix(wantAdded, "error") {
				requestID := rec.Header().Get(HeaderRequestID)
				require.NotEmpty(t, requestID)
				event, err := json.Marshal(anthropicErrorResponse{
					Type:      "error",
					Error:     anthropicError{Type: "api_error", Message: "upstream stream ended before completion"},
					RequestID: requestID,
				})
				require.NoError(t, err)
				wantAdded = strings.TrimSuffix(wantAdded, "error") + "event: error\ndata: " + string(event) + "\n\n"
			}
			require.Equal(t, wantAdded, added)
		})
	}
}

// A client that leaves is not an upstream failure: no error event, no 502.
func TestMessagesStreamClientDisconnectIsNotAnUpstreamFailure(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(backend.Close)
	e, _ := messagesTestAPI(t, backend.URL, config.Default(), nil, nil)
	// handled wraps the capture, so the captured error is set once handled closes.
	handled := make(chan struct{})
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { defer close(handled); return next(c) }
	})
	handlerErr := captureHandlerError(e)
	gateway := httptest.NewServer(e)
	t.Cleanup(gateway.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := strings.Replace(messagesTestBody, `"max_tokens":64`, `"stream":true,"max_tokens":64`, 1)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/v1/messages", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer caller-token")
	resp, err := gateway.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cancel()
	resp.Body.Close()

	select {
	case <-handled:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return after the client left")
	}
	var httpErr *echo.HTTPError
	if errors.As(*handlerErr, &httpErr) {
		require.NotEqual(t, http.StatusBadGateway, httpErr.Code)
	}
}
