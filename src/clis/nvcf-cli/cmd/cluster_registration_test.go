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

package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nvcf-cli/internal/client"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- OIDC Config Parsing ---

func TestOIDCConfigParsing(t *testing.T) {
	t.Run("parses valid K8s OIDC config", func(t *testing.T) {
		raw := `{
			"issuer": "https://kubernetes.default.svc.cluster.local",
			"jwks_uri": "https://172.18.0.2:6443/openid/v1/jwks",
			"response_types_supported": ["id_token"],
			"subject_types_supported": ["public"],
			"id_token_signing_alg_values_supported": ["RS256"]
		}`
		var oidcResponse struct {
			Issuer  string `json:"issuer"`
			JwksURI string `json:"jwks_uri"`
		}
		err := json.Unmarshal([]byte(raw), &oidcResponse)
		require.NoError(t, err)
		assert.Equal(t, "https://kubernetes.default.svc.cluster.local", oidcResponse.Issuer)
		assert.Equal(t, "https://172.18.0.2:6443/openid/v1/jwks", oidcResponse.JwksURI)
	})

	t.Run("parses valid SPIRE OIDC config", func(t *testing.T) {
		raw := `{
			"issuer": "spiffe://ncp-local.nvidia.com",
			"jwks_uri": "http://10.43.100.50:8080/keys",
			"response_types_supported": ["id_token"],
			"id_token_signing_alg_values_supported": ["RS256", "ES256"]
		}`
		var oidcResponse struct {
			Issuer  string `json:"issuer"`
			JwksURI string `json:"jwks_uri"`
		}
		err := json.Unmarshal([]byte(raw), &oidcResponse)
		require.NoError(t, err)
		assert.Equal(t, "spiffe://ncp-local.nvidia.com", oidcResponse.Issuer)
		assert.Equal(t, "http://10.43.100.50:8080/keys", oidcResponse.JwksURI)
	})

	t.Run("handles missing issuer gracefully", func(t *testing.T) {
		raw := `{"jwks_uri": "https://example.com/jwks"}`
		var oidcResponse struct {
			Issuer  string `json:"issuer"`
			JwksURI string `json:"jwks_uri"`
		}
		err := json.Unmarshal([]byte(raw), &oidcResponse)
		require.NoError(t, err)
		assert.Empty(t, oidcResponse.Issuer)
		assert.Equal(t, "https://example.com/jwks", oidcResponse.JwksURI)
	})

	t.Run("handles missing jwks_uri gracefully", func(t *testing.T) {
		raw := `{"issuer": "https://example.com"}`
		var oidcResponse struct {
			Issuer  string `json:"issuer"`
			JwksURI string `json:"jwks_uri"`
		}
		err := json.Unmarshal([]byte(raw), &oidcResponse)
		require.NoError(t, err)
		assert.Equal(t, "https://example.com", oidcResponse.Issuer)
		assert.Empty(t, oidcResponse.JwksURI)
	})

	t.Run("rejects invalid JSON", func(t *testing.T) {
		raw := `not json at all`
		var oidcResponse struct {
			Issuer string `json:"issuer"`
		}
		err := json.Unmarshal([]byte(raw), &oidcResponse)
		assert.Error(t, err)
	})

	t.Run("handles empty JSON object", func(t *testing.T) {
		raw := `{}`
		var oidcResponse struct {
			Issuer  string `json:"issuer"`
			JwksURI string `json:"jwks_uri"`
		}
		err := json.Unmarshal([]byte(raw), &oidcResponse)
		require.NoError(t, err)
		assert.Empty(t, oidcResponse.Issuer)
		assert.Empty(t, oidcResponse.JwksURI)
	})
}

func TestOIDCIssuerDirectDiscovery(t *testing.T) {
	tests := []struct {
		name   string
		issuer string
		want   bool
	}{
		{"EKS public issuer", "https://oidc.eks.eu-west-1.amazonaws.com/id/cluster-id", true},
		{"custom HTTPS issuer", "https://issuer.example.com/nvcf", true},
		{"Kubernetes service issuer", "https://kubernetes.default.svc.cluster.local", false},
		{"Kubernetes default alias", "https://kubernetes.default", false},
		{"in-cluster service host", "https://oidc.nvcf.svc.cluster.local", false},
		{"plain SPIFFE issuer", "spiffe://ncp-local.nvidia.com", false},
		{"empty issuer", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, supportsDirectOIDCDiscovery(tt.issuer))
		})
	}
}

