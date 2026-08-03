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

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type sentinelRoundTripper struct {
	inner  http.RoundTripper
	visits int
}

func (s *sentinelRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	s.visits++
	return s.inner.RoundTrip(req)
}

// TestWithTransportWrapper_AppliedAsOutermostLayer verifies the wrapper option is
// applied to the token fetcher's transport as the outermost layer, so it observes
// each token request.
func TestWithTransportWrapper_AppliedAsOutermostLayer(t *testing.T) {
	var called bool
	sentinel := &sentinelRoundTripper{}
	f := NewTokenFetcher("https://auth.example/token", "client", "secret", "scope",
		WithTransportWrapper(func(inner http.RoundTripper) http.RoundTripper {
			called = true
			sentinel.inner = inner
			return sentinel
		}),
	)
	if !called {
		t.Fatal("expected transport wrapper to be invoked")
	}
	if f.client.Transport != http.RoundTripper(sentinel) {
		t.Fatalf("expected outermost transport to be the wrapper, got %T", f.client.Transport)
	}
}

// TestWithTransportWrapper_NilIsIgnored verifies a nil wrapper leaves the
// transport chain unchanged.
func TestWithTransportWrapper_NilIsIgnored(t *testing.T) {
	f := NewTokenFetcher("https://auth.example/token", "client", "secret", "scope",
		WithTransportWrapper(nil))
	if _, ok := f.client.Transport.(*sentinelRoundTripper); ok {
		t.Fatal("nil wrapper must not wrap the transport")
	}
}

// TestWithTransportWrapper_ObservesRealTokenRequest drives an actual FetchToken
// through the wrapper, which is what makes the auth client instrumentable.
func TestWithTransportWrapper_ObservesRealTokenRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authTokenResponse{
			Token: "token", Type: "Bearer", ExpirationSeconds: 900, Scope: "scope",
		})
	}))
	defer srv.Close()

	sentinel := &sentinelRoundTripper{}
	f := NewTokenFetcher(srv.URL, "client", "secret", "scope",
		WithTransportWrapper(func(inner http.RoundTripper) http.RoundTripper {
			sentinel.inner = inner
			return sentinel
		}),
	)
	if _, err := f.FetchToken(context.Background()); err != nil {
		t.Fatalf("FetchToken: %v", err)
	}
	if sentinel.visits != 1 {
		t.Fatalf("expected the wrapper to observe exactly 1 token request, got %d", sentinel.visits)
	}
}
