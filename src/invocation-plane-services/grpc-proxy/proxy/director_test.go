/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"nvcf-grpc-proxy/proxy/consts"
	"nvcf-grpc-proxy/proxy/internal/echo"
	"nvcf-grpc-proxy/proxy/internal/test"
	"nvcf-grpc-proxy/proxy/invocation"
	"nvcf-grpc-proxy/proxy/worker"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/samber/lo"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const (
	// API always returns a non-empty function ID and version ID
	testFunctionId        = "14760dc4-3b39-43b1-83f6-ed16b1c05cc5"
	testFunctionVersionId = "0f230d58-275f-444e-b30e-9c363a076702"
)

type mockInvoker invocation.Result

func (m *mockInvoker) InvokeStatefulFunction(_ context.Context, _ net.Conn, _, _, _ string, _ *uuid.UUID, onWorkerAuthSet func(workerAuthToken string, requestId uuid.UUID, apiFunctionId string, apiFunctionVersionId string)) (invocation.Result, context.CancelFunc, error) {
	result := (invocation.Result)(*m)
	onWorkerAuthSet(result.WorkerAuthorizationToken, result.RequestId, testFunctionId, testFunctionVersionId)
	return result, func() {}, nil
}

func TestStreamDirector(t *testing.T) {
	proxyResponse := &invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "123abc",
	}
	director := NewStreamDirector((*mockInvoker)(proxyResponse))
	defer func() {
		err := director.Close()
		if err != nil {
			t.Fatalf("failed to close director %s", err)
		}
	}()
	// this is our own address, not a mock nvcf, but it doesn't matter
	healthManager, err := healthManager("http://localhost:10081", nil)
	if err != nil {
		t.Fatalf("failed to create dummy health manager")
	}
	http2Server := createHttp2Server(director, "0.0.0.0:10081", healthManager)
	listenAndServe := lo.Async(http2Server.ListenAndServe)
	defer func() {
		err := <-listenAndServe
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("failed to listen and serve %s", err)
		}
	}()
	t.Log("started proxy h2c listener")

	inferenceContainer := grpc.NewServer()
	inferenceContainer.RegisterService(&echo.Echo_ServiceDesc, &echo.Server{})
	listener, err := net.Listen("tcp", "0.0.0.0:8001")
	if err != nil {
		t.Fatalf("failed to listen as local grpc server %s", err)
	}
	grpcServe := lo.Async(func() error {
		return inferenceContainer.Serve(listener)
	})
	defer func() {
		err := <-grpcServe
		if err != nil {
			t.Fatalf("failed to serve grpc %s", err)
		}
	}()
	t.Log("started grpc inference server")

	dial, err := net.Dial("tcp", "localhost:8001")
	if err != nil {
		t.Fatalf("failed to connect to local grpc server %s", err)
	}
	err = director.RegisterWorker(proxyResponse.RequestId, proxyResponse.WorkerAuthorizationToken, testFunctionId, testFunctionVersionId, dial)
	if err != nil {
		t.Fatalf("failed to register worker %s", err)
	}
	t.Logf("registered connection to inference server with director")

	clientConn, err := grpc.Dial("localhost:10081", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to dial grpc client connection %s", err)
	}
	defer func() {
		err := clientConn.Close()
		if err != nil {
			t.Fatalf("failed to close grpc client %s", err)
		}
	}()
	client := echo.NewEchoClient(clientConn)
	ctx := metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer asdf",
		"function-id", testFunctionId,
	)
	t.Logf("connected grpc client to h2c proxy")

	group, ctx := errgroup.WithContext(ctx)
	request := &echo.EchoRequest{Message: "echo"}
	for i := 0; i < 100; i++ {
		i := i
		group.Go(func() error {
			t.Logf("started unary echo #%d", i)
			defer t.Logf("finished unary echo #%d", i)
			message, err := client.EchoMessage(ctx, request)
			if err != nil {
				return err
			}
			if message.Message != "echo" {
				t.Fatal("incorrect echo response")
			}
			return nil
		})
		group.Go(func() error {
			t.Logf("started bidirectional echo #%d", i)
			defer t.Logf("finished bidirectional echo #%d", i)
			stream, err := client.EchoMessageStreaming(ctx)
			if err != nil {
				return err
			}
			for i := 0; i < 10; i++ {
				err = stream.Send(request)
				if err != nil {
					return err
				}
				message, err := stream.Recv()
				if err != nil {
					return err
				}
				if message.Message != "echo" {
					t.Fatal("incorrect echo response")
				}
			}
			err = stream.CloseSend()
			if err != nil {
				return err
			}
			return nil
		})
	}

	err = group.Wait()
	if err != nil {
		t.Fatalf("failed during client calls %s", err)
	}

	t.Logf("finished, shutting down")
	inferenceContainer.GracefulStop()
	_ = http2Server.Close()
}

func TestUnauthenticatedError(t *testing.T) {
	proxyResponse := &invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "123abc",
	}
	selfFqdn := "http://localhost:10081"
	director := NewStreamDirector((*mockInvoker)(proxyResponse))
	defer func() {
		err := director.Close()
		if err != nil {
			t.Fatalf("failed to close director %s", err)
		}
	}()
	// this is our own address, not a mock nvcf, but it doesn't matter
	healthManager, err := healthManager("http://localhost:10081", nil)
	if err != nil {
		t.Fatalf("failed to create dummy health manager")
	}
	http2Server := createHttp2Server(director, "0.0.0.0:10081", healthManager)
	listenAndServe := lo.Async(http2Server.ListenAndServe)
	defer func() {
		err := <-listenAndServe
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("failed to listen and serve %s", err)
		}
	}()
	defer http2Server.Close()
	t.Log("started proxy h2c listener")

	t.Log("performing http test")
	request, _ := http.NewRequest(http.MethodPost, selfFqdn+"/asdf", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Error(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fail()
	}

	t.Log("performing grpc test")
	request, _ = http.NewRequest(http.MethodPost, selfFqdn+"/asdf", nil)
	request.Header.Set("Content-Type", "application/grpc")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Error(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fail()
	}
	if response.Header.Get("grpc-status") != strconv.Itoa(int(codes.Unauthenticated)) {
		t.Fail()
	}

	t.Log("finished tests")
}

