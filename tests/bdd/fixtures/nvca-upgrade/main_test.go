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

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNGCRoutePreservesVersionReturnedByRealBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/accounts/bdd-account/clusters/cluster" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("upstream request was not authenticated")
		}
		writeJSON(w, map[string]string{"nvcaVersion": "backend-version"})
	}))
	defer backend.Close()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	g := &gateway{key: key, issuer: "http://fixture", icms: backend.URL,
		probeRepo: "probe", probeTag: "different-version", client: backend.Client()}
	response := httptest.NewRecorder()
	g.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v2/sis/clusters/cluster", nil))
	var cluster map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &cluster); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || cluster["nvcaVersion"] != "backend-version" {
		t.Fatal("NGC route adapter changed the backend version")
	}
}

func TestGatewayPreservesRequestBodyAndBackendError(t *testing.T) {
	const body = `{"nvcaVersion":"requested-version"}`
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPut || string(data) != body {
			t.Error("gateway changed the cluster update request")
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"conflict"}`))
	}))
	defer backend.Close()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	g := &gateway{key: key, issuer: "http://fixture", icms: backend.URL, client: backend.Client()}
	response := httptest.NewRecorder()
	g.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/v1/accounts/bdd-account/clusters/cluster", strings.NewReader(body)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "conflict") {
		t.Fatal("gateway concealed the backend error")
	}
}
