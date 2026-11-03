/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package httpstream

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/stretchr/testify/require"
)

func localCallbackClient(t *testing.T, server *httptest.Server, enabled bool, ttl time.Duration) (*http.Client, *callbackTransport) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	base := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots}, MaxIdleConnsPerHost: 256,
		IdleConnTimeout: 5 * time.Minute, Proxy: func(r *http.Request) (*url.URL, error) {
			proxy, _ := r.Context().Value(proxyURLKey).(*url.URL)
			return proxy, nil
		},
	}
	client := &http.Client{Transport: base}
	var transport *callbackTransport
	if enabled {
		config := defaultPreconnectConfig()
		if ttl > 0 {
			config.idleTimeout = ttl
		}
		transport = newCallbackTransport(base, config)
		client.Transport = transport
	}
	t.Cleanup(client.CloseIdleConnections)
	return client, transport
}

func localHandler(t *testing.T, client *http.Client, target, proxy string) *RequestStreamHandler {
	t.Helper()
	cfg := baseConfig(target)
	if proxy != "" {
		cfg.ConnectionConfigs[0].GetHttp1Config().ProxyURI = &proxy
	}
	h, err := NewRequestStreamHandler(t.Context(), ProxyClient(client), cfg,
		&pb.WorkerInvokeFunctionRequest{RequestMethod: http.MethodPost, RequestId: "local-synthetic"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func bufferedResponse() *http.Response {
	return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"X-Synthetic": {"local"}},
		ContentLength: int64(len("synthetic result")), Body: io.NopCloser(strings.NewReader("synthetic result"))}
}

type callbackObservation struct{ method, auth, contentType, body, remote, proxyAuth, target string }

func TestCallbackPreconnectTLSBufferedAndIdleReuse(t *testing.T) {
	seen := make(chan callbackObservation, 3)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen <- callbackObservation{method: r.Method, auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"), body: string(b), remote: r.RemoteAddr}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client, tr := localCallbackClient(t, srv, true, 0)
	h := localHandler(t, client, srv.URL, "")
	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	route, ok := tr.route(req)
	require.True(t, ok)
	e := entryFor(t, tr.pool, route.key)
	require.NoError(t, e.err)
	select {
	case <-seen:
		t.Fatal("preparation sent an HTTP request before inference was ready")
	default:
	}
	preparedAddress := e.conn.LocalAddr().String()
	require.NoError(t, h.SendResponse(t.Context(), bufferedResponse(), nil))
	first := <-seen
	require.Equal(t, preparedAddress, first.remote, "POST did not use the prepared socket")
	require.Equal(t, "Bearer response-token", first.auth)
	require.Equal(t, http.MethodPost, first.method)
	require.Equal(t, h1ContentType, first.contentType)
	parsed, err := http.ReadResponse(bufio.NewReader(strings.NewReader(first.body)), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, parsed.StatusCode)
	require.Equal(t, "local", parsed.Header.Get("X-Synthetic"))
	body, err := io.ReadAll(parsed.Body)
	require.NoError(t, err)
	require.Equal(t, "synthetic result", string(body))
	require.NoError(t, h.Close())
	second := localHandler(t, client, srv.URL, "")
	require.NoError(t, second.SendResponse(t.Context(), bufferedResponse(), nil))
	require.Equal(t, first.remote, (<-seen).remote, "normal transport reuse was lost")
	require.Empty(t, seen, "callback was duplicated")
}

func TestCallbackPreconnectStreamingDoesNotBuffer(t *testing.T) {
	firstChunk := make(chan string, 1)
	completed := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parsed, err := http.ReadResponse(bufio.NewReader(r.Body), nil)
		if err != nil {
			firstChunk <- "parse failed"
			return
		}
		b := make([]byte, len("data: first\n\n"))
		_, err = io.ReadFull(parsed.Body, b)
		if err != nil {
			firstChunk <- "read failed"
			return
		}
		firstChunk <- string(b)
		_, _ = io.Copy(io.Discard, parsed.Body)
		close(completed)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client, _ := localCallbackClient(t, srv, true, 0)
	h := localHandler(t, client, srv.URL, "")
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- h.SendResponse(t.Context(), &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, ContentLength: -1, Body: reader}, nil)
	}()
	_, err := io.WriteString(writer, "data: first\n\n")
	require.NoError(t, err)
	select {
	case b := <-firstChunk:
		require.Equal(t, "data: first\n\n", b)
	case <-time.After(3 * time.Second):
		t.Fatal("stream first chunk was buffered")
	}
	select {
	case <-completed:
		t.Fatal("stream ended before the producer finished")
	default:
	}
	_, err = io.WriteString(writer, "data: [DONE]\n\n")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, <-done)
}

