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

package httpstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProxiedClient_NotNil(t *testing.T) {
	c := NewProxiedClient()
	require.NotNil(t, c)
}

// TestNewProxiedClient_ProxyResolution exercises the three branches of the
// Proxy callback installed by NewProxiedClient by sending real requests
// through the client to a target server, varying the proxy context value.
func TestNewProxiedClient_ProxyResolution(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	client := (*http.Client)(NewProxiedClient())

	// No proxy in context: Proxy returns (nil, nil) and request hits target directly.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target.URL, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Wrong type in context: Proxy returns an error, surfaced by the transport.
	badCtx := context.WithValue(context.Background(), proxyURLKey, "not-a-url")
	badReq, err := http.NewRequestWithContext(badCtx, http.MethodGet, target.URL, nil)
	require.NoError(t, err)
	_, err = client.Do(badReq)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxyURL is not a *url.URL")
}

func baseConfig(targetURI string) *pb.WorkerInvokeFunctionRequest_StatelessConfig {
	return &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  targetURI,
						ResponseAuthorizationToken: "response-token",
					},
				},
			},
		},
	}
}

func TestNewRequestStreamHandler_NilConfig(t *testing.T) {
	_, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), nil, &pb.WorkerInvokeFunctionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or empty StatelessConfig")
}

func TestNewRequestStreamHandler_EmptyConnectionConfigs(t *testing.T) {
	cfg := &pb.WorkerInvokeFunctionRequest_StatelessConfig{}
	_, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), cfg, &pb.WorkerInvokeFunctionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or empty StatelessConfig")
}

func TestNewRequestStreamHandler_NoHTTP1Config(t *testing.T) {
	// A connection config that has no Http1Config set leaves TargetURI empty.
	cfg := &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{}, // no inner config
		},
	}
	_, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), cfg, &pb.WorkerInvokeFunctionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no HTTP1ProtocolConfig found")
}

func TestNewRequestStreamHandler_BadProxyURI(t *testing.T) {
	cfg := baseConfig("http://example.invalid")
	cfg.ConnectionConfigs[0].GetHttp1Config().ProxyURI = lo.ToPtr("://bad-proxy")
	_, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), cfg,
		&pb.WorkerInvokeFunctionRequest{RequestMethod: http.MethodPost})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse proxy URI")
}

func TestNewRequestStreamHandler_GetRequestNon200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := baseConfig(srv.URL)
	cfg.ConnectionConfigs[0].GetHttp1Config().RequestAuthorizationToken = lo.ToPtr("request-token")

	// RequestMethod set so the body fetch is lazy; force it by calling GetClientRequestBody.
	handler, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), cfg,
		&pb.WorkerInvokeFunctionRequest{RequestMethod: http.MethodPost})
	require.NoError(t, err)
	defer handler.Close()

	_, err = handler.GetClientRequestBody()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GET request failed with status code 500")
}

func TestNewRequestStreamHandler_GetConnRefused(t *testing.T) {
	// Point at a closed server so Do() returns a transport error.
	srv := httptest.NewServer(http.NotFoundHandler())
	closedURL := srv.URL
	srv.Close()

	cfg := baseConfig(closedURL)
	cfg.ConnectionConfigs[0].GetHttp1Config().RequestAuthorizationToken = lo.ToPtr("request-token")

	handler, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), cfg,
		&pb.WorkerInvokeFunctionRequest{RequestMethod: http.MethodPost})
	require.NoError(t, err)
	defer handler.Close()

	_, err = handler.GetClientRequestBody()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to send GET request")
}

func TestNewRequestStreamHandler_ReadRequestError(t *testing.T) {
	// RequestMethod empty triggers the full-request parse path. Return garbage
	// so http.ReadRequest fails.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", h1ContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not a valid http request line\r\n\r\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := baseConfig(srv.URL)
	cfg.ConnectionConfigs[0].GetHttp1Config().RequestAuthorizationToken = lo.ToPtr("request-token")

	_, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), cfg,
		&pb.WorkerInvokeFunctionRequest{}) // empty method
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read client request")
}

func TestNewRequestStreamHandler_UnsupportedTransferEncoding(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", h1ContentType)
		w.WriteHeader(http.StatusOK)
		full := "POST /p HTTP/1.1\r\n" +
			"Host: example.com\r\n" +
			"Transfer-Encoding: chunked\r\n" +
			"\r\n" +
			"5\r\nhello\r\n0\r\n\r\n"
		_, _ = w.Write([]byte(full))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := baseConfig(srv.URL)
	cfg.ConnectionConfigs[0].GetHttp1Config().RequestAuthorizationToken = lo.ToPtr("request-token")

	_, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), cfg,
		&pb.WorkerInvokeFunctionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected transfer encoding")
}

