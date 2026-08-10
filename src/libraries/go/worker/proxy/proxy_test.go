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

package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/integrationtests/tools"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"golang.org/x/sync/errgroup"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proxy/handlerisolationconn"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proxy/quicconn"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proxy/rp"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils/middleware"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils/pool"
)

func TestGrpcProxy(t *testing.T) {
	t.SkipNow() // comment me to test locally. must have the grpc proxy and an invocation server running.
	ctx := context.Background()

	cluster, err := testutils.NewNatsSuperCluster(t)
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Shutdown()
	nc, err := nats.Connect(cluster.Clusters[1].Servers[2].ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}

	functionId := uuid.New().String()
	functionVersionId := uuid.New().String()
	region := cluster.Clusters[1].Region

	subject := fmt.Sprintf("stateful_session.lookup.%s.%s.>", region, functionVersionId)
	streamName := fmt.Sprintf("stateful_session_lookup_%s", region)
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:              streamName,
		Subjects:          []string{subject},
		Discard:           jetstream.DiscardNew,
		MaxAge:            1 * time.Hour,
		MaxMsgsPerSubject: 1,
		Storage:           jetstream.MemoryStorage,
	})
	if err != nil {
		t.Fatal(err)
	}

	setupLogger()
	_, _ = tracing.SetupOTELTracer(&tracing.OTELConfig{
		Enabled:     true,
		Endpoint:    "localhost:8360",
		AccessToken: "",
		Insecure:    true,
		Attributes: tracing.Attributes{
			ServiceName:    "gdn-nvcf-worker-service",
			ServiceVersion: utils.Version,
		},
	})
	defer tracing.Shutdown()

	go func() {
		cookieJar, err := cookiejar.New(nil)
		if err != nil {
			panic(err)
		}
		client := &http.Client{Jar: cookieJar, Transport: &http.Transport{}}
		request, err := http.NewRequest(http.MethodPost, "http://localhost:10081/echo", bytes.NewBufferString(`{"message": "123"}`))
		if err != nil {
			panic(err)
		}
		request.Header.Set("Authorization", "Bearer test-token")
		request.Header.Set("function-id", functionId)
		request.Close = false
		response, err := client.Do(request)
		if err != nil {
			panic(err)
		}
		defer response.Body.Close()
		dumpResponse, err := httputil.DumpResponse(response, true)
		if err != nil {
			panic(err)
		}
		zap.L().Info("first response", zap.ByteString("content", dumpResponse))

		secondClient := &http.Client{Jar: cookieJar, Transport: &http.Transport{}}
		secondRequest, err := http.NewRequest(http.MethodPost, "http://localhost:10081/echo", bytes.NewBufferString(`{"message": "123"}`))
		if err != nil {
			panic(err)
		}
		secondRequest.Header.Set("Authorization", "Bearer test-token")
		secondRequest.Header.Set("function-id", functionId)
		secondRequest.Close = false
		secondResponse, err := secondClient.Do(secondRequest)
		if err != nil {
			panic(err)
		}
		defer secondResponse.Body.Close()
		dumpSecondResponse, err := httputil.DumpResponse(response, true)
		if err != nil {
			panic(err)
		}
		zap.L().Info("second response", zap.ByteString("content", dumpSecondResponse))
	}()
	time.Sleep(500 * time.Millisecond)
	inferenceAddress := "localhost:8000"
	h, err := NewHttpProxy(nc, js, functionId, functionVersionId, func(request *httputil.ProxyRequest) {
		request.Out.URL.Scheme = "http"
		request.Out.URL.Host = inferenceAddress
	}, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	defer h.Close()
	err = h.Proxy(ctx, &pb.WorkerInvokeFunctionRequest{
		RequestId: "867c19f9-2b81-45fd-a6ac-cd74ea9b37fc",
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
						ProxyURI: "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, region)
	if err != nil {
		t.Error(err)
	}
}

func setupLogger() {
	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.DebugLevel))
	zap.ReplaceGlobals(zapLogger.GetZapLogger())
	zap.RedirectStdLog(zapLogger.GetZapLogger())
}