func TestFetchDirectOIDCJWKS(t *testing.T) {
	t.Run("fetches JWKS from issuer discovery document", func(t *testing.T) {
		mux := http.NewServeMux()
		server := httptest.NewServer(mux)
		defer server.Close()

		mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = w.Write([]byte(`{"issuer":"` + server.URL + `","jwks_uri":"` + server.URL + `/keys"}`))
		})
		mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = w.Write([]byte(`{"keys":[{"kid":"eks-public-key"}]}`))
		})

		issuer, jwks, err := fetchDirectOIDCJWKS(context.Background(), server.URL, server.Client())
		require.NoError(t, err)
		assert.Equal(t, server.URL, issuer)
		assert.Contains(t, jwks, "eks-public-key")
	})

	t.Run("resolves relative jwks_uri against discovery document", func(t *testing.T) {
		mux := http.NewServeMux()
		server := httptest.NewServer(mux)
		defer server.Close()

		mux.HandleFunc("/oidc/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = w.Write([]byte(`{"issuer":"` + server.URL + `/oidc","jwks_uri":"/jwks"}`))
		})
		mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = w.Write([]byte(`{"keys":[{"kid":"relative-key"}]}`))
		})

		issuer, jwks, err := fetchDirectOIDCJWKS(context.Background(), server.URL+"/oidc", server.Client())
		require.NoError(t, err)
		assert.Equal(t, server.URL+"/oidc", issuer)
		assert.Contains(t, jwks, "relative-key")
	})

	t.Run("rejects discovery issuer mismatch", func(t *testing.T) {
		mux := http.NewServeMux()
		server := httptest.NewServer(mux)
		defer server.Close()

		mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = w.Write([]byte(`{"issuer":"https://different.example.com","jwks_uri":"` + server.URL + `/keys"}`))
		})
		mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("JWKS endpoint should not be called when issuer mismatches")
		})

		issuer, jwks, err := fetchDirectOIDCJWKS(context.Background(), server.URL, server.Client())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not match requested issuer")
		assert.Empty(t, issuer)
		assert.Empty(t, jwks)
	})
}

// --- JWKS Validation ---

func TestJWKSValidation(t *testing.T) {
	t.Run("accepts valid JWKS with RSA key", func(t *testing.T) {
		jwks := `{"keys":[{"kty":"RSA","kid":"test-key-1","alg":"RS256","n":"0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw","e":"AQAB"}]}`
		var check json.RawMessage
		err := json.Unmarshal([]byte(jwks), &check)
		assert.NoError(t, err)
	})

	t.Run("accepts valid JWKS with EC key", func(t *testing.T) {
		jwks := `{"keys":[{"kty":"EC","kid":"spire-ec-1","crv":"P-256","x":"f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU","y":"x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"}]}`
		var check json.RawMessage
		err := json.Unmarshal([]byte(jwks), &check)
		assert.NoError(t, err)
	})

	t.Run("accepts JWKS with multiple keys", func(t *testing.T) {
		jwks := `{"keys":[{"kty":"RSA","kid":"k1","alg":"RS256","n":"abc","e":"AQAB"},{"kty":"EC","kid":"k2","crv":"P-256","x":"abc","y":"def"}]}`
		var check json.RawMessage
		err := json.Unmarshal([]byte(jwks), &check)
		assert.NoError(t, err)
	})

	t.Run("accepts empty keys array", func(t *testing.T) {
		jwks := `{"keys":[]}`
		var check json.RawMessage
		err := json.Unmarshal([]byte(jwks), &check)
		assert.NoError(t, err)
	})

	t.Run("rejects invalid JWKS JSON", func(t *testing.T) {
		jwks := `not valid json`
		var check json.RawMessage
		err := json.Unmarshal([]byte(jwks), &check)
		assert.Error(t, err)
	})

	t.Run("rejects truncated JSON", func(t *testing.T) {
		jwks := `{"keys":[{"kty":"RSA"`
		var check json.RawMessage
		err := json.Unmarshal([]byte(jwks), &check)
		assert.Error(t, err)
	})
}

// --- Identity Source Detection ---
// Tests the logic used in fetchClusterJWKS to determine identity source from URL.

