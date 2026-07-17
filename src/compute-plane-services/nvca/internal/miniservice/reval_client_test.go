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

package mscontroller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

func TestReValClientHostHeader(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		wantHost string
	}{
		{
			name:     "uses URL host by default",
			wantHost: "bare-elb.example.invalid",
		},
		{
			name:     "uses configured host override",
			host:     "reval.bare-elb.example.invalid",
			wantHost: "reval.bare-elb.example.invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotHost string
			httpClient := &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					gotHost = req.Host
					if gotHost == "" {
						gotHost = req.URL.Host
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"valid":true}`)),
						Request:    req,
					}, nil
				}),
			}

			client := NewReValClient(
				"http://bare-elb.example.invalid",
				testTokenFetcher{},
				httpClient,
				nil,
				WithReValHostHeaderOverride(tt.host),
			)
			_, err := client.Render(t.Context(), HelmReValRenderInput{NCAID: "test-nca"})
			require.NoError(t, err)
			assert.Equal(t, tt.wantHost, gotHost)
		})
	}
}

func TestReValClientPayloadLogRedactsCredentials(t *testing.T) {
	const (
		apiKey        = "super-secret-api-key"
		registryAuth  = "leaked-registry-auth"
		urlPassword   = "hunter2"
		signedToken   = "leak-me"
		wantHelmChart = "https://charts.example.invalid/chart.tgz"
	)

	var lines []string
	logger := funcr.New(func(prefix, args string) {
		lines = append(lines, prefix+" "+args)
	}, funcr.Options{Verbosity: 2})
	ctx := logf.IntoContext(t.Context(), logger)

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"valid":true}`)),
				Request:    req,
			}, nil
		}),
	}

	client := NewReValClient("http://reval.example.invalid", testTokenFetcher{}, httpClient, nil)
	_, err := client.Render(ctx, HelmReValRenderInput{
		NCAID:        "test-nca",
		HelmChartURL: fmt.Sprintf("https://user:%s@charts.example.invalid/chart.tgz?token=%s", urlPassword, signedToken),
		ReleaseName:  "my-release",
		APIKey:       apiKey,
		HelmRegistryAuthConfig: common.RegistryAuthConfig{
			K8sSecrets: []common.RegistryAuthSecret{{
				Auths: map[string]common.RegistryAuth{"registry.example.invalid": {Auth: registryAuth}},
			}},
		},
	})
	require.NoError(t, err)

	output := strings.Join(lines, "\n")
	assert.NotContains(t, output, apiKey)
	assert.NotContains(t, output, registryAuth)
	assert.NotContains(t, output, urlPassword)
	assert.NotContains(t, output, signedToken)

	assert.Contains(t, output, "my-release")
	assert.Contains(t, output, wantHelmChart)
}

func TestRedactedHelmChartURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips userinfo and query",
			in:   "https://user:pass@charts.example.invalid/chart.tgz?token=secret",
			want: "https://charts.example.invalid/chart.tgz",
		},
		{
			name: "leaves a plain URL unchanged",
			in:   "oci://registry.example.invalid/charts/foo:1.0.0",
			want: "oci://registry.example.invalid/charts/foo:1.0.0",
		},
		{
			name: "falls back on unparseable input",
			in:   "://not a url",
			want: "<unparseable>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, redactedHelmChartURL(tt.in))
		})
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// errTokenFetcher returns an error from FetchToken.
type errTokenFetcher struct{ err error }

func (f errTokenFetcher) FetchToken(_ context.Context) (string, error) { return "", f.err }

// getReValMetricCount returns the recorded http_code label value count for the given code.
func getReValMetricCount(t *testing.T, reg *prometheus.Registry, ncaID, httpCode string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if *mf.Name != metrics.MiniServiceReValRequestTotalMetricName {
			continue
		}
		for _, m := range mf.Metric {
			var gotNCAID, gotCode string
			for _, lbl := range m.Label {
				switch *lbl.Name {
				case metrics.NCAIDLabel:
					gotNCAID = *lbl.Value
				case metrics.HTTPCodeLabel:
					gotCode = *lbl.Value
				}
			}
			if gotNCAID == ncaID && gotCode == httpCode {
				return m.Counter.GetValue()
			}
		}
	}
	return 0
}

func TestReValClientMetricRecording(t *testing.T) {
	const ncaID = "test-nca"

	newMetrics := func(t *testing.T) (*metrics.Metrics, *prometheus.Registry) {
		t.Helper()
		reg := prometheus.NewRegistry()
		m := metrics.NewDefaultMetrics("", "", "", "", metrics.WithRegisterer(reg))
		t.Cleanup(func() { m.Destroy() })
		return m, reg
	}

	t.Run("records token_fetch_error on token fetch failure", func(t *testing.T) {
		m, reg := newMetrics(t)
		client := NewReValClient(
			"http://reval.example.invalid",
			errTokenFetcher{err: errors.New("auth failure")},
			&http.Client{},
			m,
		)
		_, err := client.Render(t.Context(), HelmReValRenderInput{NCAID: ncaID})
		require.Error(t, err)
		assert.Equal(t, float64(1), getReValMetricCount(t, reg, ncaID, "token_fetch_error"))
	})

	t.Run("records error on network failure (nil resp)", func(t *testing.T) {
		m, reg := newMetrics(t)
		httpClient := &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("connection refused")
			}),
		}
		client := NewReValClient("http://reval.example.invalid", testTokenFetcher{}, httpClient, m)
		_, err := client.Render(t.Context(), HelmReValRenderInput{NCAID: ncaID})
		require.Error(t, err)
		assert.Equal(t, float64(1), getReValMetricCount(t, reg, ncaID, "error"))
	})

	t.Run("records HTTP status code on successful call", func(t *testing.T) {
		m, reg := newMetrics(t)
		httpClient := &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"valid":true}`)),
					Request:    req,
				}, nil
			}),
		}
		client := NewReValClient("http://reval.example.invalid", testTokenFetcher{}, httpClient, m)
		_, _ = client.Render(t.Context(), HelmReValRenderInput{NCAID: ncaID})
		assert.Equal(t, float64(1), getReValMetricCount(t, reg, ncaID, "200"))
	})

	t.Run("records HTTP status code on non-200 response", func(t *testing.T) {
		m, reg := newMetrics(t)
		httpClient := &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`internal error`)),
					Request:    req,
				}, nil
			}),
		}
		client := NewReValClient("http://reval.example.invalid", testTokenFetcher{}, httpClient, m)
		_, err := client.Render(t.Context(), HelmReValRenderInput{NCAID: ncaID})
		require.Error(t, err)
		assert.Equal(t, float64(1), getReValMetricCount(t, reg, ncaID, "500"))
		assert.Equal(t, float64(0), getReValMetricCount(t, reg, ncaID, "error"))
	})
}
