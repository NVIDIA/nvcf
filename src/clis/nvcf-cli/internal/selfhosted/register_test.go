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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted/controlplaneprofile"

	"k8s.io/client-go/rest"
)

type fakeClusterClient struct {
	registerCalls int
	resp          *RegisterResponse
	err           error
}

func TestSelectControlPlaneProfileEndpointScope_ComputeReachableForDifferentCluster(t *testing.T) {
	doc := testRegisterProfile("control-plane")

	got, err := SelectControlPlaneProfileEndpointScope(doc, "gpu-a")
	require.NoError(t, err)

	assert.Equal(t, EndpointScopeComputeReachable, got.Name)
	assert.Equal(t, "https://sis.example.test", got.Endpoints.ICMSURL)
}

func TestSelectControlPlaneProfileEndpointScope_InClusterForControlPlaneCluster(t *testing.T) {
	doc := testRegisterProfile("control-plane")

	got, err := SelectControlPlaneProfileEndpointScope(doc, "control-plane")
	require.NoError(t, err)

	assert.Equal(t, EndpointScopeInCluster, got.Name)
	assert.Equal(t, "http://api.sis.svc.cluster.local:8080", got.Endpoints.ICMSURL)
}

func TestSelectControlPlaneProfileEndpointScope_RequiresClusterName(t *testing.T) {
	_, err := SelectControlPlaneProfileEndpointScope(testRegisterProfile("control-plane"), "")
	assert.ErrorContains(t, err, "cluster name is required")
}

func testRegisterProfile(controlPlaneCluster string) controlplaneprofile.ControlPlaneProfile {
	return controlplaneprofile.ControlPlaneProfile{
		APIVersion: controlplaneprofile.APIVersion,
		Kind:       controlplaneprofile.Kind,
		ControlPlane: controlplaneprofile.ControlPlane{
			ClusterName: controlPlaneCluster,
			NCAID:       "nvcf-default",
			Region:      "us-west-1",
			Endpoints: controlplaneprofile.Endpoints{
				InCluster: controlplaneprofile.EndpointScope{
					ICMSURL:  "http://api.sis.svc.cluster.local:8080",
					ReValURL: "http://reval.nvcf.svc.cluster.local:8080",
					NATSURL:  "nats://nats.nats-system.svc.cluster.local:4222",
				},
				ComputeReachable: controlplaneprofile.EndpointScope{
					ICMSURL:  "https://sis.example.test",
					ReValURL: "https://reval.example.test",
					NATSURL:  "tls://nats.example.test:4222",
				},
			},
		},
	}
}

func (f *fakeClusterClient) RegisterCluster(_ context.Context, in RegisterRequest) (*RegisterResponse, error) {
	f.registerCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeClusterClient) Close() error { return nil }