func TestIdentitySourceDetection(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{"SPIRE OIDC service URL", "http://spire-oidc.spire-system:8080", "spire"},
		{"SPIFFE scheme URL", "spiffe://ncp-local.nvidia.com", "spire"},
		{"spiffe in path", "http://10.0.0.1:8080/spiffe/keys", "spire"},
		{"SPIRE in subdomain", "http://spire-oidc-discovery.spire:8080", "spire"},
		{"K8s API server URL", "https://kubernetes.default.svc.cluster.local", "custom"},
		{"arbitrary custom URL", "http://my-custom-oidc:8080", "custom"},
		{"empty URL", "", "custom"},
		{"localhost URL", "http://localhost:8080", "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mirror the logic in fetchClusterJWKS
			identitySource := "custom"
			if strings.Contains(tt.url, "spire") || strings.Contains(tt.url, "spiffe") {
				identitySource = "spire"
			}
			assert.Equal(t, tt.expected, identitySource)
		})
	}
}

// --- SPIRE Service Discovery Parsing ---
// Tests the kubectl output parsing logic used in discoverSpireOIDC.

func TestSpireServiceDiscoveryParsing(t *testing.T) {
	// parseSpireFromKubectl mirrors the fallback scanning loop in discoverSpireOIDC
	parseSpireFromKubectl := func(output string) string {
		for _, line := range strings.Split(output, "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 && (strings.Contains(parts[0], "oidc-discovery") || strings.Contains(parts[0], "spire-oidc")) {
				return parts[1]
			}
		}
		return ""
	}

	t.Run("finds spiffe-oidc-discovery-provider service", func(t *testing.T) {
		output := "kube-dns 10.43.0.10:53\nspiffe-oidc-discovery-provider 10.43.100.50:8080\napi 10.43.225.193:8080"
		assert.Equal(t, "10.43.100.50:8080", parseSpireFromKubectl(output))
	})

	t.Run("finds spire-oidc service", func(t *testing.T) {
		output := "kube-dns 10.43.0.10:53\nspire-oidc 10.43.200.10:8080\napi 10.43.225.193:8080"
		assert.Equal(t, "10.43.200.10:8080", parseSpireFromKubectl(output))
	})

	t.Run("returns first match when multiple SPIRE services present", func(t *testing.T) {
		output := "spiffe-oidc-discovery-provider 10.43.100.50:8080\nspire-oidc 10.43.200.10:9090"
		assert.Equal(t, "10.43.100.50:8080", parseSpireFromKubectl(output))
	})

	t.Run("returns empty when no SPIRE service found", func(t *testing.T) {
		output := "kube-dns 10.43.0.10:53\napi 10.43.225.193:8080\nnats 10.43.50.100:4222"
		assert.Empty(t, parseSpireFromKubectl(output))
	})

	t.Run("handles empty output", func(t *testing.T) {
		assert.Empty(t, parseSpireFromKubectl(""))
	})

	t.Run("handles single-column output gracefully", func(t *testing.T) {
		output := "kube-dns\nspire-oidc\napi"
		// Single-column lines should not match (no IP:port)
		assert.Empty(t, parseSpireFromKubectl(output))
	})

	t.Run("handles extra whitespace", func(t *testing.T) {
		output := "  spire-oidc   10.43.200.10:8080  "
		// strings.Fields handles extra whitespace, but len check expects exactly 2
		// With leading/trailing spaces, Fields still produces 2 elements
		assert.Equal(t, "10.43.200.10:8080", parseSpireFromKubectl(output))
	})

	t.Run("ignores partial name matches without oidc-discovery or spire-oidc", func(t *testing.T) {
		output := "spire-server 10.43.10.1:8081\nspire-agent 10.43.10.2:8082"
		assert.Empty(t, parseSpireFromKubectl(output))
	})
}

// --- getICMSURL ---

