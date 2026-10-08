// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestUIConfigFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		gateway string
		grafana string
		want    uiConfig
		wantErr bool
	}{
		{name: "both unset", want: uiConfig{}},
		{
			name:    "both set",
			gateway: "https://llm-gateway.example.com",
			grafana: "http://spark/grafana/d/llm-demo?orgId=1",
			want: uiConfig{
				GatewayURL: "https://llm-gateway.example.com",
				GrafanaURL: "http://spark/grafana/d/llm-demo?orgId=1",
			},
		},
		{
			name:    "root-relative Grafana behind the UI's ingress",
			grafana: "/grafana/d/llm-demo",
			want:    uiConfig{GrafanaURL: "/grafana/d/llm-demo"},
		},
		{name: "relative gateway", gateway: "/v1", wantErr: true},
		{name: "Grafana path without a leading slash", grafana: "grafana/d/llm-demo", wantErr: true},
		{name: "protocol-relative Grafana", grafana: "//grafana.example.com/d/x", wantErr: true},
		{name: "no scheme", gateway: "llm-gateway.example.com:8080", wantErr: true},
		{name: "other scheme", gateway: "ftp://llm-gateway.example.com", wantErr: true},
		{name: "no host", gateway: "https://", wantErr: true},
		{name: "unparsable", grafana: "http://[::1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(gatewayPublicURL, tt.gateway)
			t.Setenv(grafanaURL, tt.grafana)

			got, err := uiConfigFromEnv()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("uiConfigFromEnv() = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("uiConfigFromEnv() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("uiConfigFromEnv() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestConfigRoute(t *testing.T) {
	tests := []struct {
		name string
		cfg  uiConfig
		want string
	}{
		{
			name: "configured",
			cfg: uiConfig{
				GatewayURL: "https://llm-gateway.example.com",
				GrafanaURL: "https://grafana.example.com/d/llm-demo",
			},
			want: `{"gatewayUrl":"https://llm-gateway.example.com","grafanaUrl":"https://grafana.example.com/d/llm-demo"}`,
		},
		{name: "unset fields are omitted", cfg: uiConfig{}, want: `{}`},
		{
			name: "root-relative Grafana",
			cfg:  uiConfig{GrafanaURL: "/grafana/d/llm-demo"},
			want: `{"grafanaUrl":"/grafana/d/llm-demo"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(zerolog.Nop(), writeBuild(t), noGateway, tt.cfg, noRecipes)

			rec := serve(h, http.MethodGet, "/api/v1/config")

			if rec.Code != http.StatusOK {
				t.Fatalf("GET /api/v1/config = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", got)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.want {
				t.Errorf("body = %s, want %s", got, tt.want)
			}
			assertMatchesSpec(t, http.MethodGet, "/api/v1/config", "200", rec.Body.Bytes())
		})
	}
}

// TestConfigRouteIsReadOnly checks that a write to the config path falls
// through to the API catch-all instead of reaching the handler or the SPA.
func TestConfigRouteIsReadOnly(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeBuild(t), noGateway, uiConfig{}, noRecipes)

	rec := serve(h, http.MethodPost, "/api/v1/config")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /api/v1/config = %d, want %d", rec.Code, http.StatusNotFound)
	}
	assertMatchesSpec(t, http.MethodGet, "/api/v1/config", "default", rec.Body.Bytes())
}