func Test_getClientConnFromProxy(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	serverConns, _, _, server := mockGrpcProxy()
	defer func() {
		err := server.Close()
		if err != nil {
			panic(err)
		}
	}()
	h3RoundTripper := createH3RoundTripper()
	defer h3RoundTripper.Close()
	clientConn, err := getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, h3RoundTripper)
	if err != nil {
		t.Fatal(err)
	}
	serverConn := <-serverConns
	err = validateConnPair(t, clientConn, serverConn)
	if err != nil {
		t.Fatal(err)
	}
}

func Test_PushListener(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	serverConns, _, _, server := mockGrpcProxy()
	defer server.Close()
	h3RoundTripper := createH3RoundTripper()
	defer h3RoundTripper.Close()
	clientConn, err := getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, h3RoundTripper)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	serverConn := <-serverConns
	defer serverConn.Close()

	// push listener runs on the worker and proxies requests to the inference container
	pushListener := NewPushListener()
	h := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(w, r.Body)
	})}

	go func() {
		err := h.Serve(pushListener)
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			panic(err)
		}
	}()
	go func() {
		err := pushListener.ServeConn(clientConn)
		if err != nil {
			panic(err)
		}
	}()

	// this http client mirrors what is running on the grpc proxy. requests are sent into it.
	client := http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return serverConn, nil
		},
	}}
	for i := 0; i < 10; i++ {
		func() {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/echo", bytes.NewReader([]byte("echo me")))
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				t.Errorf("got status code %d, want 200", response.StatusCode)
			}
			bodyBytes, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			require.Equal(t, "echo me", string(bodyBytes))
			t.Log("echo request", i, "succeeded")
		}()
	}
}

func mockGrpcProxy(interceptors ...func(http.Handler) http.Handler) (chan net.Conn, *http.Server, *http3.Server, io.Closer) {
	ca, caPrivateKey, err := tools.GenerateCA()
	if err != nil {
		panic(err)
	}
	leafCert, leafPrivateKey, err := tools.GenerateLeafCert(ca, caPrivateKey)
	if err != nil {
		panic(err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{leafCert.Raw},
			PrivateKey:  leafPrivateKey,
		}},
	}
	serverConns := make(chan net.Conn)

	h3Handler := (http.Handler)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http3Hijacker, _ := w.(http3.HTTPStreamer)
		if http3Hijacker == nil {
			err := fmt.Errorf("HTTP CONNECT request is not hijack-able")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(200)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		conn := quicconn.NewHttp3StreamConn(http3Hijacker.HTTPStream())
		serverConns <- conn
	}))
	for _, interceptor := range interceptors {
		inner := h3Handler
		h3Handler = interceptor(inner)
	}
	h3Server := &http3.Server{
		Addr:      "localhost:10084",
		TLSConfig: tlsConfig,
		Handler:   h3Handler,
	}
	h1Handler := (http.Handler)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http1Hijacker, _ := w.(http.Hijacker)
		if http1Hijacker == nil {
			err := fmt.Errorf("HTTP CONNECT request is not hijack-able")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(200)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		conn, _, err := http1Hijacker.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		serverConns <- conn
	}))
	for _, interceptor := range interceptors {
		inner := h1Handler
		h1Handler = interceptor(inner)
	}
	h1Server := &http.Server{
		Addr:    "localhost:10085",
		Handler: h1Handler,
	}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := h3Server.ListenAndServe()
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			panic(err)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := h1Server.ListenAndServe()
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			panic(err)
		}
	}()
	return serverConns, h1Server, h3Server, CloserFunc(func() error {
		err1 := h1Server.Close()
		err2 := h3Server.Close()
		if err1 != nil {
			return err1
		}
		if err2 != nil {
			return err2
		}
		wg.Wait()
		// TODO workaround for http3 server not releasing the udp listener before returning
		time.Sleep(200 * time.Millisecond)
		return nil
	})
}