func TestRegisterCluster_HappyPath(t *testing.T) {
	c := &fakeClusterClient{resp: &RegisterResponse{ClusterID: "abc", ClusterGroupID: "grp"}}
	got, err := c.RegisterCluster(context.Background(), RegisterRequest{
		ClusterName: "my-cluster", NCAID: "nvcf-default", Region: "us-west-1",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, c.registerCalls)
	assert.Equal(t, "abc", got.ClusterID)
}

func TestRegisterCluster_PropagatesError(t *testing.T) {
	c := &fakeClusterClient{err: errors.New("boom")}
	_, err := c.RegisterCluster(context.Background(), RegisterRequest{ClusterName: "x"})
	assert.ErrorContains(t, err, "boom")
}

// fakeLister is a test double for the clusterLister interface used by
// resolveExistingCluster.
type fakeLister struct {
	clusters []client.ICMSCluster
	err      error
}

func (f *fakeLister) ListClusters(_ context.Context, _, _ string) ([]client.ICMSCluster, error) {
	return f.clusters, f.err
}

func TestResolveExistingCluster_FoundByName(t *testing.T) {
	l := &fakeLister{clusters: []client.ICMSCluster{
		{ClusterName: "other", ClusterID: "id-other"},
		{ClusterName: "wanted", ClusterID: "id-wanted", ClusterGroupID: "grp-wanted"},
	}}
	r, err := resolveExistingCluster(context.Background(), l, "url", "nca", "wanted")
	require.NoError(t, err)
	assert.Equal(t, "id-wanted", r.ClusterID)
	assert.Equal(t, "grp-wanted", r.ClusterGroupID)
}

func TestResolveExistingCluster_NotFound(t *testing.T) {
	l := &fakeLister{clusters: []client.ICMSCluster{{ClusterName: "other"}}}
	_, err := resolveExistingCluster(context.Background(), l, "url", "nca", "missing")
	assert.ErrorContains(t, err, "missing")
}

func TestResolveExistingCluster_ListError(t *testing.T) {
	l := &fakeLister{err: errors.New("list failed")}
	_, err := resolveExistingCluster(context.Background(), l, "url", "nca", "x")
	assert.ErrorContains(t, err, "list failed")
}

func TestClusterClientAdapter_RegisterExistingUpdatesJWKS(t *testing.T) {
	var updateCalled bool
	var gotUpdate client.UpdateClusterJWKSRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/accounts/nca/clusters":
			http.Error(w, "cluster already exists", http.StatusConflict)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/accounts/nca/clusters":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"clusterName":"wanted","clusterId":"cl-existing","clusterGroupId":"cg-existing"}]`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/nvca/clusters/cl-existing/jwks":
			updateCalled = true
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotUpdate))
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := &client.Config{
		AuthType:       client.AuthTypeBearer,
		Token:          "admin-token",
		ClientID:       "nca",
		BaseHTTPURL:    server.URL,
		BaseGRPCURL:    "localhost:1",
		DefaultTimeout: time.Second,
	}
	inner, err := client.NewClient(cfg)
	require.NoError(t, err)
	defer inner.Close()

	adapter := &clusterClientAdapter{inner: inner, sisURL: server.URL, cfg: cfg}
	resp, err := adapter.RegisterCluster(context.Background(), RegisterRequest{
		ClusterName: "wanted",
		NCAID:       "nca",
		JWKS:        `{"keys":[{"kid":"new"}]}`,
		OIDCIssuer:  "https://issuer.example.com",
	})
	require.NoError(t, err)

	assert.Equal(t, "cl-existing", resp.ClusterID)
	assert.Equal(t, "cg-existing", resp.ClusterGroupID)
	assert.True(t, updateCalled, "existing cluster registration should refresh JWKS")
	assert.Equal(t, `{"keys":[{"kid":"new"}]}`, gotUpdate.JWKS)
	require.NotNil(t, gotUpdate.OIDCIssuer)
	assert.Equal(t, "https://issuer.example.com", *gotUpdate.OIDCIssuer)
}

// TestClusterClientAdapter_DeleteCluster_EmptyClientIDFailsLoud verifies that
// an unset NCA ID is rejected up front. Without the guard, SIS would answer
// 404 for /v1/accounts//clusters/{id} and clusterDeleteNotFound would treat
// that as "already gone", leaving the row live while the caller sees success.
func TestClusterClientAdapter_DeleteCluster_EmptyClientIDFailsLoud(t *testing.T) {
	var requested bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		t.Fatalf("adapter should not issue a request when ClientID is empty (got %s %s)", r.Method, r.URL.Path)
	}))
	defer server.Close()

	cfg := &client.Config{
		AuthType:       client.AuthTypeBearer,
		Token:          "admin-token",
		ClientID:       "",
		BaseHTTPURL:    server.URL,
		BaseGRPCURL:    "localhost:1",
		DefaultTimeout: time.Second,
	}
	inner, err := client.NewClient(cfg)
	require.NoError(t, err)
	defer inner.Close()

	adapter := &clusterClientAdapter{inner: inner, sisURL: server.URL, cfg: cfg}
	err = adapter.DeleteCluster(context.Background(), "cl-zombie")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NCA ID")
	assert.False(t, requested, "no HTTP request should be issued when ClientID is empty")
}

