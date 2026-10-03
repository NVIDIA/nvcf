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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeKubeconfig points KUBECONFIG at a file with one context, "test", for
// server.
func writeKubeconfig(t *testing.T, server string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: test
  context:
    cluster: test
    user: test
users:
- name: test
  user:
    token: test-token
current-context: test
`, server)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	t.Setenv("KUBECONFIG", path)
}

// A cluster the run cannot contact at all is unreachable: a context that does
// not exist, refused credentials, or a refused connection. A server that
// answers with an error is reachable; the probe after it reports that.
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
		"server error":        {server: status(http.StatusInternalServerError), context: "test"},
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