func TestCallbackPreconnectHTTPSProxy(t *testing.T) {
	seen := make(chan callbackObservation, 1)
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		seen <- callbackObservation{method: r.Method, auth: r.Header.Get("Authorization"), proxyAuth: r.Header.Get("Proxy-Authorization"), target: r.URL.String(), remote: r.RemoteAddr}
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	client, tr := localCallbackClient(t, proxy, true, 0)
	proxyURL, _ := url.Parse(proxy.URL)
	proxyURL.User = url.UserPassword("synthetic-user", "synthetic-password")
	h := localHandler(t, client, "http://callback.invalid", proxyURL.String())
	ctx := context.WithValue(t.Context(), proxyURLKey, proxyURL)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.TargetURI, nil)
	route, _ := tr.route(req)
	e := entryFor(t, tr.pool, route.key)
	require.NoError(t, e.err)
	require.NoError(t, h.SendResponse(t.Context(), bufferedResponse(), nil))
	got := <-seen
	require.Equal(t, e.conn.LocalAddr().String(), got.remote)
	require.Equal(t, "Bearer response-token", got.auth)
	require.Equal(t, "Basic c3ludGhldGljLXVzZXI6c3ludGhldGljLXBhc3N3b3Jk", got.proxyAuth)
	require.Equal(t, "http://callback.invalid/v2/nvcf/worker/request-attach", got.target)
}

func TestCallbackPreconnectHTTPProxyCONNECT(t *testing.T) {
	seen := make(chan callbackObservation, 1)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		seen <- callbackObservation{auth: r.Header.Get("Authorization"), proxyAuth: r.Header.Get("Proxy-Authorization")}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	connectAuth := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "expected CONNECT", http.StatusBadRequest)
			return
		}
		connectAuth <- r.Header.Get("Proxy-Authorization")
		upstream, err := net.Dial("tcp", target.Listener.Addr().String())
		if err != nil {
			http.Error(w, "local dial failed", http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(rw, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(upstream, rw)
			_ = upstream.Close()
			close(done)
		}()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
		<-done
	}))
	defer proxy.Close()
	client, tr := localCallbackClient(t, target, true, 0)
	defer client.CloseIdleConnections()
	proxyURL, _ := url.Parse(proxy.URL)
	proxyURL.User = url.UserPassword("test", "secret")
	h := localHandler(t, client, target.URL, proxyURL.String())
	ctx := context.WithValue(t.Context(), proxyURLKey, proxyURL)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, nil)
	route, _ := tr.route(req)
	e := entryFor(t, tr.pool, route.key)
	require.NoError(t, e.err)
	select {
	case <-connectAuth:
		t.Fatal("preparation sent CONNECT before the response was ready")
	default:
	}
	require.NoError(t, h.SendResponse(t.Context(), bufferedResponse(), nil))
	require.Equal(t, "Basic dGVzdDpzZWNyZXQ=", <-connectAuth)
	got := <-seen
	require.Equal(t, "Bearer response-token", got.auth)
	require.Empty(t, got.proxyAuth, "proxy credentials leaked to target")
}

func TestCallbackPreconnectAttachmentGETCannotClaimAndTokensStayPerRequest(t *testing.T) {
	getAddress := make(chan string, 1)
	finishGET := make(chan struct{})
	defer close(finishGET)
	seen := make(chan callbackObservation, 2)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getAddress <- r.RemoteAddr
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-finishGET
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		seen <- callbackObservation{remote: r.RemoteAddr, auth: r.Header.Get("Authorization")}
		w.WriteHeader(http.StatusOK)
	}))
	// Release the deliberately open GET before closing the server.
	t.Cleanup(srv.Close)
	client, tr := localCallbackClient(t, srv, true, 0)
	first := localHandler(t, client, srv.URL, "")
	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	route, _ := tr.route(req)
	e := entryFor(t, tr.pool, route.key)
	require.NoError(t, e.err)
	preparedAddress := e.conn.LocalAddr().String()
	get, err := client.Get(srv.URL + "/v2/nvcf/worker/request-attach")
	require.NoError(t, err)
	defer get.Body.Close()
	require.NotEqual(t, preparedAddress, <-getAddress)
	second := localHandler(t, client, srv.URL, "")
	second.ResponseAuthorizationToken = "another-synthetic-token"
	require.NoError(t, second.SendResponse(t.Context(), bufferedResponse(), nil))
	got := <-seen
	require.Equal(t, preparedAddress, got.remote)
	require.Equal(t, "Bearer another-synthetic-token", got.auth)
	require.NoError(t, first.Close())
	third := localHandler(t, client, srv.URL, "")
	third.ResponseAuthorizationToken = "third-synthetic-token"
	require.NoError(t, third.SendResponse(t.Context(), bufferedResponse(), nil))
	got = <-seen
	require.Equal(t, preparedAddress, got.remote)
	require.Equal(t, "Bearer third-synthetic-token", got.auth)
}