// TestLoadKubeConfigFn_SeamIsReplaceable verifies that loadKubeConfigFn is a
// package-level variable that callers can replace in tests without touching a
// real kubeconfig file.
func TestLoadKubeConfigFn_SeamIsReplaceable(t *testing.T) {
	var capturedKctx string
	prev := loadKubeConfigFn
	t.Cleanup(func() { loadKubeConfigFn = prev })

	loadKubeConfigFn = func(kctx string) (*rest.Config, error) {
		capturedKctx = kctx
		return &rest.Config{Host: "https://fake-server:6443"}, nil
	}

	cfg, err := loadKubeConfigFn("admin@cp")
	require.NoError(t, err)
	assert.Equal(t, "admin@cp", capturedKctx,
		"loadKubeConfigFn should receive the kctx argument")
	assert.Equal(t, "https://fake-server:6443", cfg.Host)
}

// TestLoadKubeConfigFn_EmptyKctxIsPassedThrough verifies that an empty kctx
// is forwarded as-is (clientcmd treats empty string as "use current-context").
func TestLoadKubeConfigFn_EmptyKctxIsPassedThrough(t *testing.T) {
	var capturedKctx string
	prev := loadKubeConfigFn
	t.Cleanup(func() { loadKubeConfigFn = prev })

	loadKubeConfigFn = func(kctx string) (*rest.Config, error) {
		capturedKctx = kctx
		return &rest.Config{Host: "https://default:6443"}, nil
	}

	cfg, err := loadKubeConfigFn("")
	require.NoError(t, err)
	assert.Equal(t, "", capturedKctx,
		"empty kctx should be passed through unchanged")
	assert.Equal(t, "https://default:6443", cfg.Host)
}

// TestClusterClientAdapter_TokenEmitsBearerOnRegister verifies the wiring that
// the --token flag (selfHostedToken in cmd/) propagates through to the SIS
// register call as an Authorization: Bearer header. Regression for the bug
// where --token=$JWT skipped nvcf-cli init but the resulting register POST
// still went out unauthenticated, returning 401.
func TestClusterClientAdapter_TokenEmitsBearerOnRegister(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/accounts/nca/clusters" {
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"clusterId":"cl-1","clusterGroupId":"cg-1"}`))
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	cfg := &client.Config{
		AuthType:       client.AuthTypeBearer,
		Token:          "test-jwt",
		ClientID:       "nca",
		BaseHTTPURL:    server.URL,
		BaseGRPCURL:    "localhost:1",
		DefaultTimeout: time.Second,
	}
	inner, err := client.NewClient(cfg)
	require.NoError(t, err)
	defer inner.Close()

	adapter := &clusterClientAdapter{inner: inner, sisURL: server.URL, cfg: cfg}
	_, err = adapter.RegisterCluster(context.Background(), RegisterRequest{
		ClusterName: "wanted",
		NCAID:       "nca",
		Region:      "us-west-1",
		JWKS:        `{"keys":[]}`,
		OIDCIssuer:  "https://issuer.example.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "Bearer test-jwt", gotAuth,
		"register POST must carry the admin JWT as Bearer when Config.Token is set")
}

func TestNewClusterClient_HitsRealSIS(t *testing.T) {
	if os.Getenv("NVCF_E2E") == "" {
		t.Skip("set NVCF_E2E=1 with a working control plane to run")
	}
	// Construct the real client from environment / config.
	// LoadConfig reads NVCF_* env vars and the ~/.nvcf.yaml config file.
	// Pass empty string to exercise the config-fallback path for sisURL.
	c, err := NewClusterClient("")
	if err != nil {
		t.Fatalf("NewClusterClient: %v", err)
	}
	defer c.Close()
	got, err := c.RegisterCluster(context.Background(), RegisterRequest{
		ClusterName: "rci-test",
		NCAID:       "nvcf-default",
		Region:      "us-west-1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, got.ClusterID)
}
