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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
