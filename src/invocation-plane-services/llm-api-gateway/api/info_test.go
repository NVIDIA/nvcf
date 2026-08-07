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

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	echo "github.com/labstack/echo/v4"

	golibversion "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
)

func newInfoEngine() *echo.Echo {
	e := echo.New()
	RegisterRoutes(e, NewHandlers(config.Default(), nil, nil))
	return e
}

func TestInfoEndpoint_GET(t *testing.T) {
	golibversion.Service = "nvcf-llm-api-gateway"
	golibversion.Version = "test-1.0.0"
	golibversion.GitHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Cleanup(func() {
		golibversion.Service = ""
		golibversion.Version = ""
		golibversion.GitHash = ""
	})

	e := newInfoEngine()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/info", nil)
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /info: got status %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("GET /info: got Content-Type %q, want application/json", ct)
	}

	var info map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("GET /info: unmarshal body: %v", err)
	}
	if info["service"] != "nvcf-llm-api-gateway" {
		t.Errorf("GET /info: service = %q, want nvcf-llm-api-gateway", info["service"])
	}
	if info["version"] != "test-1.0.0" {
		t.Errorf("GET /info: version = %q, want test-1.0.0", info["version"])
	}
	if info["commit"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("GET /info: commit = %q, want aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", info["commit"])
	}
}

func TestInfoEndpoint_GET_UnstampedFallback(t *testing.T) {
	previousService := golibversion.Service
	previousVersion := golibversion.Version
	previousGitHash := golibversion.GitHash
	golibversion.Service = ""
	golibversion.Version = ""
	golibversion.GitHash = ""
	t.Cleanup(func() {
		golibversion.Service = previousService
		golibversion.Version = previousVersion
		golibversion.GitHash = previousGitHash
	})

	e := newInfoEngine()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/info", nil)
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /info: got status %d, want %d", rec.Code, http.StatusOK)
	}

	var info map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("GET /info: unmarshal body: %v", err)
	}
	if got, want := info["service"], "unknown"; got != want {
		t.Errorf("GET /info: service = %q, want %q", got, want)
	}
	if got, want := info["version"], "unknown"; got != want {
		t.Errorf("GET /info: version = %q, want %q", got, want)
	}
	if info["commit"] == "" {
		t.Error("GET /info: commit must be populated")
	}
}

func TestInfoEndpoint_RejectsNonGET(t *testing.T) {
	e := newInfoEngine()

	for _, method := range []string{
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
		http.MethodConnect,
		http.MethodTrace,
	} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/info", nil)
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s /info: got status %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
			}
			if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
				t.Errorf("%s /info: got Allow %q, want %q", method, allow, http.MethodGet)
			}
		})
	}
}
