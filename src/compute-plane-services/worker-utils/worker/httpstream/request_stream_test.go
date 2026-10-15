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
	"bufio"
	"context"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRequestStreamHandler_Success(t *testing.T) {
	// Create a mock target server
	mux := http.NewServeMux()

	// Handle GET /v2/nvcf/worker/request-attach
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		// Check authorization
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer request-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Check Accept header
		acceptHeader := r.Header.Get("Accept")
		if acceptHeader == h1ContentType {
			// Return a full HTTP request
			w.Header().Set("Content-Type", h1ContentType)
			w.WriteHeader(http.StatusOK)
			fullRequest := "POST /test/path?query=value HTTP/1.1\r\n" +
				"Host: example.com\r\n" +
				"Content-Type: application/json\r\n" +
				"Content-Length: 15\r\n" +
				"\r\n" +
				`{"test":"data"}`
			w.Write([]byte(fullRequest))
		} else if acceptHeader == "application/octet-stream" {
			// Return just the body
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"test":"data"}`))
		} else {
			http.Error(w, "Unsupported Accept header", http.StatusUnsupportedMediaType)
		}
	})

	// Handle POST /v2/nvcf/worker/request-attach
	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		// Check authorization
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer response-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Check content type
		contentType := r.Header.Get("Content-Type")
		if contentType != h1ContentType {
			http.Error(w, "Unsupported Content-Type", http.StatusUnsupportedMediaType)
			return
		}

		w.WriteHeader(http.StatusOK)
		io.Copy(io.Discard, r.Body)
	})

	targetServer := httptest.NewServer(mux)
	defer targetServer.Close()

	// Create proxy client
	proxyClient := NewProxiedClient()

	// Test with missing request method (should fetch from GET)
	statelessConfig := &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  targetServer.URL,
						ResponseAuthorizationToken: "response-token",
						RequestAuthorizationToken:  lo.ToPtr("request-token"),
					},
				},
			},
		},
	}

	requestDataWritable := &pb.WorkerInvokeFunctionRequest{
		RequestId: "test-request-id",
		// RequestMethod is empty, should be populated from GET request
	}

	handler, err := NewRequestStreamHandler(t.Context(), proxyClient, statelessConfig, requestDataWritable)
	require.NoError(t, err)
	require.NotNil(t, handler)
	defer handler.Close()

	// Verify that request data was populated from GET request
	assert.Equal(t, http.MethodPost, requestDataWritable.RequestMethod)
	assert.Equal(t, "/test/path?query=value", requestDataWritable.RequestPath)
	assert.NotEmpty(t, requestDataWritable.RequestHeaders)
	assert.Contains(t, requestDataWritable.RequestHeaders, &pb.StringKV{Key: "Content-Type", Value: "application/json"})
	assert.Contains(t, requestDataWritable.RequestHeaders, &pb.StringKV{Key: "Content-Length", Value: "15"})

	// Verify we can get the request body
	body, err := handler.GetClientRequestBody()
	require.NoError(t, err)
	defer body.Close()

	bodyData, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, `{"test":"data"}`, string(bodyData))
}

func TestNewRequestStreamHandler_NoRequestAuth(t *testing.T) {
	// Create a mock target server
	mux := http.NewServeMux()

	// Add a catch-all handler that should not be called
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Error("Should not receive any requests when no request auth token is provided")
	})

	targetServer := httptest.NewServer(mux)
	defer targetServer.Close()

	proxyClient := NewProxiedClient()

	statelessConfig := &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  targetServer.URL,
						ResponseAuthorizationToken: "response-token",
						// No RequestAuthorizationToken
					},
				},
			},
		},
	}

	requestDataWritable := &pb.WorkerInvokeFunctionRequest{
		RequestId:     "test-request-id",
		RequestMethod: http.MethodPost,
		RequestPath:   "/test/path",
	}

	ctx := context.Background()
	handler, err := NewRequestStreamHandler(ctx, proxyClient, statelessConfig, requestDataWritable)
	require.NoError(t, err)
	require.NotNil(t, handler)
	defer handler.Close()

	// Should get NoBody
	body, err := handler.GetClientRequestBody()
	require.NoError(t, err)
	assert.Equal(t, http.NoBody, body)
}

func TestNewRequestStreamHandler_WithProxy(t *testing.T) {
	// Create a mock proxy server
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// strip the path
		proxyUrl := &url.URL{
			Scheme: r.URL.Scheme,
			Host:   r.URL.Host,
		}
		httputil.NewSingleHostReverseProxy(proxyUrl).ServeHTTP(w, r)
	}))
	defer proxyServer.Close()
	const expectedResponse = `{"test":"data"}`

	// Create a mock target server
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(expectedResponse))
	})

	// Handle POST /v2/nvcf/worker/request-attach
	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		// Check authorization
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer response-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Check content type
		contentType := r.Header.Get("Content-Type")
		if contentType != h1ContentType {
			http.Error(w, "Unsupported Content-Type", http.StatusUnsupportedMediaType)
			return
		}

		w.WriteHeader(http.StatusOK)
		io.Copy(io.Discard, r.Body)
	})

	targetServer := httptest.NewServer(mux)
	defer targetServer.Close()

	proxyClient := NewProxiedClient()

	statelessConfig := &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  targetServer.URL,
						ProxyURI:                   lo.ToPtr(proxyServer.URL),
						ResponseAuthorizationToken: "response-token",
						RequestAuthorizationToken:  lo.ToPtr("request-token"),
					},
				},
			},
		},
	}

	requestDataWritable := &pb.WorkerInvokeFunctionRequest{
		RequestId:     "test-request-id",
		RequestMethod: http.MethodPost,
		RequestPath:   "/test/path",
	}

	handler, err := NewRequestStreamHandler(t.Context(), proxyClient, statelessConfig, requestDataWritable)
	require.NoError(t, err)
	require.NotNil(t, handler)
	defer handler.Close()

	body, err := handler.GetClientRequestBody()
	require.NoError(t, err)
	defer body.Close()

	bodyData, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, expectedResponse, string(bodyData))

	err = handler.SendResponse(t.Context(), &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"result":"success"}`)),
	}, nil)
	require.NoError(t, err)
}