func TestCallbackPreconnectLongInferenceAndPeerCloseFallback(t *testing.T) {
	for _, mode := range []string{"expiry", "peer-close", "setup-failure"} {
		t.Run(mode, func(t *testing.T) {
			var posts atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			client, tr := localCallbackClient(t, srv, true, 40*time.Millisecond)
			if mode == "setup-failure" {
				tr.pool.dial = func(context.Context, callbackRoute) (net.Conn, error) { return nil, context.DeadlineExceeded }
			}
			h := localHandler(t, client, srv.URL, "")
			if mode == "peer-close" {
				req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
				route, _ := tr.route(req)
				e := entryFor(t, tr.pool, route.key)
				require.NoError(t, e.err)
				srv.CloseClientConnections()
			}
			require.Eventually(t, func() bool { return poolSize(tr.pool) == 0 }, 2*time.Second, time.Millisecond)
			require.NoError(t, h.SendResponse(t.Context(), bufferedResponse(), nil))
			require.EqualValues(t, 1, posts.Load())
		})
	}
}

func TestCallbackPreconnectTLSVerificationAndCallbackErrors(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	untrusted := newCallbackTransport(&http.Transport{}, defaultPreconnectConfig())
	untrustedClient := &http.Client{Transport: untrusted}
	defer untrustedClient.CloseIdleConnections()
	h := localHandler(t, untrustedClient, srv.URL, "")
	require.ErrorContains(t, h.SendResponse(t.Context(), bufferedResponse(), nil), "certificate")
	require.Zero(t, posts.Load())
	client, _ := localCallbackClient(t, srv, true, 0)
	h = localHandler(t, client, srv.URL, "")
	require.ErrorContains(t, h.SendResponse(t.Context(), bufferedResponse(), nil), "503")
	require.EqualValues(t, 1, posts.Load())
}

func TestCallbackPreconnectControlledOverlap(t *testing.T) {
	if testing.Short() {
		t.Skip("controlled latency experiment")
	}
	const setupDelay = 150 * time.Millisecond
	const inferenceDelay = 200 * time.Millisecond
	var baseline, prepared []time.Duration
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	for i := 0; i < 5; i++ {
		for _, enabled := range []bool{false, true} {
			client, _ := localCallbackClient(t, srv, false, 0)
			base := client.Transport.(*http.Transport)
			dialer := &net.Dialer{}
			base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				select {
				case <-time.After(setupDelay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return dialer.DialContext(ctx, network, address)
			}
			if enabled {
				client.Transport = newCallbackTransport(base, defaultPreconnectConfig())
			}
			h := localHandler(t, client, srv.URL, "")
			var entry *preparedConnection
			var transport *callbackTransport
			if enabled {
				transport = client.Transport.(*callbackTransport)
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, nil)
				require.NoError(t, err)
				route, ok := transport.route(request)
				require.True(t, ok)
				transport.pool.mu.Lock()
				entry = transport.pool.entries[route.key]
				transport.pool.mu.Unlock()
				require.NotNil(t, entry)
			}
			time.Sleep(inferenceDelay)
			start := time.Now()
			require.NoError(t, h.SendResponse(t.Context(), bufferedResponse(), nil))
			duration := time.Since(start)
			if enabled {
				transport.pool.mu.Lock()
				claimed := entry.claimed
				transport.pool.mu.Unlock()
				require.True(t, claimed, "callback did not use its preparation")
				prepared = append(prepared, duration)
			} else {
				baseline = append(baseline, duration)
			}
			_ = h.Close()
			client.CloseIdleConnections()
		}
	}
	slices.Sort(baseline)
	slices.Sort(prepared)
	t.Logf("LOCAL SYNTHETIC ONLY: n=5 per condition; injected setup=%s; simulated inference=%s; callback median disabled=%s enabled=%s; overlap savings=%s", setupDelay, inferenceDelay, baseline[2], prepared[2], baseline[2]-prepared[2])
	// Report timing without making CI correctness depend on scheduler noise.
}
