/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package httpstream

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// A shared utility can receive work from multiple invocation-service instances.
// Test real sockets and binary, non-LLM responses in both feature modes.
func TestCallbackPreconnectMultipleTargetsBinaryResponse(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			seen := []chan callbackObservation{make(chan callbackObservation, 1), make(chan callbackObservation, 1)}
			servers := make([]*httptest.Server, 2)
			for i := range servers {
				servers[i] = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					seen[i] <- callbackObservation{method: r.Method, auth: r.Header.Get("Authorization"), body: string(body), remote: r.RemoteAddr}
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(servers[i].Close)
			}
			client, transport := localCallbackClient(t, servers[0], enabled, 0)
			handlers := make([]*RequestStreamHandler, 2)
			preparedAddresses := make([]string, 2)
			for i := range handlers {
				handlers[i] = localHandler(t, client, servers[i].URL, "")
				handlers[i].ResponseAuthorizationToken = fmt.Sprintf("synthetic-target-%d", i)
				if enabled {
					req, err := http.NewRequest(http.MethodPost, servers[i].URL, nil)
					require.NoError(t, err)
					route, ok := transport.route(req)
					require.True(t, ok)
					entry := entryFor(t, transport.pool, route.key)
					require.NoError(t, entry.err)
					preparedAddresses[i] = entry.conn.LocalAddr().String()
				}
				require.Empty(t, seen[i], "preparation must not send application data")
			}
			errors := make(chan error, 2)
			for i := len(handlers) - 1; i >= 0; i-- {
				go func(i int) {
					payload := []byte{0, byte(i), 255, '\n', 128}
					response := &http.Response{StatusCode: http.StatusAccepted,
						Header:        http.Header{"Content-Type": {"application/octet-stream"}, "X-Job": {fmt.Sprint(i)}},
						ContentLength: int64(len(payload)), Body: io.NopCloser(bytes.NewReader(payload))}
					errors <- handlers[i].SendResponse(t.Context(), response, nil)
				}(i)
			}
			for range handlers {
				require.NoError(t, <-errors)
			}
			for i := range handlers {
				got := <-seen[i]
				require.Equal(t, http.MethodPost, got.method)
				require.Equal(t, fmt.Sprintf("Bearer synthetic-target-%d", i), got.auth)
				if enabled {
					require.Equal(t, preparedAddresses[i], got.remote, "socket crossed callback origins")
				}
				response, err := http.ReadResponse(bufio.NewReader(bytes.NewBufferString(got.body)), nil)
				require.NoError(t, err)
				require.Equal(t, http.StatusAccepted, response.StatusCode)
				require.Equal(t, "application/octet-stream", response.Header.Get("Content-Type"))
				require.Equal(t, fmt.Sprint(i), response.Header.Get("X-Job"))
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, []byte{0, byte(i), 255, '\n', 128}, body)
				require.Empty(t, seen[i], "callback was duplicated")
			}
		})
	}
}

func TestCallbackPreconnectProxyRoutesAndCredentialsStayIsolated(t *testing.T) {
	for _, sameProxy := range []bool{false, true} {
		t.Run(fmt.Sprintf("sameProxy=%t", sameProxy), func(t *testing.T) {
			seen := make(chan callbackObservation, 2)
			newProxy := func() *httptest.Server {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					seen <- callbackObservation{auth: r.Header.Get("Authorization"), proxyAuth: r.Header.Get("Proxy-Authorization"), remote: r.RemoteAddr, target: r.URL.String()}
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(server.Close)
				return server
			}
			proxies := []*httptest.Server{newProxy(), nil}
			proxies[1] = proxies[0]
			if !sameProxy {
				proxies[1] = newProxy()
			}
			client, transport := localCallbackClient(t, proxies[0], true, 0)
			handlers := make([]*RequestStreamHandler, 2)
			addresses := make([]string, 2)
			for i := range handlers {
				proxy, err := url.Parse(proxies[i].URL)
				require.NoError(t, err)
				proxy.User = url.UserPassword(fmt.Sprintf("synthetic-user-%d", i), "synthetic-password")
				handlers[i] = localHandler(t, client, "http://callback.invalid", proxy.String())
				handlers[i].ResponseAuthorizationToken = fmt.Sprintf("synthetic-response-%d", i)
				ctx := context.WithValue(t.Context(), proxyURLKey, proxy)
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, handlers[i].TargetURI, nil)
				require.NoError(t, err)
				route, ok := transport.route(req)
				require.True(t, ok)
				entry := entryFor(t, transport.pool, route.key)
				require.NoError(t, entry.err)
				addresses[i] = entry.conn.LocalAddr().String()
			}
			require.Empty(t, seen, "preparation sent an early callback")
			for i := len(handlers) - 1; i >= 0; i-- {
				require.NoError(t, handlers[i].SendResponse(t.Context(), bufferedResponse(), nil))
				got := <-seen
				require.Equal(t, addresses[i], got.remote, "socket crossed proxy identities")
				require.Equal(t, fmt.Sprintf("Bearer synthetic-response-%d", i), got.auth)
				proxy, err := url.Parse(proxies[i].URL)
				require.NoError(t, err)
				proxy.User = url.UserPassword(fmt.Sprintf("synthetic-user-%d", i), "synthetic-password")
				check := &http.Request{Header: make(http.Header)}
				password, _ := proxy.User.Password()
				check.SetBasicAuth(proxy.User.Username(), password)
				require.Equal(t, check.Header.Get("Authorization"), got.proxyAuth)
				require.Equal(t, "http://callback.invalid/v2/nvcf/worker/request-attach", got.target)
			}
			require.Empty(t, seen)
		})
	}
}