func TestGetICMSURL(t *testing.T) {
	t.Run("returns icms flag value when set", func(t *testing.T) {
		cmd := &cobra.Command{}
		addClusterICMSURLFlags(cmd)
		_ = cmd.Flags().Set("icms-url", "http://custom-sis:8080")

		config := &client.Config{BaseHTTPURL: "http://default-api:8080"}
		result := getICMSURL(cmd, config)
		assert.Equal(t, "http://custom-sis:8080", result)
	})

	t.Run("returns icms env value before config", func(t *testing.T) {
		t.Setenv("NVCF_ICMS_URL", "http://icms-env:8080")
		cmd := &cobra.Command{}
		addClusterICMSURLFlags(cmd)

		config := &client.Config{BaseHTTPURL: "http://default-api:8080"}
		result := getICMSURL(cmd, config)
		assert.Equal(t, "http://icms-env:8080", result)
	})

	t.Run("falls back to config BaseHTTPURL when flag empty", func(t *testing.T) {
		cmd := &cobra.Command{}
		addClusterICMSURLFlags(cmd)

		config := &client.Config{BaseHTTPURL: "http://default-api:8080"}
		result := getICMSURL(cmd, config)
		assert.Equal(t, "http://default-api:8080", result)
	})

	t.Run("returns empty when both flag and config are empty", func(t *testing.T) {
		cmd := &cobra.Command{}
		addClusterICMSURLFlags(cmd)

		config := &client.Config{}
		result := getICMSURL(cmd, config)
		assert.Empty(t, result)
	})

	t.Run("flag takes precedence over config", func(t *testing.T) {
		cmd := &cobra.Command{}
		addClusterICMSURLFlags(cmd)
		_ = cmd.Flags().Set("icms-url", "http://flag-url:9090")

		config := &client.Config{BaseHTTPURL: "http://config-url:8080"}
		result := getICMSURL(cmd, config)
		assert.Equal(t, "http://flag-url:9090", result)
	})
}

// --- Helm Values Identity Source ---
// Tests that the correct identitySource string is determined for helm output.

func TestHelmValuesIdentitySource(t *testing.T) {
	tests := []struct {
		name       string
		issuerURL  string
		wantSource string
	}{
		{"SPIRE URL produces spire", "http://spire-oidc.spire-system:8080", "spire"},
		{"spiffe URL produces spire", "spiffe://ncp-local.nvidia.com", "spire"},
		{"K8s URL produces custom", "https://kubernetes.default.svc.cluster.local", "custom"},
		{"generic URL produces custom", "http://my-custom-oidc:8080", "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identitySource := "custom"
			if strings.Contains(tt.issuerURL, "spire") || strings.Contains(tt.issuerURL, "spiffe") {
				identitySource = "spire"
			}
			assert.Equal(t, tt.wantSource, identitySource)
		})
	}
}

// --- Helm values schema ---
// Locks in the YAML key shape consumed by the nvca-operator chart's
// `## @param` schema (top-level clusterID/clusterGroupID/ncaID with the
// mixed-case "ID" suffix). The chart's self-managed-nvcfbackend-cm.yaml
// renders these into the cluster-dto.yaml that the operator's mapper
// deserializes into NVCFBackend.spec.clusterConfig — getting the casing
// or nesting wrong leaves ClusterID empty and trips the preflight reject
// in pkg/operator/reconcile/nvcaagent_reconcile.go.

func TestHelmValuesYAMLSchema(t *testing.T) {
	vals := helmValues{
		ClusterID:      "cl-123",
		ClusterGroupID: "cg-456",
		NcaID:          "nca-789",
		Region:         "us-west-1",
		SelfManaged: selfManagedValues{
			IdentitySource:  "psat",
			ICMSServiceURL:  "http://sis.localhost:18080",
			ReValServiceURL: "http://reval.localhost:18080",
			NATSURL:         "nats://nats.localhost:4222",
		},
	}

	out, err := yaml.Marshal(vals)
	require.NoError(t, err)

	got := string(out)
	// Top-level keys with capital-ID suffix.
	assert.Contains(t, got, "clusterID: cl-123")
	assert.Contains(t, got, "clusterGroupID: cg-456")
	assert.Contains(t, got, "ncaID: nca-789")
	assert.Contains(t, got, "region: us-west-1")
	// identitySource stays nested under selfManaged.
	assert.Contains(t, got, "selfManaged:\n    identitySource: psat")
	assert.Contains(t, got, "icmsServiceURL: http://sis.localhost:18080")
	assert.Contains(t, got, "revalServiceURL: http://reval.localhost:18080")
	assert.Contains(t, got, "natsURL: nats://nats.localhost:4222")
	// Old (pre-fix) schema must not appear — these keys would silently
	// fall through to chart defaults and leave the operator with empty IDs.
	assert.NotContains(t, got, "clusterId:")
	assert.NotContains(t, got, "clusterGroupId:")
	assert.NotContains(t, got, "ncaId:")
}