func TestGatewayTimeoutError(t *testing.T) {
	timeout := consts.Timeout
	consts.Timeout = 100 * time.Millisecond
	t.Cleanup(func() {
		consts.Timeout = timeout
	})
	proxyResponse := &invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "123abc",
	}
	director := NewStreamDirector((*mockInvoker)(proxyResponse))
	defer func() {
		err := director.Close()
		if err != nil {
			t.Fatalf("failed to close director %s", err)
		}
	}()
	// this is our own address, not a mock nvcf, but it doesn't matter
	healthManager, err := healthManager("http://localhost:10081", nil)
	if err != nil {
		t.Fatalf("failed to create dummy health manager")
	}
	http2Server := createHttp2Server(director, "0.0.0.0:10081", healthManager)
	listenAndServe := lo.Async(http2Server.ListenAndServe)
	defer func() {
		err := <-listenAndServe
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("failed to listen and serve %s", err)
		}
	}()
	defer http2Server.Close()
	t.Log("started proxy h2c listener")

	selfFqdn := "http://localhost:10081"

	t.Run("http", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodPost, selfFqdn+"/asdf", nil)
		request.Header.Set("authorization", "Bearer asdf")
		request.Header.Set("function-id", "1234")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusGatewayTimeout {
			t.Fatal("wrong status", response.StatusCode)
		}
	})
	t.Run("grpc", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodPost, selfFqdn+"/asdf", nil)
		request.Header.Set("authorization", "Bearer asdf")
		request.Header.Set("function-id", "1234")
		request.Header.Set("Content-Type", "application/grpc")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatal("wrong status", response.StatusCode)
		}
		if response.Header.Get("grpc-status") != strconv.Itoa(int(codes.DeadlineExceeded)) {
			t.Fatal("wrong grpc-status", response.Header.Get("grpc-status"))
		}
	})
}

func TestRequestIdHeader(t *testing.T) {
	proxyResponse := &invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "123abc",
	}
	director := NewStreamDirector((*mockInvoker)(proxyResponse))
	defer func() {
		err := director.Close()
		if err != nil {
			t.Fatalf("failed to close director %s", err)
		}
	}()
	// this is our own address, not a mock nvcf, but it doesn't matter
	healthManager, err := healthManager("http://localhost:10081", nil)
	if err != nil {
		t.Fatalf("failed to create dummy health manager")
	}
	http2Server := createHttp2Server(director, "0.0.0.0:10081", healthManager)
	listenAndServe := lo.Async(http2Server.ListenAndServe)
	defer func() {
		err := <-listenAndServe
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("failed to listen and serve %s", err)
		}
	}()
	defer http2Server.Close()
	t.Log("started proxy h2c listener")

	inferenceContainer, err := test.NewHttpServer("0.0.0.0:8001", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get(consts.RequestIdHeaderName) != proxyResponse.RequestId.String() {
			t.Log("mismatched request id")
			t.Fail()
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusOK)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer inferenceContainer.Close()
	t.Log("started http inference server")

	dial, err := net.Dial("tcp", "localhost:8001")
	if err != nil {
		t.Fatalf("failed to connect to local http server %s", err)
	}
	err = director.RegisterWorker(proxyResponse.RequestId, proxyResponse.WorkerAuthorizationToken, testFunctionId, testFunctionVersionId, dial)
	if err != nil {
		t.Fatalf("failed to register worker %s", err)
	}
	t.Logf("registered connection to inference server with director")

	request, _ := http.NewRequest(http.MethodPost, "http://localhost:10081/asdf", nil)
	request.Header.Set("authorization", "Bearer asdf")
	request.Header.Set("function-id", testFunctionId)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("wrong status", response.StatusCode)
	}
	if response.Header.Get(consts.RequestIdHeaderName) != proxyResponse.RequestId.String() {
		t.Fatal("wrong request id", response.Header.Get(consts.RequestIdHeaderName))
	}
	t.Logf("finished, shutting down")
}

func TestRequestIdCookie(t *testing.T) {
	proxyResponse := &invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "123abc",
	}
	director := NewStreamDirector((*mockInvoker)(proxyResponse))
	defer func() {
		err := director.Close()
		if err != nil {
			t.Fatalf("failed to close director %s", err)
		}
	}()
	// this is our own address, not a mock nvcf, but it doesn't matter
	healthManager, err := healthManager("http://localhost:10081", nil)
	if err != nil {
		t.Fatalf("failed to create dummy health manager")
	}
	http2Server := createHttp2Server(director, "0.0.0.0:10081", healthManager)
	listenAndServe := lo.Async(http2Server.ListenAndServe)
	defer func() {
		err := <-listenAndServe
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("failed to listen and serve %s", err)
		}
	}()
	defer http2Server.Close()
	t.Log("started proxy h2c listener")

	inferenceContainer, err := test.NewHttpServer("0.0.0.0:8001", func(writer http.ResponseWriter, request *http.Request) {
		cookie := (&http.Cookie{
			Name:  "my-cookie",
			Value: "my-value",
		}).String()
		writer.Header().Add("Set-Cookie", cookie)
		writer.WriteHeader(http.StatusOK)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer inferenceContainer.Close()
	t.Log("started http inference server")

	dial, err := net.Dial("tcp", "localhost:8001")
	if err != nil {
		t.Fatalf("failed to connect to local http server %s", err)
	}
	err = director.RegisterWorker(proxyResponse.RequestId, proxyResponse.WorkerAuthorizationToken, testFunctionId, testFunctionVersionId, dial)
	if err != nil {
		t.Fatalf("failed to register worker %s", err)
	}
	t.Logf("registered connection to inference server with director")

	request, _ := http.NewRequest(http.MethodPost, "http://localhost:10081/asdf", nil)
	request.Header.Set("authorization", "Bearer asdf")
	request.Header.Set("function-id", testFunctionId)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("wrong status", response.StatusCode)
	}
	// cookies sent by the inference server should be preserved
	// expecting my-cookie, and nvcf-request-id as a cookie
	if len(response.Header.Values("Set-Cookie")) != 2 {
		t.Fatal("wrong cookie", response.Header.Values("Set-Cookie"))
	}
	if response.Header.Values("Set-Cookie")[0] != "my-cookie=my-value" {
		t.Fatal("wrong cookie", response.Header.Values("Set-Cookie")[0])
	}
	if response.Header.Values("Set-Cookie")[1] != "nvcf-request-id="+proxyResponse.RequestId.String() {
		t.Fatal("wrong cookie", response.Header.Values("Set-Cookie")[1])
	}
	if response.Header.Get(consts.RequestIdHeaderName) != proxyResponse.RequestId.String() {
		t.Fatal("wrong request id", response.Header.Get(consts.RequestIdHeaderName))
	}
	t.Logf("finished, shutting down")
}