func TestSendResponse_Success(t *testing.T) {
	var receivedResponse *http.Response
	var receivedBody []byte
	var wg sync.WaitGroup
	wg.Add(1)

	// Create a mock target server
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		// Check authorization
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer response-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Check content type
		contentType := r.Header.Get("Content-Type")
		if contentType != h1ContentType {
			http.Error(w, "Unsupported Content-Type", http.StatusUnsupportedMediaType)
			return
		}

		// Read the response
		resp, err := http.ReadResponse(bufio.NewReader(r.Body), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Body.Close()

		receivedResponse = resp
		receivedBody = body

		w.WriteHeader(http.StatusOK)
		wg.Done()
	})

	targetServer := httptest.NewServer(mux)
	defer targetServer.Close()

	proxyClient := NewProxiedClient()

	statelessConfig := &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  targetServer.URL,
						ResponseAuthorizationToken: "response-token",
					},
				},
			},
		},
	}

	requestDataWritable := &pb.WorkerInvokeFunctionRequest{
		RequestId:     "test-request-id",
		RequestMethod: http.MethodPost,
		RequestPath:   "/test/path",
	}

	handler, err := NewRequestStreamHandler(t.Context(), proxyClient, statelessConfig, requestDataWritable)
	require.NoError(t, err)
	defer handler.Close()

	// Create a test response
	body := `{"result":"success"}`
	testResponse := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Type":  []string{"application/json"},
			"Custom-Header": []string{"custom-value"},
			"Connection":    []string{"keep-alive"}, // This should be removed
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}

	// Send the response
	var onFinishWriteCalled bool
	err = handler.SendResponse(t.Context(), testResponse, func() {
		onFinishWriteCalled = true
	})
	require.NoError(t, err)

	// Wait for the response to be received
	wg.Wait()

	// Verify the received response
	require.NotNil(t, receivedResponse)
	assert.Equal(t, 200, receivedResponse.StatusCode)
	assert.Equal(t, "application/json", receivedResponse.Header.Get("Content-Type"))
	assert.Equal(t, "custom-value", receivedResponse.Header.Get("Custom-Header"))
	assert.Empty(t, receivedResponse.Header.Get("Connection")) // Should be removed
	assert.Equal(t, body, string(receivedBody))
	assert.True(t, onFinishWriteCalled)
}

