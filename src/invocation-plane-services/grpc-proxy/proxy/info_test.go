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

package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hellofresh/health-go/v5"
)

// newInfoTestMux builds a proxy mux for exercising the /info route. The /info
// handler touches neither the director nor the health manager, so an empty
// director and a checkless health manager are sufficient.
func newInfoTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	healthManager, err := health.New()
	if err != nil {
		t.Fatalf("health.New() failed: %v", err)
	}
	return newProxyMux(&StreamDirector{}, healthManager)
}

func TestNewProxyMux_Info(t *testing.T) {
	mux := newInfoTestMux(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/info", nil)
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /info status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("GET /info Content-Type = %q, want application/json", ct)
	}

	// x_defs are not injected under `go test`; resolve() falls back to
	// "unknown" for any empty field, so all three values are non-empty.
	var info map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("GET /info body is not valid JSON: %v", err)
	}
	for _, field := range []string{"service", "version", "commit"} {
		if info[field] == "" {
			t.Errorf("GET /info field %q must be populated", field)
		}
	}
}

func TestNewProxyMux_Info_RejectsNonGET(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			mux := newInfoTestMux(t)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(method, "/info", nil)
			mux.ServeHTTP(w, r)

			if w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s /info status = %d, want %d", method, w.Code, http.StatusMethodNotAllowed)
			}
			if allow := w.Header().Get("Allow"); allow != http.MethodGet {
				t.Fatalf("%s /info Allow = %q, want %s", method, allow, http.MethodGet)
			}
			if body := w.Body.String(); body != "" {
				t.Fatalf("%s /info body = %q, want empty", method, body)
			}
		})
	}
}