func TestGetRequestIdFromHeaderOrCookie(t *testing.T) {
	validUUID := uuid.New()
	validUUIDString := validUUID.String()

	tests := []struct {
		name           string
		setupRequest   func() *http.Request
		expectedResult *uuid.UUID
		description    string
	}{
		{
			name: "request_id_from_header",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set(consts.RequestIdHeaderName, validUUIDString)
				return req
			},
			expectedResult: &validUUID,
			description:    "should return request ID from header when present",
		},
		{
			name: "request_id_from_cookie",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{
					Name:  consts.RequestIdCookieName,
					Value: validUUIDString,
				})
				return req
			},
			expectedResult: &validUUID,
			description:    "should return request ID from cookie when header is not present",
		},
		{
			name: "request_id_from_websocket_protocol",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+"."+validUUIDString)
				return req
			},
			expectedResult: &validUUID,
			description:    "should return request ID from WebSocket protocol header when header and cookie are not present",
		},
		{
			name: "request_id_from_websocket_protocol_with_spaces",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+". "+validUUIDString+" ")
				return req
			},
			expectedResult: &validUUID,
			description:    "should handle spaces in WebSocket protocol header value",
		},
		{
			name: "request_id_from_websocket_protocol_unquoted",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+"."+validUUIDString)
				return req
			},
			expectedResult: &validUUID,
			description:    "should handle UUID in dot-separated format",
		},
		{
			name: "request_id_from_multiple_websocket_protocols",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Add("Sec-WebSocket-Protocol", "other-protocol.value")
				req.Header.Add("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+"."+validUUIDString)
				req.Header.Add("Sec-WebSocket-Protocol", "another-protocol.value")
				return req
			},
			expectedResult: &validUUID,
			description:    "should find request ID from multiple WebSocket protocols",
		},
		{
			name: "header_takes_precedence_over_websocket",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set(consts.RequestIdHeaderName, validUUIDString)
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+"."+uuid.New().String())
				return req
			},
			expectedResult: &validUUID,
			description:    "should prioritize header over WebSocket protocol",
		},
		{
			name: "header_takes_precedence_over_all",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set(consts.RequestIdHeaderName, validUUIDString)
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+"."+uuid.New().String())
				req.AddCookie(&http.Cookie{
					Name:  consts.RequestIdCookieName,
					Value: uuid.New().String(),
				})
				return req
			},
			expectedResult: &validUUID,
			description:    "should prioritize header over both WebSocket protocol and cookie",
		},
		{
			name: "websocket_takes_precedence_over_cookie",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+"."+validUUIDString)
				req.AddCookie(&http.Cookie{
					Name:  consts.RequestIdCookieName,
					Value: uuid.New().String(), // different UUID
				})
				return req
			},
			expectedResult: &validUUID,
			description:    "should prioritize WebSocket protocol over cookie",
		},
		{
			name: "empty_header_falls_back_to_websocket",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set(consts.RequestIdHeaderName, "")
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+"."+validUUIDString)
				return req
			},
			expectedResult: &validUUID,
			description:    "should fall back to WebSocket protocol when header is empty",
		},
		{
			name: "empty_websocket_falls_back_to_cookie",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+".")
				req.AddCookie(&http.Cookie{
					Name:  consts.RequestIdCookieName,
					Value: validUUIDString,
				})
				return req
			},
			expectedResult: &validUUID,
			description:    "should fall back to cookie when WebSocket protocol is empty",
		},
		{
			name: "invalid_uuid_in_header",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set(consts.RequestIdHeaderName, "invalid-uuid")
				return req
			},
			expectedResult: nil,
			description:    "should return nil when header contains invalid UUID",
		},
		{
			name: "invalid_uuid_in_cookie",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{
					Name:  consts.RequestIdCookieName,
					Value: "invalid-uuid",
				})
				return req
			},
			expectedResult: nil,
			description:    "should return nil when cookie contains invalid UUID",
		},
		{
			name: "invalid_uuid_in_websocket_protocol",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+".invalid-uuid")
				return req
			},
			expectedResult: nil,
			description:    "should return nil when WebSocket protocol contains invalid UUID",
		},
		{
			name: "invalid_websocket_no_fallback",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+".invalid-uuid")
				req.AddCookie(&http.Cookie{
					Name:  consts.RequestIdCookieName,
					Value: validUUIDString,
				})
				return req
			},
			expectedResult: nil,
			description:    "should return nil when WebSocket protocol contains invalid UUID (no fallback)",
		},
		{
			name: "no_request_id_anywhere",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				return req
			},
			expectedResult: nil,
			description:    "should return nil when no request ID is provided anywhere",
		},
		{
			name: "cookie_not_found",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{
					Name:  "other-cookie",
					Value: validUUIDString,
				})
				return req
			},
			expectedResult: nil,
			description:    "should return nil when the specific cookie is not found",
		},
		{
			name: "websocket_protocol_without_prefix",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set("Sec-WebSocket-Protocol", "other-protocol.value")
				return req
			},
			expectedResult: nil,
			description:    "should return nil when WebSocket protocol doesn't contain the request ID prefix",
		},
		{
			name: "websocket_protocol_with_partial_prefix",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set("Sec-WebSocket-Protocol", `nvcf-req.`+validUUIDString)
				return req
			},
			expectedResult: nil,
			description:    "should return nil when WebSocket protocol has partial prefix match",
		},
		{
			name: "all_sources_empty",
			setupRequest: func() *http.Request {
				req, _ := http.NewRequest("GET", "/", nil)
				req.Header.Set(consts.RequestIdHeaderName, "")
				req.AddCookie(&http.Cookie{
					Name:  consts.RequestIdCookieName,
					Value: "",
				})
				req.Header.Set("Sec-WebSocket-Protocol", consts.RequestIdHeaderName+".")
				return req
			},
			expectedResult: nil,
			description:    "should return nil when all sources are empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setupRequest()
			wsProtocolHeaders := parseWebSocketProtocol(req.Header)
			result := getRequestIdFromHeaderOrCookie(req, wsProtocolHeaders)

			if tt.expectedResult == nil {
				if result != nil {
					t.Errorf("Expected nil, got %v", result)
				}
			} else {
				if result == nil {
					t.Errorf("Expected %v, got nil", tt.expectedResult)
				} else if *result != *tt.expectedResult {
					t.Errorf("Expected %v, got %v", tt.expectedResult, result)
				}
			}
		})
	}
}