type CloserFunc func() error

func (f CloserFunc) Close() error {
	return f()
}

func Test_PushListenerMultipleConnections(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	h3RoundTripper := createH3RoundTripper()
	defer h3RoundTripper.Close()
	serverConns, _, _, server := mockGrpcProxy()
	defer server.Close()

	inferenceServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
	}))
	protocols := &http.Protocols{}
	protocols.SetUnencryptedHTTP2(true)
	protocols.SetHTTP1(true)
	defer inferenceServer.Close()
	inferenceServer.Config.Protocols = protocols
	inferenceServer.Start()

	dialer := net.Dialer{Timeout: 1 * time.Second}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme = "http"
			request.Out.URL.Host = inferenceServer.Listener.Addr().String()
		},
		Transport: NewProtoRoutingTransport(
			&http.Transport{DialContext: dialer.DialContext},
			&http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			}}),
		FlushInterval: -1,
		BufferPool:    pool.ByteSlice32k,
	}
	err := rp.InjectGrpcSupportToReverseProxy(proxy)
	if err != nil {
		t.Fatal(err)
	}

	// push listener runs on the worker and proxies requests to the inference container
	pushListener := NewPushListener()
	h := &http.Server{
		Handler: h2c.NewHandler(middleware.ApplyMiddleware(proxy, "stateful session"), &http2.Server{
			MaxConcurrentStreams: math.MaxInt32, // must be kept as max int streams for compatibility
		}),
	}

	go func() {
		err := h.Serve(pushListener)
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			panic(err)
		}
	}()
	group, _ := errgroup.WithContext(t.Context())
	for i := 0; i < 20; i++ {
		go func() {
			clientConn, err := getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
				RequestId: uuid.New().String(),
				StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
					ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
						{
							Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
								Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
									ProxyURI:                "https://localhost:10084/v1/proxy",
									ProxyAuthorizationToken: "dummy-token",
								},
							},
						},
					},
				},
			}, h3RoundTripper)
			if err != nil {
				panic(err)
			}
			defer clientConn.Close()
			err = pushListener.ServeConn(clientConn)
			if err != nil {
				panic(err)
			}
			t.Log("client connection served", i)
		}()
		group.Go(func() error {
			// this http client mirrors what is running on the grpc proxy. requests are sent into it.
			protocols := &http.Protocols{}
			// protocols.SetUnencryptedHTTP2(true)
			protocols.SetHTTP1(true)
			client := http.Client{Transport: &http.Transport{
				Protocols: protocols,
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return <-serverConns, nil
				},
			}}
			for j := 0; j < 10; j++ {
				func() {
					request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/echo", bytes.NewReader([]byte("echo me")))
					if err != nil {
						t.Fatal(err)
					}
					response, err := client.Do(request)
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					if response.StatusCode != 200 {
						dump, _ := httputil.DumpResponse(response, true)
						t.Fatalf("got status code %d, want 200: %s", response.StatusCode, string(dump))
					}
					bodyBytes, err := io.ReadAll(response.Body)
					if err != nil {
						t.Fatal(err)
					}
					require.Equal(t, 0, len(bodyBytes))
					t.Log("connection", i, "request", j, "succeeded")
				}()
			}
			return nil
		})
		time.Sleep(30 * time.Millisecond)
	}
	err = group.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func allowInsecure(t *testing.T) {
	before := quicInsecure
	quicInsecure = true
	t.Cleanup(func() {
		quicInsecure = before
	})
}

