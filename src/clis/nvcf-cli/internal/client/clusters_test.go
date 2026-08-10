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

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stringPtr(s string) *string { return &s }

func newTestClient(httpClient *http.Client) *Client {
	return &Client{
		httpClient: httpClient,
	}
}

func TestMakeSISRequestPreservesSISHostWithAPIHostOverride(t *testing.T) {
	var receivedHost string
	var receivedContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		receivedContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// The host header transport is configured for a *different* base API URL
	// (api.different.example), so it must leave SIS requests (going to the
	// httptest server) untouched.
	c := newTestClient(&http.Client{
		Transport: newHostHeaderTransport("base.different.example", "api.different.example", false, server.Client().Transport),
	})

	resp, err := c.makeICMSRequest(context.Background(), "POST", server.URL, "/v1/accounts/test-nca/clusters", map[string]string{"clusterName": "test"})
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, resp.Request.URL.Host, receivedHost)
	assert.NotEqual(t, "api.different.example", receivedHost)
	assert.Equal(t, "application/json", receivedContentType)
}

func TestMakeSISRequestOverridesHostHeaderWithICMSHostConfig(t *testing.T) {
	// Gateway-routed self-hosted: icms_url dials the bare ELB but the gateway
	// HTTPRoute only matches Host: sis.<elb>. Setting Config.ICMSHost must
	// rewrite the Host header without changing the dialed URL.
	var receivedHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := newTestClient(server.Client())
	c.config = &Config{ICMSHost: "sis.bare-elb.example.com"}

	resp, err := c.makeICMSRequest(context.Background(), "GET", server.URL, "/v1/accounts/nca-1/clusters", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "sis.bare-elb.example.com", receivedHost)
	assert.NotEqual(t, resp.Request.URL.Host, receivedHost)
}

func TestRegisterCluster(t *testing.T) {
	t.Run("constructs correct request body and returns response", func(t *testing.T) {
		var receivedBody RegisterClusterRequest
		var receivedHost string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedHost = r.Host
			assert.Equal(t, "POST", r.Method)
			assert.Contains(t, r.URL.Path, "/v1/accounts/test-nca/clusters")
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &receivedBody))

			resp := RegisterClusterResponse{
				ClusterGroupID: "cg-123",
				ClusterID:      "cl-456",
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		jwks := `{"keys":[]}`
		issuer := "https://k8s.example.com"
		req := &RegisterClusterRequest{
			ClusterGroupName: "test-cluster",
			NcaID:            "test-nca",
			CloudProvider:    "ON-PREM",
			Region:           "us-west-1",
			JWKS:             &jwks,
			OIDCIssuer:       &issuer,
		}

		resp, err := c.RegisterCluster(context.Background(), server.URL, "test-nca", req)
		require.NoError(t, err)
		assert.Equal(t, "cg-123", resp.ClusterGroupID)
		assert.Equal(t, "cl-456", resp.ClusterID)
		assert.Equal(t, "test-cluster", receivedBody.ClusterGroupName)
		assert.NotEmpty(t, receivedHost)
	})

	t.Run("handles non-200 response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"forbidden"}`))
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		req := &RegisterClusterRequest{
			ClusterGroupName: "test",
			NcaID:            "nca-1",
		}

		resp, err := c.RegisterCluster(context.Background(), server.URL, "nca-1", req)
		assert.Nil(t, resp)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "ICMS API error 403")
	})

	t.Run("omits nil optional fields from JSON", func(t *testing.T) {
		var receivedRaw map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &receivedRaw)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(RegisterClusterResponse{})
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		req := &RegisterClusterRequest{
			ClusterGroupName: "test",
			NcaID:            "nca-1",
			CloudProvider:    "ON-PREM",
			Region:           "us-west-1",
			// JWKS and OIDCIssuer intentionally nil
		}

		_, err := c.RegisterCluster(context.Background(), server.URL, "nca-1", req)
		require.NoError(t, err)
		_, hasJWKS := receivedRaw["jwks"]
		_, hasIssuer := receivedRaw["oidcIssuer"]
		assert.False(t, hasJWKS, "nil JWKS should be omitted from JSON")
		assert.False(t, hasIssuer, "nil OIDCIssuer should be omitted from JSON")
	})
}

func TestUpdateClusterJWKS(t *testing.T) {
	t.Run("constructs correct request", func(t *testing.T) {
		var receivedBody UpdateClusterJWKSRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "PUT", r.Method)
			assert.Contains(t, r.URL.Path, "/v1/nvca/clusters/cl-789/jwks")

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &receivedBody))

			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		issuer := "https://k8s.example.com"
		req := &UpdateClusterJWKSRequest{
			JWKS:       `{"keys":[]}`,
			OIDCIssuer: &issuer,
		}

		err := c.UpdateClusterJWKS(context.Background(), server.URL, "cl-789", req)
		assert.NoError(t, err)
		assert.Equal(t, `{"keys":[]}`, receivedBody.JWKS)
	})

	t.Run("handles error response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		req := &UpdateClusterJWKSRequest{JWKS: `{}`}

		err := c.UpdateClusterJWKS(context.Background(), server.URL, "cl-789", req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "ICMS API error 500")
	})
}