func TestStreamDirectorWebSocket(t *testing.T) {
	proxyResponse := &invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "123abc",
	}
	director := NewStreamDirector((*mockInvoker)(proxyResponse))
	defer func() {
		err := director.Close()
		if err != nil {
			t.Fatalf("failed to close director %s", err)
		}
	}()
	// this is our own address, not a mock nvcf, but it doesn't matter
	healthManager, err := healthManager("http://localhost:10082", nil)
	if err != nil {
		t.Fatalf("failed to create dummy health manager")
	}
	http2Server := createHttp2Server(director, "0.0.0.0:10082", healthManager)
	listenAndServe := lo.Async(http2Server.ListenAndServe)
	defer func() {
		err := <-listenAndServe
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("failed to listen and serve %s", err)
		}
	}()
	defer http2Server.Close()
	t.Log("started proxy h2c listener")

	// Create a WebSocket server as the inference backend
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow connections from any origin for testing
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// Check request ID header
		if r.Header.Get(consts.RequestIdHeaderName) != proxyResponse.RequestId.String() {
			t.Log("mismatched request id")
			http.Error(w, "Invalid request ID", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Log("upgrade failed:", err)
			return
		}
		defer conn.Close()

		// Echo messages back to client
		for {
			messageType, p, err := conn.ReadMessage()
			if err != nil {
				break
			}
			echoMessage := "Echo: " + string(p)
			err = conn.WriteMessage(messageType, []byte(echoMessage))
			if err != nil {
				break
			}
		}
	})

	wsServer := &http.Server{
		Addr:    "0.0.0.0:8002",
		Handler: mux,
	}

	go func() {
		err := wsServer.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			t.Logf("WebSocket server error: %v", err)
		}
	}()
	defer wsServer.Close()
	t.Log("started WebSocket inference server")

	dial, err := net.Dial("tcp", "localhost:8002")
	if err != nil {
		t.Fatalf("failed to connect to local WebSocket server %s", err)
	}
	err = director.RegisterWorker(proxyResponse.RequestId, proxyResponse.WorkerAuthorizationToken, testFunctionId, testFunctionVersionId, dial)
	if err != nil {
		t.Fatalf("failed to register worker %s", err)
	}
	t.Logf("registered connection to inference server with director")

	// Test actual WebSocket connection
	t.Run("websocket_connection_with_protocol_headers", func(t *testing.T) {
		// Create WebSocket client with protocol headers
		header := http.Header{}
		header.Add("Sec-WebSocket-Protocol", "authorization.asdf")
		header.Add("Sec-WebSocket-Protocol", fmt.Sprintf("function-id.%s", testFunctionId))

		dialer := websocket.Dialer{}
		conn, resp, err := dialer.Dial("ws://localhost:10082/ws", header)
		if err != nil {
			t.Fatal("failed to create WebSocket client:", err)
		}
		defer conn.Close()
		if resp.Header.Get(consts.RequestIdHeaderName) != proxyResponse.RequestId.String() {
			t.Fatal("mismatched request id")
		}
		// Send a test message
		testMessage := "Hello WebSocket!"
		err = conn.WriteMessage(websocket.TextMessage, []byte(testMessage))
		if err != nil {
			t.Fatal("failed to write WebSocket message:", err)
		}

		// Read the response
		_, responseBytes, err := conn.ReadMessage()
		if err != nil {
			t.Fatal("failed to read WebSocket message:", err)
		}

		response := string(responseBytes)
		expectedResponse := "Echo: " + testMessage
		if response != expectedResponse {
			t.Fatalf("unexpected response: got %s, expected %s", response, expectedResponse)
		}
	})

	t.Logf("finished, shutting down")
}