func Test_PushListenerHttp2(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	serverConns, _, _, server := mockGrpcProxy()
	defer server.Close()
	h3RoundTripper := createH3RoundTripper()
	defer h3RoundTripper.Close()
	clientConn, err := getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, h3RoundTripper)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	serverConn := <-serverConns
	defer serverConn.Close()

	// push listener runs on the worker and proxies requests to the inference container
	pushListener := NewPushListener()
	h := &http.Server{
		Handler: h2c.NewHandler(middleware.ApplyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(10 * time.Millisecond)
			io.Copy(w, r.Body)
		}), "stateful session"), &http2.Server{
			MaxConcurrentStreams: math.MaxInt32, // must be kept as max int streams for compatibility
		}),
	}

	go func() {
		err := h.Serve(pushListener)
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			panic(err)
		}
	}()
	go func() {
		err := pushListener.ServeConn(clientConn)
		if err != nil {
			panic(err)
		}
	}()

	// this http client mirrors what is running on the grpc proxy. requests are sent into it.
	protocols := &http.Protocols{}
	protocols.SetUnencryptedHTTP2(true)
	client := http.Client{Transport: &http.Transport{
		Protocols: protocols,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return serverConn, nil
		},
	}}
	for i := 0; i < 100; i++ {
		func() {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/echo", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				t.Errorf("got status code %d, want 200", response.StatusCode)
			}
			dumpResponse, err := httputil.DumpResponse(response, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Log("echo response", i, string(dumpResponse))
			bodyBytes, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			require.Equal(t, 0, len(bodyBytes))
			t.Log("echo request", i, "succeeded")
		}()
	}
}

func TestNoHOLBlocking(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	serverConns, _, _, server := mockGrpcProxy()
	defer server.Close()

	var connCount int32
	allConnectionsReady := make(chan struct{})
	var connectionsEstablished int64

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-allConnectionsReady // Block until all 5 connections are ready
		atomic.AddInt32(&connCount, 1)
		t.Log("Active connections:", atomic.LoadInt32(&connCount))
		defer atomic.AddInt32(&connCount, -1)
		time.Sleep(1 * time.Second)
	})

	inferenceServer := httptest.NewUnstartedServer(handler)

	inferenceServer.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		count := atomic.AddInt64(&connectionsEstablished, 1)
		t.Log("New connection established, total:", count)
		if count == 5 {
			close(allConnectionsReady) // Signal that all 5 connections are ready
		}
		return ctx
	}

	httpProxy, err := NewHttpProxy(nil, nil, "function-id", "version-id",
		func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme = "http"
			request.Out.URL.Host = inferenceServer.Listener.Addr().String()
		}, nil, nil, nil)
	if err != nil {
		return
	}
	defer httpProxy.Close()

	protocols := &http.Protocols{}
	protocols.SetUnencryptedHTTP2(true)
	protocols.SetHTTP1(false)
	defer inferenceServer.Close()
	inferenceServer.Config.Protocols = protocols
	inferenceServer.Start()

	go func() {
		err := httpProxy.handler.Serve(httpProxy.listener)
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			panic(err)
		}
	}()

	group, ctx := errgroup.WithContext(t.Context())

	for c := 0; c < 5; c++ {
		go func() {
			clientConn, err := getClientConnFromProxy(ctx, &pb.WorkerInvokeFunctionRequest{
				RequestId: uuid.New().String(),
				StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
					ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
						{
							Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
								Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
									ProxyURI:                "https://localhost:10084/v1/proxy",
									ProxyAuthorizationToken: "dummy-token",
								},
							},
						},
					},
				},
			}, httpProxy.h3)
			if err != nil {
				panic(err)
			}
			handlerConn, err := handlerisolationconn.NewHandlerConn(clientConn, httpProxy.handlerPool)
			if err != nil {
				panic(err)
			}
			err = httpProxy.listener.ServeConn(handlerConn)
			if err != nil {
				panic(err)
			}
			t.Log("client connection served")
		}()
		protocols := &http.Protocols{}
		protocols.SetUnencryptedHTTP2(true)
		client := http.Client{Transport: &http.Transport{
			Protocols: protocols,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return <-serverConns, nil
			},
		}}

		for r := 0; r < 5; r++ {
			group.Go(func() error {
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/echo", nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != 200 {
					dump, _ := httputil.DumpResponse(response, true)
					t.Fatalf("got status code %d, want 200: %s", response.StatusCode, string(dump))
				}
				bodyBytes, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				require.Equal(t, 0, len(bodyBytes))
				t.Log(string(bodyBytes))
				t.Log("connection", c, "request", r, "succeeded")

				return nil
			})
			time.Sleep(30 * time.Millisecond)
		}
	}

	err = group.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func Test_deadServerConnectionAfterKeepalive(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	t.Log("starting new http3 server")
	serverConns, _, _, server := mockGrpcProxy()
	defer server.Close()
	h3RoundTripper := createH3RoundTripper()
	defer h3RoundTripper.Close()
	h3RoundTripper.wrappedTransport.QUICConfig.MaxIdleTimeout = 2 * time.Second // shorten for tests
	t.Log("getting client connection")
	clientConn, err := getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, h3RoundTripper)
	if err != nil {
		t.Fatal(err)
	}
	serverConn := <-serverConns
	err = validateConnPair(t, clientConn, serverConn)
	if err != nil {
		t.Fatal(err)
	}
	// sleeping before closing so we miss the first keepalive and are forced into the worst case
	time.Sleep(200 * time.Millisecond)
	t.Log("closing http3 server")
	// server.Shutdown(t.Context())
	server.Close()
	t.Log("closed http3 server")
	time.Sleep(200 * time.Millisecond)
	t.Log("starting new http3 server")
	serverConns, _, _, server = mockGrpcProxy()
	defer server.Close()
	time.Sleep(h3RoundTripper.wrappedTransport.QUICConfig.MaxIdleTimeout + 200*time.Millisecond)
	t.Log("getting client connection")
	clientConn, err = getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, h3RoundTripper)
	if err != nil {
		t.Fatal(err)
	}
	serverConn = <-serverConns
	err = validateConnPair(t, clientConn, serverConn)
	if err != nil {
		t.Fatal(err)
	}
}

