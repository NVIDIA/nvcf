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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// The UI's runtime configuration: addresses that differ per deployment, which
// the browser can't derive and the bundle mustn't bake in. Both are optional.
const (
	// gatewayPublicURL is the gateway's address as clients outside the cluster
	// reach it, for the UI's curl snippets. It is not GATEWAY_URL, the
	// in-cluster Service the BFF itself calls.
	gatewayPublicURL = "GATEWAY_PUBLIC_URL"
	// grafanaURL is the dashboard the UI links each endpoint to. It may also be
	// a root-relative path, such as /grafana/d/llm-demo, for a Grafana served
	// behind the same ingress as the UI: the link then works on whatever host
	// the UI was opened on.
	grafanaURL = "GRAFANA_URL"
)

// uiConfig is the body of GET /api/v1/config (spec/bff-openapi.yaml). Unset
// fields are omitted, and the UI falls back without them.
type uiConfig struct {
	GatewayURL string `json:"gatewayUrl,omitempty"`
	GrafanaURL string `json:"grafanaUrl,omitempty"`
}

// uiConfigFromEnv reads the UI's settings. A value that isn't an absolute
// http(s) URL (or, for Grafana, a root-relative path) fails startup, so a
// typo in the chart surfaces at deploy time instead of as a broken link.
func uiConfigFromEnv() (uiConfig, error) {
	var cfg uiConfig
	for _, f := range []struct {
		env           string
		dst           *string
		allowRelative bool
	}{
		{gatewayPublicURL, &cfg.GatewayURL, false},
		{grafanaURL, &cfg.GrafanaURL, true},
	} {
		raw := os.Getenv(f.env)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		switch {
		case err != nil:
		case (u.Scheme == "http" || u.Scheme == "https") && u.Host != "":
			*f.dst = raw
			continue
		case f.allowRelative && u.Scheme == "" && u.Host == "" && strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//"):
			*f.dst = raw
			continue
		}
		if f.allowRelative {
			return uiConfig{}, fmt.Errorf("%s: %q is neither an absolute http(s) URL nor a path starting with /", f.env, raw)
		}
		return uiConfig{}, fmt.Errorf("%s: %q is not an absolute http(s) URL", f.env, raw)
	}
	return cfg, nil
}

// registerConfig serves cfg at GET /api/v1/config.
func registerConfig(router *http.ServeMux, cfg uiConfig) {
	// Marshaling a struct of two strings can't fail.
	body, _ := json.Marshal(cfg)
	router.HandleFunc("GET /api/v1/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Revalidate on every load, so a redeploy with new values shows at once.
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	})
}