func TestParseWebSocketProtocol(t *testing.T) {
	tests := []struct {
		name           string
		setupHeaders   func() http.Header
		expectedResult http.Header
		description    string
	}{
		{
			name: "empty_headers",
			setupHeaders: func() http.Header {
				return http.Header{}
			},
			expectedResult: nil,
			description:    "should return nil when no Sec-WebSocket-Protocol headers are present",
		},
		{
			name: "single_protocol_header_single_kvp",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123")
				return h
			},
			expectedResult: http.Header{
				"Authorization": []string{"Bearer-token123"},
			},
			description: "should parse single key-value pair from single protocol header",
		},
		{
			name: "single_protocol_header_multiple_kvps",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123,function-id.func123")
				return h
			},
			expectedResult: http.Header{
				"Authorization": []string{"Bearer-token123"},
				"Function-Id":   []string{"func123"},
			},
			description: "should parse multiple comma-separated key-value pairs from single protocol header",
		},
		{
			name: "multiple_protocol_headers",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123")
				h.Add("Sec-WebSocket-Protocol", "function-id.func123")
				h.Add("Sec-WebSocket-Protocol", "function-version-id.v1.0")
				return h
			},
			expectedResult: http.Header{
				"Authorization":       []string{"Bearer-token123"},
				"Function-Id":         []string{"func123"},
				"Function-Version-Id": []string{"v1.0"},
			},
			description: "should parse multiple protocol headers",
		},
		{
			name: "comma_and_space_separation",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123, function-id.func123,function-version-id.v1.0")
				return h
			},
			expectedResult: http.Header{
				"Authorization":       []string{"Bearer-token123"},
				"Function-Id":         []string{"func123"},
				"Function-Version-Id": []string{"v1.0"},
			},
			description: "should handle both comma and comma-space separators",
		},
		{
			name: "leading_space_trimmed",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", " authorization.Bearer-token123,function-id.func123")
				return h
			},
			expectedResult: http.Header{
				"Authorization": []string{"Bearer-token123"},
				"Function-Id":   []string{"func123"},
			},
			description: "should trim leading spaces from protocol values",
		},
		{
			name: "protocol_without_dots",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123,no-dot-here,function-id.func123")
				return h
			},
			expectedResult: http.Header{
				"Authorization": []string{"Bearer-token123"},
				"Function-Id":   []string{"func123"},
			},
			description: "should ignore protocol values without dots",
		},
		{
			name: "empty_protocol_values",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123,,function-id.func123")
				return h
			},
			expectedResult: http.Header{
				"Authorization": []string{"Bearer-token123"},
				"Function-Id":   []string{"func123"},
			},
			description: "should handle empty values between commas",
		},
		{
			name: "empty_key_or_value",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123,.empty-key,empty-value.,function-id.func123")
				return h
			},
			expectedResult: http.Header{
				"Authorization": []string{"Bearer-token123"},
				"":              []string{"empty-key"},
				"Empty-Value":   []string{""},
				"Function-Id":   []string{"func123"},
			},
			description: "should handle entries with empty keys or values (does not filter them out)",
		},
		{
			name: "multiple_dots_in_value",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer.token.with.dots,function-id.func123")
				return h
			},
			expectedResult: http.Header{
				"Authorization": []string{"Bearer.token.with.dots"},
				"Function-Id":   []string{"func123"},
			},
			description: "should handle multiple dots in values correctly",
		},
		{
			name: "case_sensitivity",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "Authorization.Bearer-token123,function-ID.func123,Function-Version-Id.v1.0")
				return h
			},
			expectedResult: http.Header{
				"Authorization":       []string{"Bearer-token123"},
				"Function-Id":         []string{"func123"},
				"Function-Version-Id": []string{"v1.0"},
			},
			description: "should preserve case in keys and values",
		},
		{
			name: "duplicate_keys",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123,authorization.Bearer-token456")
				return h
			},
			expectedResult: http.Header{
				"Authorization": []string{"Bearer-token123", "Bearer-token456"},
			},
			description: "should handle duplicate keys by adding multiple values",
		},
		{
			name: "complex_mixed_scenario",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123, function-id.func123")
				h.Add("Sec-WebSocket-Protocol", "function-version-id.v1.0,no-dot-here")
				h.Add("Sec-WebSocket-Protocol", " request-id.550e8400-e29b-41d4-a716-446655440000")
				return h
			},
			expectedResult: http.Header{
				"Authorization":       []string{"Bearer-token123"},
				"Function-Id":         []string{"func123"},
				"Function-Version-Id": []string{"v1.0"},
				"Request-Id":          []string{"550e8400-e29b-41d4-a716-446655440000"},
			},
			description: "should handle complex mixed scenario with multiple headers and separators",
		},
		{
			name: "only_comma_separation",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123,function-id.func123,function-version-id.v1.0")
				return h
			},
			expectedResult: http.Header{
				"Authorization":       []string{"Bearer-token123"},
				"Function-Id":         []string{"func123"},
				"Function-Version-Id": []string{"v1.0"},
			},
			description: "should handle only comma separation without spaces",
		},
		{
			name: "special_characters_in_values",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token!@#$%^&*(),function-id.func_123-test")
				return h
			},
			expectedResult: http.Header{
				"Authorization": []string{"Bearer-token!@#$%^&*()"},
				"Function-Id":   []string{"func_123-test"},
			},
			description: "should handle special characters in values",
		},
		{
			name: "mixed_comma_and_comma_space_separators",
			setupHeaders: func() http.Header {
				h := make(http.Header)
				h.Add("Sec-WebSocket-Protocol", "authorization.Bearer-token123,function-id.func123, function-version-id.v1.0, request-id.12345")
				return h
			},
			expectedResult: http.Header{
				"Authorization":       []string{"Bearer-token123"},
				"Function-Id":         []string{"func123"},
				"Function-Version-Id": []string{"v1.0"},
				"Request-Id":          []string{"12345"},
			},
			description: "should handle mixed comma and comma-space separators in same header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputHeaders := tt.setupHeaders()
			result := parseWebSocketProtocol(inputHeaders)

			// Check if both results are nil
			if tt.expectedResult == nil && result == nil {
				return
			}

			// Check if one is nil and the other is not
			if (tt.expectedResult == nil) != (result == nil) {
				t.Errorf("Expected nil mismatch: expected %v, got %v", tt.expectedResult == nil, result == nil)
				return
			}

			// Compare headers
			if len(tt.expectedResult) != len(result) {
				t.Errorf("Expected %d headers, got %d headers", len(tt.expectedResult), len(result))
				return
			}

			for key, expectedValues := range tt.expectedResult {
				resultValues := result[key]
				if len(expectedValues) != len(resultValues) {
					t.Errorf("For key '%s': expected %d values, got %d values", key, len(expectedValues), len(resultValues))
					continue
				}

				for i, expectedValue := range expectedValues {
					if i >= len(resultValues) || resultValues[i] != expectedValue {
						t.Errorf("For key '%s': expected value '%s' at index %d, got '%s'",
							key, expectedValue, i, getValueAtIndex(resultValues, i))
					}
				}
			}

			// Check for unexpected keys in result
			for key := range result {
				if _, exists := tt.expectedResult[key]; !exists {
					t.Errorf("Unexpected key '%s' in result", key)
				}
			}
		})
	}
}

// Helper function to safely get value at index
func getValueAtIndex(values []string, index int) string {
	if index < len(values) {
		return values[index]
	}
	return "<missing>"
}