func TestNewRequestStreamHandler_GetFailDuringFullRequest(t *testing.T) {
	// Empty method forces the GET to be awaited inside the constructor. A non-200
	// from the GET should surface as a constructor error.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := baseConfig(srv.URL)
	cfg.ConnectionConfigs[0].GetHttp1Config().RequestAuthorizationToken = lo.ToPtr("request-token")

	_, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), cfg,
		&pb.WorkerInvokeFunctionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get client request")
}

func TestSendResponse_PostNon200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "rejected", http.StatusBadGateway)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	handler, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), baseConfig(srv.URL),
		&pb.WorkerInvokeFunctionRequest{RequestMethod: http.MethodPost, RequestPath: "/p"})
	require.NoError(t, err)
	defer handler.Close()

	err = handler.SendResponse(t.Context(), &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader("body")),
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "POST request failed with status code 502")
}

func TestSendResponse_PostConnRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	closedURL := srv.URL
	srv.Close()

	handler, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), baseConfig(closedURL),
		&pb.WorkerInvokeFunctionRequest{RequestMethod: http.MethodPost, RequestPath: "/p"})
	require.NoError(t, err)
	defer handler.Close()

	err = handler.SendResponse(t.Context(), &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("body")),
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to send POST request")
}

func TestHttpStatusText(t *testing.T) {
	// Empty Status, known code -> uses http.StatusText.
	assert.Equal(t, "Not Found", httpStatusText(&http.Response{StatusCode: 404}))
	// Empty Status, unknown code -> "status code N".
	assert.Equal(t, "status code 799", httpStatusText(&http.Response{StatusCode: 799}))
	// Non-empty Status with stutter prefix gets trimmed.
	assert.Equal(t, "OK", httpStatusText(&http.Response{StatusCode: 200, Status: "200 OK"}))
	// Non-empty Status without prefix is left as-is.
	assert.Equal(t, "Custom Reason", httpStatusText(&http.Response{StatusCode: 200, Status: "Custom Reason"}))
}

func TestGetContentTrueLength(t *testing.T) {
	// Non-zero ContentLength is returned verbatim.
	assert.Equal(t, int64(42), getContentTrueLength(&http.Response{ContentLength: 42}))
	// Zero ContentLength + nil body -> 0.
	assert.Equal(t, int64(0), getContentTrueLength(&http.Response{ContentLength: 0, Body: nil}))
	// Zero ContentLength + NoBody -> 0.
	assert.Equal(t, int64(0), getContentTrueLength(&http.Response{ContentLength: 0, Body: http.NoBody}))
	// Zero ContentLength + real body + valid Content-Length header.
	assert.Equal(t, int64(7), getContentTrueLength(&http.Response{
		ContentLength: 0,
		Body:          io.NopCloser(strings.NewReader("abcdefg")),
		Header:        http.Header{"Content-Length": []string{"7"}},
	}))
	// Zero ContentLength + real body + unparseable Content-Length header -> -1.
	assert.Equal(t, int64(-1), getContentTrueLength(&http.Response{
		ContentLength: 0,
		Body:          io.NopCloser(strings.NewReader("abc")),
		Header:        http.Header{"Content-Length": []string{"not-a-number"}},
	}))
	// Zero ContentLength + real body + no Content-Length header -> -1.
	assert.Equal(t, int64(-1), getContentTrueLength(&http.Response{
		ContentLength: 0,
		Body:          io.NopCloser(strings.NewReader("abc")),
		Header:        http.Header{},
	}))
}

func TestClose_CancelsAndClosesBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("client-body"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := baseConfig(srv.URL)
	cfg.ConnectionConfigs[0].GetHttp1Config().RequestAuthorizationToken = lo.ToPtr("request-token")

	handler, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), cfg,
		&pb.WorkerInvokeFunctionRequest{RequestMethod: http.MethodPost})
	require.NoError(t, err)

	// Materialize the body so cancelGetClientResponseBody is set and Close hits
	// the body.Close() path.
	body, err := handler.GetClientRequestBody()
	require.NoError(t, err)
	require.NotNil(t, body)

	require.NoError(t, handler.Close())
}

func TestClose_NoBody(t *testing.T) {
	// No request auth token: getClientRequestBody returns http.NoBody and
	// cancelGetClientResponseBody is nil, exercising the nil-cancel path.
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	handler, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), baseConfig(srv.URL),
		&pb.WorkerInvokeFunctionRequest{RequestMethod: http.MethodPost, RequestPath: "/p"})
	require.NoError(t, err)
	require.NoError(t, handler.Close())
}
