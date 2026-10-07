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

package selfhosted

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeKubeconfig points KUBECONFIG at a file with one context, "test", for
// server, with a static token.
func writeKubeconfig(t *testing.T, server string) {
	t.Helper()
	writeKubeconfigFor(t, server, "    token: test-token\n")
}

// writeKubeconfigFor is writeKubeconfig with user, indented to sit under
// user:, as the credentials. client-go sends them only over TLS, so an https
// server's certificate is not verified.
func writeKubeconfigFor(t *testing.T, server, user string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
    insecure-skip-tls-verify: %t
contexts:
- name: test
  context:
    cluster: test
    user: test
users:
- name: test
  user:
%scurrent-context: test
`, server, strings.HasPrefix(server, "https://"), user)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	t.Setenv("KUBECONFIG", path)
}

// A cluster no API call succeeds against is unreachable, whatever the error: a
// context that does not exist, refused credentials, a refused connection, no
// answer, or a server error. Nothing about it was checked.
func TestConnectCluster_ClassifiesAClusterItCannotReach(t *testing.T) {
	status := func(code int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed.Close()
	release := make(chan struct{})
	silent := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(silent.Close)
	t.Cleanup(func() { close(release) })
	prev := clusterFirstCallTimeout
	clusterFirstCallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { clusterFirstCallTimeout = prev })

	for name, tc := range map[string]struct {
		server, context string
		unreachable     bool
	}{
		"missing context":     {server: status(http.StatusOK), context: "nope", unreachable: true},
		"rejected token":      {server: status(http.StatusUnauthorized), context: "test", unreachable: true},
		"refused connection":  {server: closed.URL, context: "test", unreachable: true},
		"no answer":           {server: silent.URL, context: "test", unreachable: true},
		"server error":        {server: status(http.StatusInternalServerError), context: "test", unreachable: true},
		"forbidden":           {server: status(http.StatusForbidden), context: "test", unreachable: true},
		"answers the request": {server: status(http.StatusOK), context: "test"},
	} {
		writeKubeconfig(t, tc.server)
		_, err := connectCluster(context.Background(), tc.context)
		var unreachable *ClusterUnreachableError
		assert.Equal(t, tc.unreachable, errors.As(err, &unreachable), "%s: %v", name, err)
		if !tc.unreachable {
			assert.NoError(t, err, name)
		}
	}
}

// A run whose own budget ends during the first call is not told the cluster
// is unreachable: the budget, not the cluster, stopped it.
func TestConnectCluster_SpentBudgetIsNotUnreachable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	writeKubeconfig(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := connectCluster(ctx, "test")
	var unreachable *ClusterUnreachableError
	assert.False(t, errors.As(err, &unreachable), "%v", err)
}

// credentialPlugin writes an exec credential plugin that runs the shell body
// and then returns the token body left in $token, and returns the kubeconfig
// user that runs it.
func credentialPlugin(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin")
	script := "#!/bin/sh\n" + body + "\nprintf '{\"apiVersion\":\"client.authentication.k8s.io/v1\"," +
		"\"kind\":\"ExecCredential\",\"status\":{\"token\":\"%s\"}}' \"$token\"\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return fmt.Sprintf(`    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: %s
      interactiveMode: Never
`, path)
}

// Credentials from a plugin are fetched within the first call, so its time
// does not count against the call's bound, and a cached token the server
// rejects is replaced and tried again. A plugin that fails gets no call
// through: the cluster is unreachable.
func TestConnectCluster_CredentialPlugin(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	prev := clusterFirstCallTimeout
	clusterFirstCallTimeout = 500 * time.Millisecond
	t.Cleanup(func() { clusterFirstCallTimeout = prev })

	for name, tc := range map[string]struct {
		body        string
		unreachable bool
	}{
		"slower than the bound": {body: "sleep 1; token=fresh"},
		"rotates a rejected token": {
			body: `token=fresh; if [ ! -e "$0.ran" ]; then : > "$0.ran"; token=stale; fi`,
		},
		"fails":                {body: "exit 1", unreachable: true},
		"token rejected twice": {body: "token=stale", unreachable: true},
	} {
		writeKubeconfigFor(t, srv.URL, credentialPlugin(t, tc.body))
		_, err := connectCluster(context.Background(), "test")
		var unreachable *ClusterUnreachableError
		assert.Equal(t, tc.unreachable, errors.As(err, &unreachable), "%s: %v", name, err)
		if !tc.unreachable {
			assert.NoError(t, err, name)
		}
	}
}

// With a credential plugin, the bound still covers the network once the
// credentials are in hand: an API server that takes the connection and never
// answers is unreachable after the bound, not after the run's whole budget.
func TestConnectCluster_CredentialPluginAgainstASilentServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	prev := clusterFirstCallTimeout
	clusterFirstCallTimeout = 500 * time.Millisecond
	t.Cleanup(func() { clusterFirstCallTimeout = prev })

	for name, body := range map[string]string{
		"fast plugin": "token=fresh",
		"slow plugin": "sleep 1; token=fresh",
	} {
		writeKubeconfigFor(t, srv.URL, credentialPlugin(t, body))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		start := time.Now()
		_, err := connectCluster(ctx, "test")
		cancel()
		var unreachable *ClusterUnreachableError
		assert.True(t, errors.As(err, &unreachable), "%s: %v", name, err)
		assert.Less(t, time.Since(start), 5*time.Second, name)
	}
}

// shortenFirstCallRetry makes the pause before the first call's retry short.
func shortenFirstCallRetry(t *testing.T) {
	t.Helper()
	prev := firstCallRetryDelay
	firstCallRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { firstCallRetryDelay = prev })
}

// A server error on the first call, without a Retry-After for client-go to
// honour, as from a load balancer while an API server rolls, gets one more
// try. A second failure is still unreachable.
func TestConnectCluster_RetriesAServerErrorOnce(t *testing.T) {
	shortenFirstCallRetry(t)
	for _, code := range []int{
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
	} {
		for _, failures := range []int32{1, 2} {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) <= failures {
					w.WriteHeader(code)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			writeKubeconfig(t, srv.URL)
			_, err := connectCluster(context.Background(), "test")
			srv.Close()
			var unreachable *ClusterUnreachableError
			assert.Equal(t, failures > 1, errors.As(err, &unreachable), "%d x%d: %v", code, failures, err)
			assert.EqualValues(t, 2, calls.Load(), "%d x%d", code, failures)
		}
	}
}

// A connection dropped on the first call is tried again, whether it drops
// before the response or part way through its body.
func TestConnectCluster_RetriesADroppedConnection(t *testing.T) {
	shortenFirstCallRetry(t)
	for name, drop := range map[string]func(net.Conn){
		"closed": func(c net.Conn) { _ = c.Close() },
		"reset": func(c net.Conn) {
			_ = c.(*net.TCPConn).SetLinger(0)
			_ = c.Close()
		},
		"closed in the body": func(c net.Conn) {
			_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n" +
				"Content-Length: 100\r\n\r\n{\"major\""))
			_ = c.Close()
		},
	} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				conn, _, err := http.NewResponseController(w).Hijack()
				require.NoError(t, err)
				drop(conn)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		writeKubeconfig(t, srv.URL)
		_, err := connectCluster(context.Background(), "test")
		srv.Close()
		assert.NoError(t, err, name)
		assert.EqualValues(t, 2, calls.Load(), name)
	}
}

// A body that follows its headers, as an HTTP/1.1 server may send a 401, is
// read within the first call's bound, before a credential plugin logs in
// again: a login slower than the bound does not cut off the body and leave the
// cluster unreachable.
func TestConnectCluster_SlowReloginAfterADelayedRejectionBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer fresh" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		http.NewResponseController(w).Flush()
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Unauthorized","code":401}`))
	}))
	t.Cleanup(srv.Close)
	prev := clusterFirstCallTimeout
	clusterFirstCallTimeout = 500 * time.Millisecond
	t.Cleanup(func() { clusterFirstCallTimeout = prev })

	writeKubeconfigFor(t, srv.URL, credentialPlugin(t,
		`if [ -e "$0.ran" ]; then sleep 1; token=fresh; else : > "$0.ran"; token=stale; fi`))
	_, err := connectCluster(context.Background(), "test")
	assert.NoError(t, err)
}