func TestCorsPreflightRequest(t *testing.T) {
	proxyResponse := &invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "123abc",
	}
	director := NewStreamDirector((*mockInvoker)(proxyResponse))
	defer func() {
		err := director.Close()
		if err != nil {
			t.Fatalf("failed to close director %s", err)
		}
	}()

	healthManager, err := healthManager("http://localhost:10085", nil)
	if err != nil {
		t.Fatalf("failed to create dummy health manager")
	}
	http2Server := createHttp2Server(director, "0.0.0.0:10085", healthManager)
	listenAndServe := lo.Async(http2Server.ListenAndServe)
	defer func() {
		err := <-listenAndServe
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("failed to listen and serve %s", err)
		}
	}()
	defer http2Server.Close()
	t.Log("started proxy h2c listener")

	selfFqdn := "http://localhost:10085"

	t.Run("cors_preflight_options_request", func(t *testing.T) {
		// Create a preflight CORS request
		request, _ := http.NewRequest(http.MethodOptions, selfFqdn+"/test", nil)
		request.Header.Set("Access-Control-Request-Method", "POST")
		request.Header.Set("Access-Control-Request-Headers", "authorization,function-id")
		request.Header.Set("Origin", "https://example.com")

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()

		// Check that the CORS preflight request is handled correctly
		if response.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", response.StatusCode)
		}

		// Check that important CORS headers are present
		// The go-chi/cors library sets headers based on the specific request
		// With AllowOriginFunc, it should reflect the requesting origin
		expectedOrigin := "https://example.com" // This is what we sent in the Origin header
		if response.Header.Get("Access-Control-Allow-Origin") != expectedOrigin {
			t.Errorf("expected Access-Control-Allow-Origin: %s, got %s", expectedOrigin, response.Header.Get("Access-Control-Allow-Origin"))
		}
		if response.Header.Get("Access-Control-Allow-Credentials") != "true" {
			t.Errorf("expected Access-Control-Allow-Credentials: true, got %s", response.Header.Get("Access-Control-Allow-Credentials"))
		}
		// Methods and headers are set based on the request, so just check they exist
		if response.Header.Get("Access-Control-Allow-Methods") == "" {
			t.Error("expected Access-Control-Allow-Methods header to be present")
		}
		if response.Header.Get("Access-Control-Allow-Headers") == "" {
			t.Error("expected Access-Control-Allow-Headers header to be present")
		}
	})

	t.Run("non_cors_options_request", func(t *testing.T) {
		// Create a non-CORS OPTIONS request (without Access-Control-Request-Method)
		request, _ := http.NewRequest(http.MethodOptions, selfFqdn+"/test", nil)
		request.Header.Set("Origin", "https://example.com")

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()

		// This should be handled as a normal request and result in an error
		// since no authorization is provided
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", response.StatusCode)
		}
	})
}

func TestCorsErrorResponses(t *testing.T) {
	proxyResponse := &invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "123abc",
	}
	director := NewStreamDirector((*mockInvoker)(proxyResponse))
	defer func() {
		err := director.Close()
		if err != nil {
			t.Fatalf("failed to close director %s", err)
		}
	}()

	healthManager, err := healthManager("http://localhost:10086", nil)
	if err != nil {
		t.Fatalf("failed to create dummy health manager")
	}
	http2Server := createHttp2Server(director, "0.0.0.0:10086", healthManager)
	listenAndServe := lo.Async(http2Server.ListenAndServe)
	defer func() {
		err := <-listenAndServe
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("failed to listen and serve %s", err)
		}
	}()
	defer http2Server.Close()
	t.Log("started proxy h2c listener")

	selfFqdn := "http://localhost:10086"

	t.Run("unauthenticated_error_with_cors_http", func(t *testing.T) {
		// Create an HTTP request without authorization
		request, _ := http.NewRequest(http.MethodPost, selfFqdn+"/test", nil)
		request.Header.Set("Origin", "https://example.com")

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()

		// Should get 401 Unauthorized
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", response.StatusCode)
		}

		// Check that basic CORS headers are present on error response
		// The director applies CORS via its internal cors handler on errors
		origin := response.Header.Get("Access-Control-Allow-Origin")
		if origin == "" {
			t.Error("expected Access-Control-Allow-Origin header to be present on error response")
		}
		credentials := response.Header.Get("Access-Control-Allow-Credentials")
		if credentials == "" {
			t.Error("expected Access-Control-Allow-Credentials header to be present on error response")
		}
	})

	t.Run("unauthenticated_error_with_cors_grpc", func(t *testing.T) {
		// Create a gRPC request without authorization
		request, _ := http.NewRequest(http.MethodPost, selfFqdn+"/test", nil)
		request.Header.Set("Content-Type", "application/grpc")
		request.Header.Set("Origin", "https://example.com")

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()

		// gRPC requests should return 200 with grpc-status header
		if response.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", response.StatusCode)
		}

		// Should have grpc-status for unauthenticated
		grpcStatus := response.Header.Get("grpc-status")
		if grpcStatus != strconv.Itoa(int(codes.Unauthenticated)) {
			t.Fatalf("expected grpc-status %d, got %s", int(codes.Unauthenticated), grpcStatus)
		}

		// Check that basic CORS headers are present on gRPC error response
		origin := response.Header.Get("Access-Control-Allow-Origin")
		if origin == "" {
			t.Error("expected Access-Control-Allow-Origin header to be present on gRPC error response")
		}
		credentials := response.Header.Get("Access-Control-Allow-Credentials")
		if credentials == "" {
			t.Error("expected Access-Control-Allow-Credentials header to be present on gRPC error response")
		}
	})

	t.Run("missing_function_id_error_with_cors", func(t *testing.T) {
		// Create a request with auth but missing function-id
		request, _ := http.NewRequest(http.MethodPost, selfFqdn+"/test", nil)
		request.Header.Set("authorization", "Bearer valid-token")
		request.Header.Set("Origin", "https://example.com")

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()

		// Should get 400 Bad Request for missing function-id
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", response.StatusCode)
		}

		// Check that CORS headers are present
		// With AllowOriginFunc, it should reflect the requesting origin
		expectedOrigin := "https://example.com" // This is what we sent in the Origin header
		origin := response.Header.Get("Access-Control-Allow-Origin")
		if origin != expectedOrigin {
			t.Errorf("expected Access-Control-Allow-Origin: %s, got %s", expectedOrigin, origin)
		}
	})
}