func Test_deadServerConnectionBeforeKeepalive(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	t.Log("starting new http3 server")
	serverConns, _, _, server := mockGrpcProxy()
	defer server.Close()
	h3RoundTripper := createH3RoundTripper()
	defer h3RoundTripper.Close()
	h3RoundTripper.wrappedTransport.QUICConfig.MaxIdleTimeout = 2 * time.Second // shorten for tests
	t.Log("getting client connection")
	clientConn, err := getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, h3RoundTripper)
	if err != nil {
		t.Fatal(err)
	}
	serverConn := <-serverConns
	err = validateConnPair(t, clientConn, serverConn)
	if err != nil {
		t.Fatal(err)
	}
	// sleeping before closing so we miss the first keepalive and are forced into the worst case
	time.Sleep(200 * time.Millisecond)
	t.Log("closing http3 server")
	server.Close()
	t.Log("closed http3 server")
	time.Sleep(200 * time.Millisecond)
	t.Log("starting new http3 server")
	serverConns, _, _, server = mockGrpcProxy()
	defer server.Close()
	time.Sleep(200 * time.Millisecond)
	t.Log("getting client connection")
	clientConn, err = getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, h3RoundTripper)
	if err != nil {
		t.Fatal(err)
	}
	serverConn = <-serverConns
	err = validateConnPair(t, clientConn, serverConn)
	if err != nil {
		t.Fatal(err)
	}
}