func TestSendResponse_SuccessKnownContentLength(t *testing.T) {
	var receivedResponse *http.Response
	var receivedBody []byte
	var wg sync.WaitGroup
	wg.Add(1)

	// Create a mock target server
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		// Check authorization
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer response-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Check content type
		contentType := r.Header.Get("Content-Type")
		if contentType != h1ContentType {
			http.Error(w, "Unsupported Content-Type", http.StatusUnsupportedMediaType)
			return
		}

		// Check content length of overall request, this is separate from the parsed response in the body
		if r.ContentLength <= 0 {
			http.Error(w, "Content-Length must be known", http.StatusBadRequest)
			return
		}

		// Read the response
		resp, err := http.ReadResponse(bufio.NewReader(r.Body), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Body.Close()

		receivedResponse = resp
		receivedBody = body

		w.WriteHeader(http.StatusOK)
		wg.Done()
	})

	targetServer := httptest.NewServer(mux)
	defer targetServer.Close()

	proxyClient := NewProxiedClient()

	statelessConfig := &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  targetServer.URL,
						ResponseAuthorizationToken: "response-token",
					},
				},
			},
		},
	}

	requestDataWritable := &pb.WorkerInvokeFunctionRequest{
		RequestId:     "test-request-id",
		RequestMethod: http.MethodPost,
		RequestPath:   "/test/path",
	}

	handler, err := NewRequestStreamHandler(t.Context(), proxyClient, statelessConfig, requestDataWritable)
	require.NoError(t, err)
	defer handler.Close()

	// Create a test response
	body := `{"result":"success"}`
	testResponse := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Custom-Header":  []string{"custom-value"},
			"Connection":     []string{"keep-alive"}, // This should be removed
			"Content-Length": []string{strconv.Itoa(len(body))},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}

	// Send the response
	var onFinishWriteCalled bool
	err = handler.SendResponse(t.Context(), testResponse, func() {
		onFinishWriteCalled = true
	})
	require.NoError(t, err)

	// Wait for the response to be received
	wg.Wait()

	// Verify the received response
	require.NotNil(t, receivedResponse)
	assert.Equal(t, 200, receivedResponse.StatusCode)
	assert.Equal(t, "application/json", receivedResponse.Header.Get("Content-Type"))
	assert.Equal(t, "custom-value", receivedResponse.Header.Get("Custom-Header"))
	assert.Equal(t, receivedResponse.ContentLength, int64(len(body)))
	assert.Equal(t, receivedResponse.Header.Get("Content-Length"), strconv.Itoa(len(body)))
	assert.Empty(t, receivedResponse.Header.Get("Connection")) // Should be removed
	assert.Equal(t, body, string(receivedBody))
	assert.True(t, onFinishWriteCalled)
}

