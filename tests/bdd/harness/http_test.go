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

package harness

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNetHTTPClientSendsHeadersAndBody(t *testing.T) {
	var gotAuth, gotBody, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	resp, err := NewHTTPClient().Do(context.Background(), "POST", server.URL, map[string]string{"Authorization": "Bearer t"}, `{"a":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusCreated || resp.Body != `{"ok":true}` {
		t.Fatalf("resp = %+v", resp)
	}
	if gotAuth != "Bearer t" || gotBody != `{"a":1}` || gotMethod != "POST" {
		t.Fatalf("server saw auth=%q body=%q method=%q", gotAuth, gotBody, gotMethod)
	}
}

func TestNetHTTPClientDoesNotFollowRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
	}))
	defer server.Close()
	resp, err := NewHTTPClient().Do(context.Background(), "GET", server.URL, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", resp.Status)
	}
}

func TestNetHTTPClientErrorOmitsHeaderValues(t *testing.T) {
	_, err := NewHTTPClient().Do(context.Background(), "GET", "http://127.0.0.1:1", map[string]string{"Authorization": "Bearer secret-token"}, "")
	if err == nil {
		t.Fatal("expected connection error")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaks header value: %v", err)
	}
}