func validateConnPair(t *testing.T, clientConn net.Conn, serverConn net.Conn) error {
	defer clientConn.Close()
	defer serverConn.Close()
	group, _ := errgroup.WithContext(t.Context())
	type closeWriter interface {
		CloseWrite() error
	}
	group.Go(func() error {
		t.Log("sending hello from client")
		_, err := clientConn.Write([]byte("Hello from Client"))
		if err != nil {
			return err
		}
		if closer, ok := clientConn.(closeWriter); ok {
			err = closer.CloseWrite()
			if err != nil {
				return err
			}
		}
		t.Log("reading hello from server")
		readAll, err := io.ReadAll(clientConn)
		if err != nil {
			return err
		}
		require.Equal(t, "Hello from Server", string(readAll))
		t.Log("client connection read correct response from server")
		return nil
	})
	group.Go(func() error {
		t.Log("reading hello from client")
		readAll, err := io.ReadAll(serverConn)
		if err != nil {
			return err
		}
		require.Equal(t, "Hello from Client", string(readAll))
		t.Log("sending hello from server")
		_, err = serverConn.Write([]byte("Hello from Server"))
		if err != nil {
			return err
		}
		if closer, ok := serverConn.(closeWriter); ok {
			err = closer.CloseWrite()
			if err != nil {
				return err
			}
		}
		t.Log("server connection read correct response from client")
		return nil
	})
	return group.Wait()
}

func Test_gracefulShutdown(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	t.Log("starting new http3 server")
	serverConns, _, h3Server, server := mockGrpcProxy()
	defer server.Close()
	h3RoundTripper := createH3RoundTripper()
	defer h3RoundTripper.Close()
	t.Log("getting client connection")
	clientConn, err := getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, h3RoundTripper)
	if err != nil {
		t.Fatal(err)
	}
	serverConn := <-serverConns
	err = validateConnPair(t, clientConn, serverConn)
	if err != nil {
		t.Fatal(err)
	}
	// sleeping before closing so we miss the first keepalive and are forced into the worst case
	time.Sleep(200 * time.Millisecond)
	t.Log("closing http3 server")
	err = h3Server.Shutdown(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = server.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Log("closed http3 server")
	t.Log("starting new http3 server")
	serverConns, _, _, server = mockGrpcProxy()
	defer server.Close()
	t.Log("getting client connection")
	clientConn, err = getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, h3RoundTripper)
	if err != nil {
		t.Fatal(err)
	}
	serverConn = <-serverConns
	err = validateConnPair(t, clientConn, serverConn)
	if err != nil {
		t.Fatal(err)
	}
}

func Test_TCPConn(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	serverConns, _, _, server := mockGrpcProxy()
	defer server.Close()
	clientConn, err := getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{{
				Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http1Config{Http1Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP1ConnectionConfig{
					ProxyURI:                "https://localhost:10085/v1/proxy",
					ProxyAuthorizationToken: "dummy-token",
				}},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	serverConn := <-serverConns
	err = validateConnPair(t, clientConn, serverConn)
	if err != nil {
		t.Fatal(err)
	}
}

func Test_GrpcProxyRetryConnect(t *testing.T) {
	setupLogger()
	allowInsecure(t)
	// we should retry on 500, but not on 401
	try := atomic.Int32{}
	_, _, _, server := mockGrpcProxy(func(h http.Handler) http.Handler {
		// ignore the existing handler, we want to test the retry logic
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if try.Load() == 0 {
				http.Error(w, "server error", http.StatusInternalServerError)
			} else {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			}
			try.Add(1)
		})
	})
	defer func() {
		err := server.Close()
		if err != nil {
			panic(err)
		}
	}()
	h3RoundTripper := createH3RoundTripper()
	defer h3RoundTripper.Close()
	_, err := getClientConnFromProxy(t.Context(), &pb.WorkerInvokeFunctionRequest{
		RequestId: uuid.New().String(),
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
				{
					Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{
						Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
							ProxyURI:                "https://localhost:10084/v1/proxy",
							ProxyAuthorizationToken: "dummy-token",
						},
					},
				},
			},
		},
	}, h3RoundTripper)
	if err == nil {
		t.Fatal("expected error")
	}
	require.Equal(t, int32(2), try.Load())
}
