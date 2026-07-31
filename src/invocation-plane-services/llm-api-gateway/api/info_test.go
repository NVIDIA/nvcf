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

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
)

// newInfoEngine builds an echo engine with the service routes registered so the
// /info endpoint can be exercised the same way it is served at runtime.
func newInfoEngine() *echo.Echo {
	e := echo.New()
	RegisterRoutes(e, NewHandlers(config.Default(), nil, nil))
	return e
}

func TestInfoEndpoint_GET(t *testing.T) {
	t.Parallel()

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

	// x_defs are not injected under `go test`; resolve() falls back to "unknown"
	// for any empty field, so all three values are guaranteed non-empty.
	var info map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("GET /info: unmarshal body: %v", err)
	}
	for _, field := range []string{"service", "version", "commit"} {
		if info[field] == "" {
			t.Errorf("GET /info: field %q must be populated", field)
		}
	}
}

func TestInfoEndpoint_RejectsNonGET(t *testing.T) {
	t.Parallel()

	e := newInfoEngine()

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
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
			if body := rec.Body.String(); body != "" {
				t.Errorf("%s /info: got non-empty body %q", method, body)
			}
		})
	}
}