func TestListClusters(t *testing.T) {
	t.Run("returns clusters for account", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Contains(t, r.URL.Path, "/v1/accounts/test-nca/clusters")
			assert.Equal(t, "application/json", r.Header.Get("Accept"))

			resp := []ICMSCluster{
				{ClusterID: "cl-1", ClusterName: "cluster-a", ClusterGroupID: "cg-1"},
				{ClusterID: "cl-2", ClusterName: "cluster-b", ClusterGroupID: "cg-2"},
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		clusters, err := c.ListClusters(context.Background(), server.URL, "test-nca")
		require.NoError(t, err)
		assert.Len(t, clusters, 2)
		assert.Equal(t, "cl-1", clusters[0].ClusterID)
		assert.Equal(t, "cluster-a", clusters[0].ClusterName)
		assert.Equal(t, "cg-1", clusters[0].ClusterGroupID)
		assert.Equal(t, "cl-2", clusters[1].ClusterID)
	})

	t.Run("returns empty list", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		clusters, err := c.ListClusters(context.Background(), server.URL, "empty-nca")
		require.NoError(t, err)
		assert.Empty(t, clusters)
	})

	t.Run("handles error response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"forbidden"}`))
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		clusters, err := c.ListClusters(context.Background(), server.URL, "bad-nca")
		assert.Nil(t, clusters)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "ICMS API error 403")
	})

	t.Run("sets Host header from SIS URL", func(t *testing.T) {
		var receivedHost string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedHost = r.Host
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		_, err := c.ListClusters(context.Background(), server.URL, "host-test-nca")
		assert.NoError(t, err)
		assert.NotEmpty(t, receivedHost)
	})

	t.Run("parses optional enrichment fields when present", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"clusterId":"cl-1","clusterName":"a","nvcaVersion":"2.30.4","clusterStatus":"ACTIVE","nvcaLastConnected":"2026-05-30T10:00:00Z"}]`))
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		clusters, err := c.ListClusters(context.Background(), server.URL, "nca")
		require.NoError(t, err)
		require.Len(t, clusters, 1)
		assert.Equal(t, "2.30.4", clusters[0].NVCAVersion)
		assert.Equal(t, "ACTIVE", clusters[0].ClusterStatus)
		assert.Equal(t, "2026-05-30T10:00:00Z", clusters[0].NVCALastConnected)
	})

	t.Run("leaves enrichment fields empty when absent", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"clusterId":"cl-1","clusterName":"a"}]`))
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		clusters, err := c.ListClusters(context.Background(), server.URL, "nca")
		require.NoError(t, err)
		require.Len(t, clusters, 1)
		assert.Empty(t, clusters[0].NVCAVersion)
		assert.Empty(t, clusters[0].ClusterStatus)
	})
}

func TestDeleteCluster(t *testing.T) {
	t.Run("uses account-scoped endpoint", func(t *testing.T) {
		var receivedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedPath = r.URL.Path
			assert.Equal(t, "DELETE", r.Method)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		err := c.DeleteCluster(context.Background(), server.URL, "nca-test", "cl-delete")
		assert.NoError(t, err)
		// SIS rejects /v1/nvca/clusters/{id} with 404; the canonical route is
		// the account-scoped DELETE /v1/accounts/{ncaId}/clusters/{clusterId}.
		assert.Equal(t, "/v1/accounts/nca-test/clusters/cl-delete", receivedPath)
	})

	t.Run("escapes path segments", func(t *testing.T) {
		var receivedEscapedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedEscapedPath = r.URL.EscapedPath()
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		err := c.DeleteCluster(context.Background(), server.URL, "nca/with/slash", "cl id")
		assert.NoError(t, err)
		assert.Equal(t, "/v1/accounts/nca%2Fwith%2Fslash/clusters/cl%20id", receivedEscapedPath)
	})

	t.Run("handles error response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("not found"))
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		err := c.DeleteCluster(context.Background(), server.URL, "nca-test", "cl-missing")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "ICMS API error 404")
	})

	t.Run("sets Host header from SIS URL", func(t *testing.T) {
		var receivedHost string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedHost = r.Host
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		c := newTestClient(server.Client())
		err := c.DeleteCluster(context.Background(), server.URL, "nca-test", "cl-host-test")
		assert.NoError(t, err)
		assert.NotEmpty(t, receivedHost)
	})
}