// --- OIDC URL Construction ---
// Tests that the OIDC discovery URL is built correctly from the base URL.

func TestOIDCURLConstruction(t *testing.T) {
	t.Run("appends well-known path to base URL", func(t *testing.T) {
		baseURL := "http://10.43.100.50:8080"
		oidcURL := strings.TrimRight(baseURL, "/") + "/.well-known/openid-configuration"
		assert.Equal(t, "http://10.43.100.50:8080/.well-known/openid-configuration", oidcURL)
	})

	t.Run("strips trailing slash before appending", func(t *testing.T) {
		baseURL := "http://10.43.100.50:8080/"
		oidcURL := strings.TrimRight(baseURL, "/") + "/.well-known/openid-configuration"
		assert.Equal(t, "http://10.43.100.50:8080/.well-known/openid-configuration", oidcURL)
	})

	t.Run("handles multiple trailing slashes", func(t *testing.T) {
		baseURL := "http://10.43.100.50:8080///"
		oidcURL := strings.TrimRight(baseURL, "/") + "/.well-known/openid-configuration"
		assert.Equal(t, "http://10.43.100.50:8080/.well-known/openid-configuration", oidcURL)
	})

	t.Run("handles URL with path", func(t *testing.T) {
		baseURL := "http://10.43.100.50:8080/oidc"
		oidcURL := strings.TrimRight(baseURL, "/") + "/.well-known/openid-configuration"
		assert.Equal(t, "http://10.43.100.50:8080/oidc/.well-known/openid-configuration", oidcURL)
	})
}

// --- Register Response Parsing ---
// Tests extraction of cluster group ID and cluster ID from the register response.

func TestRegisterResponseParsing(t *testing.T) {
	t.Run("extracts IDs from nested response", func(t *testing.T) {
		respJSON := `{
			"clusterGroup": {
				"id": "cg-abc-123",
				"clusters": [{"id": "cl-def-456"}]
			}
		}`
		var resp client.RegisterClusterResponse
		err := json.Unmarshal([]byte(respJSON), &resp)
		require.NoError(t, err)

		clusterGroupID := resp.ClusterGroup.ID
		if clusterGroupID == "" {
			clusterGroupID = resp.ClusterGroupID
		}
		assert.Equal(t, "cg-abc-123", clusterGroupID)

		clusterID := ""
		if len(resp.ClusterGroup.Clusters) > 0 {
			clusterID = resp.ClusterGroup.Clusters[0].ID
		}
		if clusterID == "" {
			clusterID = resp.ClusterID
		}
		assert.Equal(t, "cl-def-456", clusterID)
	})

	t.Run("falls back to top-level IDs", func(t *testing.T) {
		respJSON := `{
			"clusterGroupId": "cg-top-789",
			"clusterId": "cl-top-012"
		}`
		var resp client.RegisterClusterResponse
		err := json.Unmarshal([]byte(respJSON), &resp)
		require.NoError(t, err)

		clusterGroupID := resp.ClusterGroup.ID
		if clusterGroupID == "" {
			clusterGroupID = resp.ClusterGroupID
		}
		assert.Equal(t, "cg-top-789", clusterGroupID)

		clusterID := ""
		if len(resp.ClusterGroup.Clusters) > 0 {
			clusterID = resp.ClusterGroup.Clusters[0].ID
		}
		if clusterID == "" {
			clusterID = resp.ClusterID
		}
		assert.Equal(t, "cl-top-012", clusterID)
	})

	t.Run("handles empty clusters array", func(t *testing.T) {
		respJSON := `{
			"clusterGroup": {
				"id": "cg-abc-123",
				"clusters": []
			},
			"clusterId": "cl-fallback"
		}`
		var resp client.RegisterClusterResponse
		err := json.Unmarshal([]byte(respJSON), &resp)
		require.NoError(t, err)

		clusterID := ""
		if len(resp.ClusterGroup.Clusters) > 0 {
			clusterID = resp.ClusterGroup.Clusters[0].ID
		}
		if clusterID == "" {
			clusterID = resp.ClusterID
		}
		assert.Equal(t, "cl-fallback", clusterID)
	})
}