func TestCorsWithTimeoutError(t *testing.T) {
	timeout := consts.Timeout
	consts.Timeout = 100 * time.Millisecond
	t.Cleanup(func() {
		consts.Timeout = timeout
	})

	proxyResponse := &invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "123abc",
	}
	director := NewStreamDirector((*mockInvoker)(proxyResponse))
	defer func() {
		err := director.Close()
		if err != nil {
			t.Fatalf("failed to close director %s", err)
		}
	}()

	healthManager, err := healthManager("http://localhost:10087", nil)
	if err != nil {
		t.Fatalf("failed to create dummy health manager")
	}
	http2Server := createHttp2Server(director, "0.0.0.0:10087", healthManager)
	listenAndServe := lo.Async(http2Server.ListenAndServe)
	defer func() {
		err := <-listenAndServe
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("failed to listen and serve %s", err)
		}
	}()
	defer http2Server.Close()
	t.Log("started proxy h2c listener")

	selfFqdn := "http://localhost:10087"

	t.Run("timeout_error_with_cors_http", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodPost, selfFqdn+"/test", nil)
		request.Header.Set("authorization", "Bearer asdf")
		request.Header.Set("function-id", "1234")
		request.Header.Set("Origin", "https://example.com")

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()

		// Should get 504 Gateway Timeout
		if response.StatusCode != http.StatusGatewayTimeout {
			t.Fatalf("expected status 504, got %d", response.StatusCode)
		}

		// Check that CORS headers are present on timeout error
		// With AllowOriginFunc, it should reflect the requesting origin
		expectedOrigin := "https://example.com" // This is what we sent in the Origin header
		corsHeaders := map[string]string{
			"Access-Control-Allow-Origin":      expectedOrigin,
			"Access-Control-Allow-Credentials": "true",
		}

		for header, expectedValue := range corsHeaders {
			actualValue := response.Header.Get(header)
			if actualValue != expectedValue {
				t.Errorf("expected %s: %s, got %s", header, expectedValue, actualValue)
			}
		}
	})

	t.Run("timeout_error_with_cors_grpc", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodPost, selfFqdn+"/test", nil)
		request.Header.Set("authorization", "Bearer asdf")
		request.Header.Set("function-id", "1234")
		request.Header.Set("Content-Type", "application/grpc")
		request.Header.Set("Origin", "https://example.com")

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()

		// gRPC should return 200 with grpc-status
		if response.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", response.StatusCode)
		}

		if response.Header.Get("grpc-status") != strconv.Itoa(int(codes.DeadlineExceeded)) {
			t.Fatalf("expected grpc-status %d, got %s", int(codes.DeadlineExceeded), response.Header.Get("grpc-status"))
		}

		// Check that CORS headers are present on timeout error for gRPC
		// With AllowOriginFunc, it should reflect the requesting origin
		expectedOrigin := "https://example.com" // This is what we sent in the Origin header
		corsHeaders := map[string]string{
			"Access-Control-Allow-Origin":      expectedOrigin,
			"Access-Control-Allow-Credentials": "true",
		}

		for header, expectedValue := range corsHeaders {
			actualValue := response.Header.Get(header)
			if actualValue != expectedValue {
				t.Errorf("expected %s: %s, got %s", header, expectedValue, actualValue)
			}
		}
	})
}

// mockFailingInvoker simulates various failure scenarios
type mockFailingInvoker struct {
	shouldFail bool
	failureErr error
}

func (m *mockFailingInvoker) InvokeStatefulFunction(_ context.Context, _ net.Conn, _, _, _ string, _ *uuid.UUID, onWorkerAuthSet func(workerAuthToken string, requestId uuid.UUID, apiFunctionId string, apiFunctionVersionId string)) (invocation.Result, context.CancelFunc, error) {
	if m.shouldFail {
		return invocation.Result{}, func() {}, m.failureErr
	}
	// Return a valid result if not failing
	result := invocation.Result{
		RequestId:                uuid.New(),
		WorkerAuthorizationToken: "test-token",
	}
	onWorkerAuthSet(result.WorkerAuthorizationToken, result.RequestId, testFunctionId, testFunctionVersionId)
	return result, func() {}, nil
}