func TestSendResponse_MultipleCalls(t *testing.T) {
	// Create a mock target server
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.Copy(io.Discard, r.Body)
	})

	targetServer := httptest.NewServer(mux)
	defer targetServer.Close()

	proxyClient := NewProxiedClient()

	statelessConfig := &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  targetServer.URL,
						ResponseAuthorizationToken: "response-token",
					},
				},
			},
		},
	}

	requestDataWritable := &pb.WorkerInvokeFunctionRequest{
		RequestId:     "test-request-id",
		RequestMethod: http.MethodPost,
		RequestPath:   "/test/path",
	}

	handler, err := NewRequestStreamHandler(t.Context(), proxyClient, statelessConfig, requestDataWritable)
	require.NoError(t, err)
	defer handler.Close()

	// Create a test response
	testResponse := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"result":"success"}`)),
	}

	// First call should succeed
	err = handler.SendResponse(t.Context(), testResponse, nil)
	require.NoError(t, err)

	// Second call should return error
	testResponse2 := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"result":"success2"}`)),
	}
	err = handler.SendResponse(t.Context(), testResponse2, nil)
	assert.Equal(t, errAlreadySent, err)
}

//func TestSendResponse_Success_Real(t *testing.T) {
//	proxyClient := NewProxiedClient()
//
//	statelessConfig := &pb.WorkerInvokeFunctionRequest_StatelessConfig{
//		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
//			{
//				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
//					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
//						TargetURI:                  "http://4.tcp.us-cal-1.ngrok.io:19440",
//						ResponseAuthorizationToken: "response-token",
//					},
//				},
//			},
//		},
//	}
//
//	requestDataWritable := &pb.WorkerInvokeFunctionRequest{
//		RequestId:     "test-request-id",
//		RequestMethod: http.MethodPost,
//		RequestPath:   "/test/path",
//	}
//
//	handler, err := NewRequestStreamHandler(t.Context(), proxyClient, statelessConfig, requestDataWritable)
//	require.NoError(t, err)
//	defer handler.Close()
//
//	pr, pw := io.Pipe()
//	go func() {
//		defer pw.Close()
//		io.Copy(pw, strings.NewReader(strings.Repeat(`{"result":"success"}`, 100)))
//	}()
//
//	// Create a test response
//	testResponse := &http.Response{
//		StatusCode: 200,
//		Header: http.Header{
//			"Content-Type":  []string{"application/json"},
//			"Custom-Header": []string{"custom-value"},
//			"Connection":    []string{"keep-alive"}, // This should be removed
//		},
//		Body: pr,
//	}
//
//	// Send the response
//	var onFinishWriteCalled bool
//	err = handler.SendResponse(t.Context(), testResponse, func() {
//		onFinishWriteCalled = true
//	})
//	require.NoError(t, err)
//
//	// Verify the received response
//	assert.True(t, onFinishWriteCalled)
//}
//
//func TestSendResponse_Success_Real_Proxy(t *testing.T) {
//	proxyClient := NewProxiedClient()
//
//	statelessConfig := &pb.WorkerInvokeFunctionRequest_StatelessConfig{
//		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
//			{
//				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
//					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
//						ProxyURI:                   lo.ToPtr("http://us-west-2.tcp-proxy.stg.grpc.nvcf.nvidia.com:443"),
//						TargetURI:                  "http://10-32-147-43.nvcf-invocation-service.astro-tenant-nvcf-invocation-service.svc.cluster.local:8080",
//						ResponseAuthorizationToken: "response-token",
//					},
//				},
//			},
//		},
//	}
//
//	requestDataWritable := &pb.WorkerInvokeFunctionRequest{
//		RequestId:     "test-request-id",
//		RequestMethod: http.MethodPost,
//		RequestPath:   "/test/path",
//	}
//
//	handler, err := NewRequestStreamHandler(t.Context(), proxyClient, statelessConfig, requestDataWritable)
//	require.NoError(t, err)
//	defer handler.Close()
//
//	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//		io.Copy(w, strings.NewReader(strings.Repeat(`{"result":"success"}`, 100)))
//		t.Log("finished writing request")
//	}))
//	defer s.Close()
//
//	resp, err := http.Get(s.URL)
//	require.NoError(t, err)
//
//	// Send the response
//	var onFinishWriteCalled bool
//	err = handler.SendResponse(t.Context(), resp, func() {
//		onFinishWriteCalled = true
//	})
//	require.NoError(t, err)
//
//	// Verify the received response
//	assert.True(t, onFinishWriteCalled)
//}