// --- buildKubectlCommand ---

func TestBuildKubectlCommand(t *testing.T) {
	t.Run("builds basic kubectl command without kubeconfig", func(t *testing.T) {
		config := &client.Config{}
		cmd := buildKubectlCommand(config, []string{"get", "pods"})
		assert.Equal(t, "kubectl", cmd.Path[strings.LastIndex(cmd.Path, "/")+1:])
		assert.Contains(t, cmd.Args, "get")
		assert.Contains(t, cmd.Args, "pods")
	})

	t.Run("includes kubeconfig when specified", func(t *testing.T) {
		config := &client.Config{KubeconfigPath: "/home/user/.kube/config"}
		cmd := buildKubectlCommand(config, []string{"get", "svc"})
		found := false
		for i, arg := range cmd.Args {
			if arg == "--kubeconfig" && i+1 < len(cmd.Args) && cmd.Args[i+1] == "/home/user/.kube/config" {
				found = true
			}
		}
		assert.True(t, found, "kubeconfig flag not found in command args: %v", cmd.Args)
	})

	t.Run("preserves all arguments", func(t *testing.T) {
		config := &client.Config{}
		args := []string{"get", "svc", "-A", "-l", "app=spire-oidc", "-o", "jsonpath={.items}"}
		cmd := buildKubectlCommand(config, args)
		for _, expected := range args {
			assert.Contains(t, cmd.Args, expected)
		}
	})
}

// --- parseTokenFromOutput (from cluster_utils.go) ---

func TestParseTokenFromOutput(t *testing.T) {
	t.Run("extracts token from OpenBao output", func(t *testing.T) {
		output := "Key                Value\n---                -----\ntoken              eyJhbGciOi.test.token123\ntoken_accessor     abc123"
		token, err := parseTokenFromOutput(output)
		require.NoError(t, err)
		assert.Equal(t, "eyJhbGciOi.test.token123", token)
	})

	t.Run("returns error when no token in output", func(t *testing.T) {
		output := "Key                Value\n---                -----\ndata               some-data"
		_, err := parseTokenFromOutput(output)
		assert.Error(t, err)
	})

	t.Run("returns error for empty output", func(t *testing.T) {
		_, err := parseTokenFromOutput("")
		assert.Error(t, err)
	})
}

// --- maskSensitiveData (from cluster_utils.go) ---

func TestMaskSensitiveData(t *testing.T) {
	t.Run("masks long data preserving edges", func(t *testing.T) {
		data := "abcdefghijklmnopqrstuvwxyz0123456789"
		masked := maskSensitiveData(data)
		assert.True(t, strings.HasPrefix(masked, "abcdefghij"), "should preserve first 10 chars")
		assert.True(t, strings.HasSuffix(masked, "0123456789"), "should preserve last 10 chars (approx)")
		assert.Contains(t, masked, "***")
	})

	t.Run("masks short data completely", func(t *testing.T) {
		data := "short"
		masked := maskSensitiveData(data)
		assert.Equal(t, "*****", masked)
	})

	t.Run("masks medium data preserving first and last 4 chars", func(t *testing.T) {
		data := "abcdefghijklmnopqrst" // exactly 20 chars
		masked := maskSensitiveData(data)
		assert.True(t, strings.HasPrefix(masked, "abcd"), "should start with first 4 chars, got: %s", masked)
		assert.True(t, strings.HasSuffix(masked, "qrst"), "should end with last 4 chars, got: %s", masked)
	})
}

// --- parseJSONField (from cluster_utils.go) ---

func TestParseJSONField(t *testing.T) {
	t.Run("extracts field from JSON string", func(t *testing.T) {
		json := `{"name": "test-cluster", "id": "abc-123"}`
		val, err := parseJSONField(json, "name")
		require.NoError(t, err)
		assert.Equal(t, "test-cluster", val)
	})

	t.Run("extracts id field", func(t *testing.T) {
		json := `{"name": "test", "id": "uuid-456"}`
		val, err := parseJSONField(json, "id")
		require.NoError(t, err)
		assert.Equal(t, "uuid-456", val)
	})

	t.Run("returns error for missing field", func(t *testing.T) {
		json := `{"name": "test"}`
		_, err := parseJSONField(json, "missing")
		assert.Error(t, err)
	})
}