func TestStreamDirector_ErrorHandling(t *testing.T) {
	t.Run("getAndInitWorkerConnection_propagates_invoker_error", func(t *testing.T) {
		expectedErr := errors.New("NVCF API call failed")
		failingInvoker := &mockFailingInvoker{
			shouldFail: true,
			failureErr: expectedErr,
		}

		director := NewStreamDirector(failingInvoker)
		defer director.Close()

		mockConn := &net.TCPConn{} // Use a real net.Conn type for this test
		ctx := context.Background()
		requestId := uuid.New()

		// This should return an error instead of nil connection
		workerConn, err := director.getAndInitWorkerConnection(
			ctx,
			worker.NewConnectionTrackingConn(mockConn),
			"Bearer test-auth",
			"test-function",
			"v1.0",
			&requestId,
		)

		if err == nil {
			t.Error("expected error from getAndInitWorkerConnection, got nil")
		}
		if workerConn != nil {
			t.Errorf("expected nil worker connection on error, got %v", workerConn)
		}

		// Check that the error is properly wrapped
		if !errors.Is(err, expectedErr) {
			t.Errorf("expected error to wrap %v, got %v", expectedErr, err)
		}

		// Check error message contains context
		expectedMessage := "failed to open stateful work request"
		if !strings.Contains(err.Error(), expectedMessage) {
			t.Errorf("expected error message to contain '%s', got: %s", expectedMessage, err.Error())
		}
	})

	t.Run("getAndInitWorkerConnection_success_returns_worker_connection", func(t *testing.T) {
		successInvoker := &mockFailingInvoker{shouldFail: false}

		director := NewStreamDirector(successInvoker)
		defer director.Close()

		// Create a mock TCP connection for testing
		listener, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			t.Fatalf("failed to create listener: %v", err)
		}
		defer listener.Close()

		// Create a connection to the listener
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatalf("failed to dial: %v", err)
		}
		defer conn.Close()

		ctx := context.Background()
		requestId := uuid.New()

		workerConn, err := director.getAndInitWorkerConnection(
			ctx,
			worker.NewConnectionTrackingConn(conn),
			"Bearer test-auth",
			"test-function",
			"v1.0",
			&requestId,
		)

		if err != nil {
			t.Errorf("expected no error from getAndInitWorkerConnection, got %v", err)
		}
		if workerConn == nil {
			t.Error("expected worker connection, got nil")
		}
	})

	t.Run("serveStatefulRequest_handles_worker_init_error", func(t *testing.T) {
		expectedErr := errors.New("worker initialization failed")
		failingInvoker := &mockFailingInvoker{
			shouldFail: true,
			failureErr: expectedErr,
		}

		director := NewStreamDirector(failingInvoker)
		defer director.Close()

		// Create test HTTP request and response writer
		req := httptest.NewRequest(http.MethodPost, "/test", nil)
		req.Header.Set("authorization", "Bearer test-auth")
		req.Header.Set("function-id", "test-function")
		req.Header.Set("function-version-id", "v1.0")

		recorder := httptest.NewRecorder()

		// Create a mock connection
		listener, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			t.Fatalf("failed to create listener: %v", err)
		}
		defer listener.Close()

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatalf("failed to dial: %v", err)
		}
		defer conn.Close()

		trackingConn := worker.NewConnectionTrackingConn(conn)
		ctx := worker.CtxWithConn(req.Context(), trackingConn)
		req = req.WithContext(ctx)

		// This should handle the error gracefully without panicking
		err = director.serveStatefulRequest(recorder, req)

		// Should return the error from worker initialization
		if err == nil {
			t.Error("expected error from serveStatefulRequest, got nil")
		}
		if !errors.Is(err, expectedErr) {
			t.Errorf("expected error to wrap %v, got %v", expectedErr, err)
		}
	})

	t.Run("concurrent_worker_init_errors_handled_independently", func(t *testing.T) {
		// Test that multiple concurrent requests with init errors are handled independently
		failingInvoker := &mockFailingInvoker{
			shouldFail: true,
			failureErr: errors.New("concurrent init failure"),
		}

		director := NewStreamDirector(failingInvoker)
		defer director.Close()

		const numRequests = 5
		results := make([]error, numRequests)
		var wg sync.WaitGroup

		for i := 0; i < numRequests; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()

				// Create test HTTP request
				req := httptest.NewRequest(http.MethodPost, "/test", nil)
				req.Header.Set("authorization", "Bearer test-auth")
				req.Header.Set("function-id", fmt.Sprintf("test-function-%d", index))
				req.Header.Set("function-version-id", "v1.0")

				recorder := httptest.NewRecorder()

				// Create a mock connection for each request
				listener, err := net.Listen("tcp", "localhost:0")
				if err != nil {
					results[index] = fmt.Errorf("failed to create listener: %v", err)
					return
				}
				defer listener.Close()

				conn, err := net.Dial("tcp", listener.Addr().String())
				if err != nil {
					results[index] = fmt.Errorf("failed to dial: %v", err)
					return
				}
				defer conn.Close()

				trackingConn := worker.NewConnectionTrackingConn(conn)
				ctx := worker.CtxWithConn(req.Context(), trackingConn)
				req = req.WithContext(ctx)

				results[index] = director.serveStatefulRequest(recorder, req)
			}(i)
		}

		wg.Wait()

		// All requests should have failed with the same underlying error
		for i, result := range results {
			if result == nil {
				t.Errorf("request %d: expected error, got nil", i)
			} else if !strings.Contains(result.Error(), "concurrent init failure") {
				t.Errorf("request %d: expected error to contain 'concurrent init failure', got: %v", i, result)
			}
		}
	})

	t.Run("worker_connection_retry_on_closed_connection", func(t *testing.T) {
		// This test verifies that the removed nil check logic is properly handled
		// by the new error propagation system

		callCount := 0
		var mu sync.Mutex

		// Create an invoker that succeeds on first call but returns a connection that gets closed
		retryInvoker := FunctionInvokerFunc(func(ctx context.Context, conn net.Conn, auth, functionId, functionVersionId string, requestId *uuid.UUID, onWorkerAuth func(workerAuthToken string, requestId uuid.UUID, apiFunctionId string, apiFunctionVersionId string)) (invocation.Result, context.CancelFunc, error) {
			mu.Lock()
			callCount++
			currentCall := callCount
			mu.Unlock()

			if currentCall == 1 {
				// First call succeeds
				result := invocation.Result{
					RequestId:                uuid.New(),
					WorkerAuthorizationToken: "first-token",
				}
				onWorkerAuth(result.WorkerAuthorizationToken, result.RequestId, testFunctionId, testFunctionVersionId)
				return result, func() {}, nil
			}

			// Subsequent calls also succeed (simulating retry)
			result := invocation.Result{
				RequestId:                uuid.New(),
				WorkerAuthorizationToken: "retry-token",
			}
			onWorkerAuth(result.WorkerAuthorizationToken, result.RequestId, testFunctionId, testFunctionVersionId)
			return result, func() {}, nil
		})

		director := NewStreamDirector(retryInvoker)
		defer director.Close()

		// Create a mock connection
		listener, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			t.Fatalf("failed to create listener: %v", err)
		}
		defer listener.Close()

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatalf("failed to dial: %v", err)
		}
		defer conn.Close()

		trackingConn := worker.NewConnectionTrackingConn(conn)
		ctx := context.Background()
		requestId := uuid.New()

		// First call to get a worker connection
		workerConn1, err1 := director.getAndInitWorkerConnection(
			ctx, trackingConn, "Bearer test-auth", "test-function", "v1.0", &requestId)

		if err1 != nil {
			t.Errorf("first call: expected no error, got %v", err1)
		}
		if workerConn1 == nil {
			t.Error("first call: expected worker connection, got nil")
		}

		// Close the worker connection to simulate a closed worker
		if workerConn1 != nil {
			workerConn1.Close()
		}

		// Second call with same parameters should detect closed connection and retry
		workerConn2, err2 := director.getAndInitWorkerConnection(
			ctx, trackingConn, "Bearer test-auth", "test-function", "v1.0", &requestId)

		if err2 != nil {
			t.Errorf("second call: expected no error, got %v", err2)
		}
		if workerConn2 == nil {
			t.Error("second call: expected worker connection after retry, got nil")
		}

		// Should have called the invoker twice (original + retry)
		mu.Lock()
		finalCallCount := callCount
		mu.Unlock()

		if finalCallCount != 2 {
			t.Errorf("expected invoker to be called twice (original + retry), was called %d times", finalCallCount)
		}

		// The second connection should be different from the first
		if workerConn1 == workerConn2 {
			t.Error("expected different worker connections after retry, got same connection")
		}
	})
}

// FunctionInvokerFunc is a function type that implements the FunctionInvoker interface
type FunctionInvokerFunc func(context.Context, net.Conn, string, string, string, *uuid.UUID, func(workerAuthToken string, requestId uuid.UUID, apiFunctionId string, apiFunctionVersionId string)) (invocation.Result, context.CancelFunc, error)

func (f FunctionInvokerFunc) InvokeStatefulFunction(ctx context.Context, conn net.Conn, auth, functionId, functionVersionId string, requestId *uuid.UUID, onWorkerAuth func(workerAuthToken string, requestId uuid.UUID, apiFunctionId string, apiFunctionVersionId string)) (invocation.Result, context.CancelFunc, error) {
	return f(ctx, conn, auth, functionId, functionVersionId, requestId, onWorkerAuth)
}
